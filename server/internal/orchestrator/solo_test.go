package orchestrator

import "testing"

func TestSessionBranch(t *testing.T) {
	repos := []RepoFocus{{Repo: "touwaeriol/agent-teams"}, {Repo: "git.example.com/acme/api"}}
	cases := []struct{ name, title, want string }{
		{"", "Fix login redirect", "session/0123abcd-fix-login-redirect"},
		{"", "修复登录后的跳转", "session/0123abcd-agent-teams-api"},
		{"login-fix", "修复登录", "session/0123abcd-login-fix"},
		{"", "修复 login 跳转", "session/0123abcd-login"},
	}
	for _, c := range cases {
		if got := SessionBranch("0123abcdef", c.name, c.title, repos); got != c.want {
			t.Errorf("SessionBranch(%q, %q) = %q, want %q", c.name, c.title, got, c.want)
		}
	}
	if got := SessionBranch("0123abcdef", "", "修复", nil); got != "session/0123abcd-work" {
		t.Errorf("no words at all: %q", got)
	}
}
