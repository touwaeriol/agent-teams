-- +goose Up
-- Model lists belong to users only (data-model.md §2.12): the platform list (at_models rows with
-- user_id NULL, kept by administrators) goes. No model a user can see today disappears:
--   1. every user gets a copy of each platform row they do not already have (same runtime and
--      model; label and order kept, placed after the user's own rows);
--   2. model ids the user's projects (coordinator_model / thread_model, Claude), roles and
--      employees (/model picks and role snapshots) name but the list lacks are added too;
--   3. per runtime, a user without a default gets the platform default of that runtime;
--   4. the copy is checked (every user has every platform row) before the platform rows are
--      deleted; a failed check aborts the migration and the transaction rolls everything back.
-- Rows are owned by a user from now on (user_id NOT NULL), one default per user and runtime.

-- 1. Copy the platform list to every user.
INSERT INTO at_models (id, user_id, runtime, model, label, sort, is_default)
SELECT 'mdl_' || substr(md5(u.id || '/' || p.runtime || '/' || p.model), 1, 26), u.id, p.runtime, p.model, p.label,
       COALESCE((SELECT max(m.sort) FROM at_models m WHERE m.user_id = u.id), -1)
         + row_number() OVER (PARTITION BY u.id ORDER BY p.sort, p.created_at, p.id),
       FALSE
  FROM at_users u
 CROSS JOIN at_models p
 WHERE p.user_id IS NULL
   AND NOT EXISTS (SELECT 1 FROM at_models m WHERE m.user_id = u.id AND m.runtime = p.runtime AND m.model = p.model);

-- 2. Model ids already in use by the user's projects, roles and employees.
WITH refs AS (
    SELECT user_id, 'claude' AS runtime, btrim(coordinator_model) AS model FROM at_projects
    UNION SELECT user_id, 'claude', btrim(thread_model) FROM at_projects
    UNION SELECT user_id, runtime, btrim(model) FROM at_roles
    UNION SELECT p.user_id, e.runtime, btrim(e.model) FROM at_employees e JOIN at_projects p ON p.id = e.project_id
    UNION SELECT p.user_id, e.runtime, btrim(COALESCE(e.spec->>'model', '')) FROM at_employees e JOIN at_projects p ON p.id = e.project_id
), missing AS (
    SELECT r.user_id, r.runtime, r.model FROM refs r
     WHERE r.model <> '' AND r.runtime IN ('claude', 'codex')
       AND EXISTS (SELECT 1 FROM at_users u WHERE u.id = r.user_id)
       AND NOT EXISTS (SELECT 1 FROM at_models m WHERE m.user_id = r.user_id AND m.runtime = r.runtime AND m.model = r.model)
)
INSERT INTO at_models (id, user_id, runtime, model, label, sort, is_default)
SELECT 'mdl_' || substr(md5(x.user_id || '/' || x.runtime || '/' || x.model), 1, 26), x.user_id, x.runtime, x.model, '',
       COALESCE((SELECT max(m.sort) FROM at_models m WHERE m.user_id = x.user_id), -1)
         + row_number() OVER (PARTITION BY x.user_id ORDER BY x.runtime, x.model),
       FALSE
  FROM missing x;

-- 3. The platform default of each runtime becomes the user's default where the user has none.
UPDATE at_models m SET is_default = TRUE
  FROM at_models p
 WHERE p.user_id IS NULL AND p.is_default
   AND m.user_id IS NOT NULL AND m.runtime = p.runtime AND m.model = p.model
   AND NOT EXISTS (SELECT 1 FROM at_models d WHERE d.user_id = m.user_id AND d.runtime = m.runtime AND d.is_default);

-- 4. Check the copy, then drop the platform rows.
-- +goose StatementBegin
DO $$
DECLARE
    missing_rows INT;
    missing_defaults INT;
BEGIN
    SELECT count(*) INTO missing_rows
      FROM at_users u CROSS JOIN at_models p
     WHERE p.user_id IS NULL
       AND NOT EXISTS (SELECT 1 FROM at_models m WHERE m.user_id = u.id AND m.runtime = p.runtime AND m.model = p.model);
    SELECT count(*) INTO missing_defaults
      FROM at_users u CROSS JOIN at_models p
     WHERE p.user_id IS NULL AND p.is_default
       AND NOT EXISTS (SELECT 1 FROM at_models m WHERE m.user_id = u.id AND m.runtime = p.runtime AND m.is_default);
    IF missing_rows > 0 OR missing_defaults > 0 THEN
        RAISE EXCEPTION '00210_user_models: platform models not copied to every user (% rows, % defaults missing)',
            missing_rows, missing_defaults;
    END IF;
END $$;
-- +goose StatementEnd

DELETE FROM at_models WHERE user_id IS NULL;

DROP INDEX IF EXISTS at_models_platform_default;
DROP INDEX IF EXISTS at_models_owner_model;
ALTER TABLE at_models ALTER COLUMN user_id SET NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS at_models_user_model ON at_models (user_id, runtime, model);
CREATE UNIQUE INDEX IF NOT EXISTS at_models_user_default ON at_models (user_id, runtime) WHERE is_default;

-- +goose Down
-- The platform list comes back as the preset (Opus 5.5, Claude default); the users keep their
-- rows (the copies included) but lose their defaults, which the old schema did not have.
DROP INDEX IF EXISTS at_models_user_default;
DROP INDEX IF EXISTS at_models_user_model;
ALTER TABLE at_models ALTER COLUMN user_id DROP NOT NULL;
UPDATE at_models SET is_default = FALSE WHERE is_default;
CREATE UNIQUE INDEX IF NOT EXISTS at_models_owner_model ON at_models (COALESCE(user_id, ''), runtime, model);
CREATE UNIQUE INDEX IF NOT EXISTS at_models_platform_default ON at_models (runtime) WHERE user_id IS NULL AND is_default;
INSERT INTO at_models (id, user_id, runtime, model, label, sort, is_default)
VALUES ('mdl_opus55', NULL, 'claude', 'claude-opus-5-5', 'Opus 5.5', 0, TRUE)
ON CONFLICT DO NOTHING;
