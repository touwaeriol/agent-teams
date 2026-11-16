package toolsvc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// openThread opens a thread for the brief (coordinator).
func (s *Service) openThread(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.OpenThreadInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	owner := c.sc.OwnerID
	t, err := s.orch.OpenThreadTx(ctx, tx, orchestrator.ThreadRequest{UserID: owner, ProjectID: c.emp().ProjectID,
		Title: strings.TrimSpace(in.Title), Brief: in.Brief, OnlyRoles: in.Roles, Repos: focusOf(in.Repos), OpenedBy: "coordinator"})
	if errors.Is(err, orchestrator.ErrUnknownRepo) {
		return Result{}, refuse("%s", err.Error())
	}
	if err != nil {
		return Result{}, err
	}
	if err := s.noteOpened(ctx, tx, c, t); err != nil {
		return Result{}, err
	}
	tx.AfterCommit(func(ctx context.Context) { go s.orch.StartQueued(context.WithoutCancel(ctx), owner) })
	out := protocol.OpenThreadResult{No: protocol.ThreadNo(t.No), ThreadID: t.ID, Branch: t.Branch}
	return okJSON(fmt.Sprintf("已开讨论串 #%d（%s），分支 %s", t.No, t.Title, t.Branch), out), nil
}

func focusOf(in []protocol.RepoFocus) []orchestrator.RepoFocus {
	out := make([]orchestrator.RepoFocus, len(in))
	for i, r := range in {
		out[i] = orchestrator.RepoFocus{Repo: r.Repo, Base: deref(r.Base)}
	}
	return out
}

// noteOpened puts a clickable "opened #n" line in the coordinator conversation (meta.opens_thread).
func (s *Service) noteOpened(ctx context.Context, tx *core.Tx, c *call, t core.Thread) error {
	conv, err := s.orch.ConversationOf(ctx, tx, c.emp())
	if err != nil {
		return err
	}
	m, err := core.InsertMessage(ctx, tx, core.NewMessage{ConversationID: conv, Role: "system", Author: c.emp().Name, Kind: "note",
		Content: fmt.Sprintf("开了讨论串 #%d · %s", t.No, t.Title), Meta: map[string]any{"opens_thread": t.No}})
	if err != nil {
		return err
	}
	return tx.EmitMessage(ctx, c.emp().ProjectID, nil, m)
}

// listThreads summarises the project's threads.
func (s *Service) listThreads(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.ListThreadsInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	var statuses []string
	if in.Status != nil {
		statuses = []string{string(*in.Status)}
	}
	threads, err := core.Threads(ctx, tx, c.emp().ProjectID, statuses)
	if err != nil {
		return Result{}, err
	}
	out := protocol.ListThreadsResult{Threads: []protocol.ThreadSummary{}}
	var lines []string
	for _, t := range threads {
		counts, err := core.ThreadCountsOf(ctx, tx, t)
		if err != nil {
			return Result{}, err
		}
		out.Threads = append(out.Threads, protocol.ThreadSummary{No: protocol.ThreadNo(t.No), Title: t.Title,
			Status: protocol.ThreadStatus(t.Status), PlanDone: counts.MainDone, PlanTotal: counts.MainTotal, Waiting: counts.Waiting})
		lines = append(lines, fmt.Sprintf("#%d %s [%s] 计划 %d/%d，等你处理 %d", t.No, t.Title, t.Status, counts.MainDone,
			counts.MainTotal, counts.Waiting))
	}
	if len(lines) == 0 {
		return okJSON("还没有讨论串", out), nil
	}
	return okJSON(strings.Join(lines, "\n"), out), nil
}

// threadStatus reports one thread: plan, recent activity, the lead's latest reply, PR.
func (s *Service) threadStatus(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.ThreadStatusInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	t, err := core.ThreadByNo(ctx, tx, c.emp().ProjectID, int(in.No))
	if err != nil {
		return Result{}, refuse("没有讨论串 #%d", in.No)
	}
	out, err := s.statusOf(ctx, tx, t)
	if err != nil {
		return Result{}, err
	}
	return okJSON(statusText(out), out), nil
}

func (s *Service) statusOf(ctx context.Context, tx *core.Tx, t core.Thread) (protocol.ThreadStatusResult, error) {
	out := protocol.ThreadStatusResult{No: protocol.ThreadNo(t.No), Title: t.Title, Status: protocol.ThreadStatus(t.Status),
		Plan: []protocol.PlanStep{}, PRURL: t.PRURL}
	plan, err := core.MainTasks(ctx, tx, t.ID)
	if err != nil {
		return out, err
	}
	for _, m := range plan {
		out.Plan = append(out.Plan, protocol.PlanStep{Title: m.Title, Status: protocol.PlanStepStatus(m.Status)})
	}
	if v, err := s.core.ThreadViewOf(ctx, tx, t); err == nil && v.LeadLast != nil {
		out.LeadLastReply = &v.LeadLast.Text
	}
	acts, err := core.ThreadActivity(ctx, tx, t.ID, 0, 10)
	if err != nil {
		return out, err
	}
	for _, a := range acts {
		out.Recent = append(out.Recent, a.Employee+"："+a.Text)
	}
	return out, nil
}

func statusText(r protocol.ThreadStatusResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#%d %s [%s]\n", r.No, r.Title, r.Status)
	for _, p := range r.Plan {
		fmt.Fprintf(&b, "- [%s] %s\n", p.Status, p.Title)
	}
	if r.LeadLastReply != nil {
		fmt.Fprintf(&b, "组长最近说：%s\n", *r.LeadLastReply)
	}
	if r.PRURL != nil {
		fmt.Fprintf(&b, "PR：%s\n", *r.PRURL)
	}
	for _, line := range r.Recent {
		fmt.Fprintf(&b, "· %s\n", line)
	}
	return b.String()
}

// messageThread is the coordinator talking to a thread's lead.
func (s *Service) messageThread(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.MessageThreadInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	t, err := core.ThreadByNo(ctx, tx, c.emp().ProjectID, int(in.No))
	if err != nil {
		return Result{}, refuse("没有讨论串 #%d", in.No)
	}
	if !t.Active() && t.Status != "paused" && t.Status != "queued" {
		return Result{}, refuse("讨论串 #%d 已经结束（%s）", t.No, t.Status)
	}
	lead, err := core.Lead(ctx, tx, t.ID)
	if err != nil {
		return Result{}, err
	}
	text := "[Message from coordinator]\n" + in.Text
	rec := &core.NewMessage{ConversationID: t.ID, Role: "system", Author: c.emp().Name, Kind: "chat", Content: in.Text}
	if _, err := s.orch.Deliver(ctx, tx, lead, orchestrator.Delivery{Text: text, Kind: "mail", Priority: "next", Record: rec}); err != nil {
		return Result{}, err
	}
	return okText(fmt.Sprintf("已转给 #%d 的组长", t.No)), nil
}

// remember writes project memory (source = coordinator).
func (s *Service) remember(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.RememberInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	return okText("已记住"), s.addMemory(ctx, tx, c.emp().ProjectID, strings.TrimSpace(in.Text), "coordinator")
}

func (s *Service) addMemory(ctx context.Context, tx *core.Tx, projectID, text, source string) error {
	var item struct {
		ID        string `json:"id"`
		Text      string `json:"text"`
		Source    string `json:"source"`
		Pinned    bool   `json:"pinned"`
		CreatedAt any    `json:"created_at"`
	}
	err := store.QueryRow(ctx, tx, `INSERT INTO at_project_memory (id, project_id, text, source) VALUES ($1, $2, $3, $4)
		RETURNING id, text, source, pinned, created_at`, core.NewID("me"), projectID, text, source).
		Scan(&item.ID, &item.Text, &item.Source, &item.Pinned, &item.CreatedAt)
	if err != nil {
		return err
	}
	_, err = tx.Emit(ctx, projectID, nil, core.EvMemory, source, map[string]any{"item": item, "op": "add"})
	return err
}
