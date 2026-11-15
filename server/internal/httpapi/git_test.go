package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type repoPage struct {
	Items      []RepoItem `json:"items"`
	NextCursor *string    `json:"next_cursor"`
}

func TestRepoAndBranchListing(t *testing.T) {
	h := newHarness(t)
	c, _ := h.signUp("a@example.com")
	var conn GitConnectionView
	c.must(201, "POST", "/api/git/connections", map[string]string{"token": "ghp_good"}, &conn)
	c.must(201, "POST", "/api/projects", map[string]any{"name": "R", "repo": "o/r", "git_connection_id": conn.ID}, nil)
	var page repoPage
	c.must(200, "GET", "/api/git/connections/"+conn.ID+"/repos?limit=2", nil, &page)
	if len(page.Items) != 2 || page.NextCursor == nil || page.Items[0].FullName != "o/r" {
		t.Fatalf("page1 %+v", page)
	}
	if len(page.Items[0].UsedByProjects) != 1 || page.Items[0].UsedByProjects[0].Name != "R" {
		t.Fatalf("used_by %+v", page.Items[0])
	}
	c.must(200, "GET", "/api/git/connections/"+conn.ID+"/repos?limit=2&cursor="+*page.NextCursor, nil, &page)
	if len(page.Items) != 1 || page.Items[0].FullName != "other/tool" || page.NextCursor != nil {
		t.Fatalf("page2 %+v", page)
	}
	c.must(200, "GET", "/api/git/connections/"+conn.ID+"/repos?q=CLI", nil, &page)
	if len(page.Items) != 1 || page.Items[0].FullName != "other/tool" {
		t.Fatalf("search %+v", page)
	}
	var branches []string
	c.must(200, "GET", "/api/git/connections/"+conn.ID+"/branches?repo=o/r&q=feat", nil, &branches)
	if len(branches) != 1 || branches[0] != "feature/x" {
		t.Fatalf("branches %v", branches)
	}
	other, _ := h.signUp("b@example.com")
	if r := other.do("GET", "/api/git/connections/"+conn.ID+"/repos", nil); r.Status != 404 {
		t.Fatalf("other user = %d", r.Status)
	}
	c.must(204, "DELETE", "/api/git/connections/"+conn.ID, nil, nil)
	var list []GitConnectionView
	c.must(200, "GET", "/api/git/connections", nil, &list)
	if len(list) != 0 {
		t.Fatal("not deleted")
	}
}

func TestGitHubAppFlow(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.signUp("a@example.com")
	user, _ := h.signUp("b@example.com")
	if r := user.do("POST", "/api/admin/github-app/manifest", map[string]string{}); r.Status != 403 {
		t.Fatalf("non-admin manifest = %d", r.Status)
	}
	var form struct {
		Action   string `json:"action"`
		Manifest string `json:"manifest"`
	}
	admin.must(200, "POST", "/api/admin/github-app/manifest", map[string]string{"organization": "acme"}, &form)
	if !strings.HasPrefix(form.Action, "https://github.com/organizations/acme/settings/apps/new?state=") ||
		!strings.Contains(form.Manifest, `"checks":"read"`) || !strings.Contains(form.Manifest, "http://console.test/api/git/github-app/created") {
		t.Fatalf("manifest %+v", form)
	}
	state := mustURL(form.Action).Query().Get("state")

	// GitHub redirects back without our cookie; the signed state identifies the admin.
	anon := h.client()
	r := anon.do("GET", "/api/git/github-app/created?code=good-code&state="+url.QueryEscape(state), nil)
	if r.Status != http.StatusSeeOther || r.Header.Get("Location") != "/setup/github?github_app=agent-teams-test" {
		t.Fatalf("created = %d %s", r.Status, r.Header.Get("Location"))
	}
	r = anon.do("GET", "/api/git/github-app/created?code=good-code&state=forged.1.x", nil)
	if !strings.Contains(r.Header.Get("Location"), "error=") {
		t.Fatalf("forged state accepted: %s", r.Header.Get("Location"))
	}
	var status struct {
		Configured bool       `json:"configured"`
		App        *AppPublic `json:"app"`
	}
	user.must(200, "GET", "/api/git/github-app", nil, &status)
	if !status.Configured || !status.App.WebhookActive || status.App.Owner != "boss" {
		t.Fatalf("status %+v", status)
	}

	r = user.do("GET", "/api/git/github-app/install", nil)
	loc := r.Header.Get("Location")
	if r.Status != http.StatusSeeOther || !strings.HasPrefix(loc, "https://github.com/apps/agent-teams-test/installations/new?state=") {
		t.Fatalf("install = %d %s", r.Status, loc)
	}
	installState := mustURL(loc).Query().Get("state")
	r = anon.do("GET", "/api/git/github-app/installed?installation_id=42&code=oauth-code&setup_action=install&state="+url.QueryEscape(installState), nil)
	if r.Header.Get("Location") != "/settings/git?connected=GitHub+App+%C2%B7+octo-org" {
		t.Fatalf("installed = %s", r.Header.Get("Location"))
	}
	var conns []GitConnectionView
	user.must(200, "GET", "/api/git/connections", nil, &conns)
	if len(conns) != 1 || conns[0].Kind != "app" || *conns[0].InstallationID != 42 || !conns[0].NeedsPermissionApproval {
		t.Fatalf("connections %+v", conns)
	}
	if *conns[0].InstallationSettingsURL != "https://github.com/organizations/octo-org/settings/installations/42" {
		t.Fatalf("settings url %s", *conns[0].InstallationSettingsURL)
	}
	var refreshed GitConnectionView
	user.must(200, "POST", "/api/git/connections/"+conns[0].ID+"/refresh", nil, &refreshed)
	if refreshed.NeedsPermissionApproval || refreshed.Permissions["checks"] != "read" {
		t.Fatalf("refresh %+v", refreshed)
	}
	var page repoPage
	user.must(200, "GET", "/api/git/connections/"+conns[0].ID+"/repos", nil, &page)
	if len(page.Items) != 1 || page.Items[0].FullName != "octo-org/app" {
		t.Fatalf("installation repos %+v", page)
	}
	// Installing again is idempotent.
	r = user.do("GET", "/api/git/github-app/installed?installation_id=42", nil)
	if !strings.Contains(r.Header.Get("Location"), "connected=") {
		t.Fatalf("reinstall = %s", r.Header.Get("Location"))
	}

	body := `{"action":"new_permissions_accepted","installation":{"id":42,"account":{"login":"octo-org","type":"Organization"},"permissions":{"contents":"write"}}}`
	if r := postWebhook(t, h, "installation", body, "wrong"); r.StatusCode != 401 {
		t.Fatalf("bad signature = %d", r.StatusCode)
	}
	if r := postWebhook(t, h, "installation", body, "whsecret"); r.StatusCode != 202 {
		t.Fatalf("webhook = %d", r.StatusCode)
	}
	user.must(200, "GET", "/api/git/connections", nil, &conns)
	if !conns[0].NeedsPermissionApproval {
		t.Fatal("webhook did not refresh permissions")
	}

	admin.must(204, "DELETE", "/api/admin/github-app", nil, nil)
	user.must(200, "GET", "/api/git/github-app", nil, &status)
	if status.Configured {
		t.Fatal("app still configured")
	}
}

func postWebhook(t *testing.T, h *harness, event, body, secret string) *http.Response {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	req, _ := http.NewRequest("POST", h.srv.URL+"/api/git/github-app/webhook", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}
