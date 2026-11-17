package orchapi

import (
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/artifacts"
)

func TestStoredNameKeepsUploadsValid(t *testing.T) {
	cases := map[string]string{
		"main.png":       "main.png",
		"截图 1.png":       "__ 1.png",
		"设计稿.html":       "___.html",
		".hidden.md":     "file.hidden.md",
		"-x.csv":         "file-x.csv",
		"a/b":            "a_b",
		"résumé (1).pdf": "r_sum_ _1_.pdf",
	}
	for in, want := range cases {
		got := storedName(in)
		if got != want {
			t.Errorf("storedName(%q) = %q, want %q", in, got, want)
		}
		if in != "a/b" && !artifacts.ValidPath(got) {
			t.Errorf("storedName(%q) = %q is not a valid path", in, got)
		}
	}
	if uploadName(`C:\shots\设计.png`) != "设计.png" || uploadName("../../x.png") != "x.png" {
		t.Fatal("uploadName keeps folders")
	}
}

func TestLibraryFileRejectsUnknownTypes(t *testing.T) {
	for _, name := range []string{"run.sh", "..", "图片", "shot.png", "a.JPG", "b.jpeg", "c.gif", "d.webp", "e.svg"} {
		if _, err := libraryFile(UploadBody{Name: name, Data: "aGk="}); err == nil {
			t.Errorf("%q accepted", name)
		}
	}
	if _, err := libraryFile(UploadBody{Name: "shot.png", Data: "aGk="}); err == nil || !strings.Contains(err.Error(), "不是交付物") {
		t.Errorf("png refusal %v", err)
	}
	if LibraryKind("text/html; charset=utf-8") != "design" || LibraryKind("application/pdf") != "doc" {
		t.Error("library kinds")
	}
	f, err := libraryFile(UploadBody{Name: "说明.md", Data: "aGk="})
	if err != nil || f.Path != "__.md" || f.Size != 2 {
		t.Fatalf("%+v %v", f, err)
	}
}
