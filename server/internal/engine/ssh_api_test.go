package engine_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
	"github.com/touwaeriol/agent-teams/server/internal/sshdial"
	"github.com/touwaeriol/agent-teams/server/internal/sshdial/sshtest"
	"github.com/touwaeriol/agent-teams/server/internal/sshgw"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// TestSSHAPICutsConnections (platform.SSHHooks wired to the gateway): taking a role's grant away
// or deleting the server through the REST API cuts the live SSH connection and sends the live
// session an ssh.servers_update.
func TestSSHAPICutsConnections(t *testing.T) {
	w := newWorldWith(t, nil, nil)
	tm := &team{w: w, b: w.signUp("sshapi@example.com")}
	var node nodeCreated
	tm.b.must(201, "POST", "/api/nodes", map[string]string{"name": "fake"}, &node)
	var roles struct{ Roles []store.Role }
	tm.b.must(200, "GET", "/api/roles", nil, &roles)
	dev := &roles.Roles[2]
	dev.Count = 1
	tm.b.must(200, "PUT", "/api/roles", map[string]any{"roles": roles.Roles}, nil)
	var project struct{ ID string }
	tm.b.must(201, "POST", "/api/projects", map[string]any{"name": "p"}, &project)
	tm.projectID, tm.token = project.ID, node.Token
	// The target is on loopback: allowed by the administrator setting.
	if err := w.db.SaveSetting(w.ctx, sshdial.SettingKey, sshdial.AllowedSetting{CIDRs: []string{"127.0.0.1/32"}}); err != nil {
		t.Fatal(err)
	}
	target := sshtest.Start(t, "deploy", "pw-api-secret", nil)

	// A server through the API; the first test connection pins its host key.
	var srv struct{ ID string }
	tm.b.must(201, "POST", "/api/servers", map[string]any{"name": "prod", "host": target.Host, "port": target.Port,
		"username": "deploy", "auth": map[string]string{"kind": "password", "password": "pw-api-secret"}}, &srv)
	var test struct{ Ok bool }
	tm.b.must(200, "POST", "/api/servers/"+srv.ID+"/test", nil, &test)
	if !test.Ok {
		t.Fatal("test connection failed")
	}

	tm.n = w.connect(node.Token, nodetest.Options{Features: []string{"sessions", "git_worktree", "cred_helper", "file_download", "ssh_gateway"}})
	tm.b.must(201, "POST", "/api/projects/"+project.ID+"/threads", map[string]any{"title": "Deploy", "brief": "deploy it"}, nil)
	lead, _ := expectOpen(t, w, tm.n, "lead-1")
	lead.mustCall("update_plan", map[string]any{"steps": []map[string]string{{"title": "做", "status": "todo"}}})
	lead.mustCall("assign_task", map[string]any{"assignee": dev.Role, "instruction": "deploy to prod", "main": "做"})
	member, open := startTask(t, tm, "dev-1", "deploy to prod")
	if len(open.SshServers) != 0 {
		t.Fatalf("servers before the grant: %+v", open.SshServers)
	}
	grant := func(roles []string) {
		tm.b.must(200, "PUT", "/api/projects/"+project.ID+"/servers", map[string]any{"servers": []map[string]any{
			{"server_id": srv.ID, "roles": roles}}}, nil)
	}
	update := func(want int) {
		t.Helper()
		ctx, cancel := w.waitCtx()
		defer cancel()
		f, err := tm.n.Wait(ctx, protocol.TypeSshServersUpdate, func(f nodetest.Frame) bool {
			var u protocol.SshServersUpdate
			_ = f.Decode(&u)
			return string(u.SessionID) == member.session && len(u.Servers) == want
		})
		if err != nil {
			t.Fatalf("no ssh.servers_update with %d servers: %v", want, err)
		}
		_ = f
	}
	grant([]string{dev.Role})
	update(1)

	connect := func() *ssh.Client {
		t.Helper()
		_, priv, _ := ed25519.GenerateKey(rand.Reader)
		key, _ := ssh.NewSignerFromKey(priv)
		ctx, cancel := w.waitCtx()
		defer cancel()
		pub := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key.PublicKey())))
		reqID := "k" + strconv.FormatInt(time.Now().UnixNano(), 10)
		if err := tm.n.SendUnreliable(ctx, protocol.TypeSshCertRequest, protocol.SshCertRequest{ReqID: reqID,
			SessionID: protocol.SessionID(member.session), OpenGen: protocol.OpenGen(member.gen), PublicKey: pub}); err != nil {
			t.Fatal(err)
		}
		f, err := tm.n.Wait(ctx, protocol.TypeSshCertResponse, func(f nodetest.Frame) bool { return strings.Contains(string(f.Body), reqID) })
		if err != nil {
			t.Fatal(err)
		}
		var resp protocol.SshCertResponse
		_ = f.Decode(&resp)
		if !resp.Ok {
			t.Fatalf("certificate refused: %v", *resp.Error)
		}
		pk, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(*resp.Certificate))
		signer, _ := ssh.NewCertSigner(pk.(*ssh.Certificate), key)
		gw, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(strings.TrimPrefix(resp.KnownHosts[0], sshgw.KnownHostsAlias+" ")))
		h := http.Header{}
		h.Set("Authorization", "Bearer "+node.Token)
		h.Set(sshgw.HeaderSession, member.session)
		h.Set(sshgw.HeaderOpenGen, strconv.Itoa(member.gen))
		h.Set(sshgw.HeaderServer, "prod")
		ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(w.srv.URL, "http")+"/nodes/ssh",
			&websocket.DialOptions{HTTPHeader: h, Subprotocols: []string{sshgw.Subprotocol}})
		if err != nil {
			t.Fatal(err)
		}
		nc := websocket.NetConn(w.ctx, ws, websocket.MessageBinary)
		cc, chans, reqs, err := ssh.NewClientConn(nc, sshgw.KnownHostsAlias, &ssh.ClientConfig{User: "prod",
			Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.FixedHostKey(gw), Timeout: 10 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		client := ssh.NewClient(cc, chans, reqs)
		sess, err := client.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		if out, err := sess.Output("deploy"); err != nil || string(out) != "ok\n" {
			t.Fatalf("exec through the gateway: %q %v", out, err)
		}
		return client
	}
	cut := func(client *ssh.Client, what string) {
		t.Helper()
		done := make(chan struct{})
		go func() {
			_ = client.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("connection not cut after %s", what)
		}
	}

	// The grant goes: the connection is cut, the session hears it has no server.
	client := connect()
	grant([]string{})
	cut(client, "the grant was taken away")
	update(0)

	// The server is deleted: same.
	grant([]string{dev.Role})
	update(1)
	client = connect()
	tm.b.must(204, "DELETE", "/api/servers/"+srv.ID, nil, nil)
	cut(client, "the server was deleted")
	update(0)
}
