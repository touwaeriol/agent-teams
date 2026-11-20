package engine_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/credvault"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// TestSSHServersWiring (remote-servers.md §7.1, protocol.md §8.1): a member whose role is granted
// a server gets it in session.open (the lead does not), a certificate over the node link, and an
// ssh.servers_update when the project's grants change.
func TestSSHServersWiring(t *testing.T) {
	w := newWorldWith(t, nil, nil)
	tm := &team{w: w, b: w.signUp("ssh@example.com")}
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

	ctx := w.ctx
	var userID string
	if err := w.db.Pool.Gorm(ctx).Table("at_projects").Select("user_id").Where("id = ?", project.ID).Scan(&userID).Error; err != nil {
		t.Fatal(err)
	}
	vault := testVault(t)
	id := store.NewID("srv")
	sealed, _, err := vault.SealCredential(credvault.ServerAAD(userID, id, credvault.AuthPassword, "127.0.0.1", 22, "deploy"),
		credvault.AuthPassword, credvault.Credential{Password: "pw-secret-1"})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := w.db.CreateRemoteServer(ctx, store.RemoteServer{ID: id, UserID: userID, Name: "prod", Host: "127.0.0.1", Port: 22,
		Username: "deploy", AuthKind: credvault.AuthPassword}, store.ServerCredential{Sealed: sealed})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.db.AddProjectServer(ctx, userID, project.ID, srv.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.SetProjectServerGrants(ctx, userID, project.ID, srv.ID, []string{dev.Role}); err != nil {
		t.Fatal(err)
	}

	tm.n = w.connect(node.Token, nodetest.Options{Features: []string{"sessions", "git_worktree", "cred_helper", "file_download", "ssh_gateway"}})
	var th struct{ No int }
	tm.b.must(201, "POST", "/api/projects/"+project.ID+"/threads", map[string]any{"title": "Deploy", "brief": "deploy it"}, &th)
	lead, leadOpen := expectOpen(t, w, tm.n, "lead-1")
	if len(leadOpen.SshServers) != 0 {
		t.Fatalf("the lead got servers: %+v", leadOpen.SshServers)
	}
	lead.mustCall("update_plan", map[string]any{"steps": []map[string]string{{"title": "做", "status": "todo"}}})
	lead.mustCall("assign_task", map[string]any{"assignee": dev.Role, "instruction": "deploy to prod", "main": "做"})
	member, open := startTask(t, tm, "dev-1", "deploy to prod")
	if len(open.SshServers) != 1 || open.SshServers[0].Name != "prod" {
		t.Fatalf("member session.open ssh_servers %+v", open.SshServers)
	}

	// ssh.cert_request over the link → ssh.cert_response with a certificate for prod.
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	pub := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	wctx, cancel := w.waitCtx()
	defer cancel()
	if err := tm.n.SendUnreliable(wctx, protocol.TypeSshCertRequest, protocol.SshCertRequest{ReqID: "k1",
		SessionID: protocol.SessionID(member.session), OpenGen: protocol.OpenGen(member.gen), PublicKey: pub}); err != nil {
		t.Fatal(err)
	}
	f, err := tm.n.Wait(wctx, protocol.TypeSshCertResponse, func(f nodetest.Frame) bool { return strings.Contains(string(f.Body), `"k1"`) })
	if err != nil {
		t.Fatal(err)
	}
	var resp protocol.SshCertResponse
	_ = f.Decode(&resp)
	if !resp.Ok || resp.Certificate == nil || len(resp.Servers) != 1 || !strings.HasPrefix(resp.KnownHosts[0], "agent-teams-gateway ssh-ed25519 ") {
		t.Fatalf("cert response %+v", resp)
	}
	if strings.Contains(string(f.Body), "pw-secret-1") {
		t.Fatal("the server password reached the node")
	}

	// Grants change: the member's session hears about it (reliable ssh.servers_update).
	if _, err := w.db.SetProjectServerGrants(ctx, userID, project.ID, srv.ID, []string{}); err != nil {
		t.Fatal(err)
	}
	if err := w.e.SSH.ServersChanged(ctx, project.ID); err != nil {
		t.Fatal(err)
	}
	f, err = tm.n.Wait(wctx, protocol.TypeSshServersUpdate, member.mine)
	if err != nil {
		t.Fatal(err)
	}
	var upd protocol.SshServersUpdate
	_ = f.Decode(&upd)
	if len(upd.Servers) != 0 || upd.Version < 1 || int(upd.OpenGen) != member.gen {
		t.Fatalf("servers update %+v", upd)
	}
}
