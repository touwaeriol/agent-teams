package skills

import (
	"archive/zip"
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/testdb"
)

const okSkill = "---\nname: pitch\ndescription: \"Write pitch decks\"\n---\n# Pitch\n"

func TestBuiltinsParse(t *testing.T) {
	names := BuiltinNames()
	if len(names) != 2 || names[0] != "design" || names[1] != "docs" {
		t.Fatalf("builtins %v", names)
	}
	for _, s := range Builtin() {
		if !s.Builtin || s.Description == "" || len(s.Files) == 0 {
			t.Fatalf("bad builtin %+v", s)
		}
	}
	if !strings.Contains(Builtin()[0].Files[0].Content, "publish_file") {
		t.Fatal("design skill must tell the model to publish_file")
	}
}

func TestParse(t *testing.T) {
	s, err := Parse([]File{{Path: "SKILL.md", Content: okSkill}, {Path: "ref/a.md", Content: "x"}})
	if err != nil || s.Name != "pitch" || s.Description != "Write pitch decks" || s.Size != len(okSkill)+1 {
		t.Fatalf("parse %+v %v", s, err)
	}
	cases := map[string][]File{
		"no entry":        {{Path: "README.md", Content: okSkill}},
		"no name":         {{Path: "SKILL.md", Content: "---\ndescription: d\n---\n"}},
		"bad name":        {{Path: "SKILL.md", Content: "---\nname: Bad Name\ndescription: d\n---\n"}},
		"no description":  {{Path: "SKILL.md", Content: "---\nname: ok\n---\n"}},
		"unclosed fence":  {{Path: "SKILL.md", Content: "---\nname: ok\ndescription: d\n"}},
		"dotdot":          {{Path: "SKILL.md", Content: okSkill}, {Path: "../x", Content: ""}},
		"absolute":        {{Path: "SKILL.md", Content: okSkill}, {Path: "/etc/x", Content: ""}},
		"hidden":          {{Path: "SKILL.md", Content: okSkill}, {Path: ".claude-plugin/plugin.json", Content: ""}},
		"binary":          {{Path: "SKILL.md", Content: okSkill}, {Path: "a.bin", Content: "a\x00b"}},
		"big file":        {{Path: "SKILL.md", Content: okSkill}, {Path: "a.md", Content: strings.Repeat("x", MaxFileBytes+1)}},
		"duplicate paths": {{Path: "SKILL.md", Content: okSkill}, {Path: "SKILL.md", Content: okSkill}},
	}
	for name, files := range cases {
		if _, err := Parse(files); err == nil || !IsProblem(err) {
			t.Errorf("%s: want a problem, got %v", name, err)
		}
	}
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(content))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestFromZipStripsFolderAndJunk(t *testing.T) {
	data := zipOf(t, map[string]string{"pitch/SKILL.md": okSkill, "pitch/ref/a.md": "a", "__MACOSX/pitch/._SKILL.md": "x",
		"pitch/.DS_Store": "x"})
	files, err := FromZip(data)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Parse(files)
	if err != nil || len(s.Files) != 2 || s.Files[0].Path != "SKILL.md" || s.Files[1].Path != "ref/a.md" {
		t.Fatalf("files %+v %v", s.Files, err)
	}
	if _, err := FromZip([]byte("not a zip")); !IsProblem(err) {
		t.Fatalf("want problem, got %v", err)
	}
}

func TestInstructions(t *testing.T) {
	b := bundle(Skill{Name: "pitch", Files: []File{{Path: "SKILL.md", Content: okSkill}, {Path: "ref/a.md", Content: "extra"}}})
	if Instructions(nil) != "" {
		t.Fatal("no skills, no text")
	}
	got := Instructions([]protocol.SkillBundle{b})
	if !strings.Contains(got, "## Skill: pitch") || !strings.Contains(got, "# Pitch") || !strings.Contains(got, "### pitch/ref/a.md") {
		t.Fatalf("instructions %q", got)
	}
}

func TestLibrary(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testdb.Migrated(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `INSERT INTO at_users (id, email, password_hash, role) VALUES ('u1','a@x','h','user'), ('u2','b@x','h','user')`); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(ctx, pool, "u1", []File{{Path: "SKILL.md", Content: "---\nname: design\ndescription: d\n---\n"}}); !IsProblem(err) {
		t.Fatalf("built-in name must be refused: %v", err)
	}
	s, err := Create(ctx, pool, "u1", []File{{Path: "SKILL.md", Content: okSkill}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Create(ctx, pool, "u1", []File{{Path: "SKILL.md", Content: okSkill}}); !IsProblem(err) {
		t.Fatalf("duplicate must be a problem: %v", err)
	}
	names, _ := Names(ctx, pool, "u1")
	if !names["pitch"] || !names["design"] || !names["docs"] {
		t.Fatalf("names %v", names)
	}
	other, _ := Names(ctx, pool, "u2")
	if other["pitch"] {
		t.Fatal("another user's skill leaked")
	}
	bundles, err := Bundles(ctx, pool, "u1", []string{"design", "pitch", "gone", "pitch"})
	if err != nil || len(bundles) != 2 || bundles[0].Name != "design" || bundles[1].Files[0].Content != okSkill {
		t.Fatalf("bundles %+v %v", bundles, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO at_roles (id, user_id, tier, role, skills) VALUES ('ro1','u1','member','dev', '{pitch,docs}')`); err != nil {
		t.Fatal(err)
	}
	if err := Delete(ctx, pool, "u2", s.ID); err != ErrNotFound {
		t.Fatalf("other user delete: %v", err)
	}
	if err := Delete(ctx, pool, "u1", s.ID); err != nil {
		t.Fatal(err)
	}
	var left []string
	_ = pool.QueryRow(ctx, `SELECT skills FROM at_roles WHERE id = 'ro1'`).Scan(&left)
	if len(left) != 1 || left[0] != "docs" {
		t.Fatalf("role skills after delete %v", left)
	}
}
