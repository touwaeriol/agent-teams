package toolsvc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/core"
)

// A question card the user answers by typing in the chat instead (like Claude Code, where a
// typed message dismisses the open AskUserQuestion): the card closes as answered with
// detail.answered_in_chat, a waiting call (blocking ask_user, Claude's AskUserQuestion) returns
// at once telling the asker the reply is the next message, and the message itself is delivered
// as usual with a note naming the cards it closed.

// chatAnswerNote is the waiting call's answer: the user's words arrive as the next message.
const chatAnswerNote = "用户没有在卡片上作答，而是直接发来了一条消息（紧接着送到），请按那条消息理解。"

// maxChatAnswer is how much of the typed message the closed card keeps as its answer.
const maxChatAnswer = 2000

// AnswerByChat closes the cards emp left open for the user in conversation c and returns the
// note the delivered message gets ("" when there were none).
func (s *Service) AnswerByChat(ctx context.Context, tx *core.Tx, c core.Conversation, emp core.Employee, userID, content string) (string, error) {
	open, err := core.OpenQuestionsFor(ctx, tx, c.ID, emp.Name)
	if err != nil {
		return "", err
	}
	var ids []string
	for _, q := range open {
		closed, err := s.closeByChat(ctx, tx, q, userID, content)
		if err != nil {
			return "", err
		}
		if closed {
			ids = append(ids, q.ID)
		}
	}
	if len(ids) == 0 {
		return "", nil
	}
	return fmt.Sprintf("\n\n[这条消息回复了你的提问 %s，提问卡片已关闭]", strings.Join(ids, "、")), nil
}

// closeByChat closes one card; false when someone else closed it first.
func (s *Service) closeByChat(ctx context.Context, tx *core.Tx, q core.Question, userID, content string) (bool, error) {
	if err := core.MergeQuestionDetail(ctx, tx, q, map[string]any{"answered_in_chat": true}); err != nil {
		return false, err
	}
	answer := clipAnswer(strings.TrimSpace(content))
	closed, err := core.CloseQuestion(ctx, tx, q, "answered", &answer, "user:"+userID)
	if errors.Is(err, core.ErrConflict) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := tx.EmitQuestion(ctx, closed); err != nil {
		return false, err
	}
	if err := s.refreshThread(ctx, tx, closed.ThreadID); err != nil {
		return false, err
	}
	asker, err := core.EmployeeByName(ctx, tx, deref(closed.ProjectID), closed.ThreadID, closed.Asker)
	if err != nil {
		return true, ignoreMissing(err)
	}
	s.orch.Touch(tx, asker.ID, "question.answered:"+closed.ID)
	return true, s.releaseCall(ctx, tx, closed)
}

// releaseCall returns a still waiting call with chatAnswerNote (as 其他 on every structured
// question, so Claude's AskUserQuestion gets one answer per question).
func (s *Service) releaseCall(ctx context.Context, tx *core.Tx, q core.Question) error {
	if waiting, err := callWaiting(ctx, tx, q); err != nil || !waiting {
		return err
	}
	note := chatAnswerNote
	var items []protocol.QuestionAnswerItem
	for _, sq := range structuredQuestions(q) {
		header := sq.Header
		items = append(items, protocol.QuestionAnswerItem{Question: sq.Question, Header: &header, Selected: []string{}, Other: &note})
	}
	res := okJSON(note, protocol.AskUserResult{QuestionID: protocol.QuestionID(q.ID), Answer: &note, Answers: items})
	return s.Complete(ctx, tx, *q.SessionID, *q.CallID, res)
}

func clipAnswer(s string) string {
	if len(s) <= maxChatAnswer {
		return s
	}
	n := maxChatAnswer
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
