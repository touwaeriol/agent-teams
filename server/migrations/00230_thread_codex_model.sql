-- +goose Up
-- Project model overrides by runtime (data-model.md at_projects): coordinator_model (the
-- coordinator, always Claude) and thread_model (Claude leads and members) keep their meaning;
-- thread_codex_model is the override for Codex leads and members. '' = each role's model.
ALTER TABLE at_projects ADD COLUMN IF NOT EXISTS thread_codex_model TEXT NOT NULL DEFAULT '';

-- Leads run Claude only (roles.Validate). A Codex lead saved earlier (YAML or API) would block
-- every later save of the Team page: it becomes a Claude lead following the defaults (its model
-- is a Codex id, which a Claude session cannot run).
UPDATE at_roles SET runtime = 'claude', model = '' WHERE tier = 'lead' AND runtime = 'codex';

-- +goose Down
-- The leads switched to Claude stay Claude: the earlier runtime and model are not recorded, and a
-- Claude lead is valid in the old schema too.
ALTER TABLE at_projects DROP COLUMN IF EXISTS thread_codex_model;
