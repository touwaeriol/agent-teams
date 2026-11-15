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
		rec := &core.NewMessage{ConversationID: c.ID, Role: "user", Author: "user", Kind: kind, Content: content, Meta: meta}
		m, err = o.Deliver(ctx, tx, emp, Delivery{Text: content, Kind: "user", Priority: prio, Interrupt: interrupt, Record: rec})
		return err
	})
	return m, err
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

// Archive ends a thread (sessions closed, cleanup queued).
func (o *Orchestrator) Archive(ctx context.Context, tx *core.Tx, t core.Thread) error {
	return o.setStatus(ctx, tx, t, "archived", "")
}
