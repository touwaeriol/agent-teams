package workflows

import (
	"context"
	"encoding/json"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

const cols = `id, name, description, steps, edges, is_default, created_at, updated_at`

// List returns the user's flows, the default first, then by name.
func List(ctx context.Context, db store.DB, userID string) ([]Flow, error) {
	rows, err := store.Query(ctx, db, `SELECT `+cols+` FROM at_workflows WHERE user_id = $1
		ORDER BY is_default DESC, name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Flow{}
	for rows.Next() {
		w, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

type scanner interface{ Scan(dest ...any) error }

func scan(r scanner) (Flow, error) {
	var w Flow
	var steps, edges []byte
	if err := r.Scan(&w.ID, &w.Name, &w.Description, &steps, &edges, &w.IsDefault, &w.CreatedAt, &w.UpdatedAt); err != nil {
		return w, err
	}
	if err := json.Unmarshal(steps, &w.Steps); err != nil {
		return w, err
	}
	return w, json.Unmarshal(edges, &w.Edges)
}

// Get returns one of the user's flows.
func Get(ctx context.Context, db store.DB, userID, id string) (Flow, error) {
	w, err := scan(store.QueryRow(ctx, db, `SELECT `+cols+` FROM at_workflows WHERE user_id = $1 AND id = $2`, userID, id))
	if store.IsNoRows(err) {
		return w, ErrNotFound
	}
	return w, err
}

// Create stores a validated draft as a new flow. Making it the default clears the old default;
// the user's first flow is the default even when not asked.
func Create(ctx context.Context, db store.DB, userID string, d FlowDraft) (Flow, error) {
	var n int
	if err := store.QueryRow(ctx, db, `SELECT count(*) FROM at_workflows WHERE user_id = $1`, userID).Scan(&n); err != nil {
		return Flow{}, err
	}
	if n >= MaxPerUser {
		return Flow{}, problem("最多 %d 个流程，先删掉不用的", MaxPerUser)
	}
	d.IsDefault = d.IsDefault || n == 0
	if err := clearDefault(ctx, db, userID, d.IsDefault); err != nil {
		return Flow{}, err
	}
	steps, edges := encode(d)
	w, err := scan(store.QueryRow(ctx, db, `INSERT INTO at_workflows (id, user_id, name, description, steps, edges, is_default)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+cols,
		store.NewID("wf"), userID, d.Name, d.Description, steps, edges, d.IsDefault))
	return w, uniqueName(err, d.Name)
}

// Update replaces a flow's content. Unsetting the default leaves the user without one.
func Update(ctx context.Context, db store.DB, userID, id string, d FlowDraft) (Flow, error) {
	if _, err := Get(ctx, db, userID, id); err != nil {
		return Flow{}, err
	}
	if err := clearDefault(ctx, db, userID, d.IsDefault); err != nil {
		return Flow{}, err
	}
	steps, edges := encode(d)
	w, err := scan(store.QueryRow(ctx, db, `UPDATE at_workflows SET name = $3, description = $4, steps = $5, edges = $6,
		is_default = $7, updated_at = now() WHERE user_id = $1 AND id = $2 RETURNING `+cols,
		userID, id, d.Name, d.Description, steps, edges, d.IsDefault))
	if store.IsNoRows(err) {
		return w, ErrNotFound
	}
	return w, uniqueName(err, d.Name)
}

func clearDefault(ctx context.Context, db store.DB, userID string, makingDefault bool) error {
	if !makingDefault {
		return nil
	}
	_, err := store.Exec(ctx, db, `UPDATE at_workflows SET is_default = FALSE WHERE user_id = $1 AND is_default`, userID)
	return err
}

func encode(d FlowDraft) ([]byte, []byte) {
	steps, _ := json.Marshal(d.Steps)
	edges, _ := json.Marshal(d.Edges)
	return steps, edges
}

func uniqueName(err error, name string) error {
	if store.IsUnique(err) {
		return problem("已经有叫 %s 的流程了", name)
	}
	return err
}

// Delete removes a flow; projects that picked it fall back to the user's default.
func Delete(ctx context.Context, db store.DB, userID, id string) error {
	tag, err := store.Exec(ctx, db, `DELETE FROM at_workflows WHERE user_id = $1 AND id = $2`, userID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	_, err = store.Exec(ctx, db, `UPDATE at_projects SET workflow = '' WHERE user_id = $1 AND workflow = $2`, userID, id)
	return err
}

// ForProject is the flow a project's leads follow: the picked one, else the user's default;
// ok is false for none (the project chose none, or the user has no default).
func ForProject(ctx context.Context, db store.DB, userID, pick string) (Flow, bool, error) {
	if pick == ProjectNone {
		return Flow{}, false, nil
	}
	where, arg := `is_default`, any(nil)
	if pick != ProjectDefault {
		where, arg = `id = $2`, pick
	}
	args := []any{userID}
	if arg != nil {
		args = append(args, arg)
	}
	w, err := scan(store.QueryRow(ctx, db, `SELECT `+cols+` FROM at_workflows WHERE user_id = $1 AND `+where, args...))
	if store.IsNoRows(err) {
		return Flow{}, false, nil
	}
	return w, err == nil, err
}

// LeadSection is the flow section of a thread lead's system prompt ("" without a flow).
func LeadSection(ctx context.Context, db store.DB, userID, pick string) (string, error) {
	w, ok, err := ForProject(ctx, db, userID, pick)
	if err != nil || !ok {
		return "", err
	}
	rows, err := store.Query(ctx, db, `SELECT role, title FROM at_roles WHERE user_id = $1 AND tier = 'member'`, userID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	titles := map[string]string{}
	for rows.Next() {
		var role, title string
		if err := rows.Scan(&role, &title); err != nil {
			return "", err
		}
		titles[role] = title
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return Render(w, titles), nil
}
