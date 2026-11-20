package sshgw

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/auth"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/credvault"
	"github.com/touwaeriol/agent-teams/server/internal/fanout"
	"github.com/touwaeriol/agent-teams/server/internal/kv"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/testdb"
)

const targetPassword = "Tr0ub4dor&3-target"

// fixture: one user with a node, a project using the target as "prod", granted to role "dev".
type fixture struct {
	t       *testing.T
	ctx     context.Context
	s       *store.Store
	cs      *core.Store
	vault   *credvault.Vault
	g       *Gateway
	http    *httptest.Server
	tg      *target
	user    string
	project string
	node    store.Node
	token   string
	server  store.RemoteServer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, testdb.Migrated(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	vault, err := credvault.New("sshgw-test-key")
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	fan := fanout.New(kv.NewMemory(), log)
	cs := core.New(s.Pool, fan)
	nodes, err := nodegw.New(s, cs, fan, log, nodegw.Config{})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, ctx: ctx, s: s, cs: cs, vault: vault, tg: newTarget(t, targetPassword)}
	f.g = New(Deps{Store: s, Core: cs, Nodes: nodes, Fan: fan, Vault: vault, Log: log,
		Cfg: Config{Keepalive: time.Second, Recheck: time.Hour}})
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go fan.Run(runCtx)
	go f.g.Run(runCtx)
	f.http = httptest.NewServer(http.HandlerFunc(f.g.ServeWS))
	t.Cleanup(f.http.Close)

	u, err := s.CreateUser(ctx, "sshgw@example.com", "h")
	if err != nil {
		t.Fatal(err)
	}
	f.user = u.ID
	prj, err := s.CreateProject(ctx, u.ID, store.NewProject{Name: "p", WorkMode: "worktree"})
	if err != nil {
		t.Fatal(err)
	}
	f.project = prj.ID
	f.token = "node-token-" + store.NewID("x")
	if f.node, err = s.CreateNode(ctx, u.ID, "n1", auth.HashToken(f.token)); err != nil {
		t.Fatal(err)
	}
	f.exec(`INSERT INTO at_roles (id, user_id, project_id, tier, role, read_only, gate) VALUES
		('ro_gw1', $1, NULL, 'member', 'dev', false, ''), ('ro_gw2', $1, NULL, 'member', 'ops', false, '')`, u.ID)
	f.server = f.addServer("prod", f.tg)
	if err := s.AddProjectServer(ctx, u.ID, prj.ID, f.server.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProjectServerGrants(ctx, u.ID, prj.ID, f.server.ID, []string{"dev"}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := store.Exec(f.ctx, f.s.Pool, sql, args...); err != nil {
		f.t.Fatal(err)
	}
}

// addServer stores tg as a server with its password and confirmed host key.
func (f *fixture) addServer(name string, tg *target) store.RemoteServer {
	f.t.Helper()
	id := store.NewID("srv")
	sealed, _, err := f.vault.SealCredential(credvault.ServerAAD(f.user, id, credvault.AuthPassword, "127.0.0.1", tg.port(), "deploy"),
		credvault.AuthPassword, credvault.Credential{Password: tg.password})
	if err != nil {
		f.t.Fatal(err)
	}
	srv, err := f.s.CreateRemoteServer(f.ctx, store.RemoteServer{ID: id, UserID: f.user, Name: name, Host: "127.0.0.1",
		Port: tg.port(), Username: "deploy", AuthKind: credvault.AuthPassword}, store.ServerCredential{Sealed: sealed})
	if err != nil {
		f.t.Fatal(err)
	}
	f.pinHostKey(srv, tg.hostKeyLine())
	return srv
}

func (f *fixture) pinHostKey(srv store.RemoteServer, line string) {
	f.t.Helper()
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		f.t.Fatal(err)
	}
	mac, err := f.vault.MAC(credvault.HostKeyMACData(srv.ID, srv.Host, srv.Port, line))
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.s.SetRemoteServerHostKey(f.ctx, f.user, srv.ID, line, ssh.FingerprintSHA256(pub), mac); err != nil {
		f.t.Fatal(err)
	}
}

// session adds an employee with a live session on the fixture's node (open_gen 1).
func (f *fixture) session(name, tier, role, spec string) string {
	f.t.Helper()
	emp, sess := store.NewID("em"), store.NewID("s")
	f.exec(`INSERT INTO at_employees (id, project_id, name, tier, role, runtime, spec, node_id) VALUES ($1, $2, $3, $4, $5, 'claude', $6::jsonb, $7)`,
		emp, f.project, name, tier, role, spec, f.node.ID)
	f.exec(`INSERT INTO at_agent_sessions (id, employee_id, node_id, runtime, cwd, state, open_gen) VALUES ($1, $2, $3, 'claude', '/w', 'active', 1)`,
		sess, emp, f.node.ID)
	return sess
}

// sessionKey is the session agent's in-memory key pair (the daemon's, remote-servers.md §4.2).
func sessionKey(t *testing.T) (ssh.Signer, string) {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
}

// certify asks for a certificate like ssh.cert_request does and returns the response.
func (f *fixture) certify(sid string, gen int, pub string) protocol.SshCertResponse {
	return f.g.Sign(f.ctx, f.node, &protocol.SshCertRequest{ReqID: "k1", SessionID: protocol.SessionID(sid),
		OpenGen: protocol.OpenGen(gen), PublicKey: pub})
}

// identity is what a session agent holds: the key and its certificate, plus the gateway host key.
type identity struct {
	signer  ssh.Signer // certificate signer
	gateway ssh.PublicKey
	resp    protocol.SshCertResponse
}

func (f *fixture) identity(sid string) identity {
	f.t.Helper()
	key, pub := sessionKey(f.t)
	resp := f.certify(sid, 1, pub)
	if !resp.Ok {
		f.t.Fatalf("certificate refused: %v", *resp.Error)
	}
	return f.identityFrom(key, resp)
}

func (f *fixture) identityFrom(key ssh.Signer, resp protocol.SshCertResponse) identity {
	f.t.Helper()
	pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(*resp.Certificate))
	if err != nil {
		f.t.Fatal(err)
	}
	signer, err := ssh.NewCertSigner(pk.(*ssh.Certificate), key)
	if err != nil {
		f.t.Fatal(err)
	}
	line := strings.TrimPrefix(resp.KnownHosts[0], KnownHostsAlias+" ")
	gw, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		f.t.Fatal(err)
	}
	return identity{signer: signer, gateway: gw, resp: resp}
}

// recorder keeps every byte the node side saw on the WebSocket (front-visible data).
type recorder struct {
	net.Conn
	mu  sync.Mutex
	buf bytes.Buffer
}

func (r *recorder) Read(p []byte) (int, error) {
	n, err := r.Conn.Read(p)
	r.mu.Lock()
	r.buf.Write(p[:n])
	r.mu.Unlock()
	return n, err
}

func (r *recorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	r.buf.Write(p)
	r.mu.Unlock()
	return r.Conn.Write(p)
}

// dialWS opens /nodes/ssh like the daemon's ssh-proxy; status is the HTTP status when refused.
func (f *fixture) dialWS(sid string, gen int, server, token string) (net.Conn, int, error) {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	h.Set(HeaderSession, sid)
	h.Set(HeaderOpenGen, strconv.Itoa(gen))
	h.Set(HeaderServer, server)
	url := "ws" + strings.TrimPrefix(f.http.URL, "http") + "/nodes/ssh"
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: h, Subprotocols: []string{Subprotocol}})
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		return nil, status, err
	}
	return websocket.NetConn(f.ctx, ws, websocket.MessageBinary), http.StatusSwitchingProtocols, nil
}

// connect runs the whole front path: WebSocket, then SSH with the session certificate.
func (f *fixture) connect(sid, server, user string, id identity) (*ssh.Client, *recorder, error) {
	f.t.Helper()
	nc, status, err := f.dialWS(sid, 1, server, f.token)
	if err != nil {
		return nil, nil, errors.New("websocket refused: " + strconv.Itoa(status))
	}
	rec := &recorder{Conn: nc}
	cfg := &ssh.ClientConfig{User: user, Auth: []ssh.AuthMethod{ssh.PublicKeys(id.signer)},
		HostKeyCallback: ssh.FixedHostKey(id.gateway), Timeout: 10 * time.Second}
	cc, chans, reqs, err := ssh.NewClientConn(rec, KnownHostsAlias, cfg)
	if err != nil {
		_ = nc.Close()
		return nil, rec, err
	}
	return ssh.NewClient(cc, chans, reqs), rec, nil
}

func (f *fixture) audit(kind string) []store.SSHAudit {
	f.t.Helper()
	rows, _, err := f.s.SSHAuditPage(f.ctx, store.SSHAuditFilter{UserID: f.user, Kind: kind, Limit: 200})
	if err != nil {
		f.t.Fatal(err)
	}
	return rows
}

// waitAudit waits for an audit row of kind whose reason or command contains want.
func (f *fixture) waitAudit(kind, want string) store.SSHAudit {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, a := range f.audit(kind) {
			if strings.Contains(a.Reason, want) || strings.Contains(a.Command, want) {
				return a
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.t.Fatalf("no %s audit row with %q; have %+v", kind, want, f.audit(kind))
	return store.SSHAudit{}
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
