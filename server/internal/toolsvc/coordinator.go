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

// openThread opens a thread for the brief (coordinator).
func (s *Service) openThread(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.OpenThreadInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	owner := c.sc.OwnerID
	source, err := s.sourceMessage(ctx, tx, c, in.MessageID)
	if err != nil {
		return Result{}, err
	}
	t, err := s.orch.OpenThreadTx(ctx, tx, orchestrator.ThreadRequest{UserID: owner, ProjectID: c.emp().ProjectID,
		Title: strings.TrimSpace(in.Title), Brief: in.Brief, OnlyRoles: in.Roles, Repos: focusOf(in.Repos), OpenedBy: "coordinator",
		SourceMessageID: source})
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

// sourceMessage is the user's project-chat message open_thread is for: the one named by
// message_id (it must be the user's own, in this conversation), else the user's newest message
// delivered after the coordinator's previous reply (nil when there is none).
func (s *Service) sourceMessage(ctx context.Context, tx *core.Tx, c *call, id *int) (*int64, error) {
	conv, err := s.orch.ConversationOf(ctx, tx, c.emp())
	if err != nil {
		return nil, err
	}
	if id == nil {
		return core.LatestUserMessageID(ctx, tx, conv)
	}
	m, err := core.UserWordByID(ctx, tx, conv, int64(*id))
	if errors.Is(err, core.ErrNotFound) {
		return nil, refuse("message_id %d 不是用户在项目对话里发的消息（用用户消息末尾的 [message_id: N]，或不写）", *id)
	}
	if err != nil {
		return nil, err
	}
	return &m.ID, nil
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
		parent := parentNo(ctx, tx, t)
		out.Threads = append(out.Threads, protocol.ThreadSummary{No: protocol.ThreadNo(t.No), Title: t.Title,
			Status: protocol.ThreadStatus(t.Status), PlanDone: counts.MainDone, PlanTotal: counts.MainTotal, Waiting: counts.Waiting,
			ParentNo: parent})
		line := fmt.Sprintf("#%d %s [%s] 计划 %d/%d，等你处理 %d", t.No, t.Title, t.Status, counts.MainDone,
			counts.MainTotal, counts.Waiting)
		if parent != nil {
			line += fmt.Sprintf("（#%d 的子讨论串）", *parent)
		}
		if c.thread != nil && t.ID == c.thread.ID {
			line += "（你的讨论串）"
		}
		lines = append(lines, line)
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
	out, phase, err := s.statusOf(ctx, tx, t)
	if err != nil {
		return Result{}, err
	}
	return okJSON(statusText(out, phase), out), nil
}

// statusOf is the thread's status for the coordinator; the phase is the state as the page shows
// it (core.PhaseOf), told next to the raw status.
func (s *Service) statusOf(ctx context.Context, tx *core.Tx, t core.Thread) (protocol.ThreadStatusResult, core.ThreadPhase, error) {
	out := protocol.ThreadStatusResult{No: protocol.ThreadNo(t.No), Title: t.Title, Status: protocol.ThreadStatus(t.Status),
		Plan: []protocol.PlanStep{}, PRURL: t.PRURL, ParentNo: parentNo(ctx, tx, t)}
	subs, err := core.Subthreads(ctx, tx, t.ID, false)
	if err != nil {
		return out, core.ThreadPhase{}, err
	}
	for _, sub := range subs {
		out.Subthreads = append(out.Subthreads, protocol.ThreadSummary{No: protocol.ThreadNo(sub.No), Title: sub.Title,
			Status: protocol.ThreadStatus(sub.Status)})
	}
	plan, err := core.MainTasks(ctx, tx, t.ID)
	if err != nil {
		return out, core.ThreadPhase{}, err
	}
	for _, m := range plan {
		out.Plan = append(out.Plan, protocol.PlanStep{Title: m.Title, Status: protocol.PlanStepStatus(m.Status)})
	}
	v, err := s.core.ThreadViewOf(ctx, tx, t)
	if err != nil {
		return out, core.ThreadPhase{}, err
	}
	if v.LeadLast != nil {
		out.LeadLastReply = &v.LeadLast.Text
	}
	merges, err := core.MergeQueue(ctx, tx, t.ID, true)
	if err != nil {
		return out, core.ThreadPhase{}, err
	}
	acts, err := core.ThreadActivity(ctx, tx, t.ID, 0, 10)
	if err != nil {
		return out, core.ThreadPhase{}, err
	}
	for _, a := range acts {
		out.Recent = append(out.Recent, a.Employee+"："+a.Text)
	}
	return out, core.PhaseOf(v, len(merges)), nil
}

func statusText(r protocol.ThreadStatusResult, phase core.ThreadPhase) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#%d %s [%s]\n", r.No, r.Title, r.Status)
	fmt.Fprintf(&b, "状态说明（和页面一致）：%s\n", phase.Text())
	if r.ParentNo != nil {
		fmt.Fprintf(&b, "父讨论串：#%d\n", *r.ParentNo)
	}
	for _, sub := range r.Subthreads {
		fmt.Fprintf(&b, "子讨论串 #%d %s [%s]\n", sub.No, sub.Title, sub.Status)
	}
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
	if c.emp().Tier == "lead" {
		return s.leadMessageThread(ctx, tx, c, in)
	}
	t, err := core.ThreadByNo(ctx, tx, c.emp().ProjectID, int(in.No))
	if err != nil {
		return Result{}, refuse("没有讨论串 #%d", in.No)
	}
	reopened := ""
	switch t.Status {
	case "archived":
		return Result{}, refuse("讨论串 #%d 已归档，不能发消息：需要时请用户先在页面上取消归档", t.No)
	case "done", "failed":
		// As a user's message on the page does: the finished thread is reopened, then the lead gets it.
		if t, err = core.LockThread(ctx, tx, t.ID); err != nil {
			return Result{}, err
		}
		if err := s.orch.Reopen(ctx, tx, t, orchestrator.ReopenByCoordinator); err != nil {
			return Result{}, err
		}
		if t, err = core.ThreadByID(ctx, tx, t.ID); err != nil {
			return Result{}, err
		}
		reopened = fmt.Sprintf("讨论串 #%d 原来已经结束，已重新打开（现在 %s），", t.No, t.Status)
	}
	lead, err := core.Lead(ctx, tx, t.ID)
	if err != nil {
		return Result{}, err
	}
	text := "[Message from coordinator]\n" + in.Text
	rec := &core.NewMessage{ConversationID: t.ID, Role: "system", Author: c.emp().Name, Kind: "relay", Content: in.Text,
		Meta: relayMeta("in", "message", coordinatorPeer, nil)}
	if _, err := s.orch.Deliver(ctx, tx, lead, orchestrator.Delivery{Text: text, Kind: "mail", Priority: "next", Record: rec}); err != nil {
		return Result{}, err
	}
	conv, err := s.orch.ConversationOf(ctx, tx, c.emp())
	if err != nil {
		return Result{}, err
	}
	if err := s.recordRelay(ctx, tx, t.ProjectID, conv, nil, c.emp().Name, in.Text, relayMeta("out", "message", threadPeer(t), nil)); err != nil {
		return Result{}, err
	}
	return okText(fmt.Sprintf("%s已转给 #%d 的组长", reopened, t.No)), nil
}

// forwardToThread passes the user's own project-chat messages, word for word, to a thread's
// lead (or into a new thread) as the user's words (coordinator).
func (s *Service) forwardToThread(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.ForwardToThreadInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	if (in.ThreadNo != nil) == (in.NewThread != nil) {
		return Result{}, refuse("thread_no 和 new_thread 二选一")
	}
	req := orchestrator.ForwardRequest{UserID: c.sc.OwnerID, ProjectID: c.emp().ProjectID, Note: deref(in.Note), By: "coordinator", Latest: 1}
	for _, id := range in.MessageIds {
		req.MessageIDs = append(req.MessageIDs, int64(id))
	}
	if in.Latest != nil {
		req.Latest = *in.Latest
	}
	if in.ThreadNo != nil {
		req.ThreadNo = int(*in.ThreadNo)
	} else {
		req.NewTitle = deref(in.NewThread.Title)
	}
	res, err := s.orch.Forward(ctx, tx, req)
	if err != nil {
		return Result{}, refuseForward(err, req.ThreadNo)
	}
	out := protocol.ForwardToThreadResult{No: protocol.ThreadNo(res.Thread.No), ThreadID: res.Thread.ID, Count: len(res.Sources), Created: res.Created}
	switch {
	case res.Duplicate:
		return okJSON(fmt.Sprintf("这些消息刚刚已经转发给 #%d 了，没有重复发送", res.Thread.No), out), nil
	case res.Created:
		return okJSON(fmt.Sprintf("已新开讨论串 #%d（%s），用户的 %d 条消息作为任务说明转给了组长", res.Thread.No, res.Thread.Title, len(res.Sources)), out), nil
	}
	return okJSON(fmt.Sprintf("已把用户的 %d 条消息原样转给 #%d 的组长", len(res.Sources), res.Thread.No), out), nil
}

// refuseForward explains a refused forward to the coordinator.
func refuseForward(err error, no int) error {
	switch {
	case errors.Is(err, orchestrator.ErrArchived):
		return refuse("讨论串 #%d 已归档，不能转发：请用户先在页面上取消归档，或者用 new_thread 新开一个讨论串", no)
	case errors.Is(err, core.ErrNotFound):
		return refuse("没有讨论串 #%d", no)
	case errors.Is(err, orchestrator.ErrForwardEmpty), errors.Is(err, orchestrator.ErrForwardNotUser),
		errors.Is(err, orchestrator.ErrForwardTooMany), errors.Is(err, orchestrator.ErrForwardFiles):
		return refuse("%s（message_ids 是用户消息末尾的 [message_id: N]）", err.Error())
	}
	return err
}
