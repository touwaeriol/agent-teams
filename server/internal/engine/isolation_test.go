package engine_test

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/timers"
)

// owned is one user's rows that another user must never reach.
type owned struct {
	project, thread, coordinator, question, session, mainTask string
	deliverable, schedule, memory, attachment, node           string
}

// ownResources gives the team's user one of everything a path parameter can name.
func ownResources(t *testing.T, tm *team) owned {
	t.Helper()
	b, pid := tm.b, tm.projectID
	o := owned{project: pid, thread: tm.thread.ID, session: tm.dev.session}
	var coord struct {
		ConversationID string `json:"conversation_id"`
	}
	b.must(200, "GET", "/api/projects/"+pid+"/coordinator", nil, &coord)
	o.coordinator = coord.ConversationID
	var d core.DeliverableView
	b.must(201, "POST", "/api/projects/"+pid+"/deliverables", map[string]any{"kind": "doc", "title": "文档", "url": "https://docs/x"}, &d)
	o.deliverable = d.ID
	var s timers.Schedule
	b.must(201, "POST", "/api/projects/"+pid+"/schedules", map[string]any{"name": "早报", "cron": "0 9 * * *", "message": "hi"}, &s)
	o.schedule = s.ID
	var m struct{ Name string }
	b.must(200, "PUT", "/api/projects/"+pid+"/memory/dev-1", map[string]string{"description": "约定", "content": "use pnpm"}, &m)
	o.memory = m.Name
	var att struct{ ID string }
	b.must(201, "POST", "/api/projects/"+pid+"/attachments", map[string]string{"name": "a.png", "data": base64.StdEncoding.EncodeToString(onePixelPNG)}, &att)
	o.attachment = att.ID
	var nodes []struct{ ID string }
	b.must(200, "GET", "/api/nodes", nil, &nodes)
	o.node = nodes[0].ID
	var mains []core.MainTaskView
	b.must(200, "GET", "/api/threads/"+o.thread+"/main-tasks", nil, &mains)
	o.mainTask = mains[0].ID
	tm.dev.startCall("ask_user", `{"question":"哪个？","options":["A","B"]}`)
	var waiting struct {
		Questions []core.QuestionView `json:"questions"`
	}
	eventually(t, "question card", func() bool {
		b.must(200, "GET", "/api/projects/"+pid+"/waiting", nil, &waiting)
		return len(waiting.Questions) == 1
	})
	o.question = waiting.Questions[0].ID
	return o
}

// idFor is the owner's id for an ":id" parameter, chosen by the route's collection.
func (o owned) idFor(route string) string {
	for prefix, id := range map[string]string{"/api/deliverables/": o.deliverable, "/api/schedules/": o.schedule,
		"/api/attachments/": o.attachment, "/api/nodes/": o.node,
		"/nodes/attachments/": o.attachment} {
		if strings.HasPrefix(route, prefix) {
			return id
		}
	}
	return "missing"
}

// fill puts the owner's ids into a gin route template.
func (o owned) fill(route string) string {
	parts := strings.Split(route, "/")
	for i, p := range parts {
		parts[i] = o.param(route, p)
	}
	return strings.Join(parts, "/")
}

func (o owned) param(route, p string) string {
	switch p {
	case ":pid":
		return o.project
	case ":tid":
		return o.thread
	case ":cid":
		return o.thread
	case ":qid":
		return o.question
	case ":sid":
		return o.session
	case ":mid":
		return o.mainTask
	case ":task":
		return "t1"
	case ":name":
		return "dev-1"
	case ":id":
		return o.idFor(route)
	}
	return p
}

// sweepable: user routes that name one of the owner's rows by a path parameter.
func sweepable(method, route string) bool {
	if !strings.Contains(route, ":") || strings.HasPrefix(route, "/api/admin/") || strings.HasPrefix(route, "/nodes/") {
		return false
	}
	return !strings.HasPrefix(route, "/node/dist/") && !strings.HasPrefix(route, "/api/git/") &&
		!strings.HasPrefix(route, "/api/me/") && !strings.HasPrefix(route, "/api/employees/")
}

// TestOwnershipIsolation: a second user calling every route that names the first user's
// project, thread, conversation, question, session, deliverable, schedule, memory, image or
// node gets 404 (or a refusal of the body) and never changes anything; lists and the node
// attachment route do not leak either.
func TestOwnershipIsolation(t *testing.T) {
	tm := startTeam(t)
	o := ownResources(t, tm)
	intruder := tm.w.signUp("intruder@example.com")
	swept := 0
	for _, r := range tm.w.h.Routes() {
		if !sweepable(r.Method, r.Path) {
			continue
		}
		swept++
		path := o.fill(r.Path) + queryFor(r.Path)
		code, raw := intruder.do(r.Method, path, map[string]any{})
		if !refused(r.Method, code) {
			t.Errorf("intruder %s %s = %d %s", r.Method, path, code, raw)
		}
	}
	if swept < 70 {
		t.Fatalf("only %d routes swept", swept)
	}
	ownerStillHas(t, tm, o)
	listsDoNotLeak(t, intruder, o)
	nodeCannotFetchOthersImage(t, tm, intruder, o)
}

// queryFor adds the required query parameters of a route, so validation never answers first.
func queryFor(route string) string {
	if strings.HasSuffix(route, "/office/replay") {
		return "?from=2026-01-01T00:00:00Z&to=2026-01-02T00:00:00Z"
	}
	if strings.Contains(route, "/projects/:pid/files") {
		return "?path=data/a.csv"
	}
	return ""
}

// refused: reads must be 404/403; writes may also be refused for their (empty) body.
func refused(method string, code int) bool {
	if code == http.StatusNotFound || code == http.StatusForbidden {
		return true
	}
	return method != "GET" && (code == http.StatusUnprocessableEntity || code == http.StatusBadRequest)
}

func ownerStillHas(t *testing.T, tm *team, o owned) {
	t.Helper()
	b := tm.b
	var th core.ThreadView
	b.must(200, "GET", "/api/threads/"+o.thread, nil, &th)
	if th.Status == "archived" {
		t.Fatalf("thread changed: %+v", th)
	}
	for _, path := range []string{"/api/projects/" + o.project, "/api/attachments/" + o.attachment,
		"/api/deliverables/" + o.deliverable + "/comments", "/api/conversations/" + o.coordinator + "/messages"} {
		b.must(200, "GET", path, nil, nil)
	}
	var mem []struct{ Name string }
	b.must(200, "GET", "/api/projects/"+o.project+"/memory", nil, &mem)
	var sch []timers.Schedule
	b.must(200, "GET", "/api/projects/"+o.project+"/schedules", nil, &sch)
	var nodes []struct{ ID string }
	b.must(200, "GET", "/api/nodes", nil, &nodes)
	if len(mem) != 2 || len(sch) != 1 || len(nodes) != 1 { // MEMORY.md + dev-1.md
		t.Fatalf("owner lost rows: memory %d schedules %d nodes %d", len(mem), len(sch), len(nodes))
	}
	var waiting struct {
		Questions []core.QuestionView `json:"questions"`
	}
	b.must(200, "GET", "/api/projects/"+o.project+"/waiting", nil, &waiting)
	if len(waiting.Questions) != 1 || waiting.Questions[0].Status != "open" {
		t.Fatalf("question changed: %+v", waiting.Questions)
	}
}

func listsDoNotLeak(t *testing.T, intruder *browser, o owned) {
	t.Helper()
	for _, path := range []string{"/api/projects", "/api/nodes"} {
		code, raw := intruder.do("GET", path, nil)
		if code != 200 {
			t.Fatalf("intruder %s = %d", path, code)
		}
		for _, id := range []string{o.project, o.node, o.thread, o.coordinator} {
			if strings.Contains(string(raw), id) {
				t.Errorf("%s leaks %s", path, id)
			}
		}
	}
}

// nodeCannotFetchOthersImage: the intruder's own daemon token cannot read the owner's image.
func nodeCannotFetchOthersImage(t *testing.T, tm *team, intruder *browser, o owned) {
	t.Helper()
	var node nodeCreated
	intruder.must(201, "POST", "/api/nodes", map[string]string{"name": "mine"}, &node)
	get := func(token string) int {
		req, _ := http.NewRequest("GET", tm.w.srv.URL+"/nodes/attachments/"+o.attachment, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := get(node.Token); code != 404 {
		t.Fatalf("intruder node on owner's image = %d", code)
	}
	if code := get(""); code != 401 {
		t.Fatalf("no token = %d", code)
	}
	if code := get("bogus"); code != 401 {
		t.Fatalf("bogus token = %d", code)
	}
	if code := get(tm.token); code != 200 {
		t.Fatalf("owner node = %d", code)
	}
}

// csrfExempt are the writes a signed-in browser may send without X-CSRF-Token.
var csrfExempt = map[string]bool{"/auth/login": true, "/auth/register": true, "/api/git/github-app/webhook": true}

// TestCSRFOnEveryWrite: every non-GET route refuses a cookie session without X-CSRF-Token
// (403 csrf) before its handler runs; the same session with the header writes.
func TestCSRFOnEveryWrite(t *testing.T) {
	tm := startTeam(t)
	o := ownResources(t, tm)
	checked := 0
	for _, r := range tm.w.h.Routes() {
		if r.Method == "GET" || r.Method == "HEAD" || csrfExempt[r.Path] {
			continue
		}
		checked++
		path := o.fill(r.Path)
		code, raw := tm.b.send(r.Method, path, map[string]any{}, false)
		if code != http.StatusForbidden || !strings.Contains(string(raw), `"code":"csrf"`) {
			t.Errorf("%s %s without CSRF = %d %s", r.Method, path, code, raw)
		}
	}
	if checked < 60 {
		t.Fatalf("only %d writes checked", checked)
	}
	code, _ := tm.b.send("PATCH", "/api/projects/"+o.project, map[string]any{"name": "renamed"}, true)
	if code != http.StatusOK {
		t.Fatalf("write with CSRF = %d", code)
	}
}
