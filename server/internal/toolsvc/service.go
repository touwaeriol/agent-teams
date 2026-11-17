// Package toolsvc implements every platform tool of /protocol/schemas/tools
// (architecture.md §6.3): the daemon forwards an MCP call as tool.call, the
// console runs it here and answers with tool.result. Calls are idempotent by
// (session_id, call_id) in at_tool_calls; blocking tools (ask_lead, member
// ask_user, request_review, wait_task, and the push before complete_task)
// stay "waiting" and are answered when the answer arrives — also after
// console or daemon restarts.
package toolsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"gorm.io/gorm"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/delivery"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
	"github.com/touwaeriol/agent-teams/server/internal/scheduler"
	"github.com/touwaeriol/agent-teams/server/internal/status"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Result is a tool outcome.
type Result struct {
	OK         bool
	Text       string
	Structured any
	// Wait leaves the call pending; Complete answers it later.
	Wait bool
	// WaitMeta is stored on the waiting call (e.g. {"waiting_task": "t9"}).
	WaitMeta map[string]any
	// Later computes the result after the transaction commits (network work such as open_pr).
	Later func(ctx context.Context) Result
	// Download lists the files read_deliverable queued as file.download frames before this result.
	Download *protocol.ToolDownload
}

func okText(text string) Result        { return Result{OK: true, Text: text} }
func okJSON(text string, v any) Result { return Result{OK: true, Text: text, Structured: v} }
func failText(format string, a ...any) Result {
	return Result{OK: false, Text: fmt.Sprintf(format, a...)}
}

// call is one tool invocation with its context.
type call struct {
	sc     core.SessionContext
	thread *core.Thread
	callID string
	tool   string
	args   json.RawMessage
	upload *protocol.ToolUpload // publish_file: files the daemon sent as file.chunk
}

func (c *call) emp() core.Employee { return c.sc.Employee }

func (c *call) decode(v any) error { return json.Unmarshal(c.args, v) }

type handler func(ctx context.Context, tx *core.Tx, c *call) (Result, error)

// Service runs platform tools.
type Service struct {
	core      *core.Store
	store     *store.Store
	orch      *orchestrator.Orchestrator
	sched     *scheduler.Scheduler
	deliv     *delivery.Engine
	status    *status.Engine
	gw        *nodegw.Gateway
	validator *protocol.Validator
	log       *slog.Logger
	handlers  map[string]handler
	// Reviews settles permission requests the lead decides (review_permission).
	Reviews Reviewer
}

// Reviewer is the approvals engine as review_permission needs it.
type Reviewer interface {
	LeadReview(ctx context.Context, tx *core.Tx, lead core.Employee, threadID, requestID, decision, reason string) (string, error)
}

// New builds the service and hooks its continuations into the scheduler and delivery engines.
func New(cs *core.Store, st *store.Store, orch *orchestrator.Orchestrator, sched *scheduler.Scheduler, deliv *delivery.Engine,
	se *status.Engine, gw *nodegw.Gateway, log *slog.Logger) (*Service, error) {
	v, err := protocol.NewValidator()
	if err != nil {
		return nil, err
	}
	s := &Service{core: cs, store: st, orch: orch, sched: sched, deliv: deliv, status: se, gw: gw, validator: v, log: log}
	s.handlers = s.table()
	sched.TaskFinished = s.taskFinished
	deliv.PushDone = s.pushDone
	return s, nil
}

func (s *Service) table() map[string]handler {
	return map[string]handler{
		protocol.ToolOpenThread: s.openThread, protocol.ToolListThreads: s.listThreads, protocol.ToolThreadStatus: s.threadStatus,
		protocol.ToolMessageThread: s.messageThread, protocol.ToolForwardToThread: s.forwardToThread, protocol.ToolRemember: s.remember, protocol.ToolAskUser: s.askUser,
		protocol.ToolSetStatus: s.setStatus, protocol.ToolUpdatePlan: s.updatePlan, protocol.ToolAssignTask: s.assignTask,
		protocol.ToolListTasks: s.listTasks, protocol.ToolCancelTask: s.cancelTask, protocol.ToolReplyTo: s.replyTo,
		protocol.ToolBroadcast: s.broadcast, protocol.ToolSendMessage: s.sendMessage, protocol.ToolEnqueueMerge: s.enqueueMerge,
		protocol.ToolOpenPr: s.openPR, protocol.ToolFinish: s.finish, protocol.ToolPublishDeliverable: s.publish,
		protocol.ToolCompleteTask: s.completeTask, protocol.ToolPushBranch: s.pushBranch, protocol.ToolReportProgress: s.reportProgress,
		protocol.ToolAskLead: s.askLead, protocol.ToolReadMessages: s.readMessages, protocol.ToolListTeam: s.listTeam,
		protocol.ToolGetTask: s.getTask, protocol.ToolRequestReview: s.requestReview, protocol.ToolAssignSubtask: s.assignSubtask,
		protocol.ToolWaitTask: s.waitTask, protocol.ToolPublishFile: s.publishFile, protocol.ToolReviewPermission: s.reviewPermission,
		protocol.ToolMemoryList: s.memoryList, protocol.ToolMemoryRead: s.memoryRead, protocol.ToolMemoryWrite: s.memoryWrite,
		protocol.ToolMemoryDelete: s.memoryDelete, protocol.ToolListDeliverables: s.listDeliverables,
		protocol.ToolReadDeliverable: s.readDeliverable, protocol.ToolTellUser: s.tellUser,
	}
}

// HandleCall runs a tool.call (deduplicated by session_id + call_id).
func (s *Service) HandleCall(ctx context.Context, m *protocol.ToolCall) error {
	var later func(ctx context.Context) Result
	sessionID, callID := string(m.SessionID), string(m.CallID)
	err := s.core.InTx(ctx, func(tx *core.Tx) error {
		res, run, err := s.start(ctx, tx, m)
		if err != nil || !run {
			return err
		}
		later = res.Later
		return s.settle(ctx, tx, sessionID, callID, res)
	})
	if err != nil || later == nil {
		return err
	}
	go s.runLater(context.WithoutCancel(ctx), sessionID, callID, later)
	return nil
}

func (s *Service) runLater(ctx context.Context, sessionID, callID string, fn func(context.Context) Result) {
	res := fn(ctx)
	err := s.core.InTx(ctx, func(tx *core.Tx) error { return s.Complete(ctx, tx, sessionID, callID, res) })
	if err != nil {
		s.log.Warn("finish deferred tool call", "session_id", sessionID, "call_id", callID, "err", err)
	}
}

// start records the call and runs its handler; run=false for a duplicate.
func (s *Service) start(ctx context.Context, tx *core.Tx, m *protocol.ToolCall) (Result, bool, error) {
	dup, err := s.duplicate(ctx, tx, m)
	if err != nil || dup {
		return Result{}, false, err
	}
	err = tx.Gorm(ctx).Table("at_tool_calls").Create(map[string]any{"session_id": m.SessionID, "call_id": m.CallID,
		"tool_use_id": m.ToolUseID, "tool": m.Tool, "args": []byte(m.Args)}).Error
	if err != nil {
		return Result{}, false, err
	}
	c, res := s.prepare(ctx, tx, m)
	if c == nil {
		return res, true, nil
	}
	err = tx.Savepoint(ctx, func(sub *core.Tx) error {
		var herr error
		res, herr = s.handlers[m.Tool](ctx, sub, c)
		return herr
	})
	if err != nil {
		return failure(err), true, s.ignoreUserErrors(err)
	}
	return res, true, nil
}

// duplicate re-sends the stored result of a finished call; running/waiting calls are left alone.
func (s *Service) duplicate(ctx context.Context, tx *core.Tx, m *protocol.ToolCall) (bool, error) {
	var row struct {
		Status string
		Result []byte
	}
	err := core.MapErr(toolCall(ctx, tx, string(m.SessionID), string(m.CallID)).Select("status, result").Take(&row).Error)
	st, result := row.Status, row.Result
	if errors.Is(err, core.ErrNotFound) {
		return false, nil
	}
	if err != nil || (st != "done" && st != "failed") {
		return true, err
	}
	var stored Result
	var wire storedResult
	_ = json.Unmarshal(result, &wire)
	stored = Result{OK: wire.OK, Text: wire.Text, Structured: wire.Structured}
	return true, s.sendResult(ctx, tx, string(m.SessionID), string(m.CallID), stored)
}

// prepare loads the session context and checks availability and arguments; nil call = answer res.
func (s *Service) prepare(ctx context.Context, tx *core.Tx, m *protocol.ToolCall) (*call, Result) {
	sc, err := core.SessionContextOf(ctx, tx, string(m.SessionID))
	if err != nil {
		return nil, failText("会话不存在")
	}
	if reason := s.unavailable(sc.Employee, m.Tool); reason != "" {
		return nil, failText("%s", reason)
	}
	if err := s.validator.ValidateToolInput(m.Tool, m.Args); err != nil {
		return nil, failText("参数不正确：%v", err)
	}
	c := &call{sc: sc, callID: string(m.CallID), tool: m.Tool, args: m.Args, upload: m.Upload}
	if sc.Employee.ThreadID != nil {
		t, err := core.ThreadByID(ctx, tx, *sc.Employee.ThreadID)
		if err != nil {
			return nil, failText("讨论串不存在")
		}
		c.thread = &t
	}
	return c, Result{}
}

func (s *Service) unavailable(e core.Employee, tool string) string {
	meta, ok := protocol.Tools[tool]
	if !ok || s.handlers[tool] == nil {
		return "没有这个平台工具：" + tool
	}
	if !meta.AvailableTo(protocol.Tier(e.Tier)) {
		return fmt.Sprintf("%s 不能使用 %s", e.Tier, tool)
	}
	if meta.Requires == "can_delegate" && !e.Spec.CanDelegate {
		return "这个岗位没有派子任务的权限"
	}
	return ""
}

// userError is a refusal explained to the model (tool failures never become exceptions).
type userError struct{ msg string }

func (e userError) Error() string { return e.msg }

func refuse(format string, a ...any) error { return userError{fmt.Sprintf(format, a...)} }

// knownErrors are engine errors that are explanations for the model, not faults.
var knownErrors = []error{core.ErrNotFound, scheduler.ErrUnknownAssignee, scheduler.ErrUnknownMain, scheduler.ErrUnknownDep,
	scheduler.ErrTaskState, delivery.ErrPRExists, delivery.ErrMergeInProgress, delivery.ErrPlanOpen, delivery.ErrNoRepo,
	delivery.ErrTaskNotReady, delivery.ErrBadURL, delivery.ErrPRKind, orchestrator.ErrNoNode, orchestrator.ErrRoleNotFound, orchestrator.ErrNoEmployee,
	delivery.ErrNothingToPR, delivery.ErrBaseNeedsRepo, delivery.ErrNotPushed, orchestrator.ErrUnknownRepo, core.ErrBaseFixed}

func (s *Service) ignoreUserErrors(err error) error {
	var ue userError
	if errors.As(err, &ue) {
		return nil
	}
	for _, k := range knownErrors {
		if errors.Is(err, k) {
			return nil
		}
	}
	return err
}

func failure(err error) Result {
	if errors.Is(err, core.ErrNotFound) {
		return failText("没有找到")
	}
	return failText("%s", err.Error())
}

// toolCall selects one at_tool_calls row.
func toolCall(ctx context.Context, q core.DB, sessionID, callID string) *gorm.DB {
	return q.Gorm(ctx).Table("at_tool_calls").Where("session_id = ? AND call_id = ?", sessionID, callID)
}

// liveCall selects calls that have not finished.
const liveCall = "status IN ('running','waiting')"

func cancelled() map[string]any {
	return map[string]any{"status": "cancelled", "finished_at": gorm.Expr("now()")}
}

// settle stores a finished result (or marks the call waiting) and answers the daemon.
func (s *Service) settle(ctx context.Context, tx *core.Tx, sessionID, callID string, res Result) error {
	if res.Wait || res.Later != nil {
		meta, _ := json.Marshal(nonNil(res.WaitMeta))
		return toolCall(ctx, tx, sessionID, callID).Updates(map[string]any{"status": "waiting", "result": meta}).Error
	}
	return s.Complete(ctx, tx, sessionID, callID, res)
}

func nonNil(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// storedResult is at_tool_calls.result of a finished call.
type storedResult struct {
	OK         bool   `json:"ok"`
	Text       string `json:"text"`
	Structured any    `json:"structured,omitempty"`
}

// Complete finishes a call (also a waiting one) and sends tool.result.
func (s *Service) Complete(ctx context.Context, tx *core.Tx, sessionID, callID string, res Result) error {
	raw, _ := json.Marshal(storedResult{OK: res.OK, Text: res.Text, Structured: res.Structured})
	st := "done"
	if !res.OK {
		st = "failed"
	}
	res2 := toolCall(ctx, tx, sessionID, callID).Where(liveCall).Updates(map[string]any{"status": st, "result": raw,
		"finished_at": gorm.Expr("now()")})
	if res2.Error != nil || res2.RowsAffected == 0 {
		return res2.Error
	}
	return s.sendResult(ctx, tx, sessionID, callID, res)
}

func (s *Service) sendResult(ctx context.Context, tx *core.Tx, sessionID, callID string, res Result) error {
	sess, err := core.SessionByID(ctx, tx, sessionID)
	if err != nil || sess.NodeID == nil || sess.State == "closed" {
		return ignoreMissing(err)
	}
	text := res.Text
	if text == "" {
		text = "ok"
	}
	body := protocol.ToolResult{SessionID: protocol.SessionID(sessionID), CallID: protocol.CallID(callID), Ok: res.OK,
		Content: []protocol.ContentBlock{{Type: "text", Text: text}}, Download: res.Download}
	if res.Structured != nil {
		raw, _ := json.Marshal(res.Structured)
		msg := json.RawMessage(raw)
		body.Structured = &msg
	}
	_, err = s.gw.Enqueue(ctx, tx, *sess.NodeID, nodegw.Command{Type: protocol.TypeToolResult, Body: body,
		SessionID: sessionID, OpenGen: sess.OpenGen})
	return err
}

func ignoreMissing(err error) error {
	if errors.Is(err, core.ErrNotFound) {
		return nil
	}
	return err
}

// HandleCancel marks a call cancelled (tool.cancel). A blocking question stays open when the
// handler died with the process; otherwise it is cancelled too (protocol.md §6.2).
func (s *Service) HandleCancel(ctx context.Context, m *protocol.ToolCancel) error {
	return s.core.InTx(ctx, func(tx *core.Tx) error {
		res := toolCall(ctx, tx, string(m.SessionID), string(m.CallID)).Where(liveCall).Updates(cancelled())
		if res.Error != nil || res.RowsAffected == 0 {
			return res.Error
		}
		if m.Reason != nil && *m.Reason == "process_restarted" {
			return nil
		}
		return s.cancelQuestionOf(ctx, tx, string(m.SessionID), string(m.CallID))
	})
}

func (s *Service) cancelQuestionOf(ctx context.Context, tx *core.Tx, sessionID, callID string) error {
	q, err := core.QuestionByCall(ctx, tx, sessionID, callID)
	if err != nil || q.Status != "open" {
		return ignoreMissing(err)
	}
	closed, err := core.CloseQuestion(ctx, tx, q, "cancelled", nil, "cancelled:interrupted")
	if err != nil {
		return err
	}
	if err := tx.EmitQuestion(ctx, closed); err != nil {
		return err
	}
	return s.refreshThread(ctx, tx, closed.ThreadID)
}

// Reconcile cancels the session's pending calls the daemon no longer has (sessions.report);
// their blocking questions stay open and are answered by session.send later.
func (s *Service) Reconcile(ctx context.Context, tx *core.Tx, sessionID string, pending []string) error {
	return tx.Gorm(ctx).Table("at_tool_calls").Where("session_id = ? AND NOT (call_id = ANY(?))", sessionID,
		store.TextArray(pending)).Where(liveCall).Updates(cancelled()).Error
}

// refreshThread recomputes running ↔ waiting after a question opened or closed.
func (s *Service) refreshThread(ctx context.Context, tx *core.Tx, threadID *string) error {
	if threadID == nil {
		return nil
	}
	next, err := core.RefreshWaiting(ctx, tx, *threadID)
	if err != nil {
		return err
	}
	if next == "" {
		return tx.EmitThread(ctx, *threadID)
	}
	t, err := core.ThreadByID(ctx, tx, *threadID)
	if err != nil {
		return err
	}
	return s.orch.SetStatus(ctx, tx, t, next, "")
}

func requireThread(c *call) (core.Thread, error) {
	if c.thread == nil {
		return core.Thread{}, refuse("这个工具只能在讨论串里使用")
	}
	return *c.thread, nil
}

func inList(list []string, v string) bool { return slices.Contains(list, v) }
