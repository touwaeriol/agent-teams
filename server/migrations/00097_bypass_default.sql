-- +goose Up
-- Permission modes become bypass (default) | ask (architecture.md §9, data-model.md §2.3):
-- auto is gone, auto roles become bypass, and bypass applies on any node, read-only roles
-- included (their mode is ignored, read-only is enforced by the deny hook). Employees keep a
-- copy of their role in at_employees.spec, which is rewritten the same way.

ALTER TABLE at_roles DROP CONSTRAINT IF EXISTS at_roles_read_only_bypass;
ALTER TABLE at_roles DROP CONSTRAINT IF EXISTS at_roles_permission_mode;
UPDATE at_roles SET permission_mode = 'bypass' WHERE permission_mode NOT IN ('bypass','ask');
ALTER TABLE at_roles ALTER COLUMN permission_mode SET DEFAULT 'bypass';
ALTER TABLE at_roles ADD CONSTRAINT at_roles_permission_mode CHECK (permission_mode IN ('bypass','ask'));

UPDATE at_employees
   SET spec = jsonb_set(spec, '{permission_mode}', '"bypass"')
 WHERE COALESCE(spec->>'permission_mode', 'auto') NOT IN ('bypass','ask');

-- +goose Down
-- bypass goes back to auto (the old default; bypass used to need an isolated node), ask stays.
ALTER TABLE at_roles DROP CONSTRAINT IF EXISTS at_roles_permission_mode;
UPDATE at_roles SET permission_mode = 'auto' WHERE permission_mode = 'bypass';
ALTER TABLE at_roles ALTER COLUMN permission_mode SET DEFAULT 'auto';
ALTER TABLE at_roles
  ADD CONSTRAINT at_roles_permission_mode CHECK (permission_mode IN ('auto','bypass','ask')),
  ADD CONSTRAINT at_roles_read_only_bypass CHECK (NOT (read_only AND permission_mode = 'bypass'));
UPDATE at_employees
   SET spec = jsonb_set(spec, '{permission_mode}', '"auto"')
 WHERE spec->>'permission_mode' = 'bypass';
