-- +goose Up
-- A third agent runtime: antigravity (Google Antigravity CLI driven over ACP, thread #41). Only
-- at_roles still limits runtime with a CHECK (00010; at_models with its own CHECK is gone since
-- 00320); at_employees, at_agent_sessions and at_roles.runtimes take any id.
ALTER TABLE at_roles DROP CONSTRAINT IF EXISTS at_roles_runtime_check;
ALTER TABLE at_roles ADD CONSTRAINT at_roles_runtime_check CHECK (runtime IN ('claude', 'codex', 'antigravity'));

-- +goose Down
-- Roles that run on Antigravity lose it: it leaves runtimes and models; a role whose first runtime
-- it was takes the next one (Claude when none is left) with that runtime's model.
UPDATE at_roles
   SET runtimes = COALESCE(NULLIF(array_remove(runtimes, 'antigravity'), '{}'), ARRAY['claude']),
       models = models - 'antigravity'
 WHERE 'antigravity' = ANY(runtimes) OR runtime = 'antigravity';
UPDATE at_roles
   SET runtime = runtimes[1], model = COALESCE(models->>runtimes[1], ''), models = models - runtimes[1]
 WHERE runtime = 'antigravity';
ALTER TABLE at_roles DROP CONSTRAINT IF EXISTS at_roles_runtime_check;
ALTER TABLE at_roles ADD CONSTRAINT at_roles_runtime_check CHECK (runtime IN ('claude', 'codex'));
