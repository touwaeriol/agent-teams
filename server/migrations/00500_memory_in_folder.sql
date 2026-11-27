-- +goose Up
-- Memory lives in the owner's shared folder (juicefs-shared-storage design): a project's files in
-- projects/<pid>/.memory/, the user's own memory (every project of the user reads it) in
-- shared/.memory/. The files are the source; these rows are the cache sessions, the web pages and a
-- folder that is not reachable read. pending = written here and not yet in the folder (all rows
-- moved over start pending, so the first sweep writes them out); file_mtime / file_size = the file
-- as last seen, to notice edits made on a node. type is the optional kind of a topic file
-- (frontmatter: project, feedback, reference, user).
ALTER TABLE at_memory_files
  ADD COLUMN type       TEXT NOT NULL DEFAULT '',
  ADD COLUMN pending    BOOLEAN NOT NULL DEFAULT false,
  ADD COLUMN file_mtime TIMESTAMPTZ,
  ADD COLUMN file_size  BIGINT NOT NULL DEFAULT 0;
UPDATE at_memory_files SET pending = true;
CREATE INDEX at_memory_files_pending ON at_memory_files (project_id) WHERE pending;

CREATE TABLE at_user_memory_files (
  user_id     TEXT NOT NULL REFERENCES at_users(id) ON DELETE CASCADE,
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  type        TEXT NOT NULL DEFAULT '',
  content     TEXT NOT NULL DEFAULT '',
  updated_by  TEXT NOT NULL DEFAULT '',
  pending     BOOLEAN NOT NULL DEFAULT false,
  file_mtime  TIMESTAMPTZ,
  file_size   BIGINT NOT NULL DEFAULT 0,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, name)
);
CREATE INDEX at_user_memory_files_pending ON at_user_memory_files (user_id) WHERE pending;

-- Who is editing a memory file on a node: an employee's tool call that names a .memory/ path
-- (scope 'project' or 'user', scope_id the project or user). The sweep credits the next change it
-- sees to them and reads the folder again while a touch is fresh.
CREATE TABLE at_memory_touches (
  scope    TEXT NOT NULL CHECK (scope IN ('project', 'user')),
  scope_id TEXT NOT NULL,
  name     TEXT NOT NULL,
  by       TEXT NOT NULL,
  at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (scope, scope_id, name)
);

-- +goose Down
DROP TABLE at_memory_touches;
DROP TABLE at_user_memory_files;
DROP INDEX at_memory_files_pending;
ALTER TABLE at_memory_files DROP COLUMN type, DROP COLUMN pending, DROP COLUMN file_mtime, DROP COLUMN file_size;
