package approvals

import (
	"encoding/json"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

func bash(cmd string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"command": cmd})
	return raw
}

func TestReadOnlyCommandAllows(t *testing.T) {
	for _, cmd := range []string{
		"ls -la", "ls src/app", "cat README.md", "head -n 40 go.mod", "tail -n 20 log.txt", "wc -l main.go",
		"grep -rn 'func.*Handle' server", `grep -n "TODO" a.go`, "rg -n useState web/src", "find . -name '*.go' -type f",
		"sed -n 1,80p main.go", "sed -n '10,$p' a.go", "git log --oneline -20", "git status --short", "git diff HEAD~1 -- a.go",
		"git show abc123", "git branch -a", "git branch", "pwd", "find src -maxdepth 2",
	} {
		if !ReadOnlyCommand("Bash", bash(cmd)) {
			t.Errorf("should allow %q", cmd)
		}
	}
}

func TestReadOnlyCommandRefusesWrites(t *testing.T) {
	for _, cmd := range []string{
		"rm -rf src", "echo hi > a.txt", "cat a | sh", "ls; rm a", "ls && touch b", "cat $(which x)", "cat `id`",
		`grep "$HOME" a`, "sed -i s/a/b/ f", "sed -n 1p -i f", "sed -n 'w out' f", "sed 1,3p f", "find . -delete",
		"find . -exec rm {} ;", "find . -fprint out", "rg --pre ./x foo", "rg -z foo", "git branch new", "git branch -D x",
		"git checkout main", "git commit -m x", "git push", "git -C /tmp log", "git diff --output=x", "git log -ox",
		"git diff --ext-diff", "cat /etc/passwd", "cat ../secret", "cat a/../../b", "ls ~", "tail -f log", "tail -fn 5 log",
		"npm test", "python3 x.py", "", "cat 'unterminated", "ls #x", "cat a\nrm b",
	} {
		if ReadOnlyCommand("Bash", bash(cmd)) {
			t.Errorf("must not allow %q", cmd)
		}
	}
	if ReadOnlyCommand("Edit", json.RawMessage(`{"file_path":"a.go"}`)) || ReadOnlyCommand("Bash", json.RawMessage(`{"command":["ls"]}`)) {
		t.Error("only string shell commands qualify")
	}
}

func TestReadOnlyRole(t *testing.T) {
	yes, no := true, false
	cases := map[string]struct {
		emp  core.Employee
		want bool
	}{
		"read_only member": {core.Employee{Tier: "member", Spec: store.Role{ReadOnly: &yes}}, true},
		"reviewer":         {core.Employee{Tier: "member", Spec: store.Role{Gate: "review", ReadOnly: &no}}, true},
		"lead":             {core.Employee{Tier: "lead"}, true},
		"writer member":    {core.Employee{Tier: "member", Spec: store.Role{ReadOnly: &no}}, false},
	}
	for name, c := range cases {
		if got := readOnlyRole(c.emp); got != c.want {
			t.Errorf("%s: got %v", name, got)
		}
	}
}
