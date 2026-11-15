package httpapi

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/skills"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

func zipBase64(t *testing.T, files map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(content))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestSkillsAPIAndRoleSkills(t *testing.T) {
	h := newHarness(t)
	c, _ := h.signUp("a@example.com")
	other, _ := h.signUp("b@example.com")
	var list SkillsBody
	c.must(200, "GET", "/api/skills", nil, &list)
	if len(list.Skills) != 2 || !list.Skills[0].Builtin || list.Skills[0].Name != "design" {
		t.Fatalf("builtins %+v", list.Skills)
	}
	var sk skills.Skill
	c.must(201, "POST", "/api/skills", map[string]any{"zip": zipBase64(t, map[string]string{
		"pitch/SKILL.md": "---\nname: pitch\ndescription: Pitch decks\n---\n# Pitch\n", "pitch/ref.md": "x"})}, &sk)
	if sk.ID == "" || sk.Name != "pitch" || sk.Builtin {
		t.Fatalf("created %+v", sk)
	}
	if r := c.do("POST", "/api/skills", map[string]any{"files": []skills.File{{Path: "SKILL.md", Content: "no front matter"}}}); r.Status != 422 {
		t.Fatalf("bad skill = %d %s", r.Status, r.Body)
	}
	if r := c.do("POST", "/api/skills", map[string]any{"files": []skills.File{{Path: "x/SKILL.md", Content: "---\nname: docs\ndescription: d\n---\n"}}}); r.Status != 422 {
		t.Fatalf("built-in name = %d", r.Status)
	}

	var presets presetsOutput
	c.must(200, "GET", "/api/roles/presets", nil, &presets.Body)
	designer := presets.Body.Roles[0]
	var body RolesBody
	c.must(200, "GET", "/api/roles", nil, &body)
	designer.Skills = append(designer.Skills, "pitch")
	next := append(append([]store.Role{}, body.Roles...), designer)
	c.must(200, "PUT", "/api/roles", map[string]any{"roles": next}, &body)
	if got := body.Roles[3].Skills; len(got) != 3 || got[2] != "pitch" {
		t.Fatalf("saved skills %v", got)
	}
	next = append([]store.Role{}, body.Roles...)
	next[3].Skills = []string{"nope"}
	if r := c.do("PUT", "/api/roles", map[string]any{"roles": next}); r.Status != 422 {
		t.Fatalf("unknown skill = %d", r.Status)
	}

	if r := other.do("DELETE", "/api/skills/"+sk.ID, nil); r.Status != 404 {
		t.Fatalf("other user delete = %d", r.Status)
	}
	c.must(204, "DELETE", "/api/skills/"+sk.ID, nil, nil)
	c.must(200, "GET", "/api/roles", nil, &body)
	if got := body.Roles[3].Skills; len(got) != 2 {
		t.Fatalf("skill not removed from role: %v", got)
	}
}
