package httpapi

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/plugins"
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

func pluginZip(t *testing.T, name, word string) string {
	return zipBase64(t, map[string]string{
		name + "/.claude-plugin/plugin.json": `{"name":"` + name + `","version":"1.0.0"}`,
		name + "/skills/greet/SKILL.md":      "---\nname: greet\ndescription: d\n---\n" + word,
	})
}

func TestPluginsAPI(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.signUp("a@example.com")
	c, _ := h.signUp("b@example.com")
	other, _ := h.signUp("c@example.com")

	var p plugins.Plugin
	c.must(201, "POST", "/api/plugins", map[string]any{"zip": pluginZip(t, "tinyplug", "ONE")}, &p)
	if p.ID == "" || p.Name != "tinyplug" || p.Latest != 1 || p.Builtin || p.Versions[0].Contents.Skills[0] != "greet" {
		t.Fatalf("uploaded %+v", p)
	}
	c.must(201, "POST", "/api/plugins", map[string]any{"zip": pluginZip(t, "tinyplug", "TWO")}, &p)
	if p.Latest != 2 || len(p.Versions) != 2 {
		t.Fatalf("re-upload %+v", p)
	}
	if r := c.do("POST", "/api/plugins", map[string]any{"zip": zipBase64(t, map[string]string{"README.md": "x"})}); r.Status != 422 {
		t.Fatalf("no manifest = %d", r.Status)
	}
	var list PluginsBody
	other.must(200, "GET", "/api/plugins", nil, &list)
	if len(list.Plugins) != 0 {
		t.Fatalf("another user's plugin leaked: %+v", list)
	}
	if r := other.do("DELETE", "/api/plugins/"+p.ID, nil); r.Status != 404 {
		t.Fatalf("other user delete = %d", r.Status)
	}

	// Built-ins: admin only, visible to everyone, hidden when disabled.
	if r := c.do("POST", "/api/admin/plugins", map[string]any{"zip": pluginZip(t, "brand", "B")}); r.Status != 403 {
		t.Fatalf("non-admin built-in upload = %d", r.Status)
	}
	var b plugins.Plugin
	admin.must(201, "POST", "/api/admin/plugins", map[string]any{"zip": pluginZip(t, "brand", "B")}, &b)
	other.must(200, "GET", "/api/plugins", nil, &list)
	if len(list.Plugins) != 1 || !list.Plugins[0].Builtin {
		t.Fatalf("built-in not visible: %+v", list)
	}
	admin.must(200, "PATCH", "/api/admin/plugins/"+b.ID, map[string]any{"enabled": false}, &b)
	other.must(200, "GET", "/api/plugins", nil, &list)
	if len(list.Plugins) != 0 {
		t.Fatalf("disabled built-in still listed: %+v", list)
	}
	admin.must(200, "PATCH", "/api/admin/plugins/"+b.ID, map[string]any{"enabled": true}, &b)
	if r := c.do("DELETE", "/api/admin/plugins/"+b.ID, nil); r.Status != 403 {
		t.Fatalf("non-admin built-in delete = %d", r.Status)
	}

	// Roles tick plugins by name or pinned version; unknown names and versions are refused.
	var body RolesBody
	c.must(200, "GET", "/api/roles", nil, &body)
	next := append([]store.Role{}, body.Roles...)
	next[2].Plugins = []string{"tinyplug@1", "brand"}
	c.must(200, "PUT", "/api/roles", map[string]any{"roles": next}, &body)
	if got := body.Roles[2].Plugins; len(got) != 2 || got[0] != "tinyplug@1" {
		t.Fatalf("saved plugins %v", got)
	}
	for _, bad := range []string{"tinyplug@9", "ghost"} {
		next[2].Plugins = []string{bad}
		if r := c.do("PUT", "/api/roles", map[string]any{"roles": next}); r.Status != 422 {
			t.Fatalf("%s = %d", bad, r.Status)
		}
	}
	// An admin disabling a built-in must not block saving roles that tick it.
	next[2].Plugins = []string{"tinyplug@1", "brand"}
	admin.must(200, "PATCH", "/api/admin/plugins/"+b.ID, map[string]any{"enabled": false}, &b)
	c.must(200, "PUT", "/api/roles", map[string]any{"roles": next}, &body)
	admin.must(200, "PATCH", "/api/admin/plugins/"+b.ID, map[string]any{"enabled": true}, &b)
	// Another user's plugin is never tickable.
	var ob RolesBody
	other.must(200, "GET", "/api/roles", nil, &ob)
	onext := append([]store.Role{}, ob.Roles...)
	onext[2].Plugins = []string{"tinyplug"}
	if r := other.do("PUT", "/api/roles", map[string]any{"roles": onext}); r.Status != 422 {
		t.Fatalf("another user's plugin on a role = %d", r.Status)
	}
	next[0].Plugins = []string{"brand"}
	next[2].Plugins = nil
	if r := c.do("PUT", "/api/roles", map[string]any{"roles": next}); r.Status != 422 {
		t.Fatalf("coordinator with plugins = %d", r.Status)
	}

	c.must(204, "DELETE", "/api/plugins/"+p.ID, nil, nil)
	admin.must(204, "DELETE", "/api/admin/plugins/"+b.ID, nil, nil)
	c.must(200, "GET", "/api/roles", nil, &body)
	if got := body.Roles[2].Plugins; len(got) != 0 {
		t.Fatalf("deleted plugins left on the role: %v", got)
	}
}

func TestAdminSkillsAPI(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.signUp("a@example.com")
	c, _ := h.signUp("b@example.com")
	file := []skills.File{{Path: "SKILL.md", Content: "---\nname: brand\ndescription: Brand rules\n---\n"}}
	if r := c.do("POST", "/api/admin/skills", map[string]any{"files": file}); r.Status != 403 {
		t.Fatalf("non-admin = %d", r.Status)
	}
	var sk skills.Skill
	admin.must(201, "POST", "/api/admin/skills", map[string]any{"files": file}, &sk)
	var list SkillsBody
	c.must(200, "GET", "/api/skills", nil, &list)
	if len(list.Skills) != 3 || list.Skills[2].Name != "brand" || !list.Skills[2].Builtin {
		t.Fatalf("admin built-in skill not visible: %+v", list.Skills)
	}
	if r := c.do("POST", "/api/skills", map[string]any{"files": file}); r.Status != 422 {
		t.Fatalf("user upload with a built-in name = %d", r.Status)
	}
	if r := c.do("DELETE", "/api/skills/"+sk.ID, nil); r.Status != 404 {
		t.Fatalf("user deleting a built-in = %d", r.Status)
	}
	admin.must(204, "DELETE", "/api/admin/skills/"+sk.ID, nil, nil)
	c.must(200, "GET", "/api/skills", nil, &list)
	if len(list.Skills) != 2 {
		t.Fatalf("after delete %+v", list.Skills)
	}
}
