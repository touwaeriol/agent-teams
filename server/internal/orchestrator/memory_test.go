package orchestrator

import (
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/core"
)

// TestMemorySection: every tier gets the guide (the files, and the tools as the fallback) and both
// MEMORY.md, the user's first; a member is told what to do without a shared folder.
func TestMemorySection(t *testing.T) {
	lead := memorySection(core.Employee{Tier: "lead"}, "### MEMORY.md\n- 中文", "### MEMORY.md\n- x")
	if !strings.Contains(lead, "memory_edit(name, old_string, new_string)") || !strings.Contains(lead, ".project-files/.memory/") ||
		!strings.Contains(lead, ".user-files/.memory/") || !strings.HasSuffix(lead, "## 项目记忆\n### MEMORY.md\n- x") ||
		strings.Index(lead, "- 中文") > strings.Index(lead, "## 项目记忆") || strings.Contains(lead, "complete_task") {
		t.Fatalf("lead section %q", lead)
	}
	member := memorySection(core.Employee{Tier: "member"}, "", "")
	if !strings.Contains(member, "用 Edit 改") || !strings.Contains(member, "complete_task") || !strings.Contains(member, "（还没有用户记忆）") ||
		!strings.HasSuffix(member, "（还没有项目记忆）") {
		t.Fatalf("member section %q", member)
	}
}
