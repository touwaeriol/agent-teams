package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/touwaeriol/agent-teams/server/internal/composer"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/status"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

func statusTouch(cause string) status.Observation {
	return status.Observation{Kind: status.Touch, Cause: cause}
}

// targetOf is the employee a conversation talks to (coordinator or thread lead).
func (o *Orchestrator) targetOf(ctx context.Context, tx *core.Tx, c core.Conversation) (core.Employee, error) {
	switch c.Kind {
	case "coordinator":
		emp, _, err := o.ensureCoordinatorTx(ctx, tx, c.UserID, deref(c.ProjectID))
		return emp, err
	case "thread":
		return core.Lead(ctx, tx, c.ID)
	}
	return core.Employee{}, core.ErrNotFound
}

// PostUserMessage stores the user's message and delivers it to the conversation's employee.
func (o *Orchestrator) PostUserMessage(ctx context.Context, userID, convID, content string, interrupt bool, meta map[string]any) (core.Message, error) {
	return o.PostUserMessageWith(ctx, UserMessage{UserID: userID, ConvID: convID, Content: content, Interrupt: interrupt, Meta: meta})
}

// UserMessage is what the user sends to a conversation.
type UserMessage struct {
	UserID, ConvID, Content string
	Interrupt               bool
	Meta                    map[string]any
	// Images are attachment ids of the conversation's project, in order (images, PDFs and
	// text files: a text file's content is put into the delivered text).
	Images []string
	// Deliver replaces Content in what the employee gets (an expanded custom command); the stored
	// message keeps Content.
	Deliver string
	// Context is appended to the delivered text only (the resolved @ references); the stored
	// message keeps the user's own words.
	Context string
}

// PostUserMessageWith stores the user's message (with any images) and delivers it. The GitHub
// reads a session open needs are made first, outside the transaction (WarmRepos): the delivery
// holds the project row, and every other delivery and event of the project waits for it.
func (o *Orchestrator) PostUserMessageWith(ctx context.Context, in UserMessage) (core.Message, error) {
	userID, convID, content, interrupt, meta := in.UserID, in.ConvID, in.Content, in.Interrupt, in.Meta
	if c, err := core.ConversationOfUser(ctx, o.core.Pool, userID, convID); err == nil && c.ProjectID != nil {
		o.WarmProject(ctx, *c.ProjectID)
	}
	var m core.Message
	err := o.core.InTx(ctx, func(tx *core.Tx) error {
		c, err := core.ConversationOfUser(ctx, tx, userID, convID)
		if err != nil {
			return err
		}
		if err := o.checkThreadOpen(ctx, tx, c); err != nil {
			return err
		}
		emp, err := o.targetOf(ctx, tx, c)
		if err != nil {
			return err
		}
		kind, prio := "chat", "next"
		if interrupt {
			kind, prio = "interrupt", "now"
		}
		images, err := o.messageImages(ctx, tx, c, in.Images)
		if err != nil {
			return err
		}
		if len(images) > 0 {
			meta = withImages(meta, images)
		}
		deliver := content
		if in.Deliver != "" {
			deliver = in.Deliver
		}
		text, files, err := o.inlineTexts(ctx, tx, c, deliver, images)
		if err != nil {
			return err
		}
		rec := &core.NewMessage{ConversationID: c.ID, Role: "user", Author: "user", Kind: kind, Content: content, Meta: meta}
		tag, err := messageTag(ctx, tx, c, rec)
		if err != nil {
			return err
		}
		answered, err := o.ChatAnswers(ctx, tx, c, emp, userID, content)
		if err != nil {
			return err
		}
		m, err = o.Deliver(ctx, tx, emp, Delivery{Text: text + in.Context + answered + tag, Kind: "user", Priority: prio, Interrupt: interrupt, Record: rec, Images: files})
		return err
	})
	return m, err
}

// messageTag gives the coordinator the stored id of each user message ("[message_id: 42]" at
// the end), so forward_to_thread can name the messages it forwards.
func messageTag(ctx context.Context, tx *core.Tx, c core.Conversation, rec *core.NewMessage) (string, error) {
	if c.Kind != "coordinator" {
		return "", nil
	}
	id, err := core.NextMessageID(ctx, tx)
	if err != nil {
		return "", err
	}
	rec.ID = id
	return fmt.Sprintf("\n\n[message_id: %d]", id), nil
}

// messageImages loads the attached images; they must belong to the conversation's project.
func (o *Orchestrator) messageImages(ctx context.Context, tx *core.Tx, c core.Conversation, ids []string) ([]core.AttachmentRef, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > core.MaxMessageImages {
		return nil, ErrTooManyImages
	}
	list, err := core.ProjectAttachments(ctx, tx, deref(c.ProjectID), ids)
	if err != nil {
		return nil, err
	}
	out := make([]core.AttachmentRef, len(list))
	for i, a := range list {
		out[i] = a.Ref()
	}
	return out, nil
}

// maxInlineText is how much of one attached text file goes into the message.
const maxInlineText = 60000

// inlineTexts puts the attached text files into the delivered text and returns the attachments
// that travel as files (images, PDFs).
func (o *Orchestrator) inlineTexts(ctx context.Context, tx *core.Tx, c core.Conversation, text string, refs []core.AttachmentRef) (string, []core.AttachmentRef, error) {
	var ids []string
	for _, r := range refs {
		if core.IsInlineText(r.ContentType) {
			ids = append(ids, r.ID)
		}
	}
	if len(ids) == 0 {
		return text, refs, nil
	}
	data, err := core.AttachmentTexts(ctx, tx, deref(c.ProjectID), ids)
	if err != nil {
		return "", nil, err
	}
	var b strings.Builder
	b.WriteString(text)
	files := make([]core.AttachmentRef, 0, len(refs))
	for _, r := range refs {
		if !core.IsInlineText(r.ContentType) {
			files = append(files, r)
			continue
		}
		body, cut := clipText(string(data[r.ID]), maxInlineText)
		fmt.Fprintf(&b, "\n\n[附件：%s%s]\n```\n%s\n```", r.Name, cut, body)
	}
	return b.String(), files, nil
}

// clipText keeps the first n bytes of s on a rune boundary; the note says it was cut.
func clipText(s string, n int) (string, string) {
	if len(s) <= n {
		return s, ""
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], fmt.Sprintf("，只取前 %d 字节", n)
}

func withImages(meta map[string]any, images []core.AttachmentRef) map[string]any {
	out := map[string]any{}
	for k, v := range meta {
		out[k] = v
	}
	out["attachments"] = images
	return out
}

// checkThreadOpen makes a thread conversation ready for the user's message: an archived thread
// refuses it (unarchive first), a finished one is reopened so the message wakes the lead.
func (o *Orchestrator) checkThreadOpen(ctx context.Context, tx *core.Tx, c core.Conversation) error {
	if c.Kind != "thread" {
		return nil
	}
	t, err := core.ThreadByID(ctx, tx, c.ID)
	if err != nil || !threadOver(t.Status) {
		return err
	}
	if t, err = core.LockThread(ctx, tx, c.ID); err != nil {
		return err
	}
	return o.reopenForMessage(ctx, tx, t)
}

// reopenForMessage refuses messages to an archived thread and reopens a done or failed one.
func (o *Orchestrator) reopenForMessage(ctx context.Context, tx *core.Tx, t core.Thread) error {
	switch {
	case t.Status == "archived":
		return ErrArchived
	case threadOver(t.Status):
		return o.Reopen(ctx, tx, t, "message")
	}
	return nil
}

// InterruptConversation stops the current turn of the conversation's employee.
func (o *Orchestrator) InterruptConversation(ctx context.Context, userID, convID string) error {
	return o.core.InTx(ctx, func(tx *core.Tx) error {
		c, err := core.ConversationOfUser(ctx, tx, userID, convID)
		if err != nil {
			return err
		}
		emp, err := o.targetOf(ctx, tx, c)
		if err != nil {
			return err
		}
		return o.Interrupt(ctx, tx, emp, "user_stop")
	})
}

// MessageMember sends the user's words straight to a member (also recorded in the thread conversation).
func (o *Orchestrator) MessageMember(ctx context.Context, t core.Thread, name, content string) (core.Message, error) {
	var m core.Message
	err := o.core.InTx(ctx, func(tx *core.Tx) error {
		cur, err := core.LockThread(ctx, tx, t.ID)
		if err != nil {
			return err
		}
		if err := o.reopenForMessage(ctx, tx, cur); err != nil {
			return err
		}
		emp, err := core.EmployeeByName(ctx, tx, t.ProjectID, &t.ID, name)
		if errors.Is(err, core.ErrNotFound) {
			return ErrNoEmployee
		}
		if err != nil {
			return err
		}
		deliver, cmd, err := composer.ExpandFor(ctx, tx, emp, content)
		if err != nil {
			return err
		}
		meta := map[string]any{"to": name}
		if cmd != nil {
			meta["command"] = cmd
		}
		if deliver == "" {
			deliver = content
		}
		rec := &core.NewMessage{ConversationID: t.ID, Role: "user", Author: "user", Kind: "chat", Content: content, Meta: meta}
		m, err = o.Deliver(ctx, tx, emp, Delivery{Text: "[用户直接对你说] " + deliver, Kind: "user", Priority: "next", Record: rec})
		return err
	})
	return m, err
}

// InterruptMember stops a member's current turn.
func (o *Orchestrator) InterruptMember(ctx context.Context, t core.Thread, name string) error {
	return o.core.InTx(ctx, func(tx *core.Tx) error {
		emp, err := core.EmployeeByName(ctx, tx, t.ProjectID, &t.ID, name)
		if errors.Is(err, core.ErrNotFound) {
			return ErrNoEmployee
		}
		if err != nil {
			return err
		}
		return o.Interrupt(ctx, tx, emp, "user_stop")
	})
}

// RecordReply stores a coordinator/lead final text as a conversation message.
func (o *Orchestrator) RecordReply(ctx context.Context, tx *core.Tx, e core.Employee, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	conv, err := o.conversationOf(ctx, tx, e)
	if err != nil {
		return err
	}
	m, err := core.InsertMessage(ctx, tx, core.NewMessage{ConversationID: conv, Role: "leader", Author: e.Name, Content: text})
	if err != nil {
		return err
	}
	if err := tx.EmitMessage(ctx, e.ProjectID, e.WorkThreadID(), m); err != nil {
		return err
	}
	if e.WorkThreadID() != nil {
		return tx.EmitThread(ctx, *e.WorkThreadID())
	}
	return nil
}

// Pause interrupts every turn of a thread and parks its pool (§7.1 paused). A project member
// working in another thread now is left alone.
func (o *Orchestrator) Pause(ctx context.Context, tx *core.Tx, t core.Thread, reason string) error {
	emps, err := core.ThreadParticipants(ctx, tx, t.ID)
	if err != nil {
		return err
	}
	for _, e := range emps {
		if e.ProjectMember() && (e.CurrentThreadID == nil || *e.CurrentThreadID != t.ID) {
			continue
		}
		if err := o.Interrupt(ctx, tx, e, "thread_paused"); err != nil {
			return err
		}
	}
	return o.setStatus(ctx, tx, t, "paused", reason)
}

// Resume puts a paused thread back to running (or queued when no slot is free).
func (o *Orchestrator) Resume(ctx context.Context, tx *core.Tx, t core.Thread) error {
	free, err := o.threadSlotFree(ctx, tx, t.UserID)
	if err != nil {
		return err
	}
	if !free {
		return o.setStatus(ctx, tx, t, "queued", "")
	}
	return o.setStatus(ctx, tx, t, "running", "")
}

// Archive ends a thread (sessions closed, cleanup queued) and remembers its status for Unarchive.
func (o *Orchestrator) Archive(ctx context.Context, tx *core.Tx, t core.Thread) error {
	if t.Status == "archived" {
		return nil
	}
	if err := core.UpdateThread(ctx, tx, t.ID, map[string]any{"archived_from": t.Status}); err != nil {
		return err
	}
	return o.setStatus(ctx, tx, t, "archived", "")
}

// Complete is the user closing a thread as done from the page (a finished thread its lead did
// not end, e.g. "I'll close it myself"): the optional summary is kept, the status goes to done,
// which closes every session and cancels open cards (same path as finish), and the coordinator
// is told. A thread whose PR waits for the user's decision (review) or is being merged is
// refused: the user accepts or rejects it. After a reject the PR is back with the lead, and
// the user may still close the thread (the PR stays open on the host).
func (o *Orchestrator) Complete(ctx context.Context, tx *core.Tx, t core.Thread, summary string) error {
	switch t.Status {
	case "done":
		return nil
	case "failed", "archived":
		return ErrNotActive
	}
	repos, err := core.ThreadRepos(ctx, tx, t.ID)
	if err != nil {
		return err
	}
	for _, r := range repos {
		awaiting := r.PRState == "accepting" || (t.Status == "review" && r.PRState != "merged" && r.PRState != "closed")
		if r.PRNumber != nil && awaiting {
			return ErrPRAwaiting
		}
	}
	if queued, err := core.MergeQueue(ctx, tx, t.ID, true); err != nil || len(queued) > 0 {
		if err != nil {
			return err
		}
		return ErrMerging
	}
	line := "用户在页面上把讨论串标记为完成。"
	if summary = strings.TrimSpace(summary); summary != "" {
		if err := core.UpdateThread(ctx, tx, t.ID, map[string]any{"summary": summary}); err != nil {
			return err
		}
		line += "总结：" + clipRunes(strings.SplitN(summary, "\n", 2)[0], 200)
	}
	return o.setStatusLine(ctx, tx, t, "done", "", line)
}

// deleteThreadImages removes the images attached to a thread's messages, unless a message
// elsewhere in the project still shows them.
func deleteThreadImages(ctx context.Context, tx *core.Tx, t core.Thread) error {
	_, err := store.Exec(ctx, tx, `WITH refs AS (
		SELECT m.conversation_id, r->>'id' AS id FROM at_messages m
		JOIN at_conversations c ON c.id = m.conversation_id AND c.project_id = $2,
		LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(m.meta->'attachments') = 'array'
			THEN m.meta->'attachments' ELSE '[]'::jsonb END) r)
		DELETE FROM at_attachments WHERE project_id = $2
		AND id IN (SELECT id FROM refs WHERE conversation_id = $1)
		AND id NOT IN (SELECT id FROM refs WHERE conversation_id <> $1 AND id IS NOT NULL)`, t.ID, t.ProjectID)
	return err
}

// UnarchiveStatus is where an archived thread goes back to: a finished thread stays finished,
// one archived while it was still going comes back paused (its sessions were closed).
func UnarchiveStatus(archivedFrom string) string {
	if archivedFrom == "done" || archivedFrom == "failed" {
		return archivedFrom
	}
	return "paused"
}

// Unarchive puts an archived thread back in the list.
func (o *Orchestrator) Unarchive(ctx context.Context, tx *core.Tx, t core.Thread) error {
	if t.Status != "archived" {
		return ErrNotArchived
	}
	var row struct{ ArchivedFrom string }
	if err := tx.Gorm(ctx).Table("at_threads").Select("archived_from").Where("id = ?", t.ID).Take(&row).Error; err != nil {
		return core.MapErr(err)
	}
	from := row.ArchivedFrom
	next := UnarchiveStatus(from)
	set := map[string]any{"archived_from": ""}
	if next == "paused" {
		set["finished_at"] = nil
	}
	if err := core.UpdateThread(ctx, tx, t.ID, set); err != nil {
		return err
	}
	reason := map[bool]string{true: "user", false: ""}[next == "paused"]
	return o.setStatus(ctx, tx, t, next, reason)
}

// DeleteThread removes a thread for good: its sessions are closed, then the thread and
// everything hanging off it (employees, tasks, cards, messages) is deleted. Deliverables stay
// in the project library without the thread link. purge queues the worktree removal: it runs
// after the session closes are queued (so they reach the nodes first) and before the rows go.
func (o *Orchestrator) DeleteThread(ctx context.Context, tx *core.Tx, t core.Thread, purge func() error) error {
	if err := o.EndThreadWork(ctx, tx, t.ID); err != nil {
		return err
	}
	if err := purge(); err != nil {
		return err
	}
	if _, err := tx.Emit(ctx, t.ProjectID, nil, core.EvThread, "", map[string]any{"deleted": map[string]any{"id": t.ID, "no": t.No}}); err != nil {
		return err
	}
	if err := deleteThreadImages(ctx, tx, t); err != nil {
		return err
	}
	if err := tx.Gorm(ctx).Where("id = ?", t.ID).Delete(&core.Conversation{}).Error; err != nil {
		return err
	}
	if t.Status == "running" || t.Status == "waiting" {
		tx.AfterCommit(func(ctx context.Context) { go o.StartQueued(context.WithoutCancel(ctx), t.UserID) })
	}
	return nil
}
