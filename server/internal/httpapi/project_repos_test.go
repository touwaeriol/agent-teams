package httpapi

import (
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/gitremote"
)

// TestStandaloneBase: a standalone repository's base is the upstream HEAD unless one is given,
// and must be an existing branch; an empty repository has none.
func TestStandaloneBase(t *testing.T) {
	refs := gitremote.Refs{Head: "trunk", Branches: map[string]string{"trunk": "a", "develop": "b"}}
	cases := []struct {
		want   string
		refs   gitremote.Refs
		base   string
		errHas string
	}{
		{"", refs, "trunk", ""},
		{"develop", refs, "develop", ""},
		{"main", refs, "", "没有分支 main"},
		{"", gitremote.Refs{Branches: map[string]string{}}, "", "是空的"},
	}
	for _, c := range cases {
		got, err := standaloneBase("git.example.com/a/b", c.want, c.refs)
		if c.errHas != "" {
			if err == nil || !strings.Contains(err.Error(), c.errHas) {
				t.Fatalf("%q: err %v, want %q", c.want, err, c.errHas)
			}
			continue
		}
		if err != nil || got != c.base {
			t.Fatalf("%q: %q %v, want %q", c.want, got, err, c.base)
		}
	}
}
