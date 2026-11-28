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
		"## 工作流程：开发流程", "并行", "流程说明（用户编写）：\n常规改动",
		"1. 开发（岗位 开发（dev），做事）", "   说明：改代码\n   补测试", "然后 → 测试、文档",
		"审查（岗位 审查员（reviewer），审查）", "不通过退回 → 开发（有问题）", "用户确认（用户，等用户确认）",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("render lacks %q:\n%s", want, text)
		}
	}
	if gone := Render(w, map[string]string{"dev": "开发"}); !strings.Contains(gone, "岗位 qa，已不在团队里") {
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
	if w, ok, _ := ForProject(ctx, pool, "u1", ""); !ok || w.ID != b.ID {
		t.Fatalf("default pick %+v %v", w, ok)
	}
	if _, ok, _ := ForProject(ctx, pool, "u1", ProjectNone); ok {
		t.Fatal("none must give no flow")
	}
	if w, ok, _ := ForProject(ctx, pool, "u1", a.ID); !ok || w.ID != a.ID {
		t.Fatalf("id pick %+v %v", w, ok)
	}
	section, err := LeadSection(ctx, pool, "u1", a.ID)
	if err != nil || !strings.Contains(section, "岗位 开发（dev）") || !strings.Contains(section, "岗位 qa，已不在团队里") {
		t.Fatalf("lead section %v:\n%s", err, section)
	}
	renamed := first
	renamed.Name = "开发流程 v2"
	if u, err := Update(ctx, pool, "u1", a.ID, renamed); err != nil || u.Name != "开发流程 v2" || u.IsDefault {
		t.Fatalf("update %+v %v", u, err)
	}
	if _, err := Update(ctx, pool, "u2", a.ID, renamed); err != ErrNotFound {
		t.Fatalf("u2 update: %v", err)
	}
	if _, err := store.Exec(ctx, pool, `UPDATE at_projects SET workflow = $1 WHERE id = 'p1'`, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := Delete(ctx, pool, "u2", a.ID); err != ErrNotFound {
		t.Fatalf("u2 delete: %v", err)
	}
	if err := Delete(ctx, pool, "u1", a.ID); err != nil {
		t.Fatal(err)
	}
	var pick string
	if err := store.QueryRow(ctx, pool, `SELECT workflow FROM at_projects WHERE id = 'p1'`).Scan(&pick); err != nil || pick != "" {
		t.Fatalf("project pick after delete %q %v", pick, err)
	}
}
