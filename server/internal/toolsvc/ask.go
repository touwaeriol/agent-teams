package toolsvc

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/core"
)

// Structured questions (ask_user questions[], or Claude's AskUserQuestion routed to ask_user):
// the card keeps them in detail.questions and the user's choices in detail.answers.

// AnswerChoice is the user's answer to one structured question (REST, in question order).
type AnswerChoice struct {
	Selected []string `json:"selected,omitempty" doc:"Labels of the chosen options"`
	Other    string   `json:"other,omitempty" maxLength:"4000" doc:"Free text typed into 其他"`
}

// ErrBadAnswer is a structured answer that does not fit the card.
var ErrBadAnswer = errors.New("回答和问题对不上")

// askSpec is what ask_user stores for one call.
type askSpec struct {
	text     string
	options  []string
	detail   map[string]any
	blocking bool
}

// askSpecOf validates the input and renders the card's text, options and detail.
func askSpecOf(in protocol.AskUserInput, tierBlocks bool) (askSpec, error) {
	via := in.Via != nil
	if len(in.Questions) == 0 {
		if in.Question == nil || strings.TrimSpace(*in.Question) == "" {
			return askSpec{}, refuse("ask_user needs question or questions")
		}
		return askSpec{text: *in.Question, options: in.Options, detail: map[string]any{}, blocking: tierBlocks}, nil
	}
	items := make([]any, 0, len(in.Questions))
	texts := make([]string, 0, len(in.Questions))
	for _, q := range in.Questions {
		if err := checkQuestion(q); err != nil {
			return askSpec{}, err
		}
		items = append(items, questionItem(q))
		texts = append(texts, q.Question)
	}
	spec := askSpec{text: strings.Join(texts, "\n"), detail: map[string]any{"questions": items}, blocking: tierBlocks || via}
	if len(in.Questions) == 1 {
		for _, o := range in.Questions[0].Options {
			spec.options = append(spec.options, o.Label)
		}
	}
	if via {
		spec.detail["via"] = string(*in.Via)
	}
	return spec, nil
}

func checkQuestion(q protocol.AskQuestion) error {
	seen := map[string]bool{}
	recommended := 0
	for _, o := range q.Options {
		if seen[o.Label] {
			return refuse("question %q repeats option %s", q.Question, o.Label)
		}
		seen[o.Label] = true
		if o.Recommended != nil && *o.Recommended {
			recommended++
		}
	}
	if recommended > 1 {
		return refuse("question %q may recommend at most one option", q.Question)
	}
	return nil
}

func questionItem(q protocol.AskQuestion) map[string]any {
	opts := make([]any, 0, len(q.Options))
	for _, o := range q.Options {
		opt := map[string]any{"label": o.Label}
		if o.Description != nil && *o.Description != "" {
			opt["description"] = *o.Description
		}
		if o.Recommended != nil && *o.Recommended {
			opt["recommended"] = true
		}
		opts = append(opts, opt)
	}
	return map[string]any{"question": q.Question, "header": q.Header, "multi_select": q.MultiSelect != nil && *q.MultiSelect,
		"options": opts}
}

// storedQuestion is one entry of detail.questions as read back from the database.
type storedQuestion struct {
	Question    string `json:"question"`
	Header      string `json:"header"`
	MultiSelect bool   `json:"multi_select"`
	Options     []struct {
		Label string `json:"label"`
	} `json:"options"`
}

// structuredQuestions reads detail.questions (nil for a plain question).
func structuredQuestions(q core.Question) []storedQuestion {
	raw, ok := q.Detail["questions"]
	if !ok {
		return nil
	}
	b, _ := json.Marshal(raw)
	var out []storedQuestion
	if json.Unmarshal(b, &out) != nil {
		return nil
	}
	return out
}

// structuredAnswer checks the user's choices against the card and renders the answer text
// and the per-question items. A plain text answer to a structured card counts as 其他 on
// every question.
func structuredAnswer(qs []storedQuestion, choices []AnswerChoice, text string) (string, []protocol.QuestionAnswerItem, error) {
	if len(choices) == 0 {
		if strings.TrimSpace(text) == "" {
			return "", nil, ErrBadAnswer
		}
		choices = make([]AnswerChoice, len(qs))
		for i := range choices {
			choices[i].Other = text
		}
	}
	if len(choices) != len(qs) {
		return "", nil, fmt.Errorf("%w：%d 个问题，%d 个回答", ErrBadAnswer, len(qs), len(choices))
	}
	items := make([]protocol.QuestionAnswerItem, len(qs))
	lines := make([]string, len(qs))
	for i, q := range qs {
		item, err := answerItem(q, choices[i])
		if err != nil {
			return "", nil, err
		}
		items[i] = item
		lines[i] = answerLine(item)
		if len(qs) > 1 {
			lines[i] = q.Header + "：" + lines[i]
		}
	}
	if len(qs) == 1 && strings.TrimSpace(text) != "" && len(choices[0].Selected) == 0 && choices[0].Other == "" {
		lines[0] = text
	}
	return strings.Join(lines, "\n"), items, nil
}

func answerItem(q storedQuestion, c AnswerChoice) (protocol.QuestionAnswerItem, error) {
	other := strings.TrimSpace(c.Other)
	if len(c.Selected) == 0 && other == "" {
		return protocol.QuestionAnswerItem{}, fmt.Errorf("%w：“%s”还没选", ErrBadAnswer, q.Question)
	}
	if !q.MultiSelect && len(c.Selected)+boolInt(other != "") > 1 {
		return protocol.QuestionAnswerItem{}, fmt.Errorf("%w：“%s”只能选一个", ErrBadAnswer, q.Question)
	}
	labels := make([]string, 0, len(q.Options))
	for _, o := range q.Options {
		labels = append(labels, o.Label)
	}
	for _, s := range c.Selected {
		if !slices.Contains(labels, s) {
			return protocol.QuestionAnswerItem{}, fmt.Errorf("%w：“%s”没有选项 %s", ErrBadAnswer, q.Question, s)
		}
	}
	header := q.Header
	item := protocol.QuestionAnswerItem{Question: q.Question, Header: &header, Selected: append([]string{}, c.Selected...)}
	if other != "" {
		item.Other = &other
	}
	return item, nil
}

func answerLine(a protocol.QuestionAnswerItem) string {
	parts := slices.Clone(a.Selected)
	if a.Other != nil {
		parts = append(parts, *a.Other)
	}
	return strings.Join(parts, "、")
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// storedAnswers reads detail.answers back for the tool result.
func storedAnswers(q core.Question) []protocol.QuestionAnswerItem {
	raw, ok := q.Detail["answers"]
	if !ok {
		return nil
	}
	b, _ := json.Marshal(raw)
	var out []protocol.QuestionAnswerItem
	if json.Unmarshal(b, &out) != nil {
		return nil
	}
	return out
}
