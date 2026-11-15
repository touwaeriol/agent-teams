package orchestrator

import (
	"context"
	"errors"
	"strings"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/status"
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
	return core.Employee{}, ErrLegacy
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
	// Images are attachment ids of the conversation's project, in order.
	Images []string
}

// PostUserMessageWith stores the user's message (with any images) and delivers it.
func (o *Orchestrator) PostUserMessageWith(ctx context.Context, in UserMessage) (core.Message, error) {
	userID, convID, content, interrupt, meta := in.UserID, in.ConvID, in.Content, in.Interrupt, in.Meta
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
		rec := &core.NewMessage{ConversationID: c.ID, Role: "user", Author: "user", Kind: kind, Content: content, Meta: meta}
		m, err = o.Deliver(ctx, tx, emp, Delivery{Text: content, Kind: "user", Priority: prio, Interrupt: interrupt, Record: rec, Images: images})
		return err
	})
	return m, err
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

func withImages(meta map[string]any, images []core.AttachmentRef) map[string]any {
	out := map[string]any{}
	for k, v := range meta {
		out[k] = v
	}
	out["attachments"] = images
	return out
}

// checkThreadOpen refuses messages to threads that are finished.
func (o *Orchestrator) checkThreadOpen(ctx context.Context, tx *core.Tx, c core.Conversation) error {
	if c.Kind != "thread" {
		return nil
	}
	t, err := core.ThreadByID(ctx, tx, c.ID)
	if err != nil {
		return err
	}
	if t.Status == "done" || t.Status == "archived" || t.Status == "failed" {
		return ErrNotActive
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
		emp, err := core.EmployeeByName(ctx, tx, t.ProjectID, &t.ID, name)
		if errors.Is(err, core.ErrNotFound) {
			return ErrNoEmployee
		}
		if err != nil {
			return err
		}
		rec := &core.NewMessage{ConversationID: t.ID, Role: "user", Author: "user", Kind: "chat", Content: content,
			Meta: map[string]any{"to": name}}
		m, err = o.Deliver(ctx, tx, emp, Delivery{Text: "[用户直接对你说] " + content, Kind: "user", Priority: "next", Record: rec})
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
	if err := tx.EmitMessage(ctx, e.ProjectID, e.ThreadID, m); err != nil {
		return err
	}
	if e.ThreadID != nil {
		return tx.EmitThread(ctx, *e.ThreadID)
	}
	return nil
}

// Pause interrupts every turn of a thread and parks its pool (§7.1 paused).
func (o *Orchestrator) Pause(ctx context.Context, tx *core.Tx, t core.Thread, reason string) error {
	emps, err := core.ThreadEmployees(ctx, tx, t.ID)
	if err != nil {
		return err
	}
	for _, e := range emps {
		if err := o.Interrupt(ctx, tx, e, "thread_paused"); err != nil {
			return err
		}
	}
	return o.setStatus(ctx, tx, t, "paused", reason)
}

// Resume puts a paused thread back to running (or queued when no slot is free).
func (o *Orchestrator) Resume(ctx context.Context, tx *core.Tx, t core.Thread) error {
	running, err := core.RunningThreadCount(ctx, tx, t.UserID)
	if err != nil {
		return err
	}
	if running >= o.maxRunning(ctx, t.UserID) {
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
	var from string
	if err := tx.QueryRow(ctx, `SELECT archived_from FROM at_threads WHERE id = $1`, t.ID).Scan(&from); err != nil {
		return core.MapErr(err)
	}
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
// in the project library without the thread link. The caller queues the worktree purge first.
func (o *Orchestrator) DeleteThread(ctx context.Context, tx *core.Tx, t core.Thread) error {
	if err := o.closeThreadSessions(ctx, tx, t.ID, "thread_done"); err != nil {
		return err
	}
	if _, err := tx.Emit(ctx, t.ProjectID, nil, core.EvThread, "", map[string]any{"deleted": map[string]any{"id": t.ID, "no": t.No}}); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM at_conversations WHERE id = $1`, t.ID); err != nil {
		return err
	}
	if t.Status == "running" || t.Status == "waiting" {
		tx.AfterCommit(func(ctx context.Context) { go o.StartQueued(context.WithoutCancel(ctx), t.UserID) })
	}
	return nil
}
