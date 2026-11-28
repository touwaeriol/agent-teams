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
const chatReleaseNote = "The user has not answered on the card but sent a message in the chat (delivered right after this). Card %s is still open: " +
	"if that message answers the question, close it with close_question; otherwise keep waiting, and an answer on the card arrives as a \"[Answer to %s] …\" message."

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
	list := strings.Join(ids, ", ")
	return fmt.Sprintf("\n\n[Your question cards to the user %s are still open. If this message answers one of them, close that card with close_question(question_id, answer) "+
		"instead of waiting; leave the unanswered ones open for the user]", list), nil
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
		return Result{}, refuse("you have no question %s to the user", in.QuestionID)
	}
	if q.Status != "open" {
		return Result{}, refuse("question %s is already closed", q.ID)
	}
	if err := core.MergeQuestionDetail(ctx, tx, q, map[string]any{"answered_in_chat": true}); err != nil {
		return Result{}, err
	}
	answer := strings.TrimSpace(deref(in.Answer))
	closed, err := core.CloseQuestion(ctx, tx, q, "answered", &answer, "employee:"+e.Name)
	if errors.Is(err, core.ErrConflict) {
		return Result{}, refuse("question %s is already closed", q.ID)
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
	return okText("closed " + q.ID), s.releaseCall(ctx, tx, closed, "card closed: "+answer)
}
