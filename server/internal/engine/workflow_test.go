package engine_test

import (
	"strings"
	"testing"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
)

// TestWorkflowInLeadPrompt: a saved flow (the user's first, so the default) is written into the
// next thread lead's prompt with its parallel branches; a step naming a role outside the team is
// refused; a project may pick several flows (the lead gets both and picks per task); none leaves
// the flow out; an unknown pick or none with others is refused.
func TestWorkflowInLeadPrompt(t *testing.T) {
	tm := startTeam(t)
	flow := map[string]any{"name": "开发流程", "steps": []map[string]any{
		{"id": "a", "name": "写代码", "role": "dev", "instructions": "按任务改代码"},
		{"id": "b", "name": "补测试", "role": "dev"},
		{"id": "c", "name": "写文档", "role": "dev"},
		{"id": "d", "name": "合并开 PR", "role": "lead"},
	}, "edges": []map[string]any{{"from": "a", "to": "b"}, {"from": "a", "to": "c"}, {"from": "b", "to": "d"}, {"from": "c", "to": "d"}}}
	var created struct {
		ID        string `json:"id"`
		IsDefault bool   `json:"is_default"`
	}
	tm.b.must(201, "POST", "/api/workflows", flow, &created)
	if !created.IsDefault {
		t.Fatal("the first flow must be the default")
	}
	bad := map[string]any{"name": "坏流程", "steps": []map[string]any{{"id": "x", "name": "X", "role": "ghost"}}, "edges": []any{}}
	tm.b.must(422, "POST", "/api/workflows", bad, nil)

	sys := openLeadPrompt(t, tm, "Flow one")
	for _, want := range []string{"## Workflow: 开发流程", "1. 写代码 (role", "Instructions: 按任务改代码", "then → 补测试, 写文档", "合并开 PR (the lead: you"} {
		if !strings.Contains(sys, want) {
			t.Fatalf("lead prompt misses %q:\n%s", want, sys)
		}
	}

	small := map[string]any{"name": "小改动", "steps": []map[string]any{{"id": "a", "name": "直接改", "role": "dev"}}, "edges": []any{}}
	var second struct {
		ID string `json:"id"`
	}
	tm.b.must(201, "POST", "/api/workflows", small, &second)
	tm.b.must(422, "PATCH", "/api/projects/"+tm.projectID, map[string]any{"workflows": []string{"wf_nope"}}, nil)
	tm.b.must(422, "PATCH", "/api/projects/"+tm.projectID, map[string]any{"workflows": []string{"none", second.ID}}, nil)
	var view struct{ Workflows []string }
	tm.b.must(200, "PATCH", "/api/projects/"+tm.projectID, map[string]any{"workflows": []string{created.ID, second.ID, created.ID}}, &view)
	if strings.Join(view.Workflows, ",") != created.ID+","+second.ID {
		t.Fatalf("project workflows %v", view.Workflows)
	}
	sys = openLeadPrompt(t, tm, "Flow both")
	for _, want := range []string{"## Workflows (2)", "### Flow: 开发流程", "### Flow: 小改动"} {
		if !strings.Contains(sys, want) {
			t.Fatalf("lead prompt misses %q:\n%s", want, sys)
		}
	}
	tm.b.must(200, "PATCH", "/api/projects/"+tm.projectID, map[string]any{"workflows": []string{"none"}}, &view)
	if len(view.Workflows) != 1 || view.Workflows[0] != "none" {
		t.Fatalf("project workflows %v", view.Workflows)
	}
	if sys := openLeadPrompt(t, tm, "Flow two"); strings.Contains(sys, "## Workflow") {
		t.Fatal("a project with no flow still got one")
	}
	tm.b.must(204, "DELETE", "/api/workflows/"+created.ID, nil, nil)
}

func openLeadPrompt(t *testing.T, tm *team, title string) string {
	t.Helper()
	var th core.ThreadView
	tm.b.must(201, "POST", "/api/projects/"+tm.projectID+"/threads", map[string]any{"title": title, "brief": "go"}, &th)
	ctx, cancel := tm.w.waitCtx()
	defer cancel()
	f, err := tm.n.Wait(ctx, protocol.TypeSessionOpen, func(f nodetest.Frame) bool { return strings.Contains(string(f.Body), title) })
	if err != nil {
		t.Fatal(err)
	}
	var open protocol.SessionOpen
	if err := f.Decode(&open); err != nil {
		t.Fatal(err)
	}
	return *open.Claude.SystemAppend
}
