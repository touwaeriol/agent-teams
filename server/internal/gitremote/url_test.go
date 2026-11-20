package gitremote

import (
	"errors"
	"testing"
)

func TestParseURL(t *testing.T) {
	ok := []struct{ in, url, slug, transport, provider, user string }{
		{"https://gitlab.com/acme/web.git", "https://gitlab.com/acme/web.git", "gitlab.com/acme/web", "https", "gitlab", "oauth2"},
		{"https://GitLab.com/acme/sub/web/", "https://gitlab.com/acme/sub/web.git", "gitlab.com/acme/sub/web", "https", "gitlab", "oauth2"},
		{"git@gitlab.com:acme/web.git", "ssh://git@gitlab.com/acme/web.git", "gitlab.com/acme/web", "ssh", "gitlab", "git"},
		{"ssh://deploy@git.example.com:2222/team/app.git", "ssh://deploy@git.example.com:2222/team/app.git", "git.example.com:2222/team/app", "ssh", "generic", "deploy"},
		{"https://git.example.com:8443/team/app", "https://git.example.com:8443/team/app.git", "git.example.com:8443/team/app", "https", "generic", ""},
		{"ssh://git.example.com:22/team/app", "ssh://git@git.example.com/team/app.git", "git.example.com/team/app", "ssh", "generic", "git"},
	}
	for _, c := range ok {
		p, err := ParseURL(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if p.URL != c.url || p.Slug != c.slug || p.Transport != c.transport || p.Provider != c.provider || p.DefaultUsername != c.user {
			t.Fatalf("%s: %+v", c.in, p)
		}
	}
	for _, bad := range []string{"", "https://user:tok@gitlab.com/a/b", "http://gitlab.com/a/b", "git://x.com/a/b", "file:///tmp/a/b",
		"https://gitlab.com/onlyone", "https://gitlab.com/a/../b", "https://gitlab.com/a/b?x=1", "ext::sh -c x", "https://-bad.com/a/b"} {
		if _, err := ParseURL(bad); !errors.Is(err, ErrURL) {
			t.Fatalf("%q accepted (%v)", bad, err)
		}
	}
}
