-- +goose Up
-- Thread lifecycle and image input (data-model.md §2.4, §2.11): archived threads remember the
-- status they had so "取消归档" can put them back, and images the user attaches to a message
-- are stored per project and fetched by the daemon when it delivers the message.

ALTER TABLE at_threads
  ADD COLUMN IF NOT EXISTS archived_from TEXT NOT NULL DEFAULT '';   -- status before archiving ('' = not archived)

CREATE TABLE IF NOT EXISTS at_attachments (   -- an image the user attached to a message
  id           TEXT PRIMARY KEY,
  project_id   TEXT NOT NULL REFERENCES at_projects(id) ON DELETE CASCADE,
  user_id      TEXT NOT NULL REFERENCES at_users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  content_type TEXT NOT NULL,                  -- image/png | image/jpeg | image/gif | image/webp
  size         INT  NOT NULL,
  sha256       TEXT NOT NULL,
  data         BYTEA NOT NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS at_attachments_project ON at_attachments (project_id, created_at);

-- +goose Down
DROP TABLE IF EXISTS at_attachments;
ALTER TABLE at_threads DROP COLUMN IF EXISTS archived_from;
