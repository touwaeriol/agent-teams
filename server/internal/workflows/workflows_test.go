package workflows

import (
	"context"
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/testdb"
)

// devTestReview: dev, then test and docs in parallel, both into review, which sends back to dev.
func devTestReview() FlowDraft {
	return FlowDraft{Name: "开发流程", Description: "常规改动",
		Steps: []FlowStep{
			{ID: "review", Name: "审查", Role: "reviewer", Kind: "review"},
			{ID: "dev", Name: "开发", Role: "dev", Instructions: "改代码\n补测试"},
			{ID: "test", Name: "测试", Role: "qa"},
			{ID: "docs", Name: "文档", Role: "dev"},
			{ID: "ok", Name: "用户确认", Kind: "approval"},
		},
		Edges: []FlowEdge{
			{From: "dev", To: "test"}, {From: "dev", To: "docs"},
			{From: "test", To: "review"}, {From: "docs", To: "review"},
			{From: "review", To: "dev", Kind: "reject", Label: "有问题"},
			{From: "review", To: "ok"},
		}}
}

var team = map[string]bool{"dev": true, "qa": true, "reviewer": true}

func TestValidate(t *testing.T) {
	d := devTestReview()
	Normalize(&d)
	if err := Validate(d, team); err != nil {
		t.Fatalf("valid flow: %v", err)
	}
	if d.Steps[1].Kind != "work" || d.Edges[0].Kind != "next" {
		t.Fatalf("defaults not filled: %+v %+v", d.Steps[1], d.Edges[0])
	}
	cases := map[string]func(*FlowDraft){
		"no name":        func(d *FlowDraft) { d.Name = "" },
		"no steps":       func(d *FlowDraft) { d.Steps, d.Edges = nil, nil },
		"bad id":         func(d *FlowDraft) { d.Steps[0].ID = "a b" },
		"duplicate id":   func(d *FlowDraft) { d.Steps[1].ID = "review" },
		"no step name":   func(d *FlowDraft) { d.Steps[0].Name = "" },
		"unknown role":   func(d *FlowDraft) { d.Steps[0].Role = "ghost" },
		"no role":        func(d *FlowDraft) { d.Steps[0].Role = "" },
		"bad kind":       func(d *FlowDraft) { d.Steps[0].Kind = "party" },
		"dangling edge":  func(d *FlowDraft) { d.Edges[0].To = "nowhere" },
		"self edge":      func(d *FlowDraft) { d.Edges[0].To = "dev" },
		"bad edge kind":  func(d *FlowDraft) { d.Edges[0].Kind = "maybe" },
		"duplicate edge": func(d *FlowDraft) { d.Edges = append(d.Edges, FlowEdge{From: "dev", To: "test", Kind: "next"}) },
		"off canvas":     func(d *FlowDraft) { d.Steps[0].X = MaxCoord + 1 },
		"long label":     func(d *FlowDraft) { d.Edges[4].Label = strings.Repeat("字", MaxLabelRunes+1) },
	}
	for name, mutate := range cases {
		d := devTestReview()
		Normalize(&d)
		mutate(&d)
		Normalize(&d)
		if err := Validate(d, team); !IsProblem(err) {
			t.Errorf("%s: want a problem, got %v", name, err)
		}
	}
	lead := devTestReview()
	lead.Steps[0].Role = "lead"
	Normalize(&lead)
	if err := Validate(lead, team); err != nil {
		t.Fatalf("lead may own a step: %v", err)
	}
}

// TestOrderedAndRender: parallel steps follow their common step, a join comes after both
// branches, reject edges do not order, and the lead text names roles, kinds and both edge kinds.
func TestOrderedAndRender(t *testing.T) {
	d := devTestReview()
	Normalize(&d)
	w := Flow{Name: d.Name, Description: d.Description, Steps: d.Steps, Edges: d.Edges}
	var ids []string
	for _, s := range Ordered(w) {
		ids = append(ids, s.ID)
	}
	if got := strings.Join(ids, ","); got != "dev,test,docs,review,ok" {
		t.Fatalf("order %s", got)
	}
	text := Render(w, map[string]string{"dev": "开发", "qa": "测试", "reviewer": "审查员"})
	for _, want := range []string{
		"## Workflow: 开发流程", "parallel", "Flow description (written by the user):\n常规改动",
		"1. 开发 (role 开发 (dev), work)", "   Instructions: 改代码\n   补测试", "then → 测试, 文档",
		"审查 (role 审查员 (reviewer), review)", "on failure, back to → 开发 (有问题)", "用户确认 (the user, user approval)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("render lacks %q:\n%s", want, text)
		}
	}
	if gone := Render(w, map[string]string{"dev": "开发"}); !strings.Contains(gone, "role qa, no longer on the team") {
		t.Fatalf("a removed role must be flagged:\n%s", gone)
	}
	cycle := Flow{Name: "c", Steps: []FlowStep{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}},
		Edges: []FlowEdge{{From: "a", To: "b", Kind: "next"}, {From: "b", To: "a", Kind: "next"}}}
	if o := Ordered(cycle); len(o) != 2 || o[0].ID != "a" {
		t.Fatalf("cycle order %+v", o)
	}
}

// TestLibrary: the first flow becomes the default, a new default takes over, names are unique
// per user, other users see nothing, the project pick resolves (default, none, id), and
// deleting a picked flow sends its projects back to the default.
func TestLibrary(t *testing.T) {
	ctx := context.Background()
	pool, err := store.OpenPool(ctx, testdb.Migrated(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := store.Exec(ctx, pool, `INSERT INTO at_users (id, email, password_hash) VALUES ('u1','a@x','h'), ('u2','b@x','h');
		INSERT INTO at_roles (id, user_id, tier, role, title) VALUES ('r1','u1','member','dev','开发'), ('r2','u1','lead','lead','组长');
		INSERT INTO at_projects (id, user_id, name) VALUES ('p1','u1','P')`); err != nil {
		t.Fatal(err)
	}
	first := devTestReview()
	Normalize(&first)
	a, err := Create(ctx, pool, "u1", first)
	if err != nil || !a.IsDefault || len(a.Steps) != 5 || len(a.Edges) != 6 {
		t.Fatalf("first flow %+v %v", a, err)
	}
	if _, err := Create(ctx, pool, "u1", first); !IsProblem(err) {
		t.Fatalf("duplicate name: %v", err)
	}
	second := FlowDraft{Name: "小改动", Steps: []FlowStep{{ID: "dev", Name: "开发", Role: "dev", Kind: "work"}}, Edges: []FlowEdge{}, IsDefault: true}
	b, err := Create(ctx, pool, "u1", second)
	if err != nil || !b.IsDefault {
		t.Fatalf("second flow %+v %v", b, err)
	}
	list, _ := List(ctx, pool, "u1")
	if len(list) != 2 || list[0].ID != b.ID || list[1].IsDefault {
		t.Fatalf("list %+v", list)
	}
	if other, _ := List(ctx, pool, "u2"); len(other) != 0 {
		t.Fatalf("u2 sees %+v", other)
	}
	if _, err := Get(ctx, pool, "u2", a.ID); err != ErrNotFound {
		t.Fatalf("u2 get: %v", err)
	}
	if got, _ := ForProject(ctx, pool, "u1", nil); len(got) != 1 || got[0].ID != b.ID {
		t.Fatalf("default pick %+v", got)
	}
	if got, _ := ForProject(ctx, pool, "u1", []string{ProjectNone}); len(got) != 0 {
		t.Fatal("none must give no flow")
	}
	if got, _ := ForProject(ctx, pool, "u1", []string{a.ID}); len(got) != 1 || got[0].ID != a.ID {
		t.Fatalf("id pick %+v", got)
	}
	if got, _ := ForProject(ctx, pool, "u1", []string{b.ID, "wf_gone", a.ID}); len(got) != 2 || got[0].ID != b.ID || got[1].ID != a.ID {
		t.Fatalf("several picks keep their order and skip gone ones %+v", got)
	}
	section, err := LeadSection(ctx, pool, "u1", []string{a.ID})
	if err != nil || !strings.Contains(section, "## Workflow: 开发流程") || !strings.Contains(section, "role 开发 (dev)") || !strings.Contains(section, "role qa, no longer on the team") {
		t.Fatalf("lead section %v:\n%s", err, section)
	}
	both, err := LeadSection(ctx, pool, "u1", []string{a.ID, b.ID})
	if err != nil || !strings.Contains(both, "## Workflows (2)") || !strings.Contains(both, "pick the flow that best fits") ||
		!strings.Contains(both, "### Flow: 开发流程") || !strings.Contains(both, "### Flow: 小改动") || strings.Count(both, "```mermaid") != 2 {
		t.Fatalf("two flows %v:\n%s", err, both)
	}
	renamed := first
	renamed.Name = "开发流程 v2"
	if u, err := Update(ctx, pool, "u1", a.ID, renamed); err != nil || u.Name != "开发流程 v2" || u.IsDefault {
		t.Fatalf("update %+v %v", u, err)
	}
	if _, err := Update(ctx, pool, "u2", a.ID, renamed); err != ErrNotFound {
		t.Fatalf("u2 update: %v", err)
	}
	if _, err := store.Exec(ctx, pool, `UPDATE at_projects SET workflows = ARRAY[$1, $2] WHERE id = 'p1'`, a.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := Delete(ctx, pool, "u2", a.ID); err != ErrNotFound {
		t.Fatalf("u2 delete: %v", err)
	}
	if err := Delete(ctx, pool, "u1", a.ID); err != nil {
		t.Fatal(err)
	}
	var picks store.TextArray
	if err := store.QueryRow(ctx, pool, `SELECT workflows FROM at_projects WHERE id = 'p1'`).Scan(&picks); err != nil || len(picks) != 1 || picks[0] != b.ID {
		t.Fatalf("project picks after delete %v %v", picks, err)
	}
}

// TestBranchesAndMermaid: a branch edge needs its condition; branches order like next edges,
// render as "按条件只走一条" and appear in the Mermaid chart with their conditions, next
// edges plain and reject edges dashed; quotes in names cannot break the chart.
func TestBranchesAndMermaid(t *testing.T) {
	d := FlowDraft{Name: "分支", Steps: []FlowStep{
		{ID: "ask", Name: "判断\"要不要\"设计", Role: "lead", Kind: "decision"},
		{ID: "design", Name: "设计", Role: "dev"},
		{ID: "dev", Name: "开发", Role: "dev"},
		{ID: "review", Name: "审查", Role: "reviewer", Kind: "review"},
	}, Edges: []FlowEdge{
		{From: "ask", To: "design", Kind: "branch", Label: "需要设计"},
		{From: "ask", To: "dev", Kind: "branch", Label: "不需要"},
		{From: "design", To: "dev"},
		{From: "dev", To: "review"},
		{From: "review", To: "dev", Kind: "reject"},
	}}
	Normalize(&d)
	if err := Validate(d, team); err != nil {
		t.Fatalf("valid branches: %v", err)
	}
	unlabeled := d
	unlabeled.Edges = append([]FlowEdge{}, d.Edges...)
	unlabeled.Edges[0].Label = ""
	if err := Validate(unlabeled, team); !IsProblem(err) {
		t.Fatalf("a branch without its condition: %v", err)
	}
	w := Flow{Name: d.Name, Steps: d.Steps, Edges: d.Edges}
	text := Render(w, map[string]string{"dev": "开发", "reviewer": "审查员"})
	for _, want := range []string{
		"take only one by condition: if 需要设计 → 设计; if 不需要 → 开发", "\"branch\" arrows, take only one",
		"```mermaid\nflowchart LR\n", `s1["判断#quot;要不要#quot;设计<br/>lead · decision"]`, `s1 -->|"需要设计"| s2`,
		"s2 --> s3", `s4 -.->|"reject"| s3`, "Each step in detail",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("render lacks %q:\n%s", want, text)
		}
	}
}
