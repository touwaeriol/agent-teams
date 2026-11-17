package delivery

import (
	"fmt"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/github"
)

func TestErrorClasses(t *testing.T) {
	gh := func(status int, msg string) error {
		return fmt.Errorf("o/r#7：%w", &github.Error{Status: status, Message: msg})
	}
	cases := []struct {
		err             error
		limited, report bool
	}{
		{gh(429, "Too Many Requests"), true, false},
		{gh(403, "You have exceeded a secondary rate limit"), true, false},
		{gh(403, "API rate limit exceeded for installation"), true, false},
		{gh(403, "Resource not accessible by integration"), false, true},
		{gh(405, "Pull Request is not mergeable"), false, true},
		{gh(409, "Head branch was modified"), false, true},
		{gh(502, "Bad Gateway"), false, false},
		{fmt.Errorf("dial tcp: connection refused"), false, false},
	}
	for _, c := range cases {
		if rateLimited(c.err) != c.limited || permanent(c.err) != c.report {
			t.Errorf("%v: rateLimited %v permanent %v", c.err, rateLimited(c.err), permanent(c.err))
		}
	}
	if s := mergeErrorText("合并失败", gh(405, "Pull Request is not mergeable")); s == "" || s == "合并失败：o/r#7：Pull Request is not mergeable" {
		t.Errorf("405 not explained: %s", s)
	}
}
