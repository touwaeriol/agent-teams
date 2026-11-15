package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// ErrNotFound is returned for a plugin that does not exist or is not the caller's.
var ErrNotFound = errors.New("plugin not found")

// KeepVersions is how many recent versions an upload keeps; older ones go unless a role pins them.
const KeepVersions = 20

// Summary is what a version provides, as the API shows it (MCP servers by name only).
type Summary struct {
	Skills     []string `json:"skills"`
	Commands   []string `json:"commands"`
	Agents     []string `json:"agents"`
	Hooks      bool     `json:"hooks"`
	MCPServers []string `json:"mcp_servers"`
}

// Version is one uploaded version of a plugin.
type Version struct {
	Version         int       `json:"version"`
	ManifestVersion string    `json:"manifest_version" doc:"plugin.json version, as written"`
	Description     string    `json:"description"`
	Size            int       `json:"size"`
	SHA256          string    `json:"sha256"`
	Contents        Summary   `json:"contents"`
	CreatedAt       time.Time `json:"created_at"`
}

// Plugin is a library entry with its versions, newest first.
type Plugin struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Builtin  bool      `json:"builtin" doc:"Platform built-in (created by an admin, every user may tick it)"`
	Enabled  bool      `json:"enabled"`
	Latest   int       `json:"latest"`
	Versions []Version `json:"versions"`
}

// Owner scopes library writes: a user's own plugins, or the platform built-ins (UserID "").
type Owner struct{ UserID string }

// Builtins is the owner of platform built-in plugins.
var Builtins = Owner{}

func (o Owner) arg() any {
	if o.UserID == "" {
		return nil
	}
	return o.UserID
}

// Summarize drops MCP server details (commands, headers) from Contents.
func Summarize(c Contents) Summary {
	s := Summary{Skills: nonNil(c.Skills), Commands: nonNil(c.Commands), Agents: nonNil(c.Agents), Hooks: c.Hooks, MCPServers: []string{}}
	for name := range c.MCPServers {
		s.MCPServers = append(s.MCPServers, name)
	}
	sort.Strings(s.MCPServers)
	return s
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// List returns the user's plugins, then the enabled built-ins whose name the user does not use.
func List(ctx context.Context, db store.DB, userID string) ([]Plugin, error) {
	return query(ctx, db, `SELECT id, name, user_id IS NULL, enabled, latest FROM at_plugins p
		WHERE user_id = $1 OR (user_id IS NULL AND enabled
			AND NOT EXISTS (SELECT 1 FROM at_plugins o WHERE o.user_id = $1 AND o.name = p.name))
		ORDER BY user_id IS NULL, name`, userID)
}

// ListBuiltin returns every platform built-in, disabled ones included (admin page).
func ListBuiltin(ctx context.Context, db store.DB) ([]Plugin, error) {
	return query(ctx, db, `SELECT id, name, TRUE, enabled, latest FROM at_plugins WHERE user_id IS NULL ORDER BY name`)
}

func query(ctx context.Context, db store.DB, sql string, args ...any) ([]Plugin, error) {
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	out := []Plugin{}
	for rows.Next() {
		var p Plugin
		if err := rows.Scan(&p.ID, &p.Name, &p.Builtin, &p.Enabled, &p.Latest); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Versions, err = versions(ctx, db, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func versions(ctx context.Context, db store.DB, id string) ([]Version, error) {
	rows, err := db.Query(ctx, `SELECT version, manifest_version, description, size, sha256, contents, created_at
		FROM at_plugin_versions WHERE plugin_id = $1 ORDER BY version DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Version{}
	for rows.Next() {
		var v Version
		var raw []byte
		if err := rows.Scan(&v.Version, &v.ManifestVersion, &v.Description, &v.Size, &v.SHA256, &raw, &v.CreatedAt); err != nil {
			return nil, err
		}
		var c Contents
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		v.Contents = Summarize(c)
		out = append(out, v)
	}
	return out, rows.Err()
}

// Upload validates a package and stores it as the next version of its name for the owner.
func Upload(ctx context.Context, db store.DB, owner Owner, by string, files []File) (Plugin, error) {
	pkg, err := Parse(files)
	if err != nil {
		return Plugin{}, err
	}
	if err := checkName(ctx, db, owner, pkg.Name); err != nil {
		return Plugin{}, err
	}
	id, version, err := insertVersion(ctx, db, owner, by, pkg)
	if err != nil {
		return Plugin{}, err
	}
	if err := prune(ctx, db, owner, id, pkg.Name, version); err != nil {
		return Plugin{}, err
	}
	return Get(ctx, db, owner, id)
}

// checkName keeps a user's plugin from taking a built-in's name (built-ins may shadow nothing).
func checkName(ctx context.Context, db store.DB, owner Owner, name string) error {
	if owner.UserID == "" {
		return nil
	}
	var taken bool
	err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM at_plugins WHERE user_id IS NULL AND name = $1)`, name).Scan(&taken)
	if err == nil && taken {
		return problem("%s 是内置插件的名字，请在 plugin.json 里换一个 name", name)
	}
	return err
}

// insertVersion bumps the plugin's latest (creating the plugin on first upload) and stores the
// version in one statement, so concurrent uploads get distinct numbers.
func insertVersion(ctx context.Context, db store.DB, owner Owner, by string, pkg Package) (string, int, error) {
	conflict := `(user_id, name) WHERE user_id IS NOT NULL`
	if owner.UserID == "" {
		conflict = `(name) WHERE user_id IS NULL`
	}
	files, _ := json.Marshal(pkg.Files)
	contents, _ := json.Marshal(pkg.Contents)
	var id string
	var version int
	err := db.QueryRow(ctx, `WITH p AS (
			INSERT INTO at_plugins (id, user_id, name, latest, created_by) VALUES ($1, $2, $3, 1, $4)
			ON CONFLICT `+conflict+` DO UPDATE SET latest = at_plugins.latest + 1, updated_at = now()
			RETURNING id, latest)
		INSERT INTO at_plugin_versions (plugin_id, version, manifest_version, description, files, contents, size, sha256, created_by)
		SELECT id, latest, $5, $6, $7, $8, $9, $10, $4 FROM p RETURNING plugin_id, version`,
		store.NewID("pl"), owner.arg(), pkg.Name, by, pkg.ManifestVersion, pkg.Description, files, contents, pkg.Size, pkg.SHA256).
		Scan(&id, &version)
	return id, version, err
}

// prune drops versions older than the last KeepVersions that no role pins.
func prune(ctx context.Context, db store.DB, owner Owner, id, name string, latest int) error {
	_, err := db.Exec(ctx, `DELETE FROM at_plugin_versions v WHERE plugin_id = $1 AND version <= $2
		AND NOT EXISTS (SELECT 1 FROM at_roles r WHERE ($3::text IS NULL OR r.user_id = $3)
			AND $4 || '@' || v.version = ANY(r.plugins))`, id, latest-KeepVersions, owner.arg(), name)
	return err
}

// Get returns one of the owner's plugins.
func Get(ctx context.Context, db store.DB, owner Owner, id string) (Plugin, error) {
	list, err := query(ctx, db, `SELECT id, name, user_id IS NULL, enabled, latest FROM at_plugins
		WHERE id = $1 AND user_id IS NOT DISTINCT FROM $2`, id, owner.arg())
	if err != nil {
		return Plugin{}, err
	}
	if len(list) == 0 {
		return Plugin{}, ErrNotFound
	}
	return list[0], nil
}

// SetEnabled turns one of the owner's plugins on or off; sessions skip disabled plugins.
func SetEnabled(ctx context.Context, db store.DB, owner Owner, id string, on bool) (Plugin, error) {
	tag, err := db.Exec(ctx, `UPDATE at_plugins SET enabled = $3, updated_at = now()
		WHERE id = $1 AND user_id IS NOT DISTINCT FROM $2`, id, owner.arg(), on)
	if err != nil {
		return Plugin{}, err
	}
	if tag.RowsAffected() == 0 {
		return Plugin{}, ErrNotFound
	}
	return Get(ctx, db, owner, id)
}

// Delete removes a plugin with all versions and takes it off the roles that used it: the
// owner's roles, or for a built-in every role whose user has no own plugin of that name.
func Delete(ctx context.Context, db store.DB, owner Owner, id string) (string, error) {
	var name string
	err := db.QueryRow(ctx, `DELETE FROM at_plugins WHERE id = $1 AND user_id IS NOT DISTINCT FROM $2 RETURNING name`,
		id, owner.arg()).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	_, err = db.Exec(ctx, `UPDATE at_roles r SET plugins = ARRAY(SELECT x FROM unnest(r.plugins) x WHERE split_part(x, '@', 1) <> $2),
			updated_at = now()
		WHERE ($1::text IS NULL OR r.user_id = $1)
			AND EXISTS (SELECT 1 FROM unnest(r.plugins) x WHERE split_part(x, '@', 1) = $2)
			AND ($1::text IS NOT NULL OR NOT EXISTS (SELECT 1 FROM at_plugins o WHERE o.user_id = r.user_id AND o.name = $2))`,
		owner.arg(), name)
	return name, err
}
