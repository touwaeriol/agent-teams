package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// A daemon restart (or a session process restart) cuts the turns that were running: the
// employee was working, waiting for an approval card (its own, or one at the lead) or for a
// blocking tool such as ask_user. Nothing on the node continues such a turn, so the console
// resumes it with a system note (the session reopens by its SDK session id).

// cutTurnNote starts the note; the open requests are appended.
const cutTurnNote = "（平台）节点守护进程重启（或会话进程中断），打断了你上一个回合。请从中断的地方继续未完成的工作。"

// cutTurn reports whether a state change means the running turn was cut off: a running
// session only parks when the daemon shuts down and only restarts when its process died.
func cutTurn(prev, next string) bool {
	return prev == "turn_running" && (next == "parked" || next == "opening")
}

// ResumeCutTurn resumes the employee of a session whose pending requests a restart lost.
func (o *Orchestrator) ResumeCutTurn(ctx context.Context, tx *core.Tx, s core.AgentSession) error {
	return o.resumeCutTurn(ctx, tx, s)
}

// resumeCutTurn sends the employee of s the restart note, once per interruption (the shutdown's
// parked state, the report and the lost cards may all notice the same restart).
func (o *Orchestrator) resumeCutTurn(ctx context.Context, tx *core.Tx, s core.AgentSession) error {
	sc, err := core.SessionContextOf(ctx, tx, s.ID)
	if err != nil {
		return ignoreMissing(err)
	}
	e := sc.Employee // a project member in the session's thread
	if e.ProjectMember() && e.CurrentThreadID == nil {
		return nil // its thread is gone: nothing to resume there
	}
	conv, err := o.conversationOf(ctx, tx, e)
	if err != nil {
		return err
	}
	var pending int64
	err = tx.Gorm(ctx).Table("at_messages").Where(`conversation_id = ?
		AND (delivered_at IS NULL OR created_at > now() - interval '2 minutes')
		AND meta->>'to' = ? AND (meta->>'resume_cut')::boolean`, conv, e.Name).Count(&pending).Error
	if err != nil {
		return err
	}
	if pending > 0 {
		return nil
	}
	text, err := cutTurnText(ctx, tx, e)
	if err != nil {
		return err
	}
	rec := &core.NewMessage{ConversationID: conv, Role: "system", Author: "platform", Kind: "note", Content: text,
		Meta: map[string]any{"to": e.Name, "resume_cut": true}}
	_, err = o.Deliver(ctx, tx, e, Delivery{Text: text, Kind: "system", Priority: "next", Record: rec})
	return err
}

// cutTurnText explains what the interruption did to the employee's open requests.
func cutTurnText(ctx context.Context, q core.DB, e core.Employee) (string, error) {
	cards, err := openRequests(ctx, q, e, `kind = 'approval' AND jsonb_exists(detail, 'detached_at') AND status IN ('open','answered')`)
	if err != nil {
		return "", err
	}
	asked, err := openRequests(ctx, q, e, `kind <> 'approval' AND blocking AND status = 'open'`)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(cutTurnNote)
	if len(cards) > 0 {
		fmt.Fprintf(&b, "\n\n你之前等待的权限审批在中断时被取消了：%s。请原样重新发起同一个操作（同样的工具和参数），"+
			"平台会把它接回原来的审批卡片继续等待；卡片如果已经有了答复，会立即生效。", strings.Join(cards, "；"))
	} else {
		b.WriteString("之前等待的审批已失效，需要的话重新申请。")
	}
	if len(asked) > 0 {
		fmt.Fprintf(&b, "\n\n你之前提出的问题仍在等待回答：%s。回答会以消息送达，不要重复提问。", strings.Join(asked, "；"))
	}
	return b.String(), nil
}

// openRequests lists the employee's questions matching where as "q3 Bash make test".
func openRequests(ctx context.Context, q core.DB, e core.Employee, where string) ([]string, error) {
	var qs []core.Question
	err := q.Gorm(ctx).Select("id, kind, text, detail").
		Where("project_id = ? AND COALESCE(thread_id, '') = COALESCE(?, '') AND asker = ?", e.ProjectID, e.WorkThreadID(), e.Name).
		Where(where).Order("created_at").Find(&qs).Error
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(qs))
	for _, x := range qs {
		out = append(out, x.ID+" "+requestLine(x.Kind, x.Text, x.Detail))
	}
	return out, nil
}

func requestLine(kind, text string, detail map[string]any) string {
	if kind != "approval" {
		return clipRunes(text, 80)
	}
	input, _ := detail["input"].(map[string]any)
	for _, key := range []string{"command", "cmd", "file_path", "path"} {
		if v, ok := input[key].(string); ok && v != "" {
			return fmt.Sprintf("%v %s", detail["tool_name"], clipHead(v, 160))
		}
	}
	return fmt.Sprint(detail["tool_name"])
}

// clipHead keeps the end of a long command or path (the file name matters most).
func clipHead(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n:])
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// stalledNote wakes a lead whose running thread has nobody working after a restart. It is a
// platform → lead note (the chat hides it), sent once per stall.
const stalledNote = "（平台）节点守护进程重启后检查：这个线程仍是运行中，但现在没有成员在工作，也没有待处理的卡片或消息。" +
	"请确认进度：所有工作都已完成，就调用 finish(summary) 收尾（没有代码改动时不需要 PR，线程直接结束）；" +
	"在等用户决定或回复，就用 ask_user 问一次，然后等待；还有工作就继续推进（分派、催促成员）。" +
	"用户说过会自己关闭这个线程的话，什么都不用做，也不要回复这条检查。这条检查同一次停滞只发一次。"

// stalledDebounce: never two stalled notes within this time (reconnect bursts).
const stalledDebounce = "10 minutes"

// wakeStalledThreads: after a reconnect (sessions.report), a running thread of the node whose
// employees are all idle, with no open card or question and no message on its way, would sit
// in "running" forever; its lead gets a note, once per stall: at_threads.stalled_note_at
// remembers the last one, and another goes out only after something meaningful happened in the
// thread since (a user message or answer, a task, plan, merge or CI change, a PR update; a
// resume clears the mark) and never within stalledDebounce of the last one.
func (o *Orchestrator) wakeStalledThreads(ctx context.Context, tx *core.Tx, node store.Node) error {
	rows, err := store.Query(ctx, tx, `SELECT DISTINCT t.id FROM at_threads t JOIN at_agent_sessions a ON a.thread_id = t.id
		AND a.state <> 'closed'
		WHERE a.node_id = $1 AND t.status = 'running'
		AND NOT EXISTS (SELECT 1 FROM at_agent_sessions a2 WHERE a2.thread_id = t.id
			AND a2.state IN ('turn_running','opening','queued'))
		AND NOT EXISTS (SELECT 1 FROM at_questions q WHERE q.thread_id = t.id AND q.status = 'open')
		AND NOT EXISTS (SELECT 1 FROM at_messages m WHERE m.conversation_id = t.id AND m.delivered_at IS NULL
			AND jsonb_exists(m.meta, 'msg_id') AND m.created_at > now() - interval '1 day')
		AND `+stalledArmed, node.ID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if t, err := core.ThreadByID(ctx, tx, id); err != nil || t.Solo() {
			continue // a session's employee idles while it waits for the user: not a stall
		}
		lead, err := core.Lead(ctx, tx, id)
		if err != nil {
			continue
		}
		// Claim the note under the row lock: concurrent reconnects send it once.
		res := tx.Gorm(ctx).Exec(`UPDATE at_threads t SET stalled_note_at = now() WHERE t.id = ? AND `+stalledArmed, id)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			continue
		}
		rec := &core.NewMessage{ConversationID: id, Role: "system", Author: "platform", Kind: "note", Content: stalledNote,
			Meta: map[string]any{"to": lead.Name, "stalled": true}}
		if _, err := o.Deliver(ctx, tx, lead, Delivery{Text: stalledNote, Kind: "system", Priority: "later", Record: rec}); err != nil {
			return err
		}
	}
	return nil
}

// stalledArmed (on thread t) holds when the thread may get a stalled note: none was sent yet in
// this stall, or the last one is older than stalledDebounce and something happened since.
const stalledArmed = `(t.stalled_note_at IS NULL OR (t.stalled_note_at < now() - interval '` + stalledDebounce + `' AND (
	EXISTS (SELECT 1 FROM at_messages m WHERE m.conversation_id = t.id AND m.role = 'user' AND m.created_at > t.stalled_note_at)
	OR EXISTS (SELECT 1 FROM at_questions q WHERE q.thread_id = t.id AND q.answered_at > t.stalled_note_at)
	OR EXISTS (SELECT 1 FROM at_events ev WHERE ev.thread_id = t.id AND ev.created_at > t.stalled_note_at
		AND ev.type IN ('task','main_task','merge','ci'))
	OR EXISTS (SELECT 1 FROM at_thread_repos r WHERE r.thread_id = t.id AND r.updated_at > t.stalled_note_at))))`
