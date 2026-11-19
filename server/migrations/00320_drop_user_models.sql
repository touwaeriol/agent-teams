-- +goose Up
-- The user model list (at_models) goes: every role (coordinator and lead too) names its model by
-- hand, and a session runs (orchestrator.Model)
--   member:      /model (at_employees.model) → project (thread_model / thread_codex_model) → role → built in
--   lead:        project (thread_model / thread_codex_model) → role → built in
--   coordinator: project (coordinator_model) → role → built in
-- with the built-in default claude-opus-5-5 for Claude and '' (the Codex CLI's own) for Codex.
-- This also lifts 00310: the coordinator's and leads' role models and the project's
-- coordinator_model are settings again.
--
-- Before, a role without a model followed the owner's default of its runtime (at_models
-- is_default; for the coordinator and leads that default was all they ran). So that those roles
-- keep running the same model, the owner's default is written into every role whose model is ''
-- (templates and project copies, every tier) and into every employee's copy of its role
-- (at_employees.spec->>'model' empty or missing), matching the runtime. A Claude default equal
-- to the built-in claude-opus-5-5 is not written: '' already ends there, and the role keeps
-- following the built-in default. A Codex default is always written (the Codex built-in is '').
UPDATE at_roles r SET model = m.model
  FROM at_models m
 WHERE m.user_id = r.user_id AND m.runtime = r.runtime AND m.is_default
   AND r.model = ''
   AND NOT (m.runtime = 'claude' AND m.model = 'claude-opus-5-5');

UPDATE at_employees e SET spec = jsonb_set(e.spec, '{model}', to_jsonb(m.model))
  FROM at_projects p, at_models m
 WHERE p.id = e.project_id
   AND m.user_id = p.user_id AND m.runtime = e.runtime AND m.is_default
   AND jsonb_typeof(e.spec) = 'object' AND COALESCE(e.spec->>'model', '') = ''
   AND NOT (m.runtime = 'claude' AND m.model = 'claude-opus-5-5');

DROP TABLE IF EXISTS at_models;

-- +goose Down
-- The table comes back empty, as 00210 left its structure (rows owned by users, one default per
-- user and runtime); a user's next read of the old API seeds the starter list. The backfilled role
-- and employee models are not undone: they name the same model the old defaults gave.
CREATE TABLE IF NOT EXISTS at_models (
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL REFERENCES at_users(id) ON DELETE CASCADE,
    runtime     TEXT NOT NULL CHECK (runtime IN ('claude', 'codex')),
    model       TEXT NOT NULL,
    label       TEXT NOT NULL DEFAULT '',
    sort        INT  NOT NULL DEFAULT 0,
    is_default  BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS at_models_user_model ON at_models (user_id, runtime, model);
CREATE UNIQUE INDEX IF NOT EXISTS at_models_user_default ON at_models (user_id, runtime) WHERE is_default;
