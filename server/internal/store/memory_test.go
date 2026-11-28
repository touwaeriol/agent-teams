package store_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/testdb"
)

func memoryProject(t *testing.T) (*store.Store, string) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, testdb.Migrated(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	u, err := s.CreateUser(ctx, "m@example.com", "h")
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.CreateProject(ctx, u.ID, store.NewProject{Name: "m", WorkMode: "worktree"})
	if err != nil {
		t.Fatal(err)
	}
	return s, p.ID
}

// TestMemoryFiles: names are normalized, the index follows writes and deletes, remember and
// thread summaries land in the right files, and the prompt lists unlinked files.
func TestMemoryFiles(t *testing.T) {
	ctx := context.Background()
	s, pid := memoryProject(t)
	q := s.Pool
	if p, _ := store.MemoryPrompt(ctx, q, store.ProjectMemory(pid)); p != "" {
		t.Fatalf("empty project prompt %q", p)
	}
	f, created, err := store.WriteMemoryFile(ctx, q, store.ProjectMemory(pid), store.MemoryWrite{Name: " build ", Description: "怎么\n构建", Content: "make", By: "lead-1 · #2"})
	if err != nil || !created || f.Name != "build.md" || f.UpdatedBy != "lead-1 · #2" {
		t.Fatalf("write %+v %v %v", f, created, err)
	}
	if _, created, _ = store.WriteMemoryFile(ctx, q, store.ProjectMemory(pid), store.MemoryWrite{Name: "build.md", Description: "构建步骤", Content: "make all"}); created {
		t.Fatal("rewrite reported as created")
	}
	if err := store.AppendMemoryNote(ctx, q, store.ProjectMemory(pid), "用户喜欢小 PR", "coordinator"); err != nil {
		t.Fatal(err)
	}
	idx, _ := store.GetMemoryFile(ctx, q, store.ProjectMemory(pid), "memory.md")
	if idx.Content != "# 项目记忆\n- [build.md](build.md) — 构建步骤\n- 用户喜欢小 PR\n" {
		t.Fatalf("index %q", idx.Content)
	}
	for _, bad := range []store.MemoryWrite{{Name: "a/b", Description: "x"}, {Name: "../x", Description: "x"}, {Name: "x", Description: ""},
		{Name: "x", Description: "x", Content: strings.Repeat("字", store.MemoryContentMax+1)}} {
		if _, _, err := store.WriteMemoryFile(ctx, q, store.ProjectMemory(pid), bad); !store.IsMemoryError(err) {
			t.Fatalf("write %q accepted: %v", bad.Name, err)
		}
	}
	// A file the index does not link (the user removed its line) is still listed in the prompt.
	if _, _, err := store.WriteMemoryFile(ctx, q, store.ProjectMemory(pid), store.MemoryWrite{Name: store.MemoryIndex, Content: "# 项目记忆\n- 用户喜欢小 PR\n"}); err != nil {
		t.Fatal(err)
	}
	prompt, _ := store.MemoryPrompt(ctx, q, store.ProjectMemory(pid))
	if !strings.Contains(prompt, "### MEMORY.md\n# 项目记忆\n- 用户喜欢小 PR") || !strings.Contains(prompt, "Other memory files:\n- build.md — 构建步骤") {
		t.Fatalf("prompt %q", prompt)
	}
	if err := store.DeleteMemoryFile(ctx, q, store.ProjectMemory(pid), "MEMORY.md", "user"); !store.IsMemoryError(err) {
		t.Fatalf("deleted the index: %v", err)
	}
	if err := store.DeleteMemoryFile(ctx, q, store.ProjectMemory(pid), "build", "user"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteMemoryFile(ctx, q, store.ProjectMemory(pid), "build", "user"); err == nil {
		t.Fatal("deleted twice")
	}
	for i := 1; i <= 32; i++ {
		if err := store.AppendThreadSummary(ctx, q, pid, i, fmt.Sprintf("T%d", i), "总结"); err != nil {
			t.Fatal(err)
		}
	}
	sum, _ := store.GetMemoryFile(ctx, q, store.ProjectMemory(pid), store.ThreadSummaries)
	if strings.Contains(sum.Content, "## #2 T2\n") || !strings.HasPrefix(sum.Content, "## #3 T3\n总结") || !strings.HasSuffix(sum.Content, "## #32 T32\n总结") {
		t.Fatalf("summaries keep the latest 30: %q", sum.Content[:40])
	}
	idx, _ = store.GetMemoryFile(ctx, q, store.ProjectMemory(pid), store.MemoryIndex)
	if strings.Count(idx.Content, "](thread-summaries.md)") != 1 {
		t.Fatalf("index %q", idx.Content)
	}
	list, _ := store.ListMemoryFiles(ctx, q, store.ProjectMemory(pid))
	if len(list) != 2 || list[0].Name != store.MemoryIndex {
		t.Fatalf("list %+v", list)
	}
}

// TestMemoryPromptBounds: a long MEMORY.md is cut at 200 lines with a note to read the rest.
func TestMemoryPromptBounds(t *testing.T) {
	ctx := context.Background()
	s, pid := memoryProject(t)
	var b strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&b, "- 第 %d 条\n", i)
	}
	if _, _, err := store.WriteMemoryFile(ctx, s.Pool, store.ProjectMemory(pid), store.MemoryWrite{Name: "MEMORY.md", Content: b.String()}); err != nil {
		t.Fatal(err)
	}
	p, _ := store.MemoryPrompt(ctx, s.Pool, store.ProjectMemory(pid))
	if !strings.Contains(p, "- 第 199 条") || strings.Contains(p, "- 第 200 条") || !strings.Contains(p, `memory_read("MEMORY.md")`) {
		t.Fatalf("prompt not bounded: …%s", p[len(p)-200:])
	}
}
