package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/touwaeriol/agent-teams/server/internal/credvault"
	"github.com/touwaeriol/agent-teams/server/internal/sshdial"
	"github.com/touwaeriol/agent-teams/server/internal/sshdial/sshtest"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

func passwordServer(name, host string, port int, pw string) map[string]any {
	return map[string]any{"name": name, "host": host, "port": port, "username": "deploy",
		"auth": map[string]any{"kind": "password", "password": pw}}
}

// noSecrets fails when a response body carries a credential or any sealed column.
func noSecrets(t *testing.T, r response, secrets ...string) {
	t.Helper()
	body := string(r.Body)
	for _, s := range append(secrets, "ciphertext", "wrapped_dek", "dek_nonce", "kek_id", "host_key_mac", "PRIVATE KEY", `"password":`, `"passphrase":`, `"private_key":`) {
		if strings.Contains(body, s) {
			t.Fatalf("response leaks %q: %s", s, body)
		}
	}
}

func (h *harness) vault() *credvault.Vault {
	v, err := credvault.New(testSecret)
	if err != nil {
		h.t.Fatal(err)
	}
	return v
}

func TestServersCRUD(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	c, me := h.signUp("a@example.com")
	other, _ := h.signUp("b@example.com")

	for body, code := range map[string]string{
		`{"name":"Bad Name","host":"203.0.113.10","username":"deploy","auth":{"kind":"password","password":"x"}}`:      "validation",
		`{"name":"prod","host":"-oProxyCommand=x","username":"deploy","auth":{"kind":"password","password":"x"}}`:      "validation",
		`{"name":"prod","host":"127.0.0.1","username":"deploy","auth":{"kind":"password","password":"x"}}`:             "address_blocked",
		`{"name":"prod","host":"169.254.169.254","username":"deploy","auth":{"kind":"password","password":"x"}}`:       "address_blocked",
		`{"name":"prod","host":"203.0.113.10","username":"-x","auth":{"kind":"password","password":"x"}}`:              "validation",
		`{"name":"prod","host":"203.0.113.10","username":"deploy","auth":{"kind":"password","password":""}}`:           "validation",
		`{"name":"prod","host":"203.0.113.10","username":"deploy","auth":{"kind":"private_key","private_key":"nope"}}`: "validation",
	} {
		r := c.do("POST", "/api/servers", json.RawMessage(body))
		if r.Status != 422 || r.apiError(t).Code != code {
			t.Errorf("%s = %d %s", body, r.Status, r.Body)
		}
	}
	_, keyPEM := sshtest.NewKey(t, "pp")
	if r := c.do("POST", "/api/servers", map[string]any{"name": "k", "host": "203.0.113.11", "username": "deploy",
		"auth": map[string]any{"kind": "private_key", "private_key": keyPEM}}); r.Status != 422 {
		t.Fatalf("key without passphrase = %d %s", r.Status, r.Body)
	}

	var prod ServerView
	r := c.must(201, "POST", "/api/servers", passwordServer("prod", "203.0.113.10", 0, "hunter2-secret"), &prod)
	noSecrets(t, r, "hunter2-secret")
	if prod.Port != 22 || prod.AuthKind != "password" || prod.HostKeyFP != "" || len(prod.Projects) != 0 || !strings.HasPrefix(prod.ID, "srv_") {
		t.Fatalf("created %+v", prod)
	}
	var key ServerView
	_, key2PEM := sshtest.NewKey(t, "pp-secret-x")
	r = c.must(201, "POST", "/api/servers", map[string]any{"name": "key-box", "host": "203.0.113.11", "port": 2222, "username": "ops",
		"auth": map[string]any{"kind": "private_key", "private_key": key2PEM, "passphrase": "pp-secret-x"}}, nil)
	r.json(t, &key)
	noSecrets(t, r, "pp-secret-x")
	if !key.HasPassphrase || !strings.HasPrefix(key.KeyFingerprint, "SHA256:") {
		t.Fatalf("key server %+v", key)
	}
	if r := c.do("POST", "/api/servers", passwordServer("prod", "203.0.113.12", 22, "x")); r.Status != 409 {
		t.Fatalf("duplicate = %d", r.Status)
	}
	var list []ServerView
	r = c.must(200, "GET", "/api/servers", nil, &list)
	noSecrets(t, r, "hunter2-secret", "pp-secret-x")
	if len(list) != 2 || list[0].Name != "key-box" {
		t.Fatalf("list %+v", list)
	}
	// Another user sees nothing of it.
	other.must(200, "GET", "/api/servers", nil, &list)
	if len(list) != 0 {
		t.Fatalf("other list %+v", list)
	}
	for _, m := range []string{"GET", "DELETE"} {
		if r := other.do(m, "/api/servers/"+prod.ID, nil); r.Status != 404 {
			t.Fatalf("other %s = %d", m, r.Status)
		}
	}
	if r := other.do("PATCH", "/api/servers/"+prod.ID, map[string]any{"name": "mine"}); r.Status != 404 {
		t.Fatalf("other patch = %d", r.Status)
	}

	// Host, port, username or kind changes need a new credential.
	for _, body := range []map[string]any{{"host": "203.0.113.20"}, {"port": 2200}, {"username": "root"}} {
		if r := c.do("PATCH", "/api/servers/"+prod.ID, body); r.Status != 422 || r.apiError(t).Code != "credential_required" {
			t.Fatalf("patch %v = %d %s", body, r.Status, r.Body)
		}
	}
	if r := c.do("PATCH", "/api/servers/"+prod.ID, map[string]any{"host": "127.0.0.1",
		"auth": map[string]any{"kind": "password", "password": "x"}}); r.Status != 422 || r.apiError(t).Code != "address_blocked" {
		t.Fatalf("patch to loopback = %d %s", r.Status, r.Body)
	}
	var upd ServerView
	r = c.must(200, "PATCH", "/api/servers/"+prod.ID, map[string]any{"host": "203.0.113.20", "port": 2200,
		"auth": map[string]any{"kind": "password", "password": "new-pass-zz"}}, &upd)
	noSecrets(t, r, "new-pass-zz")
	if upd.Host != "203.0.113.20" || upd.Port != 2200 || !upd.CredUpdatedAt.After(prod.CredUpdatedAt) {
		t.Fatalf("updated %+v", upd)
	}
	// The stored credential opens with the row's new values.
	sec, err := h.db.RemoteServerSecret(ctx, me.ID, prod.ID)
	if err != nil {
		t.Fatal(err)
	}
	cred, err := h.vault().OpenCredential(credvault.ServerAAD(me.ID, prod.ID, sec.AuthKind, sec.Host, sec.Port, sec.Username), sec.Sealed)
	if err != nil || cred.Password != "new-pass-zz" {
		t.Fatalf("stored credential %v", err)
	}
	// Switching to a private key; a name change alone needs no credential.
	c.must(200, "PATCH", "/api/servers/"+prod.ID, map[string]any{"auth": map[string]any{"kind": "private_key", "private_key": keyPEM, "passphrase": "pp"}}, &upd)
	if upd.AuthKind != "private_key" || !upd.HasPassphrase {
		t.Fatalf("switched %+v", upd)
	}
	c.must(200, "PATCH", "/api/servers/"+prod.ID, map[string]any{"name": "prod2"}, &upd)
	if r := c.do("PATCH", "/api/servers/"+prod.ID, map[string]any{"name": "key-box"}); r.Status != 409 {
		t.Fatalf("rename to taken = %d", r.Status)
	}

	c.must(204, "DELETE", "/api/servers/"+prod.ID, nil, nil)
	if r := c.do("GET", "/api/servers/"+prod.ID, nil); r.Status != 404 {
		t.Fatalf("after delete = %d", r.Status)
	}
	if n := countAudit(t, h, me.ID, "server_create"); n != 2 {
		t.Fatalf("audit server_create = %d", n)
	}
}

func countAudit(t *testing.T, h *harness, userID, action string) int {
	t.Helper()
	var n int64
	if err := h.db.Pool.Gorm(context.Background()).Table("at_audit_log").Where("user_id = ? AND action = ?", userID, action).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return int(n)
}

func execSQL(t *testing.T, h *harness, sql string, args ...any) {
	t.Helper()
	if _, err := store.Exec(context.Background(), h.db.Pool, sql, args...); err != nil {
		t.Fatal(err)
	}
}

func TestProjectServers(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	c, me := h.signUp("a@example.com")
	other, otherUser := h.signUp("b@example.com")
	execSQL(t, h, `INSERT INTO at_roles (id, user_id, tier, role, read_only, gate) VALUES
		('ro_t3a', $1, 'member', 'sx-dev', false, ''), ('ro_t3b', $1, 'member', 'sx-ops', false, ''),
		('ro_t3c', $1, 'member', 'sx-review', false, 'review'), ('ro_t3d', $1, 'member', 'sx-ro', true, '')`, me.ID)
	var prj ProjectView
	c.must(201, "POST", "/api/projects", map[string]any{"name": "P"}, &prj)
	var prod, staging, foreign ServerView
	c.must(201, "POST", "/api/servers", passwordServer("prod", "203.0.113.10", 22, "x"), &prod)
	c.must(201, "POST", "/api/servers", passwordServer("staging", "203.0.113.11", 22, "x"), &staging)
	other.must(201, "POST", "/api/servers", passwordServer("foreign", "203.0.113.12", 22, "x"), &foreign)

	var v ProjectServersView
	c.must(200, "GET", "/api/projects/"+prj.ID+"/servers", nil, &v)
	if len(v.Servers) != 0 || len(v.Available) != 2 {
		t.Fatalf("empty view %+v", v)
	}
	reasons := map[string]string{}
	for _, r := range v.Roles {
		reasons[r.Role] = r.Reason
		if r.Grantable != (r.Reason == "") {
			t.Errorf("role %+v", r)
		}
		if (r.Tier == "lead" && r.Reason != "lead") || (r.Tier == "coordinator" && r.Reason != "coordinator") {
			t.Errorf("tier role %+v", r)
		}
	}
	if reasons["sx-dev"] != "" || reasons["sx-review"] != "read_only" || reasons["sx-ro"] != "read_only" {
		t.Fatalf("reasons %v", reasons)
	}
	put := func(cl *client, status int, servers ...map[string]any) response {
		return cl.must(status, "PUT", "/api/projects/"+prj.ID+"/servers", map[string]any{"servers": servers}, nil)
	}
	grant := func(id string, roles ...string) map[string]any {
		return map[string]any{"server_id": id, "roles": roles}
	}
	for _, bad := range []string{"sx-review", "sx-ro", "nobody"} {
		if r := put(c, 422, grant(prod.ID, "sx-dev", bad)); r.apiError(t).Code != "role_not_grantable" {
			t.Errorf("grant %s: %s", bad, r.Body)
		}
	}
	// The lead and coordinator role templates every user has.
	roles, err := h.db.Roles(ctx, me.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range roles {
		if r.Tier != "member" {
			put(c, 422, grant(prod.ID, r.Role))
		}
	}
	put(c, 404, grant(foreign.ID, "sx-dev"))
	put(other, 404, grant(prod.ID, "sx-dev"))
	put(c, 422, grant(prod.ID, "sx-dev"), grant(prod.ID, "sx-ops"))
	if _, changed := h.ssh.take(); len(changed) != 0 {
		t.Fatalf("failed puts notified %v", changed)
	}
	put(c, 200, grant(prod.ID, "sx-ops", "sx-dev"), grant(staging.ID))
	c.must(200, "GET", "/api/projects/"+prj.ID+"/servers", nil, &v)
	if len(v.Servers) != 2 || v.Servers[0].Server.Name != "prod" || !slices.Equal(v.Servers[0].Roles, []string{"sx-dev", "sx-ops"}) ||
		len(v.Servers[1].Roles) != 0 || len(v.Available[0].Projects) != 1 {
		t.Fatalf("view %+v", v)
	}
	if _, changed := h.ssh.take(); !slices.Equal(changed, []string{prj.ID}) {
		t.Fatalf("changed %v", changed)
	}
	put(c, 200, grant(prod.ID, "sx-dev", "sx-ops"), grant(staging.ID)) // same: no notification
	if _, changed := h.ssh.take(); len(changed) != 0 {
		t.Fatalf("unchanged put notified %v", changed)
	}
	if r := other.do("GET", "/api/projects/"+prj.ID+"/servers", nil); r.Status != 404 {
		t.Fatalf("other get = %d", r.Status)
	}
	_ = otherUser

	// Taking sx-ops off prod revokes its certificates naming prod.
	cert, err := h.db.InsertSSHCert(ctx, store.SSHCert{SessionID: "s_x", OpenGen: 1, NodeID: "n", ProjectID: prj.ID, Role: "sx-ops",
		PubkeyFP: "SHA256:k", Principals: []string{"prod"}, ValidAfter: time.Now(), ValidBefore: time.Now().Add(10 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	put(c, 200, grant(prod.ID, "sx-dev"), grant(staging.ID))
	if revoked, changed := h.ssh.take(); !slices.Equal(revoked, []int64{cert.Serial}) || !slices.Equal(changed, []string{prj.ID}) {
		t.Fatalf("revoked %v changed %v", revoked, changed)
	}
	// Renaming and deleting a used server notify its projects.
	c.must(200, "PATCH", "/api/servers/"+staging.ID, map[string]any{"name": "stage"}, nil)
	if _, changed := h.ssh.take(); !slices.Equal(changed, []string{prj.ID}) {
		t.Fatalf("rename changed %v", changed)
	}
	c.must(204, "DELETE", "/api/servers/"+prod.ID, nil, nil)
	if _, changed := h.ssh.take(); !slices.Equal(changed, []string{prj.ID}) {
		t.Fatalf("delete changed %v", changed)
	}
	put(c, 200)
	c.must(200, "GET", "/api/projects/"+prj.ID+"/servers", nil, &v)
	if len(v.Servers) != 0 {
		t.Fatalf("after clearing %+v", v)
	}
}

func TestServerTestConnection(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	c, me := h.signUp("a@example.com")
	userKey, keyPEM := sshtest.NewKey(t, "pp")
	target := sshtest.Start(t, "deploy", "right-pw", userKey.PublicKey())

	// Loopback is blocked until an administrator allows it.
	if r := c.do("POST", "/api/servers", passwordServer("box", target.Host, target.Port, "right-pw")); r.Status != 422 {
		t.Fatalf("loopback = %d", r.Status)
	}
	if err := h.db.SaveSetting(ctx, sshdial.SettingKey, sshdial.AllowedSetting{CIDRs: []string{"127.0.0.1/32"}}); err != nil {
		t.Fatal(err)
	}
	var box, wrong, key ServerView
	c.must(201, "POST", "/api/servers", passwordServer("box", target.Host, target.Port, "right-pw"), &box)
	c.must(201, "POST", "/api/servers", passwordServer("wrong", target.Host, target.Port, "bad-pw"), &wrong)
	c.must(201, "POST", "/api/servers", map[string]any{"name": "key", "host": target.Host, "port": target.Port, "username": "deploy",
		"auth": map[string]any{"kind": "private_key", "private_key": keyPEM, "passphrase": "pp"}}, &key)

	test := func(id string) ServerTestResult {
		t.Helper()
		var res ServerTestResult
		r := c.must(200, "POST", "/api/servers/"+id+"/test", nil, &res)
		noSecrets(t, r, "right-pw", "bad-pw")
		return res
	}
	fp := ssh.FingerprintSHA256(target.HostKey())
	if res := test(box.ID); !res.OK || !res.HostKeyPinned || res.HostKeyFP != fp || res.HostKeyType != "ssh-ed25519" {
		t.Fatalf("first test %+v", res)
	}
	var got ServerView
	c.must(200, "GET", "/api/servers/"+box.ID, nil, &got)
	if got.HostKeyFP != fp || got.HostKeyConfirmedAt == nil {
		t.Fatalf("pinned %+v", got)
	}
	if res := test(box.ID); !res.OK || res.HostKeyPinned {
		t.Fatalf("second test %+v", res)
	}
	if res := test(key.ID); !res.OK {
		t.Fatalf("private key test %+v", res)
	}
	if res := test(wrong.ID); res.OK || res.Error != "auth_failed" || !res.HostKeyPinned {
		t.Fatalf("wrong password %+v", res)
	}

	// The target's host key changes: refused, no login, until the user resets the pin.
	logins := target.Logins.Load()
	target.RotateHostKey(t)
	res := test(box.ID)
	if res.OK || res.Error != "host_key_mismatch" || res.ExpectedHostFP != fp || res.HostKeyFP == fp {
		t.Fatalf("mismatch %+v", res)
	}
	if target.Logins.Load() != logins {
		t.Fatal("credential sent despite host key mismatch")
	}
	c.must(200, "POST", "/api/servers/"+box.ID+"/host-key/reset", nil, &got)
	if got.HostKeyFP != "" {
		t.Fatalf("after reset %+v", got)
	}
	newFP := ssh.FingerprintSHA256(target.HostKey())
	if res := test(box.ID); !res.OK || !res.HostKeyPinned || res.HostKeyFP != newFP {
		t.Fatalf("after reset test %+v", res)
	}

	// A host key written straight into the database (no valid MAC) is not trusted.
	execSQL(t, h, `UPDATE at_remote_servers SET host_key = $1 WHERE id = $2`, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(target.HostKey()))), key.ID)
	if res := test(key.ID); res.OK || res.Error != "host_key_invalid" {
		t.Fatalf("forged host key %+v", res)
	}
	// A credential that no longer matches the row (host changed behind the API) does not open.
	execSQL(t, h, `UPDATE at_remote_servers SET username = 'root' WHERE id = $1`, wrong.ID)
	if res := test(wrong.ID); res.OK || res.Error != "credential_unreadable" {
		t.Fatalf("moved credential %+v", res)
	}
	// Nothing listening.
	var closed ServerView
	c.must(201, "POST", "/api/servers", passwordServer("closed", "127.0.0.1", 1, "x"), &closed)
	if res := test(closed.ID); res.OK || (res.Error != "connect_failed" && res.Error != "timeout") {
		t.Fatalf("closed port %+v", res)
	}

	// Every test is in the server's access log.
	var page SshAuditPage
	c.must(200, "GET", "/api/servers/"+box.ID+"/audit?kind=test", nil, &page)
	if len(page.Items) != 4 || page.Items[0].Kind != "test" || page.Items[0].Employee != "user" {
		t.Fatalf("audit %d %+v", len(page.Items), page.Items)
	}
	if countAudit(t, h, me.ID, "server_host_key_pin") != 4 {
		t.Fatalf("pin audit = %d", countAudit(t, h, me.ID, "server_host_key_pin"))
	}

	// Rate limit: 10 tests per server per minute.
	var last response
	for i := 0; i < 10; i++ {
		last = c.do("POST", "/api/servers/"+closed.ID+"/test", nil)
	}
	if last.Status != 429 {
		t.Fatalf("rate limit = %d", last.Status)
	}
}

func TestServerAudit(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	c, me := h.signUp("a@example.com")
	other, otherUser := h.signUp("b@example.com")
	var prj, otherPrj ProjectView
	c.must(201, "POST", "/api/projects", map[string]any{"name": "P"}, &prj)
	other.must(201, "POST", "/api/projects", map[string]any{"name": "O"}, &otherPrj)
	var prod ServerView
	c.must(201, "POST", "/api/servers", passwordServer("prod", "203.0.113.10", 22, "x"), &prod)
	for i := 0; i < 5; i++ {
		kind, emp := store.SSHAuditExec, "dev-1"
		if i%2 == 1 {
			kind, emp = store.SSHAuditDenied, "dev-2"
		}
		if _, err := h.db.InsertSSHAudit(ctx, store.SSHAudit{UserID: me.ID, ProjectID: prj.ID, ServerID: prod.ID, ServerName: "prod",
			Employee: emp, Kind: kind, Command: fmt.Sprintf("echo %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.db.InsertSSHAudit(ctx, store.SSHAudit{UserID: otherUser.ID, ProjectID: otherPrj.ID, ServerID: prod.ID, Kind: "exec"}); err != nil {
		t.Fatal(err)
	}
	var page SshAuditPage
	var seen []string
	url := "/api/projects/" + prj.ID + "/server-audit?limit=2"
	for i := 0; i < 5; i++ {
		c.must(200, "GET", url, nil, &page)
		for _, it := range page.Items {
			seen = append(seen, it.Command)
		}
		if page.NextCursor == nil {
			break
		}
		url = "/api/projects/" + prj.ID + "/server-audit?limit=2&cursor=" + *page.NextCursor
	}
	if !slices.Equal(seen, []string{"echo 4", "echo 3", "echo 2", "echo 1", "echo 0"}) {
		t.Fatalf("paged %v", seen)
	}
	c.must(200, "GET", "/api/projects/"+prj.ID+"/server-audit?kind=denied&employee=dev-2", nil, &page)
	if len(page.Items) != 2 {
		t.Fatalf("filtered %d", len(page.Items))
	}
	c.must(200, "GET", "/api/servers/"+prod.ID+"/audit?employee=dev-1", nil, &page)
	if len(page.Items) != 3 {
		t.Fatalf("server audit %d", len(page.Items))
	}
	if r := c.do("GET", "/api/servers/"+prod.ID+"/audit?cursor=junk", nil); r.Status != 400 {
		t.Fatalf("bad cursor = %d", r.Status)
	}
	if r := other.do("GET", "/api/projects/"+prj.ID+"/server-audit", nil); r.Status != 404 {
		t.Fatalf("other project audit = %d", r.Status)
	}
	if r := other.do("GET", "/api/servers/"+prod.ID+"/audit", nil); r.Status != 404 {
		t.Fatalf("other server audit = %d", r.Status)
	}
}
