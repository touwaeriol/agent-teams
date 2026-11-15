package artifacts

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
)

func TestSigner(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := &Signer{keys: [][]byte{[]byte("new"), []byte("old")}, now: func() time.Time { return now }}
	token, exp := s.Sign("dl_abc", 3)
	if id, v, ok := s.Verify(token); !ok || id != "dl_abc" || v != 3 || !exp.After(now) {
		t.Fatalf("verify %s %d %v", id, v, ok)
	}
	old := &Signer{keys: [][]byte{[]byte("old")}, now: s.now}
	oldToken, _ := old.Sign("dl_abc", 1)
	if _, _, ok := s.Verify(oldToken); !ok {
		t.Fatal("token signed with a rotated-out key must still verify")
	}
	for _, bad := range []string{strings.Replace(token, ".3.", ".4.", 1), token + "x", "dl_abc.3", ""} {
		if _, _, ok := s.Verify(bad); ok {
			t.Errorf("accepted %q", bad)
		}
	}
	now = exp
	if _, _, ok := s.Verify(token); ok {
		t.Fatal("expired token accepted")
	}
}

func TestPathsAndTypes(t *testing.T) {
	for _, ok := range []string{"index.html", "assets/logo.svg", "Spec v2.md", "data.csv", "font.woff2"} {
		if !ValidPath(ok) {
			t.Errorf("%s refused", ok)
		}
	}
	for _, bad := range []string{"../x.html", "/abs.html", ".env.json", "a/.git/x.md", "run.sh", "x.exe", "noext"} {
		if ValidPath(bad) {
			t.Errorf("%s accepted", bad)
		}
	}
	if Entry([]string{"a.md"}) != "a.md" || Entry([]string{"a.css", "index.html"}) != "index.html" || Entry([]string{"a.html", "b.html"}) != "" {
		t.Fatal("entry")
	}
	if Render(ContentType("x.MD")) != "markdown" || Render(ContentType("a.png")) != "image" || Render(ContentType("a.htm")) != "html" ||
		Render(ContentType("a.pdf")) != "pdf" || Render(ContentType("a.csv")) != "text" {
		t.Fatal("render kinds")
	}
}

func decl(path, body string) protocol.UploadFile {
	sum := sha256.Sum256([]byte(body))
	return protocol.UploadFile{Path: path, Size: len(body), SHA256: hex.EncodeToString(sum[:]), ContentType: "text/plain"}
}

func TestSplit(t *testing.T) {
	files, err := Split([]byte("<p>hi</p>body{}"), []protocol.UploadFile{decl("index.html", "<p>hi</p>"), decl("s.css", "body{}")})
	if err != nil || len(files) != 2 || string(files[1].Data) != "body{}" || files[0].ContentType != "text/html; charset=utf-8" {
		t.Fatalf("split %+v %v", files, err)
	}
	cases := map[string]struct {
		data string
		decl []protocol.UploadFile
	}{
		"checksum":    {"evil", []protocol.UploadFile{{Path: "a.md", Size: 4, SHA256: decl("a.md", "good").SHA256}}},
		"short":       {"ab", []protocol.UploadFile{decl("a.md", "abc")}},
		"trailing":    {"abcd", []protocol.UploadFile{decl("a.md", "abc")}},
		"type":        {"MZ", []protocol.UploadFile{decl("a.exe", "MZ")}},
		"none":        {"", nil},
		"too big":     {strings.Repeat("x", MaxBytes+1), []protocol.UploadFile{decl("a.txt", strings.Repeat("x", MaxBytes+1))}},
		"negative":    {"", []protocol.UploadFile{{Path: "a.md", Size: -1}}},
		"dot segment": {"a", []protocol.UploadFile{decl("../a.md", "a")}},
	}
	for name, c := range cases {
		if _, err := Split([]byte(c.data), c.decl); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestPolicy(t *testing.T) {
	s := NewServer(nil, nil, "", "https://agent.example.com", nil)
	p := s.policy()
	if !strings.HasPrefix(p, "sandbox allow-scripts") || strings.Contains(p, "allow-same-origin") ||
		!strings.Contains(p, "frame-ancestors 'self' https://agent.example.com") {
		t.Fatalf("policy %s", p)
	}
	iso := NewServer(nil, nil, "https://files.example.net", "", nil)
	if !iso.Isolated() || !strings.Contains(iso.policy(), "allow-same-origin") {
		t.Fatal("isolated origin keeps same-origin inside its own sandbox")
	}
	if !sameHost("https://files.example.net", "FILES.example.net") || sameHost("https://files.example.net", "agent.example.com") {
		t.Fatal("sameHost")
	}
}
