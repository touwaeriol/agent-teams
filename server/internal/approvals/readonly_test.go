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
		"ls *.go", "cat src/*.ts", "wc -l web/src/*.tsx", "grep -n foo *.md", "git log --oneline -- 'src/*.go'",
		"rg -g '*.go' foo", "git diff --stat", "git log --output-indicator-new=+",
		"git -C owner__repo log --oneline -5", "git -C owner__repo status", "git -Cowner__repo diff", "git -C a -C b show HEAD",
		"git -C owner__repo branch -a", "git fetch", "git fetch --prune origin", "git -C a fetch origin main", "git ls-remote --heads origin",
		"git remote -v", "git remote get-url origin", "git tag", "git tag -l", "git reflog", "git describe --tags", "git stash list",
		"git worktree list", "git rev-list --count HEAD", "git cat-file -p HEAD",
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
		// globs: a file named like a flag must not reach a command whose flags write or run
		"find . *", "sed -n 1p *", "rg foo *", "git diff *", "tail -n 5 *.log",
		// globs that match . and ..
		"cat .*/secret", "ls .?", "cat '.'*", "cat .?/.?/etc/passwd", "cat a/.*", "ls [.][.]", "cat [/]etc/passwd",
		// values attached to short flags, abbreviated long flags, bundled short flags
		"grep -f/etc/passwd x", "grep -f~/.ssh/id_rsa x", "git diff --outp=x", "git show --ext", "git log --textc",
		"rg -nz foo", "rg --hostname-bin=./x foo", "rg --pre=./x foo", "git log -po",
		"find . -execdir sh ;", "find . -okdir rm {} +", "find . -fprintf out x", "find . -fls out",
		"git branch --list x", "git -c core.pager=sh log",
		"git -C ../x log", "git -C a/../.. log", "git -C ~ log", "git -C a push", "git -C a branch new", "git -C", "git -C a",
		"git -C a -c core.pager=sh log", "git -C a log --output=x",
		"git fetch origin main:main", "git fetch origin +x", "git fetch https://e/x", "git fetch ext::sh", "git fetch ./x",
		"git fetch --upload-pack=x origin", "git fetch --force origin", "git ls-remote --upload-pa=x origin", "git tag v1",
		"git tag -d v1", "git remote add x y", "git reflog expire --all", "git cat-file --filters HEAD:a", "git stash pop",
		"git worktree add x", "git --no-pager log", "FOO=1 ls", "ls\tsrc;id",
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
