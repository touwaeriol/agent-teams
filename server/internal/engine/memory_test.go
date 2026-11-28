package engine_test

import (
	"strings"
	"testing"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
)

// TestMemoryTools: leads write, edit and delete memory files (the index line follows), members
// have no write tools, rejected writes come back as tool errors, the user's own memory is written
// with scope user, and a session opened afterwards gets the project instructions, the user's
// MEMORY.md and the project's in its system prompt.
func TestMemoryTools(t *testing.T) {
	tm := startTeam(t)
	res := tm.lead.mustCall("memory_write", map[string]string{"name": "testing", "description": "怎么跑测试", "content": "用 docker 跑 make test"})
	if !strings.Contains(text(res), "已创建 testing.md") {
		t.Fatalf("write %q", text(res))
	}
	if res := tm.lead.call("memory_write", map[string]string{"name": "notes", "content": "x"}); res.Ok || !strings.Contains(text(res), "描述") {
		t.Fatalf("write without description: %+v", res)
	}
	if res := tm.dev.call("memory_write", map[string]string{"name": "x", "description": "x", "content": "x"}); res.Ok {
		t.Fatal("member wrote memory")
	}
	if res := tm.dev.mustCall("memory_read", map[string]string{"name": "testing.md"}); text(res) != "用 docker 跑 make test" {
		t.Fatalf("member read %q", text(res))
	}
	if res := tm.dev.mustCall("memory_read", map[string]string{"name": "MEMORY.md"}); !strings.Contains(text(res), "- [testing.md](testing.md) — 怎么跑测试") {
		t.Fatalf("index %q", text(res))
	}
	if res := tm.dev.call("memory_read", map[string]string{"name": "nope"}); res.Ok {
		t.Fatal("read a missing file")
	}
	res = tm.lead.mustCall("memory_edit", map[string]any{"name": "testing.md", "old_string": "make test", "new_string": "make test-all"})
	if !strings.Contains(text(res), "1 处") {
		t.Fatalf("edit %q", text(res))
	}
	if res := tm.lead.call("memory_edit", map[string]any{"name": "testing.md", "old_string": "nope", "new_string": "x"}); res.Ok || !strings.Contains(text(res), "old_string") {
		t.Fatalf("edit of missing text: %+v", res)
	}
	if res := tm.dev.mustCall("memory_read", map[string]string{"name": "testing.md"}); text(res) != "用 docker 跑 make test-all" {
		t.Fatalf("after edit %q", text(res))
	}
	tm.lead.mustCall("memory_write", map[string]any{"name": "MEMORY.md", "content": "# 用户记忆\n- 回复都用中文", "scope": "user"})
	if res := tm.dev.mustCall("memory_read", map[string]any{"name": "MEMORY.md", "scope": "user"}); !strings.Contains(text(res), "- 回复都用中文") {
		t.Fatalf("user memory %q", text(res))
	}
	tm.lead.mustCall("memory_write", map[string]string{"name": "deploy.md", "description": "发布", "content": "滚动发布"})
	tm.lead.mustCall("memory_delete", map[string]string{"name": "deploy.md"})
	if res := tm.lead.mustCall("memory_list", map[string]any{}); strings.Contains(text(res), "deploy.md") || !strings.Contains(text(res), "testing.md") {
		t.Fatalf("list %q", text(res))
	}

	tm.b.must(200, "PATCH", "/api/projects/"+tm.projectID, map[string]any{"context": "回答都以【测】开头"}, nil)
	var second core.ThreadView
	tm.b.must(201, "POST", "/api/projects/"+tm.projectID+"/threads", map[string]any{"title": "Next", "brief": "go"}, &second)
	ctx, cancel := tm.w.waitCtx()
	defer cancel()
	f, err := tm.n.Wait(ctx, protocol.TypeSessionOpen, func(f nodetest.Frame) bool { return strings.Contains(string(f.Body), "Next") })
	if err != nil {
		t.Fatal(err)
	}
	var open protocol.SessionOpen
	if err := f.Decode(&open); err != nil {
		t.Fatal(err)
	}
	sys := *open.Claude.SystemAppend
	for _, want := range []string{"## Project instructions", "回答都以【测】开头", "## User memory", "- 回复都用中文", "## Project memory", "### MEMORY.md", "- [testing.md](testing.md) — 怎么跑测试", "memory_write(name, description, content)"} {
		if !strings.Contains(sys, want) {
			t.Fatalf("new lead prompt misses %q:\n%s", want, sys)
		}
	}
	ins, lang, mem := strings.Index(sys, "## Project instructions"), strings.Index(sys, "## Reply language"), strings.Index(sys, "## Project memory")
	if lang < ins || mem < lang || ins < 0 {
		t.Fatalf("project instructions are not ahead of the rest: 项目指令=%d 回复语言=%d 项目记忆=%d", ins, lang, mem)
	}
	if strings.Contains(sys, "deploy.md") {
		t.Fatal("deleted file still in the prompt")
	}
}
