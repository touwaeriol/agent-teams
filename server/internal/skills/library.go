package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// ErrNotFound is returned for a skill that does not exist or is not the user's.
var ErrNotFound = errors.New("skill not found")

// MaxPerRole caps how many skills one role may load; MaxBundleBytes caps their total
// size so session.open stays well under the 4 MiB frame limit.
const (
	MaxPerRole     = 16
	MaxBundleBytes = 2 << 20
)

// List returns the shipped built-in skills, the admin-created built-ins (user_id NULL) whose
// name the user does not use, and the user's own uploads.
func List(ctx context.Context, db store.DB, userID string) ([]Skill, error) {
	rows, err := store.Query(ctx, db, `SELECT id, name, description, size, user_id IS NULL FROM at_skills s
		WHERE user_id = $1 OR (user_id IS NULL
			AND NOT EXISTS (SELECT 1 FROM at_skills o WHERE o.user_id = $1 AND o.name = s.name))
		ORDER BY user_id IS NOT NULL, name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := Builtin()
	for rows.Next() {
		var s Skill
		if err := rows.Scan(&s.ID, &s.Name, &s.Description, &s.Size, &s.Builtin); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Names is the set of skill names a user's roles may use.
func Names(ctx context.Context, db store.DB, userID string) (map[string]bool, error) {
	list, err := List(ctx, db, userID)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, s := range list {
		out[s.Name] = true
	}
	return out, nil
}

// Create validates and stores an uploaded skill: the user's own, or a platform built-in
// every user may tick when userID is "" (admins only).
func Create(ctx context.Context, db store.DB, userID string, files []File) (Skill, error) {
	s, err := Parse(files)
	if err != nil {
		return Skill{}, err
	}
	if err := checkName(ctx, db, userID, s.Name); err != nil {
		return Skill{}, err
	}
	raw, _ := json.Marshal(s.Files)
	s.ID, s.Builtin = store.NewID("sk"), userID == ""
	_, err = store.Exec(ctx, db, `INSERT INTO at_skills (id, user_id, name, description, files, size) VALUES ($1,$2,$3,$4,$5,$6)`,
		s.ID, ownerArg(userID), s.Name, s.Description, raw, s.Size)
	if store.IsUnique(err) {
		return Skill{}, problem("已经有一个叫 %s 的技能，先删除它再上传", s.Name)
	}
	return s, err
}

// checkName keeps uploads off the names of shipped built-ins, and user uploads off the
// names of admin built-ins.
func checkName(ctx context.Context, db store.DB, userID, name string) error {
	if _, ok := builtin(name); ok {
		return problem("%s 是内置技能的名字，请在 SKILL.md 里换一个 name", name)
	}
	if userID == "" {
		return nil
	}
	var taken bool
	err := store.QueryRow(ctx, db, `SELECT EXISTS (SELECT 1 FROM at_skills WHERE user_id IS NULL AND name = $1)`, name).Scan(&taken)
	if err == nil && taken {
		return problem("%s 是内置技能的名字，请在 SKILL.md 里换一个 name", name)
	}
	return err
}

func ownerArg(userID string) any {
	if userID == "" {
		return nil
	}
	return userID
}

// Delete removes an uploaded skill and takes it off the roles that used it: the user's own
// roles, or for an admin built-in (userID "") every role whose user has no own skill of that name.
func Delete(ctx context.Context, db store.DB, userID, id string) error {
	var name string
	err := store.QueryRow(ctx, db, `DELETE FROM at_skills WHERE id = $1 AND user_id IS NOT DISTINCT FROM $2 RETURNING name`,
		id, ownerArg(userID)).Scan(&name)
	if store.IsNoRows(err) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	_, err = store.Exec(ctx, db, `UPDATE at_roles r SET skills = array_remove(r.skills, $2), updated_at = now()
		WHERE ($1::text IS NULL OR r.user_id = $1) AND $2 = ANY(r.skills)
			AND ($1::text IS NOT NULL OR NOT EXISTS (SELECT 1 FROM at_skills o WHERE o.user_id = r.user_id AND o.name = $2))`,
		ownerArg(userID), name)
	return err
}

// Bundles loads the named skills for session.open; unknown names (deleted since) are skipped.
func Bundles(ctx context.Context, db store.DB, userID string, names []string) ([]protocol.SkillBundle, error) {
	out := []protocol.SkillBundle{}
	total := 0
	for _, name := range dedupe(names) {
		s, err := load(ctx, db, userID, name)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if total += sizeOf(s.Files); total > MaxBundleBytes || len(out) == MaxPerRole {
			break
		}
		out = append(out, bundle(s))
	}
	return out, nil
}

func load(ctx context.Context, db store.DB, userID, name string) (Skill, error) {
	if s, ok := builtin(name); ok {
		return s, nil
	}
	s := Skill{Name: name}
	var raw []byte
	err := store.QueryRow(ctx, db, `SELECT id, description, files, user_id IS NULL FROM at_skills
		WHERE name = $2 AND (user_id = $1 OR user_id IS NULL) ORDER BY user_id IS NULL LIMIT 1`, userID, name).
		Scan(&s.ID, &s.Description, &raw, &s.Builtin)
	if store.IsNoRows(err) {
		return s, ErrNotFound
	}
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(raw, &s.Files)
}

func bundle(s Skill) protocol.SkillBundle {
	b := protocol.SkillBundle{Name: s.Name, Files: make([]protocol.SkillFile, len(s.Files))}
	for i, f := range s.Files {
		b.Files[i] = protocol.SkillFile{Path: f.Path, Content: f.Content}
	}
	return b
}

// Instructions renders skills as text for runtimes without skill loading (Codex): each
// SKILL.md in full, plus the list of its other files.
func Instructions(bundles []protocol.SkillBundle) string {
	if len(bundles) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n# Skills\n\nFollow the matching skill when a task calls for it.\n")
	for _, s := range bundles {
		fmt.Fprintf(&b, "\n## Skill: %s\n\n", s.Name)
		for _, f := range s.Files {
			if f.Path == EntryFile {
				b.WriteString(strings.TrimSpace(f.Content))
				b.WriteString("\n")
			}
		}
		for _, f := range s.Files {
			if f.Path != EntryFile {
				fmt.Fprintf(&b, "\n### %s/%s\n\n%s\n", s.Name, f.Path, strings.TrimSpace(f.Content))
			}
		}
	}
	return b.String()
}

func sizeOf(files []File) int {
	n := 0
	for _, f := range files {
		n += len(f.Content)
	}
	return n
}

func dedupe(names []string) []string {
	out := []string{}
	for _, n := range names {
		if n != "" && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}
