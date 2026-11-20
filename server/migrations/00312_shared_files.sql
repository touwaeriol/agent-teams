-- +goose Up
-- Project shared files (讨论串 #33, architecture.md §6.3 "共享文件"): files every session and user of
-- the project can store and fetch, kept by the console (never in git). One row per path, overwritten
-- in place (no versions). Limits live in server/internal/sharedfiles (20 MB per file, 500 MB and
-- 1000 files per project).
--   uploaded_by         display name of the last uploader: the employee's name (dev-1) or "user"
--   uploader_id         the employee who uploaded it (NULL for the user)
--   uploader_thread_id  the thread the employee's session worked in (NULL for the user / coordinator)
CREATE TABLE at_shared_files (
  project_id         TEXT NOT NULL REFERENCES at_projects(id) ON DELETE CASCADE,
  path               TEXT NOT NULL,
  content_type       TEXT NOT NULL,
  size               BIGINT NOT NULL,
  sha256             TEXT NOT NULL,
  data               BYTEA NOT NULL,
  uploaded_by        TEXT NOT NULL,
  uploader_id        TEXT REFERENCES at_employees(id) ON DELETE SET NULL,
  uploader_thread_id TEXT REFERENCES at_threads(id) ON DELETE SET NULL,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (project_id, path)
);

-- +goose Down
DROP TABLE at_shared_files;
