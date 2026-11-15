package delivery

import "testing"

func TestWebURL(t *testing.T) {
	ok := []string{"https://www.figma.com/file/x", "http://intranet.example/doc", " https://x.dev/a?b=c "}
	bad := []string{"javascript:alert(1)", "JavaScript:alert(1)", "data:text/html,<b>x</b>", "/api/x", "//evil.com/x",
		"https:", "file:///etc/passwd", "vbscript:x", ""}
	for _, u := range ok {
		if !WebURL(u) {
			t.Errorf("refused %q", u)
		}
	}
	for _, u := range bad {
		if WebURL(u) {
			t.Errorf("accepted %q", u)
		}
	}
}
