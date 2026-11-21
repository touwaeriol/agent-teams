package toolsvc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/core"
)

// The user types in the chat while a question card of the coordinator or lead is still open.
// The agent decides whether that message answers the card: the message is delivered with a note
// naming the open cards, and the agent closes the ones it answers with close_question. A
// blocking call (Claude's AskUserQuestion) cannot read the message while it waits, so it returns
// at once saying a message came; the card stays open, and an answer on the card later arrives as
// "[Answer to qN] …".

// chatReleaseNote is what a waiting call returns when the user writes in the chat instead.
const chatReleaseNote = "用户还没在卡片上作答，但在对话里发来了一条消息（紧接着送到）。卡片 %s 仍然开着：" +
	"如果那条消息已经回答了问题，用 close_question 关掉它；否则继续等，用户在卡片上的回答会以 “[Answer to %s] …” 的消息送达。"

// NoteChatReply runs before a user message reaches emp: it releases emp's waiting question
// calls in conversation c and returns the note the delivered message gets ("" = no open cards).
func (s *Service) NoteChatReply(ctx context.Context, tx *core.Tx, c core.Conversation, emp core.Employee, _, _ string) (string, error) {
	open, err := core.OpenQuestionsFor(ctx, tx, c.ID, emp.Name)
	if err != nil || len(open) == 0 {
		return "", err
	}
	ids := make([]string, 0, len(open))
	for _, q := range open {
		if err := s.releaseCall(ctx, tx, q, fmt.Sprintf(chatReleaseNote, q.ID, q.ID)); err != nil {
			return "", err
		}
		ids = append(ids, q.ID)
	}
	list := strings.Join(ids, "、")
	return fmt.Sprintf("\n\n[你给用户的提问卡片 %s 还开着。如果这条消息已经回答了其中的问题，用 close_question(question_id, answer) 关掉对应卡片，"+
		"不用再等；没回答的就继续开着等用户]", list), nil
}

// releaseCall returns a still waiting call with note (as 其他 on every structured question, so
// Claude's AskUserQuestion gets one answer per question).
func (s *Service) releaseCall(ctx context.Context, tx *core.Tx, q core.Question, note string) error {
	if waiting, err := callWaiting(ctx, tx, q); err != nil || !waiting {
		return err
	}
	var items []protocol.QuestionAnswerItem
	for _, sq := range structuredQuestions(q) {
		header := sq.Header
		items = append(items, protocol.QuestionAnswerItem{Question: sq.Question, Header: &header, Selected: []string{}, Other: &note})
	}
	res := okJSON(note, protocol.AskUserResult{QuestionID: protocol.QuestionID(q.ID), Answer: &note, Answers: items})
	return s.Complete(ctx, tx, *q.SessionID, *q.CallID, res)
}

// closeQuestion is the coordinator or lead closing its own open card because the user answered
// it in the chat: the card closes as answered with detail.answered_in_chat.
func (s *Service) closeQuestion(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.CloseQuestionInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	e := c.emp()
	conv, err := s.orch.ConversationOf(ctx, tx, e)
	if err != nil {
		return Result{}, err
	}
	q, err := core.LockQuestion(ctx, tx, conv, string(in.QuestionID))
	if err != nil || q.Asker != e.Name || q.Audience != "user" || q.Kind != "question" {
		return Result{}, refuse("没有你给用户的提问 %s", in.QuestionID)
	}
	if q.Status != "open" {
		return Result{}, refuse("提问 %s 已经关了", q.ID)
	}
	if err := core.MergeQuestionDetail(ctx, tx, q, map[string]any{"answered_in_chat": true}); err != nil {
		return Result{}, err
	}
	answer := strings.TrimSpace(deref(in.Answer))
	closed, err := core.CloseQuestion(ctx, tx, q, "answered", &answer, "employee:"+e.Name)
	if errors.Is(err, core.ErrConflict) {
		return Result{}, refuse("提问 %s 已经关了", q.ID)
	}
	if err != nil {
		return Result{}, err
	}
	if err := tx.EmitQuestion(ctx, closed); err != nil {
		return Result{}, err
	}
	if err := s.refreshThread(ctx, tx, closed.ThreadID); err != nil {
		return Result{}, err
	}
	return okText("已关闭 " + q.ID), s.releaseCall(ctx, tx, closed, "卡片已关闭："+answer)
}
