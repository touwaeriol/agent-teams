package delivery

import (
	"strings"
	"testing"

	protocol "github.com/touwaeriol/agent-teams/protocol"
)

// TestPrepareError: a missing start branch tells the user where to fix it; other failures keep
// git's output.
func TestPrepareError(t *testing.T) {
	code := func(c protocol.GitResultErrorCode) *protocol.GitResult {
		msg := "git said no"
		return &protocol.GitResult{ErrorCode: &c, Error: &msg}
	}
	got := prepareError("dev-1", map[string]any{"repo": "o/r", "base": "main"}, code("base_missing")).String()
	if !strings.Contains(got, "起点分支 main 在仓库 o/r 里不存在") || !strings.Contains(got, "项目设置") {
		t.Fatalf("missing base: %s", got)
	}
	if got := prepareError("dev-1", map[string]any{"repo": "o/r", "base": ""}, code("base_missing")).String(); !strings.Contains(got, "没有配置起点分支") {
		t.Fatalf("no base: %s", got)
	}
	if got := prepareError("dev-1", nil, code("dirty_conflict")).String(); !strings.Contains(got, "dirty_conflict") || !strings.Contains(got, "git said no") {
		t.Fatalf("other: %s", got)
	}
}
