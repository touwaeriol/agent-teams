package engine_test

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/approvals"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
)

// fakeAgent drives one employee's session on the fake node.
type fakeAgent struct {
	t       *testing.T
	w       *world
	node    *nodetest.Node
	session string
	gen     int
	events  int
}

// expectOpen waits for session.open of an employee, answers session.opened with the
// tools the role allows and waits for session.release.
func expectOpen(t *testing.T, w *world, n *nodetest.Node, name string) (*fakeAgent, protocol.SessionOpen) {
	t.Helper()
	ctx, cancel := w.waitCtx()
	defer cancel()
	f, err := n.Wait(ctx, protocol.TypeSessionOpen, func(f nodetest.Frame) bool { return strings.Contains(string(f.Body), `"name":"`+name+`"`) })
	if err != nil {
		t.Fatal(err)
	}
	var open protocol.SessionOpen
	if err := f.Decode(&open); err != nil {
		t.Fatal(err)
	}
	a := &fakeAgent{t: t, w: w, node: n, session: string(open.SessionID), gen: int(open.OpenGen)}
	a.opened(open)
	return a, open
}

func (a *fakeAgent) opened(open protocol.SessionOpen) {
	tools := []string{}
	for _, p := range open.PlatformTools {
		tools = append(tools, "mcp__platform__"+p.Name)
	}
	if open.Claude != nil {
		tools = append(tools, open.Claude.Tools...)
	}
	sdk := "sdk-" + a.session
	gen := protocol.OpenGen(a.gen)
	a.send(protocol.TypeSessionOpened, protocol.SessionOpened{SessionID: protocol.SessionID(a.session), OpenGen: gen, SDKSessionID: &sdk,
		Init: &protocol.SessionInit{Tools: tools}})
	ctx, cancel := a.w.waitCtx()
	defer cancel()
	if _, err := a.node.Wait(ctx, protocol.TypeSessionRelease, a.mine); err != nil {
		a.t.Fatal(err)
	}
}

func (a *fakeAgent) mine(f nodetest.Frame) bool {
	return strings.Contains(string(f.Body), `"session_id":"`+a.session+`"`)
}

func (a *fakeAgent) send(typ string, body any) int {
	a.t.Helper()
	ctx, cancel := a.w.waitCtx()
	defer cancel()
	seq, err := a.node.Send(ctx, typ, body)
	if err != nil {
		a.t.Fatal(err)
	}
	if err := a.node.WaitAck(ctx, seq); err != nil {
		_ = pprof.Lookup("goroutine").WriteTo(os.Stderr, 1)
		a.t.Fatal(err)
	}
	return seq
}

// call invokes a platform tool and returns its tool.result.
func (a *fakeAgent) call(tool string, args any) protocol.ToolResult {
	a.t.Helper()
	callID := fmt.Sprintf("tc_%s_%d", tool, time.Now().UnixNano())
	raw, _ := json.Marshal(args)
	gen := protocol.OpenGen(a.gen)
	a.send(protocol.TypeToolCall, protocol.ToolCall{CallID: protocol.CallID(callID), SessionID: protocol.SessionID(a.session),
		OpenGen: &gen, Tool: tool, Args: raw})
	return a.result(callID)
}

func (a *fakeAgent) result(callID string) protocol.ToolResult {
	a.t.Helper()
	ctx, cancel := a.w.waitCtx()
	defer cancel()
	f, err := a.node.Wait(ctx, protocol.TypeToolResult, func(f nodetest.Frame) bool { return strings.Contains(string(f.Body), callID) })
	if err != nil {
		a.t.Fatal(err)
	}
	var res protocol.ToolResult
	_ = f.Decode(&res)
	return res
}

func (a *fakeAgent) mustCall(tool string, args any) protocol.ToolResult {
	a.t.Helper()
	res := a.call(tool, args)
	if !res.Ok {
		a.t.Fatalf("%s failed: %+v", tool, res.Content)
	}
	return res
}

// expectSend waits for a session.send to this session whose text contains needle and confirms delivery.
func (a *fakeAgent) expectSend(needle string) protocol.SessionSend {
	a.t.Helper()
	ctx, cancel := a.w.waitCtx()
	defer cancel()
	f, err := a.node.Wait(ctx, protocol.TypeSessionSend, func(f nodetest.Frame) bool { return a.mine(f) && strings.Contains(string(f.Body), needle) })
	if err != nil {
		a.t.Fatal(err)
	}
	var s protocol.SessionSend
	_ = f.Decode(&s)
	a.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: s.SessionID, MsgID: s.MsgID})
	return s
}

func (a *fakeAgent) event(kind string, fill func(*protocol.SessionEvent)) {
	a.events++
	ev := protocol.SessionEvent{SessionID: protocol.SessionID(a.session), EventNo: a.events, Kind: protocol.SessionEventKind(kind), At: time.Now().UTC()}
	if fill != nil {
		fill(&ev)
	}
	a.send(protocol.TypeSessionEvent, ev)
}

func text(res protocol.ToolResult) string {
	if len(res.Content) == 0 {
		return ""
	}
	return res.Content[0].Text
}

type nodeCreated struct {
	Node  struct{ ID string } `json:"node"`
	Token string              `json:"token"`
}

// TestThreadEndToEnd runs: user message → coordinator open_thread → lead plans and assigns →
// member claims, asks for approval (answered through the API) and completes → lead is told →
// everything visible on the project SSE stream.
func TestThreadEndToEnd(t *testing.T) {
	w := newWorld(t, nil)
	b := w.signUp("boss@example.com")
	var node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "fake"}, &node)
	b.must(200, "GET", "/api/roles", nil, nil)
	askMembers(t, w)
	var project struct{ ID string }
	b.must(201, "POST", "/api/projects", map[string]any{"name": "demo", "context": "a demo project"}, &project)
	// approvalFlow drives the user's card; lead review has its own tests (questions_test.go).
	b.must(200, "PATCH", "/api/projects/"+project.ID, map[string]any{"delivery": map[string]any{"approval_routing": "user"}}, nil)
	n := w.connect(node.Token, nodetest.Options{})
	if n.Welcome.NodeID != node.Node.ID || n.Welcome.Protocol != 2 {
		t.Fatalf("welcome %+v", n.Welcome)
	}
	_, err := n.Send(w.ctx, protocol.TypeSessionsReport, protocol.SessionsReport{Sessions: []protocol.SessionReportItem{}})
	if err != nil {
		t.Fatal(err)
	}
	events := b.stream("/api/projects/"+project.ID+"/events", "0")

	var coord struct {
		ConversationID string `json:"conversation_id"`
	}
	b.must(200, "GET", "/api/projects/"+project.ID+"/coordinator", nil, &coord)
	b.must(202, "POST", "/api/conversations/"+coord.ConversationID+"/messages", map[string]any{"content": "给项目加一个登录页"}, nil)

	coordinator, open := expectOpen(t, w, n, "coordinator")
	assertCoordinatorSpec(t, open)
	if open.FirstMessage == nil || !strings.Contains(open.FirstMessage.Text, "登录页") {
		t.Fatalf("first message %+v", open.FirstMessage)
	}
	res := coordinator.mustCall("open_thread", map[string]any{"title": "Login page", "brief": "做一个登录页，带表单校验"})
	var opened protocol.OpenThreadResult
	_ = json.Unmarshal(*res.Structured, &opened)
	if opened.No != 1 || opened.Branch != "thread/1-login-page" {
		t.Fatalf("open_thread result %+v", opened)
	}
	events.wait(t, "thread", `"no":1`, `"status":"running"`)

	lead, leadOpen := expectOpen(t, w, n, "lead-1")
	assertLeadSpec(t, leadOpen)
	if !strings.Contains(leadOpen.FirstMessage.Text, "做一个登录页") {
		t.Fatalf("lead brief %q", leadOpen.FirstMessage.Text)
	}
	lead.mustCall("update_plan", map[string]any{"steps": []map[string]string{{"title": "页面", "status": "todo"}}})
	events.wait(t, "main_task", `"title":"页面"`)
	res = lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "写登录表单", "main": "页面"})
	if !strings.Contains(string(*res.Structured), `"pooled":true`) {
		t.Fatalf("assign_task %s", *res.Structured)
	}

	dev, devOpen := expectOpen(t, w, n, "dev-1")
	if devOpen.FirstMessage == nil || !strings.Contains(devOpen.FirstMessage.Text, "[Task t1") {
		t.Fatalf("task brief %+v", devOpen.FirstMessage)
	}
	dev.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: protocol.SessionID(dev.session), MsgID: devOpen.FirstMessage.MsgID})
	events.wait(t, "task", `"id":"t1"`, `"status":"claimed"`, `"claimed_by":"dev-1"`)
	events.wait(t, "task", `"id":"t1"`, `"status":"running"`)
	events.wait(t, "status", `"name":"dev-1"`, `"state":"claiming"`)

	dev.event("tool_started", func(e *protocol.SessionEvent) {
		tool, cat, sum, use := "Edit", "edit", "改 Login.tsx", "toolu_1"
		e.Tool, e.Category, e.Summary, e.ToolUseID = &tool, &cat, &sum, &use
	})
	events.wait(t, "status", `"name":"dev-1"`, `"state":"typing"`)
	events.wait(t, "activity", "改 Login.tsx")

	approvalFlow(t, w, b, dev, opened.ThreadID, events)
	// Approval cards alone are not reported to the coordinator (it would spend a turn on each).
	_, coordMsgs := b.do("GET", "/api/conversations/"+coord.ConversationID+"/messages", nil)
	if strings.Contains(string(coordMsgs), "· waiting]") {
		t.Fatalf("coordinator got an approval-only waiting report: %s", coordMsgs)
	}

	res = dev.mustCall("complete_task", map[string]any{"task_id": "t1", "summary": "表单写好了"})
	if !strings.Contains(string(*res.Structured), `"status":"done"`) {
		t.Fatalf("complete_task %s", *res.Structured)
	}
	events.wait(t, "task", `"id":"t1"`, `"status":"done"`)
	events.wait(t, "main_task", `"status":"done"`, `"auto_done":true`)
	lead.expectSend("[Task t1 · dev-1 · done] 表单写好了")

	lead.event("text", func(e *protocol.SessionEvent) {
		final, msg := true, "登录页做完了，准备开 PR"
		e.Final, e.Text = &final, &msg
	})
	events.wait(t, "message", `"role":"leader"`, "准备开 PR")
	events.wait(t, "message", `"author":"user"`, "登录页")

	var thread core.ThreadView
	b.must(200, "GET", "/api/threads/"+opened.ThreadID, nil, &thread)
	if thread.Counts.TasksDone != 1 || thread.Counts.MainDone != 1 || thread.LeadLast == nil || len(thread.Members) != 3 {
		t.Fatalf("thread view %+v", thread)
	}
	if errs := n.Errors(); len(errs) > 0 {
		t.Fatalf("protocol errors: %+v", errs)
	}
}

func assertCoordinatorSpec(t *testing.T, open protocol.SessionOpen) {
	t.Helper()
	c := open.Claude
	if c == nil || strings.Join(c.Tools, ",") != "TodoWrite,AskUserQuestion" || c.PermissionMode != "dontAsk" || c.DenyNonPlatformTools == nil || !*c.DenyNonPlatformTools {
		t.Fatalf("coordinator options %+v", c)
	}
	if open.GitCredentials != nil && *open.GitCredentials {
		t.Fatal("coordinator must not get git credentials")
	}
	names := map[string]bool{}
	for _, p := range open.PlatformTools {
		names[p.Name] = true
	}
	if !names["open_thread"] || !names["remember"] || names["complete_task"] || names["assign_task"] {
		t.Fatalf("coordinator tools %v", names)
	}
}

func assertLeadSpec(t *testing.T, open protocol.SessionOpen) {
	t.Helper()
	c := open.Claude
	if c == nil || c.ReadOnly == nil || !*c.ReadOnly || c.BashPolicy == nil || !contains(c.DisallowedTools, "Edit") {
		t.Fatalf("lead options %+v", c)
	}
	if open.Cwd != protocol.RelPath("projects/"+open.Employee.ProjectID+"/threads/1/lead") {
		t.Fatalf("lead cwd %s", open.Cwd)
	}
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// approvalFlow: the member asks for Bash, the card is answered "allow_always" through the API,
// the daemon gets approval.resolve, and the same request later is allowed by the remembered rule.
func approvalFlow(t *testing.T, w *world, b *browser, dev *fakeAgent, threadID string, events *sse) {
	t.Helper()
	input := json.RawMessage(`{"command":"npm install"}`)
	gen := protocol.OpenGen(dev.gen)
	dev.send(protocol.TypeApprovalRequest, protocol.ApprovalRequest{ApprovalID: "ap_one", SessionID: protocol.SessionID(dev.session),
		OpenGen: &gen, Source: "claude_can_use_tool", ToolName: "Bash", Input: input,
		Available:   []protocol.ApprovalDecision{"allow", "allow_always", "deny"},
		Suggestions: []json.RawMessage{json.RawMessage(`{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"npm install"}],"behavior":"allow","destination":"localSettings"}`)}})
	events.wait(t, "question", `"kind":"approval"`, `"status":"open"`)
	events.wait(t, "status", `"name":"dev-1"`, `"state":"raising_hand"`)
	events.wait(t, "thread", `"status":"waiting"`)

	var cards []core.QuestionView
	b.must(200, "GET", "/api/conversations/"+threadID+"/questions?status=open", nil, &cards)
	if len(cards) != 1 || cards[0].Kind != "approval" || cards[0].Detail["tool_name"] != "Bash" {
		t.Fatalf("cards %+v", cards)
	}
	var answered core.QuestionView
	b.must(200, "POST", "/api/conversations/"+threadID+"/questions/"+cards[0].ID+"/answer", map[string]string{"answer": "allow_always"}, &answered)
	if answered.Status != "answered" || answered.DecidedBy == "" {
		t.Fatalf("answered %+v", answered)
	}
	resolve := waitResolve(t, w, dev, "ap_one")
	if resolve.Decision != "allow_always" {
		t.Fatalf("resolve %+v", resolve)
	}
	events.wait(t, "thread", `"status":"running"`)

	dev.send(protocol.TypeApprovalRequest, protocol.ApprovalRequest{ApprovalID: "ap_two", SessionID: protocol.SessionID(dev.session),
		OpenGen: &gen, Source: "claude_can_use_tool", ToolName: "Bash", Input: input, Available: []protocol.ApprovalDecision{"allow", "deny"}})
	auto := waitResolve(t, w, dev, "ap_two")
	if auto.Decision != "allow" || !strings.HasPrefix(auto.DecidedBy, "policy:rule:") {
		t.Fatalf("remembered rule did not apply: %+v", auto)
	}
	dev.send(protocol.TypeApprovalRequest, protocol.ApprovalRequest{ApprovalID: "ap_bad", SessionID: protocol.SessionID(dev.session),
		OpenGen: &gen, Source: "claude_can_use_tool", ToolName: "Bash", Input: json.RawMessage(`{"command":"cat ~/.config/agent-teams/node.env"}`),
		Available: []protocol.ApprovalDecision{"allow", "deny"}})
	if denied := waitResolve(t, w, dev, "ap_bad"); denied.Decision != "deny" || denied.DecidedBy != approvals.DecidedDenyList {
		t.Fatalf("deny list: %+v", denied)
	}
}

func waitResolve(t *testing.T, w *world, a *fakeAgent, approvalID string) protocol.ApprovalResolve {
	t.Helper()
	ctx, cancel := w.waitCtx()
	defer cancel()
	f, err := a.node.Wait(ctx, protocol.TypeApprovalResolve, func(f nodetest.Frame) bool { return strings.Contains(string(f.Body), approvalID) })
	if err != nil {
		t.Fatal(err)
	}
	var r protocol.ApprovalResolve
	if err := f.Decode(&r); err != nil {
		t.Fatalf("decode %s: %v", f.Body, err)
	}
	return r
}
