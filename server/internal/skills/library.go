package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// List returns the built-in skills followed by the user's uploaded ones.
func List(ctx context.Context, db store.DB, userID string) ([]Skill, error) {
	rows, err := db.Query(ctx, `SELECT id, name, description, size FROM at_skills WHERE user_id = $1 ORDER BY name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := Builtin()
	for rows.Next() {
		var s Skill
		if err := rows.Scan(&s.ID, &s.Name, &s.Description, &s.Size); err != nil {
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

// Create validates and stores an uploaded skill.
func Create(ctx context.Context, db store.DB, userID string, files []File) (Skill, error) {
	s, err := Parse(files)
	if err != nil {
		return Skill{}, err
	}
	if _, ok := builtin(s.Name); ok {
		return Skill{}, problem("%s 是内置技能的名字，请在 SKILL.md 里换一个 name", s.Name)
	}
	raw, _ := json.Marshal(s.Files)
	s.ID = store.NewID("sk")
	_, err = db.Exec(ctx, `INSERT INTO at_skills (id, user_id, name, description, files, size) VALUES ($1,$2,$3,$4,$5,$6)`,
		s.ID, userID, s.Name, s.Description, raw, s.Size)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return Skill{}, problem("已经有一个叫 %s 的技能，先删除它再上传", s.Name)
	}
	return s, err
}

// Delete removes an uploaded skill and takes it off the user's roles.
func Delete(ctx context.Context, db store.DB, userID, id string) error {
	var name string
	err := db.QueryRow(ctx, `DELETE FROM at_skills WHERE id = $1 AND user_id = $2 RETURNING name`, id, userID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `UPDATE at_roles SET skills = array_remove(skills, $2), updated_at = now()
		WHERE user_id = $1 AND $2 = ANY(skills)`, userID, name)
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
	err := db.QueryRow(ctx, `SELECT id, description, files FROM at_skills WHERE user_id = $1 AND name = $2`, userID, name).
		Scan(&s.ID, &s.Description, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
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
