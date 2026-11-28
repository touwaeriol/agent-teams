-- +goose Up
-- Workflows (流程图, docs/v2/workflows.md): a user's flow templates — steps (a member role each,
-- with instructions and a kind) and the edges between them (next, or back on rejection). The
-- flow is a reference for thread leads: it is written into their system prompt and the lead
-- splits tasks by it; the platform neither expands nor blocks tasks.
--   steps   [{"id", "name", "role", "kind": work|review|decision|approval, "instructions", "x", "y"}]
--   edges   [{"from", "to", "kind": next|branch|reject, "label"}]
--   is_default  the flow of the user's projects that pick none (at most one per user)
-- at_projects.workflows: the flows its leads follow, any number (the lead picks the one that
-- fits each task): {} = the user's default flow, {none} = no flow, else workflow ids in order.
CREATE TABLE IF NOT EXISTS at_workflows (
  id          TEXT PRIMARY KEY,                    -- wf_…
  user_id     TEXT NOT NULL REFERENCES at_users(id) ON DELETE CASCADE,
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  steps       JSONB NOT NULL DEFAULT '[]',
  edges       JSONB NOT NULL DEFAULT '[]',
  is_default  BOOLEAN NOT NULL DEFAULT FALSE,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (user_id, name)
);
CREATE UNIQUE INDEX IF NOT EXISTS at_workflows_default ON at_workflows (user_id) WHERE is_default;

ALTER TABLE at_projects ADD COLUMN IF NOT EXISTS workflows TEXT[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE at_projects DROP COLUMN IF EXISTS workflows;
DROP TABLE IF EXISTS at_workflows;
