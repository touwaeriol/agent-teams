package toolsvc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
)

// Subthreads and talk between threads (threads.md "子讨论串与讨论串之间的对话"): a lead opens
// subthreads of its own thread (one level), messages any other thread of the project and the
// coordinator. Every such message is a "relay" card in both conversations (meta.relay), so the
// user sees "已向 #7 发送 1 条消息" on one side and "来自 #3 的 1 条消息" on the other.

const (
	// MaxOpenSubthreads is how many unfinished subthreads one thread may have.
	MaxOpenSubthreads = 5
	// MaxRelaysPerHour is how many messages two threads (or a thread and the coordinator) may
	// exchange in an hour.
	MaxRelaysPerHour = 20
)

// relayPeer names the other side of a relay card: the coordinator (no = 0) or a thread.
type relayPeer struct {
	no    int
	title string
}

var coordinatorPeer = relayPeer{}

func threadPeer(t core.Thread) relayPeer { return relayPeer{no: t.No, title: t.Title} }

// relayMeta is meta.relay: dir in | out, kind message | report | opened, peer coordinator | thread.
func relayMeta(dir, kind string, p relayPeer, extra map[string]any) map[string]any {
	r := map[string]any{"dir": dir, "kind": kind, "peer": "coordinator"}
	if p.no > 0 {
		r["peer"], r["no"], r["title"] = "thread", p.no, p.title
	}
	for k, v := range extra {
		r[k] = v
	}
	return map[string]any{"relay": r}
}

// recordRelay stores a relay card in a conversation without delivering it to anyone.
func (s *Service) recordRelay(ctx context.Context, tx *core.Tx, projectID, conv string, thread *string, author, text string, meta map[string]any) error {
	m, err := core.InsertMessage(ctx, tx, core.NewMessage{ConversationID: conv, Role: "system", Author: author, Kind: "relay",
		Content: text, Meta: meta})
	if err != nil {
		return err
	}
	return tx.EmitMessage(ctx, projectID, thread, m)
}

// relaysLastHour counts the messages conversation conv received from peer in the last hour.
func relaysLastHour(ctx context.Context, tx *core.Tx, conv string, p relayPeer) (int64, error) {
	var n int64
	g := tx.Gorm(ctx).Table("at_messages").Where(`conversation_id = ? AND kind = 'relay' AND meta->'relay'->>'dir' = 'in'
		AND meta->'relay'->>'kind' = 'message' AND created_at > now() - interval '1 hour'`, conv)
	if p.no > 0 {
		g = g.Where(`meta->'relay'->>'peer' = 'thread' AND meta->'relay'->>'no' = ?`, fmt.Sprint(p.no))
	} else {
		g = g.Where(`meta->'relay'->>'peer' = 'coordinator'`)
	}
	return n, g.Count(&n).Error
}

// openSubthread opens a subthread of the lead's thread (lead).
func (s *Service) openSubthread(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	parent, err := requireThread(c)
	if err != nil {
		return Result{}, err
	}
	var in protocol.OpenSubthreadInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	if err := subthreadAllowed(ctx, tx, parent); err != nil {
		return Result{}, err
	}
	files, err := s.useAttachments(ctx, tx, c, in.Attachments)
	if err != nil {
		return Result{}, err
	}
	// The brief is stored as written; its text files are inlined when the thread starts (briefFiles).
	brief := subthreadBrief(parent, in.Brief)
	t, err := s.orch.OpenThreadTx(ctx, tx, orchestrator.ThreadRequest{UserID: c.sc.OwnerID, ProjectID: parent.ProjectID,
		Title: strings.TrimSpace(in.Title), Brief: brief, OnlyRoles: in.Roles, Repos: focusOf(in.Repos),
		OpenedBy: "lead", ParentID: &parent.ID})
	if errors.Is(err, orchestrator.ErrUnknownRepo) || errors.Is(err, orchestrator.ErrRoleNotFound) {
		return Result{}, refuse("%s", err.Error())
	}
	if err != nil {
		return Result{}, err
	}
	if err := s.noteSubthread(ctx, tx, c, parent, t); err != nil {
		return Result{}, err
	}
	if err := s.noteBriefFiles(ctx, tx, c, t, files); err != nil {
		return Result{}, err
	}
	owner := c.sc.OwnerID
	tx.AfterCommit(func(ctx context.Context) { go s.orch.StartQueued(context.WithoutCancel(ctx), owner) })
	out := protocol.OpenSubthreadResult{No: protocol.ThreadNo(t.No), ThreadID: t.ID, Branch: t.Branch}
	return okJSON(fmt.Sprintf("opened subthread #%d (%s), branch %s"+carried(files)+". Do not wait for it: carry on with your work; you get [Subthread #%d · …] when it needs the user, waits for review, ends or fails.",
		t.No, t.Title, t.Branch, t.No), out), nil
}

// noteBriefFiles records the attachments open_subthread gave in the new thread's conversation
// (meta.attachments + meta.brief_attachments): its chat shows them, and they go to its lead with
// the brief when the thread starts (orchestrator briefFiles).
func (s *Service) noteBriefFiles(ctx context.Context, tx *core.Tx, c *call, t core.Thread, files []core.AttachmentRef) error {
	if len(files) == 0 {
		return nil
	}
	m, err := core.InsertMessage(ctx, tx, core.NewMessage{ConversationID: t.ID, Role: "system", Author: c.emp().Name, Kind: "note",
		Content: fmt.Sprintf("%s 随任务说明附带了 %d 个附件", c.emp().Name, len(files)),
		Meta:    map[string]any{"attachments": files, orchestrator.BriefAttachmentsKey: true, "thread_no": t.No}})
	if err != nil {
		return err
	}
	return tx.EmitMessage(ctx, t.ProjectID, &t.ID, m)
}

// withFiles adds meta.attachments to a relay card's meta (none: unchanged).
func withFiles(meta map[string]any, files []core.AttachmentRef) map[string]any {
	if len(files) > 0 {
		meta["attachments"] = files
	}
	return meta
}

// subthreadAllowed: one level only, at most MaxOpenSubthreads unfinished ones.
func subthreadAllowed(ctx context.Context, tx *core.Tx, parent core.Thread) error {
	if parent.ParentID != nil {
		return refuse("a subthread cannot open subthreads: ask the parent thread's lead with message_thread if needed, or do it yourself")
	}
	open, err := core.Subthreads(ctx, tx, parent.ID, true)
	if err != nil {
		return err
	}
	if len(open) >= MaxOpenSubthreads {
		return refuse("%d subthreads are still open (%s), at most %d: wait for them to end, or close the ones you do not need with close_subthread",
			len(open), threadNos(open), MaxOpenSubthreads)
	}
	return nil
}

func threadNos(ts []core.Thread) string {
	parts := make([]string, len(ts))
	for i, t := range ts {
		parts[i] = fmt.Sprintf("#%d", t.No)
	}
	return strings.Join(parts, ", ")
}

// subthreadBrief heads the brief with where it comes from and how to report back.
func subthreadBrief(parent core.Thread, brief string) string {
	return fmt.Sprintf("（这是讨论串 #%d「%s」的组长开的子讨论串。状态变化会自动通知它；需要和它商量时用 message_thread(no: %d)。）\n\n%s",
		parent.No, parent.Title, parent.No, strings.TrimSpace(brief))
}

// noteSubthread shows the new subthread in the parent's chat and in the project chat.
func (s *Service) noteSubthread(ctx context.Context, tx *core.Tx, c *call, parent, t core.Thread) error {
	text := fmt.Sprintf("开了子讨论串 #%d · %s", t.No, t.Title)
	if err := s.recordRelay(ctx, tx, parent.ProjectID, parent.ID, &parent.ID, c.emp().Name, text,
		relayMeta("out", "opened", threadPeer(t), nil)); err != nil {
		return err
	}
	_, conv, err := s.orch.EnsureCoordinatorTx(ctx, tx, parent.UserID, parent.ProjectID)
	if err != nil {
		return err
	}
	text = fmt.Sprintf("讨论串 #%d 开了子讨论串 #%d · %s", parent.No, t.No, t.Title)
	return s.recordRelay(ctx, tx, parent.ProjectID, conv, nil, c.emp().Name, text,
		relayMeta("in", "opened", threadPeer(parent), map[string]any{"sub_no": t.No, "sub_title": t.Title}))
}

// closeSubthread ends one of the lead's subthreads as done (lead).
func (s *Service) closeSubthread(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	parent, err := requireThread(c)
	if err != nil {
		return Result{}, err
	}
	var in protocol.CloseSubthreadInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	t, err := ownSubthread(ctx, tx, parent, int(in.No))
	if err != nil {
		return Result{}, err
	}
	if core.ThreadEnded(t.Status) {
		return okText(fmt.Sprintf("subthread #%d has already ended (%s)", t.No, t.Status)), nil
	}
	if t, err = core.LockThread(ctx, tx, t.ID); err != nil {
		return Result{}, err
	}
	err = s.orch.CloseByParent(ctx, tx, t, parent.No, deref(in.Reason))
	switch {
	case errors.Is(err, orchestrator.ErrPRAwaiting):
		return Result{}, refuse("the PR of subthread #%d is waiting for the user's review, so it cannot be closed: wait for the user", t.No)
	case errors.Is(err, orchestrator.ErrMerging):
		return Result{}, refuse("subthread #%d is merging and cannot be closed: wait for the merge to end", t.No)
	case err != nil:
		return Result{}, err
	}
	return okText(fmt.Sprintf("subthread #%d closed (done)", t.No)), nil
}

// ownSubthread loads subthread no of parent.
func ownSubthread(ctx context.Context, tx *core.Tx, parent core.Thread, no int) (core.Thread, error) {
	t, err := core.ThreadByNo(ctx, tx, parent.ProjectID, no)
	if err != nil || t.ParentID == nil || *t.ParentID != parent.ID {
		return core.Thread{}, refuse("#%d is not your subthread", no)
	}
	return t, nil
}

// openSubthreadsRefusal refuses finish while the thread has unfinished subthreads.
func openSubthreadsRefusal(ctx context.Context, tx *core.Tx, t core.Thread) error {
	open, err := core.Subthreads(ctx, tx, t.ID, true)
	if err != nil || len(open) == 0 {
		return err
	}
	return refuse("subthreads %s have not ended: wait for them, or close them with close_subthread, then finish", threadNos(open))
}

// leadMessageThread is a lead talking to another thread's lead.
func (s *Service) leadMessageThread(ctx context.Context, tx *core.Tx, c *call, in protocol.MessageThreadInput) (Result, error) {
	from, err := requireThread(c)
	if err != nil {
		return Result{}, err
	}
	if int(in.No) == from.No {
		return Result{}, refuse("you cannot message your own thread")
	}
	t, err := core.ThreadByNo(ctx, tx, from.ProjectID, int(in.No))
	if err != nil {
		return Result{}, refuse("no thread #%d", in.No)
	}
	if err := relayBudget(ctx, tx, from, t); err != nil {
		return Result{}, err
	}
	t, reopened, err := s.reopenForLead(ctx, tx, from, t)
	if err != nil {
		return Result{}, err
	}
	lead, err := core.Lead(ctx, tx, t.ID)
	if err != nil {
		return Result{}, err
	}
	text := fmt.Sprintf("[Message from thread #%d · %s · %s]\n%s\n(reply with message_thread(no: %d); if no reply is needed, do not reply.)",
		from.No, from.Title, c.emp().Name, in.Text, from.No)
	files, err := s.useAttachments(ctx, tx, c, in.Attachments)
	if err != nil {
		return Result{}, err
	}
	if text, err = s.orch.DeliveryText(ctx, tx, t.ProjectID, text, files); err != nil {
		return Result{}, err
	}
	rec := &core.NewMessage{ConversationID: t.ID, Role: "system", Author: c.emp().Name, Kind: "relay", Content: in.Text,
		Meta: withFiles(relayMeta("in", "message", threadPeer(from), nil), files)}
	if _, err := s.orch.Deliver(ctx, tx, lead, orchestrator.Delivery{Text: text, Kind: "mail", Priority: "next", Record: rec, Attachments: files}); err != nil {
		return Result{}, err
	}
	if err := s.recordRelay(ctx, tx, from.ProjectID, from.ID, &from.ID, c.emp().Name, in.Text,
		withFiles(relayMeta("out", "message", threadPeer(t), nil), files)); err != nil {
		return Result{}, err
	}
	return okText(fmt.Sprintf("%ssent to the lead of #%d%s", reopened, t.No, carried(files))), nil
}

// relayBudget: two threads exchange at most MaxRelaysPerHour messages an hour (both directions).
func relayBudget(ctx context.Context, tx *core.Tx, a, b core.Thread) error {
	ab, err := relaysLastHour(ctx, tx, b.ID, threadPeer(a))
	if err != nil {
		return err
	}
	ba, err := relaysLastHour(ctx, tx, a.ID, threadPeer(b))
	if err != nil {
		return err
	}
	if ab+ba >= MaxRelaysPerHour {
		return refuse("#%d and #%d exchanged %d messages this hour, the limit: gather what you have to say and send it a bit later, or carry on by yourself first", a.No, b.No, ab+ba)
	}
	return nil
}

// reopenForLead: archived refuses; a finished thread is reopened only when it is the caller's subthread.
func (s *Service) reopenForLead(ctx context.Context, tx *core.Tx, from, t core.Thread) (core.Thread, string, error) {
	switch t.Status {
	case "archived":
		return t, "", refuse("thread #%d is archived and cannot be messaged", t.No)
	case "done", "failed":
	default:
		return t, "", nil
	}
	if t.ParentID == nil || *t.ParentID != from.ID {
		return t, "", refuse("thread #%d has ended (%s) and cannot be messaged: to have it carry on, ask the coordinator with message_coordinator", t.No, t.Status)
	}
	t, err := core.LockThread(ctx, tx, t.ID)
	if err != nil {
		return t, "", err
	}
	if err := s.orch.Reopen(ctx, tx, t, orchestrator.ReopenByParent); err != nil {
		return t, "", err
	}
	if t, err = core.ThreadByID(ctx, tx, t.ID); err != nil {
		return t, "", err
	}
	return t, fmt.Sprintf("subthread #%d had ended and is reopened (now %s); ", t.No, t.Status), nil
}

// messageCoordinator is a lead talking to the project coordinator (lead).
func (s *Service) messageCoordinator(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	from, err := requireThread(c)
	if err != nil {
		return Result{}, err
	}
	var in protocol.MessageCoordinatorInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	coord, conv, err := s.orch.EnsureCoordinatorTx(ctx, tx, from.UserID, from.ProjectID)
	if err != nil {
		return Result{}, err
	}
	n, err := relaysLastHour(ctx, tx, conv, threadPeer(from))
	if err != nil {
		return Result{}, err
	}
	if n >= MaxRelaysPerHour {
		return Result{}, refuse("you sent the coordinator %d messages this hour, the limit: gather what you have to say and send it a bit later", n)
	}
	text := fmt.Sprintf("[Message from thread #%d · %s · %s]\n%s\n(reply with message_thread(no: %d); if no reply is needed, do not reply.)",
		from.No, from.Title, c.emp().Name, in.Text, from.No)
	files, err := s.useAttachments(ctx, tx, c, in.Attachments)
	if err != nil {
		return Result{}, err
	}
	if text, err = s.orch.DeliveryText(ctx, tx, from.ProjectID, text, files); err != nil {
		return Result{}, err
	}
	rec := &core.NewMessage{ConversationID: conv, Role: "system", Author: c.emp().Name, Kind: "relay", Content: in.Text,
		Meta: withFiles(relayMeta("in", "message", threadPeer(from), nil), files)}
	if _, err := s.orch.Deliver(ctx, tx, coord, orchestrator.Delivery{Text: text, Kind: "mail", Priority: "next", Record: rec, Attachments: files}); err != nil {
		return Result{}, err
	}
	if err := s.recordRelay(ctx, tx, from.ProjectID, from.ID, &from.ID, c.emp().Name, in.Text,
		withFiles(relayMeta("out", "message", coordinatorPeer, nil), files)); err != nil {
		return Result{}, err
	}
	return okText("sent to the coordinator" + carried(files)), nil
}

// parentNo is the parent thread's number of a subthread (nil: top level or the parent is gone).
func parentNo(ctx context.Context, q core.DB, t core.Thread) *protocol.ThreadNo {
	if t.ParentID == nil {
		return nil
	}
	p, err := core.ThreadByID(ctx, q, *t.ParentID)
	if err != nil {
		return nil
	}
	no := protocol.ThreadNo(p.No)
	return &no
}
