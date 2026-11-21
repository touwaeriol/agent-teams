package toolsvc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/approvals"
	"github.com/touwaeriol/agent-teams/server/internal/composer"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
	"github.com/touwaeriol/agent-teams/server/internal/status"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Errors for the REST answer endpoint.
var (
	ErrNotQuestion = errors.New("这是审批卡片，请用 allow / deny / allow_always 回答")
	ErrAnswered    = errors.New("已经回答过了")
	ErrNotForUser  = errors.New("这个问题是问组长的")
)

// ask creates a question card. Blocking questions keep the call waiting until answered.
func (s *Service) ask(ctx context.Context, tx *core.Tx, c *call, audience string, spec askSpec) (core.Question, error) {
	text, options, blocking := spec.text, spec.options, spec.blocking
	e := c.emp()
	conv, err := s.orch.ConversationOf(ctx, tx, e)
	if err != nil {
		return core.Question{}, err
	}
	sid := c.sc.Session.ID
	n := core.NewQuestion{ConversationID: conv, ProjectID: e.ProjectID, ThreadID: e.WorkThreadID(), Asker: e.Name, Audience: audience,
		Kind: "question", Text: text, Options: options, Detail: spec.detail, SessionID: &sid, Blocking: blocking}
	if blocking {
		n.CallID = &c.callID
	}
	if c.thread != nil {
		if t, err := core.CurrentTask(ctx, tx, c.thread.ID, e.Name); err == nil {
			n.TaskID = &t.ID
		}
	}
	q, err := core.InsertQuestion(ctx, tx, n)
	if err != nil {
		return q, err
	}
	if err := tx.EmitQuestion(ctx, q); err != nil {
		return q, err
	}
	s.orch.Touch(tx, e.ID, "question.open:"+q.ID)
	return q, nil
}

// askUser: non-blocking for coordinator and lead (answer comes back as a message), blocking for members.
func (s *Service) askUser(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.AskUserInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	spec, err := askSpecOf(in, protocol.Tools[protocol.ToolAskUser].BlocksFor(protocol.Tier(c.emp().Tier)))
	if err != nil {
		return Result{}, err
	}
	blocking := spec.blocking
	q, err := s.ask(ctx, tx, c, "user", spec)
	if err != nil {
		return Result{}, err
	}
	if err := s.refreshThread(ctx, tx, q.ThreadID); err != nil {
		return Result{}, err
	}
	if err := s.notice(ctx, tx, c, q); err != nil {
		return Result{}, err
	}
	if blocking {
		return Result{Wait: true}, nil
	}
	return okJSON(fmt.Sprintf("已提问 %s，用户回答后会以 “[Answer to %s] …” 的消息发给你。", q.ID, q.ID),
		protocol.AskUserResult{QuestionID: protocol.QuestionID(q.ID)}), nil
}

// notice pushes a user notification when the project wants one (notify_on_ask).
func (s *Service) notice(ctx context.Context, tx *core.Tx, c *call, q core.Question) error {
	d, err := core.DeliveryOf(ctx, tx, c.emp().ProjectID)
	if err != nil || !d.NotifyOnAsk {
		return err
	}
	_, err = tx.EmitUser(ctx, c.sc.OwnerID, "notice", map[string]any{"kind": "question", "project_id": c.emp().ProjectID,
		"question": core.QuestionViewOf(q, nil), "text": q.Asker + "：" + q.Text})
	return err
}

// askLead blocks until the lead answers with reply_to.
func (s *Service) askLead(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	t, err := requireThread(c)
	if err != nil {
		return Result{}, err
	}
	var in protocol.AskLeadInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	q, err := s.ask(ctx, tx, c, "lead", askSpec{text: in.Question, detail: map[string]any{}, blocking: true})
	if err != nil {
		return Result{}, err
	}
	task := ""
	if q.TaskID != nil {
		task = " · " + *q.TaskID
	}
	text := fmt.Sprintf("[Question %s from %s%s]\n%s\n(answer with reply_to)", q.ID, c.emp().Name, task, in.Question)
	return Result{Wait: true}, s.toLead(ctx, tx, t, text, "next")
}

func (s *Service) toLead(ctx context.Context, tx *core.Tx, t core.Thread, text, priority string) error {
	lead, err := core.Lead(ctx, tx, t.ID)
	if err != nil {
		return err
	}
	_, err = s.orch.Deliver(ctx, tx, lead, orchestrator.Delivery{Text: text, Kind: "mail", Priority: priority})
	return err
}

// replyTo is the lead answering a member's question.
func (s *Service) replyTo(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	t, err := requireThread(c)
	if err != nil {
		return Result{}, err
	}
	var in protocol.ReplyToInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	q, err := core.LockQuestion(ctx, tx, t.ID, string(in.QuestionID))
	if err != nil {
		return Result{}, refuse("没有问题 %s", in.QuestionID)
	}
	if q.Kind == "approval" {
		return Result{}, refuse("%s 是权限请求，用 review_permission 判断", q.ID)
	}
	if q.Status != "open" || q.Audience != "lead" {
		return Result{}, refuse("问题 %s 不是待你回答的问题", q.ID)
	}
	return okText("已回答 " + q.ID), s.answer(ctx, tx, q, in.Answer, "employee:"+c.emp().Name)
}

// reviewPermission is the lead deciding a member's permission request (approvals.LeadReview).
func (s *Service) reviewPermission(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	t, err := requireThread(c)
	if err != nil {
		return Result{}, err
	}
	var in protocol.ReviewPermissionInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	text, err := s.Reviews.LeadReview(ctx, tx, c.emp(), t.ID, string(in.RequestID), string(in.Decision), in.Reason)
	if errors.Is(err, approvals.ErrNotReview) || errors.Is(err, approvals.ErrReviewTaken) || errors.Is(err, approvals.ErrBadDecision) {
		return Result{}, refuse("%s：%s", in.RequestID, err.Error())
	}
	return okText(text), err
}

// AnswerQuestion is the user answering a question card (REST): a text answer, or one choice
// per structured question (detail.questions).
func (s *Service) AnswerQuestion(ctx context.Context, user store.User, convID, qid, answer string, choices []AnswerChoice) (core.Question, error) {
	var out core.Question
	err := s.core.InTx(ctx, func(tx *core.Tx) error {
		q, err := core.LockQuestion(ctx, tx, convID, qid)
		if err != nil {
			return err
		}
		switch {
		case q.Kind == "approval":
			return ErrNotQuestion
		case q.Status != "open":
			return ErrAnswered
		case q.Audience != "user":
			return ErrNotForUser
		}
		text, err := s.recordChoices(ctx, tx, q, answer, choices)
		if err != nil {
			return err
		}
		if err := s.answer(ctx, tx, q, text, "user:"+user.ID); err != nil {
			return err
		}
		out, err = core.QuestionByID(ctx, tx, convID, qid)
		return err
	})
	return out, err
}

// recordChoices stores a structured card's answers in detail.answers and returns the answer text.
func (s *Service) recordChoices(ctx context.Context, tx *core.Tx, q core.Question, answer string, choices []AnswerChoice) (string, error) {
	qs := structuredQuestions(q)
	if qs == nil {
		if strings.TrimSpace(answer) == "" {
			return "", ErrBadAnswer
		}
		return answer, nil
	}
	text, items, err := structuredAnswer(qs, choices, answer)
	if err != nil {
		return "", err
	}
	return text, core.MergeQuestionDetail(ctx, tx, q, map[string]any{"answers": items})
}

// answer closes the question and gets the answer to the asker: the waiting call's result
// when it is still pending, else a session.send "[Answer to qN] …" (non-blocking fallback).
func (s *Service) answer(ctx context.Context, tx *core.Tx, q core.Question, answer, by string) error {
	closed, err := core.CloseQuestion(ctx, tx, q, "answered", &answer, by)
	if err != nil {
		return err
	}
	if err := tx.EmitQuestion(ctx, closed); err != nil {
		return err
	}
	if err := s.refreshThread(ctx, tx, closed.ThreadID); err != nil {
		return err
	}
	asker, err := core.EmployeeByName(ctx, tx, deref(closed.ProjectID), closed.ThreadID, closed.Asker)
	if err != nil {
		return ignoreMissing(err)
	}
	s.orch.Touch(tx, asker.ID, "question.answered:"+closed.ID)
	if done, err := s.answerCall(ctx, tx, closed, answer); err != nil || done {
		return err
	}
	text := fmt.Sprintf("[Answer to %s] %s", closed.ID, answer)
	_, err = s.orch.Deliver(ctx, tx, asker, orchestrator.Delivery{Text: text, Kind: "answer", Priority: "next",
		Record: answerRecord(closed, asker, text)})
	return err
}

// answerRecord keeps a project member's answer in the question's thread, wherever the member
// works now (nil: the asker's own conversation).
func answerRecord(q core.Question, asker core.Employee, text string) *core.NewMessage {
	if !asker.ProjectMember() || q.ThreadID == nil {
		return nil
	}
	return &core.NewMessage{ConversationID: *q.ThreadID, Role: "system", Author: "platform", Kind: "note", Content: text,
		Meta: map[string]any{"to": asker.Name}}
}

func (s *Service) answerCall(ctx context.Context, tx *core.Tx, q core.Question, answer string) (bool, error) {
	if waiting, err := callWaiting(ctx, tx, q); err != nil || !waiting {
		return false, err
	}
	res := okJSON(answer, protocol.AskUserResult{QuestionID: protocol.QuestionID(q.ID), Answer: &answer, Answers: storedAnswers(q)})
	return true, s.Complete(ctx, tx, *q.SessionID, *q.CallID, res)
}

// callWaiting tells whether the blocking call that asked q still waits for its answer.
func callWaiting(ctx context.Context, tx *core.Tx, q core.Question) (bool, error) {
	if q.CallID == nil || q.SessionID == nil {
		return false, nil
	}
	var call struct{ Status string }
	err := core.MapErr(toolCall(ctx, tx, *q.SessionID, *q.CallID).Select("status").Take(&call).Error)
	if err != nil || call.Status != "waiting" {
		return false, ignoreMissing(err)
	}
	return true, nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// sendMessage leaves mail for a teammate ("lead" or a member) and pushes it to a live session.
func (s *Service) sendMessage(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	t, err := requireThread(c)
	if err != nil {
		return Result{}, err
	}
	var in protocol.SendMessageInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	to, err := s.recipient(ctx, tx, t, c.emp(), in.To)
	if err != nil {
		return Result{}, err
	}
	if err := s.mail(ctx, tx, t, c.emp().Name, to.Name, in.Text); err != nil {
		return Result{}, err
	}
	text := in.Text
	if deliver, _, err := composer.ExpandFor(ctx, tx, to, text); err != nil {
		return Result{}, err
	} else if deliver != "" {
		text = deliver // the recipient's prompt command, expanded
	}
	head, err := mailHeader(ctx, tx, t, to, c.emp().Name, "Message", "留言")
	if err != nil {
		return Result{}, err
	}
	return okText("sent"), s.pushMail(ctx, tx, to, head+"\n"+text)
}

// mailHeader opens mail pushed to a teammate. A project member working in another thread (or in
// none) gets it where it is, without switching threads, told which thread it comes from: the mail
// stays in the sender's thread (shared-members.md §6).
func mailHeader(ctx context.Context, tx *core.Tx, t core.Thread, to core.Employee, from, kind, kindZh string) (string, error) {
	if !to.ProjectMember() {
		return fmt.Sprintf("[%s from %s]", kind, from), nil
	}
	th, err := core.WorkThreadOf(ctx, tx, to)
	if err != nil {
		return "", err
	}
	if th != nil && *th == t.ID {
		return fmt.Sprintf("[%s from %s]", kind, from), nil
	}
	return fmt.Sprintf("[#%d · 来自 %s 的%s]", t.No, from, kindZh), nil
}

func (s *Service) recipient(ctx context.Context, tx *core.Tx, t core.Thread, from core.Employee, to string) (core.Employee, error) {
	if to == "lead" {
		return core.Lead(ctx, tx, t.ID)
	}
	if to == from.Name {
		return core.Employee{}, refuse("不能给自己留言")
	}
	e, err := core.EmployeeByName(ctx, tx, t.ProjectID, &t.ID, to)
	if err != nil {
		return e, refuse("本讨论串没有 %s", to)
	}
	return e, nil
}

func (s *Service) mail(ctx context.Context, tx *core.Tx, t core.Thread, from, to, body string) error {
	m, err := core.InsertMail(ctx, tx, t.ID, from, to, body)
	if err != nil {
		return err
	}
	return tx.EmitMail(ctx, t, m)
}

// pushMail sends mail to a recipient that has had a session (the lead always gets it): a
// parked or closed session wakes up for it. Members that never started read it in their
// first task brief or with read_messages.
func (s *Service) pushMail(ctx context.Context, tx *core.Tx, to core.Employee, text string) error {
	if to.Tier != "lead" {
		if _, err := core.LatestSession(ctx, tx, to.ID); err != nil {
			return ignoreMissing(err)
		}
	}
	_, err := s.orch.Deliver(ctx, tx, to, orchestrator.Delivery{Text: text, Kind: "mail", Priority: "later"})
	return err
}

// broadcast is the lead's message to every member.
func (s *Service) broadcast(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	t, err := requireThread(c)
	if err != nil {
		return Result{}, err
	}
	var in protocol.BroadcastInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	if err := s.mail(ctx, tx, t, c.emp().Name, "*", in.Text); err != nil {
		return Result{}, err
	}
	emps, err := core.ThreadParticipants(ctx, tx, t.ID)
	if err != nil {
		return Result{}, err
	}
	for _, e := range emps {
		if e.Tier != "member" {
			continue
		}
		head, err := mailHeader(ctx, tx, t, e, c.emp().Name, "Broadcast", "广播")
		if err != nil {
			return Result{}, err
		}
		if err := s.pushMail(ctx, tx, e, head+"\n"+in.Text); err != nil {
			return Result{}, err
		}
	}
	return okText("sent"), nil
}

// readMessages returns the unread mail (direct and broadcast) of the thread the caller works in
// and marks it read; a project member is also told about unread mail addressed to it in the
// project's other threads (read there, when it works there).
func (s *Service) readMessages(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	t, err := requireThread(c)
	if err != nil {
		return Result{}, err
	}
	mail, err := core.UnreadMail(ctx, tx, t.ID, c.emp().Name)
	if err != nil {
		return Result{}, err
	}
	out := protocol.ReadMessagesResult{Messages: []protocol.Mail{}}
	var lines []string
	for _, m := range mail {
		out.Messages = append(out.Messages, protocol.Mail{ID: int(m.ID), Sender: m.Sender, Recipient: m.Recipient, Body: m.Body,
			CreatedAt: m.CreatedAt})
		lines = append(lines, fmt.Sprintf("%s → %s：%s", m.Sender, m.Recipient, m.Body))
	}
	if len(lines) == 0 {
		lines = append(lines, "没有未读留言")
	}
	if c.emp().ProjectMember() {
		other, err := core.UnreadElsewhere(ctx, tx, t.ProjectID, t.ID, c.emp().Name)
		if err != nil {
			return Result{}, err
		}
		if note := elsewhereNote(other); note != "" {
			lines = append(lines, note)
		}
	}
	return okJSON(strings.Join(lines, "\n"), out), nil
}

// elsewhereNote mentions unread mail in other threads ("" when none).
func elsewhereNote(other []core.ThreadCount) string {
	if len(other) == 0 {
		return ""
	}
	parts := make([]string, len(other))
	for i, o := range other {
		parts[i] = fmt.Sprintf("#%d %d 条", o.No, o.N)
	}
	return "其他讨论串还有发给你的未读留言：" + strings.Join(parts, "、") +
		"（推送到你会话时已标明讨论串；到那个讨论串干活时用 read_messages 查看）"
}

// setStatus updates the bubble only (§11.3).
func (s *Service) setStatus(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.SetStatusInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	kind := "plain"
	if in.Kind != nil {
		kind = string(*in.Kind)
	}
	id := c.emp().ID
	tx.AfterCommit(func(ctx context.Context) {
		s.status.Observe(ctx, id, status.Observation{Kind: status.SetStatus, Cause: "set_status", Text: in.Text, BubbleKd: kind})
	})
	return okText("ok"), nil
}

// reportProgress records an activity line and, for a member, the progress of its current task
// (the task anchor carries it, shared-members.md §5.2).
func (s *Service) reportProgress(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.ReportProgressInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	if err := core.SetTaskProgress(ctx, tx, c.emp(), in.Text); err != nil {
		return Result{}, err
	}
	return okText("ok"), tx.EmitActivity(ctx, c.emp().ProjectID, c.emp().WorkThreadID(), c.emp().Name, in.Text)
}

// publish registers a deliverable link.
func (s *Service) publish(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.PublishDeliverableInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	d, err := s.deliv.Publish(ctx, tx, c.emp().ProjectID, c.emp().WorkThreadID(), in.Kind, in.Title, in.URL, c.emp().Name,
		map[string]any{"by": c.emp().Name})
	if err != nil {
		return Result{}, err
	}
	if err := s.orch.RecordArtifact(ctx, tx, c.emp(), d, 0); err != nil {
		return Result{}, err
	}
	return okJSON("已登记交付物 "+d.ID, protocol.PublishDeliverableResult{DeliverableID: d.ID}), nil
}
