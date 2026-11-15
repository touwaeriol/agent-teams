-- +goose Up
-- Permission modes per role (architecture.md §9, data-model.md §2.3): skip_permissions becomes
-- permission_mode auto | bypass | ask. Existing roles move to auto, skip_permissions roles to
-- bypass. Employees keep a copy of their role in at_employees.spec, which is rewritten the same way.

ALTER TABLE at_roles
  ADD COLUMN IF NOT EXISTS permission_mode TEXT NOT NULL DEFAULT 'auto';
UPDATE at_roles SET permission_mode = CASE WHEN skip_permissions THEN 'bypass' ELSE 'auto' END;
ALTER TABLE at_roles DROP COLUMN IF EXISTS skip_permissions;   -- also drops CHECK (NOT (read_only AND skip_permissions))
ALTER TABLE at_roles
  ADD CONSTRAINT at_roles_permission_mode CHECK (permission_mode IN ('auto','bypass','ask')),
  ADD CONSTRAINT at_roles_read_only_bypass CHECK (NOT (read_only AND permission_mode = 'bypass'));

UPDATE at_employees
   SET spec = (spec - 'skip_permissions') || jsonb_build_object('permission_mode',
       CASE WHEN COALESCE((spec->>'skip_permissions')::boolean, false) THEN 'bypass' ELSE 'auto' END)
 WHERE NOT spec ? 'permission_mode';

-- +goose Down
UPDATE at_employees
   SET spec = (spec - 'permission_mode') || jsonb_build_object('skip_permissions', spec->>'permission_mode' = 'bypass')
 WHERE spec ? 'permission_mode';
ALTER TABLE at_roles DROP CONSTRAINT IF EXISTS at_roles_read_only_bypass;
ALTER TABLE at_roles DROP CONSTRAINT IF EXISTS at_roles_permission_mode;
ALTER TABLE at_roles ADD COLUMN IF NOT EXISTS skip_permissions BOOLEAN NOT NULL DEFAULT FALSE;
UPDATE at_roles SET skip_permissions = (permission_mode = 'bypass');
ALTER TABLE at_roles ADD CHECK (NOT (read_only AND skip_permissions));
ALTER TABLE at_roles DROP COLUMN IF EXISTS permission_mode;
