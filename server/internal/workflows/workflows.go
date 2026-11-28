// Package workflows keeps a user's flow templates (流程图, docs/v2/workflows.md): steps, each
// done by a role, and the edges between them. A thread lead gets its project's flow as text in
// its system prompt (Render) and splits tasks by it; the platform neither expands nor blocks
// tasks.
package workflows

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits of one flow.
const (
	MaxSteps        = 30
	MaxEdges        = 60
	MaxPerUser      = 20
	MaxNameRunes    = 40
	MaxDescRunes    = 2000
	MaxInstrRunes   = 2000
	MaxLabelRunes   = 20
	MaxCoord        = 100000
	ProjectNone     = "none" // at_projects.workflow: no flow for this project
	ProjectDefault  = ""     // at_projects.workflow: the user's default flow
	leadRole        = "lead"
	approvalKind    = "approval"
	nextEdge        = "next"
	rejectEdge      = "reject"
	defaultStepKind = "work"
)

// FlowStep kinds and edge kinds.
var (
	StepKinds = []string{"work", "review", "decision", approvalKind}
	EdgeKinds = []string{nextEdge, rejectEdge}
	stepID    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
)

// ErrNotFound is returned for a flow that does not exist or is not the user's.
var ErrNotFound = errors.New("workflow not found")

// FlowStep is one box of the flow.
type FlowStep struct {
	ID           string  `json:"id" maxLength:"32" doc:"Unique in the flow: letters, digits, _ and -"`
	Name         string  `json:"name" maxLength:"40"`
	Role         string  `json:"role" maxLength:"24" doc:"A member role (its role key, e.g. dev) or lead; '' only for an approval step (the user confirms)"`
	Kind         string  `json:"kind" required:"false" enum:"work,review,decision,approval" doc:"work = does the step; review / decision = checks the previous step's result; approval = waits for the user's confirmation"`
	Instructions string  `json:"instructions" required:"false" maxLength:"2000" doc:"What the step does and how it is accepted"`
	X            float64 `json:"x" required:"false" doc:"Position on the canvas"`
	Y            float64 `json:"y" required:"false"`
}

// FlowEdge is one arrow of the flow.
type FlowEdge struct {
	From  string `json:"from" maxLength:"32"`
	To    string `json:"to" maxLength:"32"`
	Kind  string `json:"kind" required:"false" enum:"next,reject" doc:"next = then; reject = back to this step when the step rejects"`
	Label string `json:"label,omitempty" maxLength:"20"`
}

// Flow is a row of at_workflows.
type Flow struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Steps       []FlowStep `json:"steps"`
	Edges       []FlowEdge `json:"edges"`
	IsDefault   bool       `json:"is_default"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// FlowDraft is what a create or update stores.
type FlowDraft struct {
	Name        string     `json:"name" maxLength:"40"`
	Description string     `json:"description,omitempty" maxLength:"2000"`
	Steps       []FlowStep `json:"steps" maxItems:"30"`
	Edges       []FlowEdge `json:"edges" required:"false" maxItems:"60"`
	IsDefault   bool       `json:"is_default,omitempty" doc:"The flow of the projects that pick none"`
}

// Problem is a validation failure shown to the user.
type Problem struct{ msg string }

func (p *Problem) Error() string { return p.msg }

func problem(format string, args ...any) error { return &Problem{fmt.Sprintf(format, args...)} }

// IsProblem reports whether err is a validation failure.
func IsProblem(err error) bool {
	var p *Problem
	return errors.As(err, &p)
}

// Normalize trims the draft's text and fills the default kinds.
func Normalize(d *FlowDraft) {
	d.Name = strings.TrimSpace(d.Name)
	d.Description = strings.TrimSpace(d.Description)
	for i := range d.Steps {
		s := &d.Steps[i]
		s.ID, s.Name, s.Role = strings.TrimSpace(s.ID), strings.TrimSpace(s.Name), strings.TrimSpace(s.Role)
		s.Instructions = strings.TrimSpace(s.Instructions)
		if s.Kind == "" {
			s.Kind = defaultStepKind
		}
	}
	for i := range d.Edges {
		e := &d.Edges[i]
		e.From, e.To, e.Label = strings.TrimSpace(e.From), strings.TrimSpace(e.To), strings.TrimSpace(e.Label)
		if e.Kind == "" {
			e.Kind = nextEdge
		}
	}
	if d.Steps == nil {
		d.Steps = []FlowStep{}
	}
	if d.Edges == nil {
		d.Edges = []FlowEdge{}
	}
}

// Validate checks a normalized draft; roles is the set of the user's member role keys.
func Validate(d FlowDraft, roles map[string]bool) error {
	if err := checkHeader(d); err != nil {
		return err
	}
	ids := map[string]bool{}
	for i, s := range d.Steps {
		if err := checkStep(i, s, roles, ids); err != nil {
			return err
		}
		ids[s.ID] = true
	}
	seen := map[FlowEdge]bool{}
	for i, e := range d.Edges {
		if err := checkEdge(i, e, ids, seen); err != nil {
			return err
		}
		seen[FlowEdge{From: e.From, To: e.To, Kind: e.Kind}] = true
	}
	return nil
}

func checkHeader(d FlowDraft) error {
	switch {
	case d.Name == "":
		return problem("请填写流程名称")
	case utf8.RuneCountInString(d.Name) > MaxNameRunes:
		return problem("流程名称最多 %d 个字", MaxNameRunes)
	case utf8.RuneCountInString(d.Description) > MaxDescRunes:
		return problem("流程说明最多 %d 个字", MaxDescRunes)
	case len(d.Steps) == 0:
		return problem("流程至少要有一个步骤")
	case len(d.Steps) > MaxSteps:
		return problem("一个流程最多 %d 个步骤", MaxSteps)
	case len(d.Edges) > MaxEdges:
		return problem("一个流程最多 %d 条连线", MaxEdges)
	}
	return nil
}

func checkStep(i int, s FlowStep, roles map[string]bool, ids map[string]bool) error {
	label := stepLabel(i, s)
	switch {
	case !stepID.MatchString(s.ID):
		return problem("%s 的 id 只能是 1–32 个字母、数字、_ 或 -", label)
	case ids[s.ID]:
		return problem("步骤 id 重复：%s", s.ID)
	case s.Name == "":
		return problem("第 %d 个步骤没有名称", i+1)
	case utf8.RuneCountInString(s.Name) > MaxNameRunes:
		return problem("%s 的名称最多 %d 个字", label, MaxNameRunes)
	case utf8.RuneCountInString(s.Instructions) > MaxInstrRunes:
		return problem("%s 的说明最多 %d 个字", label, MaxInstrRunes)
	case !slices.Contains(StepKinds, s.Kind):
		return problem("%s 的类型只能是做事、审查、决策或等用户确认", label)
	case !inCanvas(s.X) || !inCanvas(s.Y):
		return problem("%s 的位置超出画布", label)
	}
	return checkStepRole(label, s, roles)
}

func checkStepRole(label string, s FlowStep, roles map[string]bool) error {
	switch {
	case s.Role == "" && s.Kind == approvalKind:
		return nil
	case s.Role == "":
		return problem("%s 需要选一个岗位", label)
	case s.Role == leadRole || roles[s.Role]:
		return nil
	}
	return problem("%s 的岗位 %s 不在你的团队里", label, s.Role)
}

func checkEdge(i int, e FlowEdge, ids map[string]bool, seen map[FlowEdge]bool) error {
	switch {
	case !ids[e.From] || !ids[e.To]:
		return problem("第 %d 条连线连到了不存在的步骤", i+1)
	case e.From == e.To:
		return problem("第 %d 条连线首尾是同一个步骤", i+1)
	case !slices.Contains(EdgeKinds, e.Kind):
		return problem("第 %d 条连线的类型只能是“然后”或“不通过退回”", i+1)
	case utf8.RuneCountInString(e.Label) > MaxLabelRunes:
		return problem("第 %d 条连线的文字最多 %d 个字", i+1, MaxLabelRunes)
	case seen[FlowEdge{From: e.From, To: e.To, Kind: e.Kind}]:
		return problem("第 %d 条连线重复了", i+1)
	}
	return nil
}

func stepLabel(i int, s FlowStep) string {
	if s.Name != "" {
		return "步骤“" + s.Name + "”"
	}
	return fmt.Sprintf("第 %d 个步骤", i+1)
}

func inCanvas(v float64) bool { return v >= -MaxCoord && v <= MaxCoord }
