-- +goose Up
-- Design capability (data-model.md §2.4, §2.8): skills per role, user-uploaded skills,
-- files published with publish_file (versioned), their upload chunks, and comments.

ALTER TABLE at_roles ADD COLUMN IF NOT EXISTS skills TEXT[] NOT NULL DEFAULT '{}';

CREATE TABLE IF NOT EXISTS at_skills (
  id          TEXT PRIMARY KEY,                    -- sk_…
  user_id     TEXT NOT NULL REFERENCES at_users(id) ON DELETE CASCADE,
  name        TEXT NOT NULL,                       -- SKILL.md name; never a built-in name
  description TEXT NOT NULL DEFAULT '',
  files       JSONB NOT NULL,                      -- [{"path": "SKILL.md", "content": "..."}], text only
  size        INT  NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (user_id, name)
);

CREATE TABLE IF NOT EXISTS at_deliverable_files (
  deliverable_id TEXT NOT NULL REFERENCES at_deliverables(id) ON DELETE CASCADE,
  version        INT  NOT NULL,
  path           TEXT NOT NULL,                    -- index.html, spec.md, assets/logo.svg …
  content_type   TEXT NOT NULL,
  size           INT  NOT NULL,
  sha256         TEXT NOT NULL,
  data           BYTEA NOT NULL,
  published_by   TEXT NOT NULL DEFAULT '',
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (deliverable_id, version, path)
);

CREATE TABLE IF NOT EXISTS at_upload_chunks (
  upload_id  TEXT NOT NULL,
  idx        INT  NOT NULL,
  session_id TEXT NOT NULL REFERENCES at_agent_sessions(id) ON DELETE CASCADE,
  data       BYTEA NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (upload_id, idx)
);
CREATE INDEX IF NOT EXISTS at_upload_chunks_age ON at_upload_chunks (created_at);

CREATE TABLE IF NOT EXISTS at_deliverable_comments (
  id             TEXT PRIMARY KEY,                 -- dc_…
  deliverable_id TEXT NOT NULL REFERENCES at_deliverables(id) ON DELETE CASCADE,
  version        INT,
  author         TEXT NOT NULL,                    -- user id
  text           TEXT NOT NULL,
  sent_to        TEXT NOT NULL DEFAULT '',         -- employee the comment was delivered to
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS at_deliverable_comments_dl ON at_deliverable_comments (deliverable_id, created_at);

-- +goose Down
DROP TABLE IF EXISTS at_deliverable_comments;
DROP TABLE IF EXISTS at_upload_chunks;
DROP TABLE IF EXISTS at_deliverable_files;
DROP TABLE IF EXISTS at_skills;
ALTER TABLE at_roles DROP COLUMN IF EXISTS skills;
