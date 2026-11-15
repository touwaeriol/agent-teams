package engine_test

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/core"
)

// TestRESTSurface walks the read endpoints and the smaller writes of api.md §3.2–§3.5, §3.11, §3.13.
func TestRESTSurface(t *testing.T) {
	tm := startTeam(t)
	b, pid, tid := tm.b, tm.projectID, tm.thread.ID
	tm.dev.mustCall("send_message", map[string]string{"to": "lead", "text": "表单字段确认了"})
	tm.lead.expectSend("[Message from dev-1]")
	tm.dev.mustCall("report_progress", map[string]string{"text": "写到一半"})

	var mail struct{ Items []core.MailView }
	b.must(200, "GET", "/api/threads/"+tid+"/mail", nil, &mail)
	if len(mail.Items) != 1 || mail.Items[0].Recipient != "lead-1" {
		t.Fatalf("mail %+v", mail)
	}
	var acts struct{ Items []core.Activity }
	b.must(200, "GET", "/api/threads/"+tid+"/activity", nil, &acts)
	if len(acts.Items) == 0 || !strings.Contains(fmt.Sprint(acts.Items), "写到一半") {
		t.Fatalf("activity %+v", acts)
	}
	var members []core.EmployeeBrief
	b.must(200, "GET", "/api/threads/"+tid+"/members", nil, &members)
	if len(members) != 3 || members[0].Name != "lead-1" || members[0].Node == nil || !members[0].Node.Online {
		t.Fatalf("members %+v", members)
	}
	var mains []core.MainTaskView
	b.must(200, "GET", "/api/threads/"+tid+"/main-tasks", nil, &mains)
	b.must(200, "PATCH", "/api/threads/"+tid+"/main-tasks/"+mains[0].ID, map[string]string{"status": "done"}, &mains)
	if mains[0].Status != "done" || mains[0].AutoDone {
		t.Fatalf("manual tick %+v", mains)
	}
	b.must(200, "GET", "/api/threads/"+tid+"/merge-queue", nil, nil)
	b.must(200, "GET", "/api/threads/"+tid+"/checks", nil, nil)
	b.must(202, "POST", "/api/threads/"+tid+"/members/dev-1/interrupt", nil, nil)
	tm.waitFrame(protocol.TypeSessionInterrupt, tm.dev)
	b.must(202, "POST", "/api/threads/"+tid+"/members/dev-1/message", map[string]string{"content": "先做校验"}, nil)
	tm.dev.expectSend("[用户直接对你说] 先做校验")
	if code, _ := b.do("POST", "/api/threads/"+tid+"/members/nobody/message", map[string]string{"content": "x"}); code != 404 {
		t.Fatalf("unknown member = %d", code)
	}

	deliverables(t, tm)
	officeViews(t, tm)

	var search struct {
		Threads  []core.ThreadView `json:"threads"`
		Messages []any             `json:"messages"`
		Tasks    []core.TaskView   `json:"tasks"`
	}
	b.must(200, "GET", "/api/projects/"+pid+"/search?q="+url.QueryEscape("do"), nil, &search)
	if len(search.Threads) != 1 || len(search.Tasks) != 1 {
		t.Fatalf("search %+v", search)
	}
	var overview struct {
		Project struct{ ID string } `json:"project"`
		Threads []core.ThreadView   `json:"threads"`
		Nodes   []struct{ Online bool }
	}
	b.must(200, "GET", "/api/projects/"+pid+"/overview", nil, &overview)
	if overview.Project.ID != pid || len(overview.Threads) != 1 || len(overview.Nodes) != 1 || !overview.Nodes[0].Online {
		t.Fatalf("overview %+v", overview)
	}
	if code, _ := b.do("GET", "/api/projects/"+pid+"/ci/detect", nil); code != 409 {
		t.Fatalf("ci detect without repo = %d", code)
	}
	legacy(t, tm)
	other := tm.w.signUp("stranger@example.com")
	if code, _ := other.do("GET", "/api/threads/"+tid, nil); code != 404 {
		t.Fatalf("another user's thread = %d", code)
	}
}

func (tm *team) waitFrame(typ string, a *fakeAgent) {
	tm.w.t.Helper()
	ctx, cancel := tm.w.waitCtx()
	defer cancel()
	if _, err := tm.n.Wait(ctx, typ, a.mine); err != nil {
		tm.w.t.Fatal(err)
	}
}

func deliverables(t *testing.T, tm *team) {
	b, pid := tm.b, tm.projectID
	tm.dev.mustCall("publish_deliverable", map[string]string{"kind": "doc", "title": "设计文档", "url": "https://docs/x"})
	var d core.DeliverableView
	b.must(201, "POST", "/api/projects/"+pid+"/deliverables", map[string]any{"kind": "design", "title": "首页稿", "url": "https://f/x", "thread_no": 1}, &d)
	b.must(422, "POST", "/api/projects/"+pid+"/deliverables", map[string]any{"kind": "doc", "title": "x", "url": "javascript:alert(1)"}, nil)
	if d.ThreadNo == nil || *d.ThreadNo != 1 {
		t.Fatalf("deliverable %+v", d)
	}
	var page struct{ Items []core.DeliverableView }
	b.must(200, "GET", "/api/projects/"+pid+"/deliverables?q="+url.QueryEscape("文档"), nil, &page)
	if len(page.Items) != 1 || page.Items[0].PublishedBy != "dev-1" {
		t.Fatalf("search deliverables %+v", page)
	}
	b.must(200, "PATCH", "/api/deliverables/"+d.ID, map[string]string{"title": "首页稿 v2"}, &d)
	b.must(204, "DELETE", "/api/deliverables/"+d.ID, nil, nil)
	var msg core.MessageView
	b.must(202, "POST", "/api/projects/"+pid+"/deliverables/request", map[string]string{"kind": "slides", "brief": "投资人介绍"}, &msg)
	if msg.Content != "做一个幻灯片：投资人介绍" || msg.Meta["request_kind"] != "slides" {
		t.Fatalf("request %+v", msg)
	}
	expectOpen(t, tm.w, tm.n, "coordinator")
}

func officeViews(t *testing.T, tm *team) {
	b, pid := tm.b, tm.projectID
	var snap struct {
		LayoutID  string               `json:"layout_id"`
		Employees []core.EmployeeBrief `json:"employees"`
		Board     struct {
			ByThread map[string]map[string]int `json:"by_thread"`
		} `json:"board"`
		LastEventID int64 `json:"last_event_id"`
	}
	b.must(200, "GET", "/api/projects/"+pid+"/office", nil, &snap)
	if snap.LayoutID != "default" || len(snap.Employees) < 3 || snap.LastEventID == 0 || snap.Board.ByThread["1"]["running"] != 1 {
		t.Fatalf("office %+v", snap)
	}
	from := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	to := time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
	var replay struct {
		Events []struct{ Type string } `json:"events"`
	}
	b.must(200, "GET", "/api/projects/"+pid+"/office/replay?from="+from+"&to="+to, nil, &replay)
	if len(replay.Events) == 0 {
		t.Fatal("empty replay")
	}
	if code, _ := b.do("GET", "/api/projects/"+pid+"/office/replay?from="+from+"&to="+time.Now().Add(7*time.Hour).UTC().Format(time.RFC3339), nil); code != 422 {
		t.Fatalf("replay over 6h = %d", code)
	}
	var card struct {
		Name        string         `json:"name"`
		CurrentTask *core.TaskView `json:"current_task"`
		Session     *struct{ State string }
	}
	devID := ""
	for _, e := range snap.Employees {
		if e.Name == "dev-1" {
			devID = e.ID
		}
	}
	b.must(200, "GET", "/api/employees/"+devID, nil, &card)
	if card.CurrentTask == nil || card.CurrentTask.ID != "t1" || card.Session == nil {
		t.Fatalf("card %+v", card)
	}
}

func legacy(t *testing.T, tm *team) {
	b := tm.b
	var convs []struct{ ID, Kind string }
	b.must(200, "GET", "/api/conversations?kind=thread", nil, &convs)
	if len(convs) != 1 || convs[0].Kind != "thread" {
		t.Fatalf("conversations %+v", convs)
	}
	var tasks []struct{ ID, Status string }
	b.must(200, "GET", "/api/conversations/"+tm.thread.ID+"/tasks", nil, &tasks)
	if len(tasks) != 1 || tasks[0].ID != "t1" {
		t.Fatalf("legacy tasks %+v", tasks)
	}
	b.must(200, "GET", "/api/conversations/"+tm.thread.ID+"/plan", nil, nil)
	b.must(200, "GET", "/api/conversations/"+tm.thread.ID+"/mail", nil, nil)
	if code, _ := b.do("POST", "/api/conversations", map[string]string{"title": "x"}); code != 409 {
		t.Fatalf("create v1 conversation = %d", code)
	}
	if _, err := tm.w.e.Core.Pool.Exec(tm.w.ctx, `INSERT INTO at_conversations (id, user_id, title, project_id) SELECT 'oldchat00001', user_id, 'old', id FROM at_projects WHERE id = $1`, tm.projectID); err != nil {
		t.Fatal(err)
	}
	if code, body := b.do("POST", "/api/conversations/oldchat00001/messages", map[string]string{"content": "hi"}); code != 409 || !strings.Contains(string(body), "legacy_read_only") {
		t.Fatalf("write to old chat = %d %s", code, body)
	}
	var page struct{ Items []core.MessageView }
	b.must(200, "GET", "/api/conversations/oldchat00001/messages", nil, &page)
}
