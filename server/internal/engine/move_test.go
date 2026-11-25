package engine_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/timers"
)

var moveFeatures = []string{"sessions", "git_worktree", "cred_helper", "file_download", "session_move"}

// moveTeam is startTeam on a node "a" with session_move, plus an idle second node "b".
func moveTeam(t *testing.T) (*team, *nodetest.Node, string) {
	t.Helper()
	w := newWorld(t, nil)
	tm := &team{w: w, b: w.signUp("move@example.com")}
	var a, b nodeCreated
	tm.b.must(201, "POST", "/api/nodes", map[string]string{"name": "a"}, &a)
	tm.token = a.Token
	tm.b.must(200, "GET", "/api/roles", nil, nil)
	askMembers(t, w)
	var project struct{ ID string }
	tm.b.must(201, "POST", "/api/projects", map[string]any{"name": "p"}, &project)
	tm.projectID = project.ID
	tm.n = w.connect(a.Token, nodetest.Options{Features: moveFeatures})
	tm.b.must(201, "POST", "/api/projects/"+project.ID+"/threads", map[string]any{"title": "Work", "brief": "do it"}, &tm.thread)
	tm.lead, _ = expectOpen(t, w, tm.n, "lead-1")
	tm.lead.mustCall("update_plan", map[string]any{"steps": []map[string]string{{"title": "做", "status": "todo"}}})
	tm.lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "do t1", "main": "做"})
	var open protocol.SessionOpen
	tm.dev, open = expectOpen(t, w, tm.n, "dev-1")
	tm.dev.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: protocol.SessionID(tm.dev.session), MsgID: open.FirstMessage.MsgID})
	tm.b.must(201, "POST", "/api/nodes", map[string]string{"name": "b"}, &b)
	nb := w.connect(b.Token, nodetest.Options{Features: moveFeatures})
	return tm, nb, b.Token
}

// expectExport waits for the old node's session.close (node_move) and session.export.
func expectExport(t *testing.T, tm *team, sessionID string) protocol.SessionExport {
	t.Helper()
	return waitExport(t, tm.w, tm.n, sessionID)
}

func waitExport(t *testing.T, w *world, n *nodetest.Node, sessionID string) protocol.SessionExport {
	t.Helper()
	ctx, cancel := w.waitCtx()
	defer cancel()
	f, err := n.Wait(ctx, protocol.TypeSessionClose, func(f nodetest.Frame) bool { return strings.Contains(string(f.Body), sessionID) })
	if err != nil || !strings.Contains(string(f.Body), `"node_move"`) {
		t.Fatalf("close %v %s", err, f.Body)
	}
	f, err = n.Wait(ctx, protocol.TypeSessionExport, nil)
	if err != nil {
		t.Fatal(err)
	}
	var exp protocol.SessionExport
	if err := f.Decode(&exp); err != nil {
		t.Fatal(err)
	}
	return exp
}

func moveRequest(t *testing.T, tm *team, method, id, token string, body []byte) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, tm.w.srv.URL+"/nodes/moves/"+id, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		sum := sha256.Sum256(body)
		req.Header.Set("X-Content-SHA256", hex.EncodeToString(sum[:]))
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, raw
}

// TestSessionMovesOnUsageLimit: a usage limit on node a with node b free moves dev-1 there: the
// session closes on a (node_move), a exports and uploads the archive, messages sent meanwhile are
// held, and the session opens on b resuming the same conversation with session.open.import (only
// b can download the archive). The thread is never paused; the archive is dropped once b answered.
func TestSessionMovesOnUsageLimit(t *testing.T) {
	tm, nb, tokenB := moveTeam(t)
	old := tm.dev.session
	rateLimited(tm.dev, time.Hour)
	exp := expectExport(t, tm, old)
	if string(exp.SessionID) != old || exp.Runtime != "claude" || exp.Resume.SDKSessionID == nil || *exp.Resume.SDKSessionID != "sdk-"+old {
		t.Fatalf("export %+v", exp)
	}
	if st, _ := threadStatus(tm); st != "running" {
		t.Fatalf("thread %s while moving", st)
	}
	tm.lead.mustCall("send_message", map[string]any{"to": "dev-1", "text": "别忘了写测试"})

	archive := []byte("pretend archive")
	if code, _ := moveRequest(t, tm, "PUT", string(exp.ExportID), tokenB, archive); code != 404 {
		t.Fatalf("upload by the other node = %d", code)
	}
	if code, raw := moveRequest(t, tm, "PUT", string(exp.ExportID), tm.token, archive); code != 204 {
		t.Fatalf("upload = %d %s", code, raw)
	}
	sum := sha256.Sum256(archive)
	digest, size := hex.EncodeToString(sum[:]), len(archive)
	tm.dev.send(protocol.TypeSessionExported, protocol.SessionExported{ExportID: exp.ExportID, SessionID: exp.SessionID, Ok: true, Size: &size, SHA256: &digest})

	ctx, cancel := tm.w.waitCtx()
	defer cancel()
	f, err := nb.Wait(ctx, protocol.TypeSessionOpen, func(f nodetest.Frame) bool { return strings.Contains(string(f.Body), `"name":"dev-1"`) })
	if err != nil {
		t.Fatal(err)
	}
	var open protocol.SessionOpen
	_ = f.Decode(&open)
	if open.Import == nil || open.Import.ExportID != exp.ExportID || open.Import.Size != size || open.Import.SHA256 != digest {
		t.Fatalf("import %+v", open.Import)
	}
	if open.Resume == nil || open.Resume.SDKSessionID == nil || *open.Resume.SDKSessionID != "sdk-"+old {
		t.Fatalf("resume %+v", open.Resume)
	}
	if open.FirstMessage == nil || !strings.Contains(open.FirstMessage.Text, "迁移到节点 b") || !strings.Contains(open.FirstMessage.Text, "别忘了写测试") {
		t.Fatalf("first message %+v", open.FirstMessage)
	}
	if code, _ := moveRequest(t, tm, "GET", string(exp.ExportID), tm.token, nil); code != 404 {
		t.Fatalf("download by the old node = %d", code)
	}
	if code, raw := moveRequest(t, tm, "GET", string(exp.ExportID), tokenB, nil); code != 200 || string(raw) != string(archive) {
		t.Fatalf("download = %d %q", code, raw)
	}
	moved := &fakeAgent{t: t, w: tm.w, node: nb, session: string(open.SessionID), gen: int(open.OpenGen)}
	moved.opened(open)
	var status string
	var hasArchive bool
	eventually(t, "move done", func() bool {
		_ = store.QueryRow(tm.w.ctx, tm.w.e.Core.Pool, `SELECT status, archive IS NOT NULL FROM at_session_moves WHERE id = $1`, exp.ExportID).Scan(&status, &hasArchive)
		return status == "done" && !hasArchive
	})
	var waits int
	_ = store.QueryRow(tm.w.ctx, tm.w.e.Core.Pool, `SELECT count(*) FROM at_rate_limit_waits`).Scan(&waits)
	if st, _ := threadStatus(tm); st != "running" || waits != 0 {
		t.Fatalf("thread %s, waits %d", st, waits)
	}
	var node string
	_ = store.QueryRow(tm.w.ctx, tm.w.e.Core.Pool, `SELECT n.name FROM at_employees e JOIN at_nodes n ON n.id = e.node_id WHERE e.name = 'dev-1'`).Scan(&node)
	if node != "b" {
		t.Fatalf("dev-1 on %q", node)
	}
}

// TestSessionMoveFallsBack: an export that fails releases the held messages as before (the old
// node, the same conversation, a plain "continue" note); the next limit pauses the thread instead
// of trying again.
func TestSessionMoveFallsBack(t *testing.T) {
	tm, nb, _ := moveTeam(t)
	old := tm.dev.session
	rateLimited(tm.dev, time.Hour)
	exp := expectExport(t, tm, old)
	why := "transcript not found on this node"
	tm.dev.send(protocol.TypeSessionExported, protocol.SessionExported{ExportID: exp.ExportID, SessionID: exp.SessionID, Error: &why})
	agent, open := expectOpen(t, tm.w, tm.n, "dev-1")
	if open.Import != nil || open.Resume == nil || open.FirstMessage == nil || !strings.Contains(open.FirstMessage.Text, "请从中断的地方继续") ||
		strings.Contains(open.FirstMessage.Text, "迁移") {
		t.Fatalf("fallback open %+v %+v", open.Import, open.FirstMessage)
	}
	expectNone(t, nb, protocol.TypeSessionOpen, 300*time.Millisecond, nil)
	tm.dev = agent
	rateLimited(tm.dev, time.Hour)
	eventually(t, "thread paused", func() bool { st, why := threadStatus(tm); return st == "paused" && why == timers.PauseReason })
}

// TestSessionMoveImportFails: the new node cannot import (resume_failed): the move is marked
// failed and the employee gets the usual recap session.
func TestSessionMoveImportFails(t *testing.T) {
	tm, nb, _ := moveTeam(t)
	rateLimited(tm.dev, time.Hour)
	exp := expectExport(t, tm, tm.dev.session)
	archive := []byte("archive")
	if code, _ := moveRequest(t, tm, "PUT", string(exp.ExportID), tm.token, archive); code != 204 {
		t.Fatalf("upload = %d", code)
	}
	sum := sha256.Sum256(archive)
	digest, size := hex.EncodeToString(sum[:]), len(archive)
	// The live digest of the turn that hit the limit arrives after the session closed for the move.
	dctx, dcancel := tm.w.waitCtx()
	defer dcancel()
	if err := tm.n.SendUnreliable(dctx, protocol.TypeSessionDigest, protocol.SessionDigest{SessionID: exp.SessionID, OpenGen: protocol.OpenGen(tm.dev.gen), Digest: "待办 / 计划（最后一次更新）：\n- [in_progress] 写迁移测试"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "digest stored", func() bool {
		var d string
		_ = store.QueryRow(tm.w.ctx, tm.w.e.Core.Pool, `SELECT digest FROM at_agent_sessions WHERE id = $1`, exp.SessionID).Scan(&d)
		return strings.Contains(d, "写迁移测试")
	})
	tm.dev.send(protocol.TypeSessionExported, protocol.SessionExported{ExportID: exp.ExportID, SessionID: exp.SessionID, Ok: true, Size: &size, SHA256: &digest})
	ctx, cancel := tm.w.waitCtx()
	defer cancel()
	f, err := nb.Wait(ctx, protocol.TypeSessionOpen, func(f nodetest.Frame) bool { return strings.Contains(string(f.Body), `"name":"dev-1"`) })
	if err != nil {
		t.Fatal(err)
	}
	var open protocol.SessionOpen
	_ = f.Decode(&open)
	failed := &fakeAgent{t: t, w: tm.w, node: nb, session: string(open.SessionID), gen: int(open.OpenGen)}
	yes, why := true, "import of moved session failed: bundle verify"
	failed.send(protocol.TypeSessionOpened, protocol.SessionOpened{SessionID: open.SessionID, OpenGen: open.OpenGen, ResumeFailed: &yes, Error: &why})
	var status, errText string
	eventually(t, "move failed", func() bool {
		_ = store.QueryRow(tm.w.ctx, tm.w.e.Core.Pool, `SELECT status, error FROM at_session_moves WHERE id = $1`, exp.ExportID).Scan(&status, &errText)
		return status == "failed"
	})
	if !strings.Contains(errText, "bundle verify") {
		t.Fatalf("error %q", errText)
	}
	recap, err := waitOpenAnywhere(tm, []*nodetest.Node{tm.n, nb}, "dev-1", string(open.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	if recap.Import != nil || recap.Resume != nil || recap.FirstMessage == nil || !strings.Contains(recap.FirstMessage.Text, "[会话恢复]") ||
		!strings.Contains(recap.FirstMessage.Text, "[in_progress] 写迁移测试") {
		t.Fatalf("recap open %+v", recap.FirstMessage)
	}
}

// TestSessionMoveExportTimesOut: nobody answers the export; the timers give it up and the held
// messages reopen the old conversation on the old node.
func TestSessionMoveExportTimesOut(t *testing.T) {
	tm, _, _ := moveTeam(t)
	rateLimited(tm.dev, time.Hour)
	exp := expectExport(t, tm, tm.dev.session)
	if _, err := store.Exec(tm.w.ctx, tm.w.e.Core.Pool, `UPDATE at_session_moves SET updated_at = now() - interval '11 minutes' WHERE id = $1`, exp.ExportID); err != nil {
		t.Fatal(err)
	}
	tm.w.e.Timers.Tick(tm.w.ctx)
	_, open := expectOpen(t, tm.w, tm.n, "dev-1")
	if open.Import != nil || open.Resume == nil || open.FirstMessage == nil || !strings.Contains(open.FirstMessage.Text, "请从中断的地方继续") {
		t.Fatalf("reopen %+v", open.FirstMessage)
	}
	var errText string
	_ = store.QueryRow(tm.w.ctx, tm.w.e.Core.Pool, `SELECT error FROM at_session_moves WHERE id = $1`, exp.ExportID).Scan(&errText)
	if !strings.Contains(errText, "in time") {
		t.Fatalf("error %q", errText)
	}
}

// waitOpenAnywhere waits for a session.open of the employee on any of the nodes other than session skip.
func waitOpenAnywhere(tm *team, nodes []*nodetest.Node, name, skip string) (protocol.SessionOpen, error) {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, n := range nodes {
			ctx, cancel := context.WithTimeout(tm.w.ctx, 100*time.Millisecond)
			f, err := n.Wait(ctx, protocol.TypeSessionOpen, func(f nodetest.Frame) bool {
				return strings.Contains(string(f.Body), `"name":"`+name+`"`) && !strings.Contains(string(f.Body), skip)
			})
			cancel()
			if err == nil {
				var open protocol.SessionOpen
				return open, f.Decode(&open)
			}
		}
	}
	return protocol.SessionOpen{}, context.DeadlineExceeded
}

// TestSessionMoveLeadAndCoordinator: a thread's lead and the project's coordinator move like any
// member: same conversation id, session.open.import on the other node.
func TestSessionMoveLeadAndCoordinator(t *testing.T) {
	for _, name := range []string{"lead-1", "coordinator"} {
		t.Run(name, func(t *testing.T) { moveOne(t, name) })
	}
}

func moveOne(t *testing.T, name string) {
	tm, nb, tokenB := moveTeam(t)
	agent := tm.lead
	if name == "coordinator" {
		agent = openCoordinator(t, tm, nb)
	}
	from, to, token := tm.n, nb, tm.token
	if agent.node == nb {
		from, to, token = nb, tm.n, tokenB
	}
	old := agent.session
	rateLimited(agent, time.Hour)
	exp := waitExport(t, tm.w, from, old)
	if exp.Resume.SDKSessionID == nil || *exp.Resume.SDKSessionID != "sdk-"+old {
		t.Fatalf("export %+v", exp)
	}
	archive := []byte("archive of " + name)
	if code, _ := moveRequest(t, tm, "PUT", string(exp.ExportID), token, archive); code != 204 {
		t.Fatalf("upload = %d", code)
	}
	sum := sha256.Sum256(archive)
	digest, size := hex.EncodeToString(sum[:]), len(archive)
	agent.send(protocol.TypeSessionExported, protocol.SessionExported{ExportID: exp.ExportID, SessionID: exp.SessionID, Ok: true, Size: &size, SHA256: &digest})
	ctx, cancel := tm.w.waitCtx()
	defer cancel()
	f, err := to.Wait(ctx, protocol.TypeSessionOpen, func(f nodetest.Frame) bool { return strings.Contains(string(f.Body), `"name":"`+name+`"`) })
	if err != nil {
		t.Fatal(err)
	}
	var moved protocol.SessionOpen
	_ = f.Decode(&moved)
	if moved.Import == nil || moved.Import.ExportID != exp.ExportID || moved.Resume == nil || moved.Resume.SDKSessionID == nil || *moved.Resume.SDKSessionID != "sdk-"+old {
		t.Fatalf("open %+v %+v", moved.Import, moved.Resume)
	}
}

// openCoordinator starts the coordinator's session with a project chat message (on either node).
func openCoordinator(t *testing.T, tm *team, nb *nodetest.Node) *fakeAgent {
	t.Helper()
	var coord struct {
		ConversationID string `json:"conversation_id"`
	}
	tm.b.must(200, "GET", "/api/projects/"+tm.projectID+"/coordinator", nil, &coord)
	tm.b.must(202, "POST", "/api/conversations/"+coord.ConversationID+"/messages", map[string]any{"content": "进度怎么样"}, nil)
	var open protocol.SessionOpen
	var node *nodetest.Node
	eventually(t, "coordinator opens", func() bool {
		for _, n := range []*nodetest.Node{tm.n, nb} {
			ctx, cancel := context.WithTimeout(tm.w.ctx, 100*time.Millisecond)
			f, err := n.Wait(ctx, protocol.TypeSessionOpen, func(f nodetest.Frame) bool { return strings.Contains(string(f.Body), `"name":"coordinator"`) })
			cancel()
			if err == nil {
				node = n
				return f.Decode(&open) == nil
			}
		}
		return false
	})
	c := &fakeAgent{t: t, w: tm.w, node: node, session: string(open.SessionID), gen: int(open.OpenGen)}
	c.opened(open)
	c.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: open.SessionID, MsgID: open.FirstMessage.MsgID})
	return c
}

// slowUpload starts a PUT /nodes/moves/{id} whose body is a pipe declared as size bytes; the
// returned writer feeds it and the channel delivers the status (audit #55 N5).
func slowUpload(t *testing.T, tm *team, id, token string, size int64, sha string) (*io.PipeWriter, <-chan int) {
	t.Helper()
	pr, pw := io.Pipe()
	req, _ := http.NewRequest("PUT", tm.w.srv.URL+"/nodes/moves/"+id, pr)
	req.ContentLength = size
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Content-SHA256", sha)
	done := make(chan int, 1)
	go func() {
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- -1
			return
		}
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
		done <- res.StatusCode
	}()
	t.Cleanup(func() { _ = pw.Close() })
	return pw, done
}

func statusWithin(t *testing.T, done <-chan int, what string) int {
	t.Helper()
	select {
	case code := <-done:
		return code
	case <-time.After(10 * time.Second):
		t.Fatalf("%s: no response (the server waited for the body?)", what)
		return 0
	}
}

// TestSessionMoveUploadLimits: an upload is refused before its body is read when the move is not the
// node's or the declared size is over the cap, a bad X-Content-SHA256 is refused up front, and a
// node's second upload queues behind its first.
func TestSessionMoveUploadLimits(t *testing.T) {
	tm, _, tokenB := moveTeam(t)
	rateLimited(tm.dev, time.Hour)
	exp := expectExport(t, tm, tm.dev.session)
	id := string(exp.ExportID)
	archive := []byte("pretend archive")
	sum := sha256.Sum256(archive)
	digest := hex.EncodeToString(sum[:])

	// Another node's upload gets 404 although its body never ends.
	pw, done := slowUpload(t, tm, id, tokenB, 64<<20, digest)
	_, _ = pw.Write([]byte("x"))
	if code := statusWithin(t, done, "other node"); code != 404 {
		t.Fatalf("upload by the other node = %d", code)
	}
	// Over the cap by Content-Length: 413 without reading.
	_, done = slowUpload(t, tm, id, tm.token, 128<<20+1, digest)
	if code := statusWithin(t, done, "too large"); code != 413 {
		t.Fatalf("oversized upload = %d", code)
	}
	// Malformed digest header: 400 up front.
	_, done = slowUpload(t, tm, id, tm.token, 1<<20, "abc")
	if code := statusWithin(t, done, "bad sha"); code != 400 {
		t.Fatalf("bad sha upload = %d", code)
	}
	// One upload per node: a second one waits (unread) while the first is still sending, then runs.
	pw, first := slowUpload(t, tm, id, tm.token, int64(len(archive)), digest)
	_, _ = pw.Write(archive[:4])
	time.Sleep(200 * time.Millisecond) // the first holds the node's slot
	second := make(chan int, 1)
	go func() { code, _ := moveRequest(t, tm, "PUT", id, tm.token, archive); second <- code }()
	select {
	case code := <-second:
		t.Fatalf("second upload finished (%d) while the first held the node's slot", code)
	case <-time.After(300 * time.Millisecond):
	}
	_, _ = pw.Write(archive[4:])
	_ = pw.Close()
	if code := statusWithin(t, first, "first upload"); code != 204 {
		t.Fatalf("first upload = %d", code)
	}
	if code := statusWithin(t, second, "second upload"); code != 204 {
		t.Fatalf("queued upload = %d", code)
	}
}
