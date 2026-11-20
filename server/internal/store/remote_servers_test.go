package store_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/credvault"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/testdb"
)

type sshFixture struct {
	s        *store.Store
	vault    *credvault.Vault
	user     string
	other    string
	project  string
	project2 string
	otherPrj string
}

func newSSHFixture(t *testing.T) *sshFixture {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, testdb.Migrated(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	v, err := credvault.New("test-key")
	if err != nil {
		t.Fatal(err)
	}
	f := &sshFixture{s: s, vault: v}
	u, err := s.CreateUser(ctx, "ssh@example.com", "h")
	if err != nil {
		t.Fatal(err)
	}
	o, err := s.CreateUser(ctx, "other@example.com", "h")
	if err != nil {
		t.Fatal(err)
	}
	f.user, f.other = u.ID, o.ID
	for _, p := range []struct {
		dst  *string
		user string
		name string
	}{{&f.project, u.ID, "p1"}, {&f.project2, u.ID, "p2"}, {&f.otherPrj, o.ID, "op"}} {
		prj, err := s.CreateProject(ctx, p.user, store.NewProject{Name: p.name, WorkMode: "worktree"})
		if err != nil {
			t.Fatal(err)
		}
		*p.dst = prj.ID
	}
	// Role templates of the user, plus a project copy of sx-qa that is read-only in p1 only.
	exec(t, s, `INSERT INTO at_roles (id, user_id, project_id, tier, role, read_only, gate) VALUES
		('ro_sx1', $1, NULL, 'member', 'sx-dev', false, ''),
		('ro_sx2', $1, NULL, 'member', 'sx-ops', false, ''),
		('ro_sx3', $1, NULL, 'member', 'sx-review', true, 'review'),
		('ro_sx4', $1, NULL, 'member', 'sx-qa', false, ''),
		('ro_sx5', $1, $2, 'member', 'sx-qa', true, '')`, u.ID, f.project)
	return f
}

func exec(t *testing.T, s *store.Store, sql string, args ...any) {
	t.Helper()
	if _, err := store.Exec(context.Background(), s.Pool, sql, args...); err != nil {
		t.Fatal(err)
	}
}

func (f *sshFixture) server(t *testing.T, user, name, host string) store.RemoteServer {
	t.Helper()
	id := store.NewID("srv")
	sealed, _, err := f.vault.SealCredential(credvault.ServerAAD(user, id, credvault.AuthPassword, host, 22, "deploy"),
		credvault.AuthPassword, credvault.Credential{Password: "pw-" + name})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := f.s.CreateRemoteServer(context.Background(), store.RemoteServer{ID: id, UserID: user, Name: name, Host: host,
		Port: 22, Username: "deploy", AuthKind: credvault.AuthPassword}, store.ServerCredential{Sealed: sealed})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

// session adds an employee with a live session in project and returns the session id.
func (f *sshFixture) session(t *testing.T, project, name, tier, role, spec string) string {
	t.Helper()
	emp, sess := store.NewID("em"), store.NewID("s")
	exec(t, f.s, `INSERT INTO at_employees (id, project_id, name, tier, role, runtime, spec) VALUES ($1, $2, $3, $4, $5, 'claude', $6::jsonb)`,
		emp, project, name, tier, role, spec)
	exec(t, f.s, `INSERT INTO at_agent_sessions (id, employee_id, runtime, cwd, state, open_gen) VALUES ($1, $2, 'claude', '/w', 'idle', 2)`,
		sess, emp)
	return sess
}

func names(list []store.RemoteServer) []string {
	out := []string{}
	for _, s := range list {
		out = append(out, s.Name)
	}
	return out
}

func TestRemoteServerCRUD(t *testing.T) {
	ctx := context.Background()
	f := newSSHFixture(t)
	prod := f.server(t, f.user, "prod", "prod.example.com")
	f.server(t, f.user, "staging", "staging.example.com")
	f.server(t, f.other, "prod", "x.example.com") // names are per user

	if _, err := f.s.CreateRemoteServer(ctx, store.RemoteServer{ID: store.NewID("srv"), UserID: f.user, Name: "prod", Host: "h",
		Port: 22, Username: "u", AuthKind: "password"}, store.ServerCredential{Sealed: credvault.Sealed{Ciphertext: []byte{1},
		Nonce: []byte{1}, WrappedDEK: []byte{1}, DEKNonce: []byte{1}, KEKID: "k"}}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate name: %v", err)
	}
	if _, err := f.s.CreateRemoteServer(ctx, store.RemoteServer{ID: store.NewID("srv"), UserID: f.user, Name: "Bad Name", Host: "h",
		Port: 22, Username: "u", AuthKind: "password"}, store.ServerCredential{Sealed: credvault.Sealed{Ciphertext: []byte{1},
		Nonce: []byte{1}, WrappedDEK: []byte{1}, DEKNonce: []byte{1}, KEKID: "k"}}); err == nil {
		t.Fatal("invalid name accepted")
	}
	list, err := f.s.RemoteServers(ctx, f.user)
	if err != nil || !slices.Equal(names(list), []string{"prod", "staging"}) {
		t.Fatalf("list %v %v", names(list), err)
	}
	if _, err := f.s.RemoteServer(ctx, f.other, prod.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other user sees server: %v", err)
	}
	if got, err := f.s.RemoteServerByName(ctx, f.user, "prod"); err != nil || got.ID != prod.ID {
		t.Fatalf("by name %v %v", got.ID, err)
	}

	// The sealed credential opens only with the row's own values.
	sec, err := f.s.RemoteServerSecret(ctx, f.user, prod.ID)
	if err != nil {
		t.Fatal(err)
	}
	aad := credvault.ServerAAD(f.user, prod.ID, sec.AuthKind, sec.Host, sec.Port, sec.Username)
	if c, err := f.vault.OpenCredential(aad, sec.Sealed); err != nil || c.Password != "pw-prod" {
		t.Fatalf("open %v", err)
	}

	// Host key, then a host change: needs a new credential and drops the host key.
	mac, _ := f.vault.MAC(credvault.HostKeyMACData(prod.ID, prod.Host, prod.Port, "ssh-ed25519 AAAA"))
	if got, err := f.s.SetRemoteServerHostKey(ctx, f.user, prod.ID, "ssh-ed25519 AAAA", "SHA256:x", mac); err != nil ||
		got.HostKeyFP != "SHA256:x" || got.HostKeyConfirmedAt == nil {
		t.Fatalf("host key %+v %v", got, err)
	}
	newHost := "evil.example.com"
	if _, _, err := f.s.UpdateRemoteServer(ctx, f.user, prod.ID, store.RemoteServerPatch{Host: &newHost}, nil); !errors.Is(err, store.ErrCredentialRequired) {
		t.Fatalf("host change without credential: %v", err)
	}
	// Writing the host straight into the table leaves a credential that no longer opens.
	exec(t, f.s, `UPDATE at_remote_servers SET host = $1 WHERE id = $2`, newHost, prod.ID)
	sec, _ = f.s.RemoteServerSecret(ctx, f.user, prod.ID)
	if _, err := f.vault.OpenCredential(credvault.ServerAAD(f.user, prod.ID, sec.AuthKind, sec.Host, sec.Port, sec.Username), sec.Sealed); !errors.Is(err, credvault.ErrOpen) {
		t.Fatalf("moved host still opens: %v", err)
	}
	if ok, _ := f.vault.VerifyMAC(credvault.HostKeyMACData(prod.ID, sec.Host, sec.Port, sec.HostKey), sec.HostKeyMAC); ok {
		t.Fatal("host key MAC valid after the host moved")
	}
	exec(t, f.s, `UPDATE at_remote_servers SET host = $1 WHERE id = $2`, prod.Host, prod.ID)

	sealed, _, _ := f.vault.SealCredential(credvault.ServerAAD(f.user, prod.ID, "password", "new.example.com", 2222, "deploy"),
		"password", credvault.Credential{Password: "new"})
	host, port, name := "new.example.com", 2222, "prod2"
	got, _, err := f.s.UpdateRemoteServer(ctx, f.user, prod.ID, store.RemoteServerPatch{Host: &host, Port: &port, Name: &name},
		func(m store.RemoteServer) (*store.ServerCredential, error) {
			if m.Host != host || m.Port != port || m.Name != name || m.Username != "deploy" {
				t.Errorf("merged %+v", m)
			}
			return &store.ServerCredential{Sealed: sealed}, nil
		})
	if err != nil || got.Host != host || got.Port != port || got.Name != name || got.HostKey != "" || got.HostKeyConfirmedAt != nil ||
		!got.CredUpdatedAt.After(prod.CredUpdatedAt) {
		t.Fatalf("update %+v %v", got, err)
	}
	sec, _ = f.s.RemoteServerSecret(ctx, f.user, prod.ID)
	if c, err := f.vault.OpenCredential(credvault.ServerAAD(f.user, prod.ID, sec.AuthKind, sec.Host, sec.Port, sec.Username), sec.Sealed); err != nil || c.Password != "new" {
		t.Fatalf("open after update: %v", err)
	}
	// Renaming to a taken name.
	taken := "staging"
	if _, _, err := f.s.UpdateRemoteServer(ctx, f.user, prod.ID, store.RemoteServerPatch{Name: &taken}, nil); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("rename to taken: %v", err)
	}

	// Reseal listing covers every user's servers.
	all, err := f.s.SealedRemoteServers(ctx)
	if err != nil || len(all) != 3 {
		t.Fatalf("sealed %d %v", len(all), err)
	}
	if err := f.s.ResealRemoteServer(ctx, prod.ID, sealed, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := f.s.DeleteRemoteServer(ctx, f.other, prod.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other deletes: %v", err)
	}
	if _, err := f.s.DeleteRemoteServer(ctx, f.user, prod.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := f.s.RemoteServers(ctx, f.user); !slices.Equal(names(list), []string{"staging"}) {
		t.Fatalf("after delete %v", names(list))
	}
}

func TestProjectServerGrants(t *testing.T) {
	ctx := context.Background()
	f := newSSHFixture(t)
	prod := f.server(t, f.user, "prod", "prod.example.com")
	staging := f.server(t, f.user, "staging", "staging.example.com")
	foreign := f.server(t, f.other, "foreign", "f.example.com")

	if err := f.s.AddProjectServer(ctx, f.user, f.project, foreign.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other user's server: %v", err)
	}
	if err := f.s.AddProjectServer(ctx, f.user, f.otherPrj, prod.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other user's project: %v", err)
	}
	// The database refuses a cross-user link even when written directly.
	if _, err := store.Exec(ctx, f.s.Pool, `INSERT INTO at_project_servers (project_id, server_id, user_id) VALUES ($1, $2, $3)`,
		f.otherPrj, prod.ID, f.user); err == nil {
		t.Fatal("cross-user link accepted")
	}
	for _, id := range []string{prod.ID, staging.ID} {
		if err := f.s.AddProjectServer(ctx, f.user, f.project, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.s.AddProjectServer(ctx, f.user, f.project, prod.ID); err != nil {
		t.Fatalf("add again: %v", err)
	}
	if _, err := f.s.SetProjectServerGrants(ctx, f.user, f.project2, prod.ID, []string{"sx-dev"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("grant in a project without the server: %v", err)
	}
	for _, bad := range []string{"sx-review", "sx-qa", "lead", "coordinator", "nobody"} {
		if _, err := f.s.SetProjectServerGrants(ctx, f.user, f.project, prod.ID, []string{"sx-dev", bad}); !errors.Is(err, store.ErrRoleNotGrantable) {
			t.Errorf("grant %s: %v", bad, err)
		}
	}
	// sx-qa is read-only only in p1's copy; the template is grantable in p2.
	if err := f.s.GrantableRoles(ctx, f.user, f.project2, []string{"sx-qa"}); err != nil {
		t.Fatalf("template sx-qa: %v", err)
	}
	if _, err := f.s.SetProjectServerGrants(ctx, f.user, f.project, prod.ID, []string{"sx-ops", "sx-dev", "sx-dev"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SetProjectServerGrants(ctx, f.user, f.project, staging.ID, []string{"sx-dev"}); err != nil {
		t.Fatal(err)
	}
	ps, err := f.s.ProjectServers(ctx, f.user, f.project)
	if err != nil || len(ps) != 2 || ps[0].Server.Name != "prod" || !slices.Equal(ps[0].Roles, []string{"sx-dev", "sx-ops"}) ||
		!slices.Equal(ps[1].Roles, []string{"sx-dev"}) {
		t.Fatalf("project servers %+v %v", ps, err)
	}
	if ps, _ := f.s.ProjectServers(ctx, f.other, f.project); len(ps) != 0 {
		t.Fatalf("other user sees project servers %+v", ps)
	}
	if ids, _ := f.s.ServerProjectIDs(ctx, f.user, prod.ID); !slices.Equal(ids, []string{f.project}) {
		t.Fatalf("server projects %v", ids)
	}

	dev := f.session(t, f.project, "dev-1", "member", "sx-dev", `{"read_only": false}`)
	ops := f.session(t, f.project, "ops-1", "member", "sx-ops", `{}`)
	lead := f.session(t, f.project, "lead-1", "lead", "sx-dev", `{}`) // a lead never, whatever its role name
	ro := f.session(t, f.project, "dev-ro", "member", "sx-dev", `{"read_only": true}`)
	gate := f.session(t, f.project, "dev-gate", "member", "sx-dev", `{"gate": "review"}`)
	other := f.session(t, f.project2, "dev-2", "member", "sx-dev", `{}`)

	x, list, err := f.s.SessionServers(ctx, dev)
	if err != nil || !slices.Equal(names(list), []string{"prod", "staging"}) || x.ProjectID != f.project || x.UserID != f.user ||
		x.OpenGen != 2 || x.Employee != "dev-1" {
		t.Fatalf("dev servers %v %+v %v", names(list), x, err)
	}
	if _, list, _ := f.s.SessionServers(ctx, ops); !slices.Equal(names(list), []string{"prod"}) {
		t.Fatalf("ops servers %v", names(list))
	}
	for _, sid := range []string{lead, ro, gate, other} {
		if _, list, err := f.s.SessionServers(ctx, sid); err != nil || len(list) != 0 {
			t.Errorf("session %s servers %v %v", sid, names(list), err)
		}
		if _, _, err := f.s.SessionServerGrant(ctx, sid, "prod"); !errors.Is(err, store.ErrNotGranted) {
			t.Errorf("session %s grant: %v", sid, err)
		}
	}
	if _, sec, err := f.s.SessionServerGrant(ctx, dev, "staging"); err != nil || sec.ID != staging.ID || len(sec.Sealed.Ciphertext) == 0 {
		t.Fatalf("dev staging grant %v", err)
	}
	if _, _, err := f.s.SessionServerGrant(ctx, ops, "staging"); !errors.Is(err, store.ErrNotGranted) {
		t.Fatalf("ops staging: %v", err)
	}
	if _, _, err := f.s.SessionServerGrant(ctx, "s_missing", "prod"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown session: %v", err)
	}
	live, err := f.s.LiveSSHSessions(ctx, f.project)
	if err != nil || len(live) != 2 {
		t.Fatalf("live %d %v", len(live), err)
	}

	// Certificates: revoking a role's grant revokes its certificates naming the server only.
	now := time.Now()
	cert := func(sid, role string, gen int, principals ...string) store.SSHCert {
		c, err := f.s.InsertSSHCert(ctx, store.SSHCert{SessionID: sid, OpenGen: gen, NodeID: "n1", ProjectID: f.project, Role: role,
			PubkeyFP: "SHA256:k", Principals: principals, ValidAfter: now.Add(-time.Minute), ValidBefore: now.Add(10 * time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	cDev := cert(dev, "sx-dev", 2, "prod", "staging")
	cOps := cert(ops, "sx-ops", 2, "prod")
	cOld := cert(dev, "sx-dev", 1, "prod")
	if cOps.Serial <= cDev.Serial || !slices.Equal(cDev.Principals, []string{"prod", "staging"}) || cDev.ThreadID != "" {
		t.Fatalf("certs %+v %+v", cDev, cOps)
	}
	revoked, err := f.s.SetProjectServerGrants(ctx, f.user, f.project, prod.ID, []string{"sx-dev"})
	if err != nil || !slices.Equal(revoked, []int64{cOps.Serial}) {
		t.Fatalf("grant removed revoked %v %v", revoked, err)
	}
	if c, _ := f.s.SSHCert(ctx, cOps.Serial); c.RevokedAt == nil || c.RevokeReason != "grant_removed" {
		t.Fatalf("ops cert %+v", c)
	}
	if revoked, _ := f.s.RevokeOldGenCerts(ctx, dev, 2, "gen_advanced"); !slices.Equal(revoked, []int64{cOld.Serial}) {
		t.Fatalf("old gen %v", revoked)
	}
	// Renaming a server revokes certificates naming its old name.
	newName := "staging2"
	if _, revoked, err := f.s.UpdateRemoteServer(ctx, f.user, staging.ID, store.RemoteServerPatch{Name: &newName}, nil); err != nil ||
		!slices.Equal(revoked, []int64{cDev.Serial}) {
		t.Fatalf("rename revoked %v %v", revoked, err)
	}
	cDev2 := cert(dev, "sx-dev", 2, "prod")
	if revoked, _ := f.s.RemoveProjectServer(ctx, f.user, f.project, prod.ID); !slices.Equal(revoked, []int64{cDev2.Serial}) {
		t.Fatalf("remove revoked %v", revoked)
	}
	if _, list, _ := f.s.SessionServers(ctx, dev); !slices.Equal(names(list), []string{"staging2"}) {
		t.Fatalf("after remove %v", names(list))
	}
	cDev3 := cert(dev, "sx-dev", 2, "staging2")
	if revoked, _ := f.s.RevokeSessionCerts(ctx, dev, "session_closed"); !slices.Equal(revoked, []int64{cDev3.Serial}) {
		t.Fatalf("session revoked %v", revoked)
	}
	if revoked, _ := f.s.RevokeSessionCerts(ctx, dev, "session_closed"); len(revoked) != 0 {
		t.Fatalf("revoked twice %v", revoked)
	}
	// Deleting a server cascades to links and grants.
	if _, err := f.s.DeleteRemoteServer(ctx, f.user, staging.ID); err != nil {
		t.Fatal(err)
	}
	if ps, _ := f.s.ProjectServers(ctx, f.user, f.project); len(ps) != 0 {
		t.Fatalf("after delete %+v", ps)
	}
	exec(t, f.s, `UPDATE at_ssh_certs SET valid_before = now() - interval '31 days' WHERE serial = $1`, cDev.Serial)
	if _, n, err := f.s.PruneSSH(ctx, time.Now()); err != nil || n != 1 {
		t.Fatalf("prune certs %d %v", n, err)
	}
}

func TestSSHAudit(t *testing.T) {
	ctx := context.Background()
	f := newSSHFixture(t)
	exit := 0
	long := strings.Repeat("é", store.MaxAuditCommand) // 2 bytes each: cut on a rune boundary
	a, err := f.s.InsertSSHAudit(ctx, store.SSHAudit{UserID: f.user, ProjectID: f.project, ServerID: "srv_1", ServerName: "prod",
		Kind: store.SSHAuditExec, Command: long, ExitCode: &exit, Detail: map[string]any{"channel": 1}})
	if err != nil || a.ID == 0 || !a.CommandTruncated || len(a.Command) != store.MaxAuditCommand || !strings.HasPrefix(long, a.Command) {
		t.Fatalf("insert %d %v %d %v", a.ID, a.CommandTruncated, len(a.Command), err)
	}
	if _, err := f.s.InsertSSHAudit(ctx, store.SSHAudit{UserID: f.user, Kind: "bogus"}); err == nil {
		t.Fatal("bad kind accepted")
	}
	for i := 0; i < 5; i++ {
		kind := store.SSHAuditConnect
		if i%2 == 1 {
			kind = store.SSHAuditDenied
		}
		if _, err := f.s.InsertSSHAudit(ctx, store.SSHAudit{UserID: f.user, ProjectID: f.project2, ServerID: "srv_2", Kind: kind}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.s.InsertSSHAudit(ctx, store.SSHAudit{UserID: f.other, ProjectID: f.otherPrj, ServerID: "srv_2", Kind: "connect"}); err != nil {
		t.Fatal(err)
	}
	var seen []int64
	cursor := ""
	for page := 0; page < 10; page++ {
		rows, next, err := f.s.SSHAuditPage(ctx, store.SSHAuditFilter{UserID: f.user, ServerID: "srv_2", Cursor: cursor, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			seen = append(seen, r.ID)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != 5 || !slices.IsSortedFunc(seen, func(a, b int64) int { return int(b - a) }) {
		t.Fatalf("paged %v", seen)
	}
	if rows, _, _ := f.s.SSHAuditPage(ctx, store.SSHAuditFilter{UserID: f.user, ProjectID: f.project2, Kind: store.SSHAuditDenied}); len(rows) != 2 {
		t.Fatalf("denied %d", len(rows))
	}
	if rows, _, _ := f.s.SSHAuditPage(ctx, store.SSHAuditFilter{UserID: f.user, ProjectID: f.project}); len(rows) != 1 || rows[0].Detail["channel"] != float64(1) || *rows[0].ExitCode != 0 {
		t.Fatalf("project rows %+v", rows)
	}
	if _, _, err := f.s.SSHAuditPage(ctx, store.SSHAuditFilter{UserID: f.user, Cursor: "junk"}); !errors.Is(err, store.ErrBadCursor) {
		t.Fatalf("bad cursor: %v", err)
	}
	exec(t, f.s, `UPDATE at_ssh_audit SET created_at = now() - interval '366 days' WHERE id = $1`, a.ID)
	if n, _, err := f.s.PruneSSH(ctx, time.Now()); err != nil || n != 1 {
		t.Fatalf("prune audit %d %v", n, err)
	}
}

func TestInsertSettingIfAbsent(t *testing.T) {
	ctx := context.Background()
	f := newSSHFixture(t)
	if ok, err := f.s.InsertSettingIfAbsent(ctx, "t38", map[string]int{"a": 1}); err != nil || !ok {
		t.Fatalf("first %v %v", ok, err)
	}
	if ok, err := f.s.InsertSettingIfAbsent(ctx, "t38", map[string]int{"a": 2}); err != nil || ok {
		t.Fatalf("second %v %v", ok, err)
	}
	var got map[string]int
	if _, err := f.s.Setting(ctx, "t38", &got); err != nil || got["a"] != 1 {
		t.Fatalf("value %v %v", got, err)
	}
	// The SSH keys live in at_settings, sealed.
	signer, sk, err := credvault.EnsureKey(ctx, f.s, f.vault, credvault.SessionCAKey)
	if err != nil || signer == nil || sk.Fingerprint == "" {
		t.Fatalf("ensure %v", err)
	}
	if _, again, err := credvault.EnsureKey(ctx, f.s, f.vault, credvault.SessionCAKey); err != nil || again.Fingerprint != sk.Fingerprint {
		t.Fatalf("again %v", err)
	}
}
