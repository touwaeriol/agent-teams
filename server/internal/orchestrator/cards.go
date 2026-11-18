package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/core"
)

// Chat cards (architecture.md §6.6): the few tool calls the chats show, stored as messages of
// role "system" whose kind tells the web which card to draw. Every other tool call stays hidden.
//
//	todos     the employee's TodoWrite checklist, updated in place while unfinished
//	plan      an ExitPlanMode plan (Markdown)
//	artifact  a deliverable published with publish_file / publish_deliverable
//	image     a screenshot or image sent with publish_file: not a deliverable, kept as an
//	          attachment (meta.attachments) so the chat shows it
//	resumed   "会话已恢复": the session was rebuilt from a recap after its transcript was lost
const (
	CardTodos    = "todos"
	CardPlan     = "plan"
	CardArtifact = "artifact"
	CardImage    = "image"
	CardResumed  = "resumed"
)

// RecordTodos keeps one live checklist per employee: the latest unfinished one is updated
// in place, a list after a finished one starts a new card. Only the coordinator's and the
// lead's lists are kept (the people the user talks to); members' would only add noise.
func (o *Orchestrator) RecordTodos(ctx context.Context, tx *core.Tx, e core.Employee, todos []protocol.TodoItem) error {
	if e.Tier == "member" || len(todos) == 0 {
		return nil
	}
	conv, err := o.conversationOf(ctx, tx, e)
	if err != nil {
		return err
	}
	meta := map[string]any{"todos": todoMeta(todos), "employee": e.Name}
	content := todoText(todos)
	last, err := core.LatestCard(ctx, tx, conv, CardTodos, e.Name)
	if err == nil && !allDone(last.Meta["todos"]) {
		m, err := core.UpdateCard(ctx, tx, last.ID, content, meta)
		if err != nil {
			return err
		}
		return tx.EmitMessage(ctx, e.ProjectID, e.WorkThreadID(), m)
	}
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		return err
	}
	return o.recordCard(ctx, tx, e, conv, CardTodos, content, meta)
}

// RecordPlan stores an ExitPlanMode plan as a card.
func (o *Orchestrator) RecordPlan(ctx context.Context, tx *core.Tx, e core.Employee, plan string) error {
	plan = strings.TrimSpace(plan)
	if plan == "" {
		return nil
	}
	conv, err := o.conversationOf(ctx, tx, e)
	if err != nil {
		return err
	}
	return o.recordCard(ctx, tx, e, conv, CardPlan, plan, map[string]any{"employee": e.Name})
}

// RecordArtifact shows a published deliverable in the publisher's conversation.
func (o *Orchestrator) RecordArtifact(ctx context.Context, tx *core.Tx, e core.Employee, d core.Deliverable, version int) error {
	conv, err := o.conversationOf(ctx, tx, e)
	if err != nil {
		return err
	}
	meta := map[string]any{"employee": e.Name, "deliverable": map[string]any{"id": d.ID, "kind": d.Kind, "title": d.Title,
		"url": d.URL, "hosted": d.Detail["hosted"] == true, "entry": d.Detail["entry"], "render": d.Detail["render"],
		"version": version}}
	return o.recordCard(ctx, tx, e, conv, CardArtifact, fmt.Sprintf("%s 发布了交付物：%s", e.Name, d.Title), meta)
}

// RecordImage shows an image an employee sent with publish_file (stored as an attachment, not a
// deliverable) in the employee's conversation.
func (o *Orchestrator) RecordImage(ctx context.Context, tx *core.Tx, e core.Employee, title string, img core.AttachmentRef) error {
	conv, err := o.conversationOf(ctx, tx, e)
	if err != nil {
		return err
	}
	meta := withImages(map[string]any{"employee": e.Name, "title": title}, []core.AttachmentRef{img})
	return o.recordCard(ctx, tx, e, conv, CardImage, fmt.Sprintf("%s 上传了截图：%s", e.Name, title), meta)
}

func (o *Orchestrator) recordCard(ctx context.Context, tx *core.Tx, e core.Employee, conv, kind, content string, meta map[string]any) error {
	m, err := core.InsertMessage(ctx, tx, core.NewMessage{ConversationID: conv, Role: "system", Author: e.Name, Kind: kind,
		Content: content, Meta: meta})
	if err != nil {
		return err
	}
	return tx.EmitMessage(ctx, e.ProjectID, e.WorkThreadID(), m)
}

func todoMeta(todos []protocol.TodoItem) []map[string]any {
	out := make([]map[string]any, len(todos))
	for i, t := range todos {
		item := map[string]any{"content": t.Content, "status": string(t.Status)}
		if t.ActiveForm != nil && *t.ActiveForm != "" {
			item["active_form"] = *t.ActiveForm
		}
		out[i] = item
	}
	return out
}

func todoText(todos []protocol.TodoItem) string {
	marks := map[protocol.TodoItemStatus]string{"completed": "[x]", "in_progress": "[~]", "pending": "[ ]"}
	lines := make([]string, len(todos))
	for i, t := range todos {
		lines[i] = marks[t.Status] + " " + t.Content
	}
	return strings.Join(lines, "\n")
}

// allDone reports whether a stored checklist (meta.todos) has every item completed.
func allDone(v any) bool {
	items, ok := v.([]any)
	if !ok || len(items) == 0 {
		return true
	}
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m["status"] != "completed" {
			return false
		}
	}
	return true
}
