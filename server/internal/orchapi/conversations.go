package orchapi

import (
	"context"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/toolsvc"
)

type pidInput struct {
	PID string `path:"pid" maxLength:"64"`
}

type cidInput struct {
	CID string `path:"cid" maxLength:"64"`
}

// CoordinatorView is GET /api/projects/{pid}/coordinator.
type CoordinatorView struct {
	ConversationID string             `json:"conversation_id"`
	Employee       core.EmployeeBrief `json:"employee"`
	SessionState   string             `json:"session_state" doc:"'' until the first message opens a session"`
}

type coordinatorOutput struct{ Body CoordinatorView }

// MessagePage is a page of conversation messages (oldest first).
type MessagePage struct {
	Items      []core.MessageView `json:"items"`
	NextCursor *string            `json:"next_cursor" doc:"Pass as ?before= for older messages"`
}

type messagesInput struct {
	CID    string `path:"cid" maxLength:"64"`
	Before string `query:"before" doc:"Message id cursor"`
	Limit  int    `query:"limit" minimum:"0" maximum:"1000" doc:"0 = all (v1 behaviour)"`
}

type messagesOutput struct{ Body MessagePage }

type postMessageInput struct {
	CID            string `path:"cid" maxLength:"64"`
	IdempotencyKey string `header:"Idempotency-Key" maxLength:"128"`
	Body           struct {
		Content     string   `json:"content" maxLength:"100000" doc:"Required unless attachments are given"`
		Interrupt   bool     `json:"interrupt,omitempty" doc:"Interrupt the current turn first"`
		Attachments []string `json:"attachments,omitempty" maxItems:"8" doc:"Image ids from POST /api/projects/{pid}/attachments, in order"`
	}
}

type messageOutput struct{ Body core.MessageView }

type questionsInput struct {
	CID    string `path:"cid" maxLength:"64"`
	Status string `query:"status" enum:"open,all,answered,cancelled," default:"all"`
}

type questionsOutput struct{ Body []core.QuestionView }

type answerInput struct {
	CID            string `path:"cid" maxLength:"64"`
	QID            string `path:"qid" maxLength:"16"`
	IdempotencyKey string `header:"Idempotency-Key" maxLength:"128"`
	Body           struct {
		Answer  string                 `json:"answer,omitempty" maxLength:"20000" doc:"Approvals: allow | allow_always | deny; questions: the text answer"`
		Answers []toolsvc.AnswerChoice `json:"answers,omitempty" maxItems:"4" doc:"Structured questions (detail.questions): one choice per question, in order"`
		Reason  string                 `json:"reason,omitempty" maxLength:"2000" doc:"Why an approval was denied (sent to the employee)"`
	}
}

type questionOutput struct{ Body core.QuestionView }

func (a *API) registerConversations(api huma.API) {
	huma.Register(api, op("get-coordinator", "GET", "/api/projects/{pid}/coordinator", "conversations",
		summary("The project coordinator and its conversation (created on first use, no session yet)")), a.getCoordinator)
	huma.Register(api, op("list-messages", "GET", "/api/conversations/{cid}/messages", "conversations"), a.listMessages)
	huma.Register(api, op("post-message", "POST", "/api/conversations/{cid}/messages", "conversations", status(202),
		summary("Send a message to the conversation's employee (coordinator or thread lead)")), a.postMessage)
	huma.Register(api, op("interrupt-conversation", "POST", "/api/conversations/{cid}/interrupt", "conversations", status(202)), a.interrupt)
	huma.Register(api, op("list-questions", "GET", "/api/conversations/{cid}/questions", "conversations"), a.listQuestions)
	huma.Register(api, op("answer-question", "POST", "/api/conversations/{cid}/questions/{qid}/answer", "conversations",
		summary("Answer a question, or decide an approval card (allow / allow_always / deny)")), a.answer)
}

func (a *API) getCoordinator(ctx context.Context, in *pidInput) (*coordinatorOutput, error) {
	u, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	emp, conv, err := a.e.Orch.EnsureCoordinator(ctx, u.ID, p.ID)
	if err != nil {
		return nil, err
	}
	brief, err := a.e.Core.EmployeeBriefOf(ctx, a.e.Core.Pool, emp.ID)
	if err != nil {
		return nil, err
	}
	out := &coordinatorOutput{Body: CoordinatorView{ConversationID: conv, Employee: brief}}
	if s, err := core.LiveSession(ctx, a.e.Core.Pool, emp.ID); err == nil {
		out.Body.SessionState = s.State
	}
	return out, nil
}

func (a *API) listMessages(ctx context.Context, in *messagesInput) (*messagesOutput, error) {
	_, c, err := a.conversation(ctx, in.CID)
	if err != nil {
		return nil, err
	}
	limit := in.Limit
	if limit == 0 {
		limit = 100000
	}
	msgs, err := core.Messages(ctx, a.e.Core.Pool, c.ID, parseCursor(in.Before), limit)
	if err != nil {
		return nil, err
	}
	page := MessagePage{Items: make([]core.MessageView, 0, len(msgs))}
	for _, m := range msgs {
		page.Items = append(page.Items, core.MessageViewOf(m))
	}
	if len(msgs) > 0 {
		page.NextCursor = cursorOf(msgs[0].ID, len(msgs) == limit)
	}
	return &messagesOutput{Body: page}, nil
}

func (a *API) postMessage(ctx context.Context, in *postMessageInput) (*messageOutput, error) {
	u, c, err := a.conversation(ctx, in.CID)
	if err != nil {
		return nil, err
	}
	if err := a.limitMessages(ctx, u.ID); err != nil {
		return nil, err
	}
	content := strings.TrimSpace(in.Body.Content)
	if content == "" && len(in.Body.Attachments) == 0 {
		return nil, httpapi.Invalid("content", "消息不能为空")
	}
	v, err := idempotent(ctx, a, u.ID, "message:"+c.ID, in.IdempotencyKey, func() (core.MessageView, error) {
		m, err := a.e.Orch.PostUserMessageWith(ctx, orchestrator.UserMessage{UserID: u.ID, ConvID: c.ID, Content: content,
			Interrupt: in.Body.Interrupt, Images: in.Body.Attachments})
		return core.MessageViewOf(m), err
	})
	if err != nil {
		return nil, fail(err, "没有这个对话")
	}
	return &messageOutput{Body: v}, nil
}

func (a *API) interrupt(ctx context.Context, in *cidInput) (*struct{}, error) {
	u, c, err := a.conversation(ctx, in.CID)
	if err != nil {
		return nil, err
	}
	return nil, fail(a.e.Orch.InterruptConversation(ctx, u.ID, c.ID), "没有这个对话")
}

func (a *API) listQuestions(ctx context.Context, in *questionsInput) (*questionsOutput, error) {
	_, c, err := a.conversation(ctx, in.CID)
	if err != nil {
		return nil, err
	}
	qs, err := core.ConversationQuestions(ctx, a.e.Core.Pool, c.ID, in.Status)
	if err != nil {
		return nil, err
	}
	views, err := a.questionViews(ctx, qs)
	return &questionsOutput{Body: views}, err
}

func (a *API) questionViews(ctx context.Context, qs []core.Question) ([]core.QuestionView, error) {
	out := make([]core.QuestionView, 0, len(qs))
	for _, q := range qs {
		no, err := core.ThreadNoOf(ctx, a.e.Core.Pool, q.ThreadID)
		if err != nil {
			return nil, err
		}
		out = append(out, core.QuestionViewOf(q, no))
	}
	return out, nil
}

func (a *API) answer(ctx context.Context, in *answerInput) (*questionOutput, error) {
	u, c, err := a.conversation(ctx, in.CID)
	if err != nil {
		return nil, err
	}
	v, err := idempotent(ctx, a, u.ID, "answer:"+c.ID+":"+in.QID, in.IdempotencyKey, func() (core.QuestionView, error) {
		return a.answerCard(ctx, u, c.ID, in)
	})
	if err != nil {
		return nil, fail(err, "没有这个问题")
	}
	return &questionOutput{Body: v}, nil
}

func (a *API) answerCard(ctx context.Context, u store.User, convID string, in *answerInput) (core.QuestionView, error) {
	q, err := core.QuestionByID(ctx, a.e.Core.Pool, convID, in.QID)
	if err != nil {
		return core.QuestionView{}, err
	}
	if q.Kind == "approval" {
		q, err = a.e.Approvals.Answer(ctx, u, convID, in.QID, strings.TrimSpace(in.Body.Answer), in.Body.Reason)
	} else {
		q, err = a.e.Tools.AnswerQuestion(ctx, u, convID, in.QID, strings.TrimSpace(in.Body.Answer), in.Body.Answers)
	}
	if err != nil {
		return core.QuestionView{}, err
	}
	no, err := core.ThreadNoOf(ctx, a.e.Core.Pool, q.ThreadID)
	return core.QuestionViewOf(q, no), err
}
