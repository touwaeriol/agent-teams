-- +goose Up
-- A role may run on several agent runtimes (Claude Code, Codex…): runtimes lists them in display
-- order (runtime stays the first one), models holds the model of each further runtime by id
-- (model stays the first runtime's). Each new session of an employee takes the runtime and node
-- with the most quota left (orchestrator.pickNode). Existing roles and the employees' role
-- snapshots get their single runtime.
ALTER TABLE at_roles
    ADD COLUMN runtimes TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN models JSONB NOT NULL DEFAULT '{}';
UPDATE at_roles SET runtimes = ARRAY[runtime];
UPDATE at_employees SET spec = spec || jsonb_build_object('runtimes', jsonb_build_array(COALESCE(spec->>'runtime', runtime)), 'models', '{}'::jsonb)
    WHERE jsonb_typeof(spec) = 'object';

-- +goose Down
UPDATE at_employees SET spec = spec - 'runtimes' - 'models' WHERE jsonb_typeof(spec) = 'object';
ALTER TABLE at_roles DROP COLUMN runtimes, DROP COLUMN models;
