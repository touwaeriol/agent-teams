-- +goose Up
-- Plugin library (data-model.md §2.9, architecture.md §6.5): Claude Code plugin packages that
-- a user uploads for themselves (user_id set) or an admin publishes as platform built-ins
-- (user_id NULL). Every upload of the same name is a new version; roles tick plugins by name
-- ("name" follows the latest version, "name@3" pins one). Skills get the same split: an
-- admin-created skill has user_id NULL and every user may tick it.

ALTER TABLE at_roles ADD COLUMN IF NOT EXISTS plugins TEXT[] NOT NULL DEFAULT '{}';

ALTER TABLE at_skills ALTER COLUMN user_id DROP NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS at_skills_builtin_name ON at_skills (name) WHERE user_id IS NULL;

CREATE TABLE IF NOT EXISTS at_plugins (
  id          TEXT PRIMARY KEY,                    -- pl_…
  user_id     TEXT REFERENCES at_users(id) ON DELETE CASCADE, -- NULL = platform built-in
  name        TEXT NOT NULL,                       -- plugin.json name
  enabled     BOOLEAN NOT NULL DEFAULT TRUE,
  latest      INT NOT NULL DEFAULT 0,              -- highest version number
  created_by  TEXT NOT NULL DEFAULT '',
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS at_plugins_user_name ON at_plugins (user_id, name) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS at_plugins_builtin_name ON at_plugins (name) WHERE user_id IS NULL;

CREATE TABLE IF NOT EXISTS at_plugin_versions (
  plugin_id        TEXT NOT NULL REFERENCES at_plugins(id) ON DELETE CASCADE,
  version          INT  NOT NULL,                  -- 1, 2, … per upload
  manifest_version TEXT NOT NULL DEFAULT '',       -- plugin.json "version", as written
  description      TEXT NOT NULL DEFAULT '',
  files            JSONB NOT NULL,                 -- [{"path", "content", "encoding"?, "executable"?}]
  contents         JSONB NOT NULL,                 -- {"skills", "commands", "agents", "hooks", "mcp_servers"}
  size             INT  NOT NULL,                  -- decompressed bytes
  sha256           TEXT NOT NULL,
  created_by       TEXT NOT NULL DEFAULT '',
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (plugin_id, version)
);

-- +goose Down
DROP TABLE IF EXISTS at_plugin_versions;
DROP TABLE IF EXISTS at_plugins;
DROP INDEX IF EXISTS at_skills_builtin_name;
DELETE FROM at_skills WHERE user_id IS NULL;
ALTER TABLE at_skills ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE at_roles DROP COLUMN IF EXISTS plugins;
