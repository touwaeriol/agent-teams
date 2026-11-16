package orchestrator

import (
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/core"
)

// TestMemorySection: everyone gets the guide and MEMORY.md; only members are told they read only.
func TestMemorySection(t *testing.T) {
	lead := memorySection(core.Employee{Tier: "lead"}, "### MEMORY.md\n- x")
	if !strings.Contains(lead, "memory_write(name, description, content)") || !strings.HasSuffix(lead, "### MEMORY.md\n- x") {
		t.Fatalf("lead section %q", lead)
	}
	member := memorySection(core.Employee{Tier: "member"}, "")
	if strings.Contains(member, "memory_write(") || !strings.Contains(member, "你只能读记忆") || !strings.HasSuffix(member, "（还没有记忆）") {
		t.Fatalf("member section %q", member)
	}
}
