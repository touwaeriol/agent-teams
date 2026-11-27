-- +goose Up
-- Folders to remove from a user's shared folder whose removal failed (a deleted project's
-- projects/<pid> when the volume could not be reached): the shared-folder sweep retries them.
CREATE TABLE at_fs_removals (
  user_id    TEXT NOT NULL REFERENCES at_users(id) ON DELETE CASCADE,
  path       TEXT NOT NULL,
  attempts   INT NOT NULL DEFAULT 0,
  error      TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, path)
);

-- +goose Down
DROP TABLE at_fs_removals;
