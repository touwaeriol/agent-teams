-- +goose Up
-- Sessions (多仓库会话): a lightweight entry next to projects. A session is a project of kind
-- 'session' with one thread whose "lead" is a writing employee the user talks to directly: no
-- coordinator, no shared members, no task pool. Its repositories are the project's (all side by
-- side in the employee's directory); the branch picked for each is the thread's base there.
ALTER TABLE at_projects ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'project'
  CHECK (kind IN ('project', 'session'));

-- +goose Down
ALTER TABLE at_projects DROP COLUMN IF EXISTS kind;
