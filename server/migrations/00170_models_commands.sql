-- +goose Up
-- Model lists and composer commands (data-model.md §2.12).
-- at_models: the models /model and the role model field offer, per runtime. user_id NULL rows
-- are the platform list the administrators keep (order, one default per runtime: what a session
-- opens with when nothing else names a model); a user's own rows add model ids for that user.
-- at_roles.hidden_commands: platform commands (/model, /status…) the role's chats leave out;
-- at_roles.commands: the role's custom commands [{name, description, mode, prompt}] (mode
-- prompt: expanded server-side from the template; passthrough: sent to Claude Code as typed).
-- at_employees.model: a model picked with /model for this employee (thread lead or members),
-- ahead of the project and role models.
CREATE TABLE IF NOT EXISTS at_models (
    id          TEXT PRIMARY KEY,
    user_id     TEXT REFERENCES at_users(id) ON DELETE CASCADE,
    runtime     TEXT NOT NULL CHECK (runtime IN ('claude', 'codex')),
    model       TEXT NOT NULL,
    label       TEXT NOT NULL DEFAULT '',
    sort        INT  NOT NULL DEFAULT 0,
    is_default  BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS at_models_owner_model ON at_models (COALESCE(user_id, ''), runtime, model);
CREATE UNIQUE INDEX IF NOT EXISTS at_models_platform_default ON at_models (runtime) WHERE user_id IS NULL AND is_default;
INSERT INTO at_models (id, user_id, runtime, model, label, sort, is_default)
VALUES ('mdl_opus55', NULL, 'claude', 'claude-opus-5-5', 'Opus 5.5', 0, TRUE)
ON CONFLICT DO NOTHING;

ALTER TABLE at_roles ADD COLUMN IF NOT EXISTS hidden_commands TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE at_roles ADD COLUMN IF NOT EXISTS commands JSONB NOT NULL DEFAULT '[]';
ALTER TABLE at_employees ADD COLUMN IF NOT EXISTS model TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE at_employees DROP COLUMN IF EXISTS model;
ALTER TABLE at_roles DROP COLUMN IF EXISTS commands;
ALTER TABLE at_roles DROP COLUMN IF EXISTS hidden_commands;
DROP TABLE IF EXISTS at_models;
