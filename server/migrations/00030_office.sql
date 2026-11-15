-- +goose Up
ALTER TABLE at_projects ADD COLUMN IF NOT EXISTS office_layout TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS at_office_layouts (
    id          TEXT PRIMARY KEY,
    owner_id    TEXT REFERENCES at_users(id) ON DELETE CASCADE,
    version     INT NOT NULL DEFAULT 1,
    doc         JSONB NOT NULL,
    updated_by  TEXT NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS at_office_layouts;
ALTER TABLE at_projects DROP COLUMN IF EXISTS office_layout;
