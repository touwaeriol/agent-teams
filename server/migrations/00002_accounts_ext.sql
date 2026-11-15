-- +goose Up
ALTER TABLE at_users
    ADD COLUMN IF NOT EXISTS platform_version SMALLINT NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS max_running_threads INT,
    ADD COLUMN IF NOT EXISTS event_seq BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS display_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS password_changed_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS must_change_password BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS last_login_at TIMESTAMPTZ;

ALTER TABLE at_sessions
    ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN IF NOT EXISTS last_seen_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS user_agent TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS csrf_secret TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS at_sessions_expires ON at_sessions (expires_at);
CREATE INDEX IF NOT EXISTS at_sessions_user ON at_sessions (user_id);

ALTER TABLE at_nodes
    ADD COLUMN IF NOT EXISTS max_active_sessions INT,
    ADD COLUMN IF NOT EXISTS park_after_s INT,
    ADD COLUMN IF NOT EXISTS isolated BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS isolation TEXT NOT NULL DEFAULT 'none',
    ADD COLUMN IF NOT EXISTS labels JSONB NOT NULL DEFAULT '[]',
    ADD COLUMN IF NOT EXISTS last_hello JSONB NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS last_seen_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS epoch TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS ack_seq BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS cmd_seq BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS public_key TEXT;

ALTER TABLE at_git_connections
    ADD COLUMN IF NOT EXISTS permissions JSONB NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS account_type TEXT NOT NULL DEFAULT '',   -- App installs: User | Organization (settings URL)
    ADD COLUMN IF NOT EXISTS checked_at TIMESTAMPTZ;

ALTER TABLE at_conversations
    ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'chat';

CREATE TABLE IF NOT EXISTS at_audit_log (
    id          BIGSERIAL PRIMARY KEY,
    user_id     TEXT REFERENCES at_users(id) ON DELETE SET NULL,
    actor       TEXT NOT NULL,
    action      TEXT NOT NULL,
    target_type TEXT NOT NULL DEFAULT '',
    target_id   TEXT NOT NULL DEFAULT '',
    detail      JSONB NOT NULL DEFAULT '{}',
    ip          INET,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS at_audit_log_user ON at_audit_log (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS at_audit_log_action ON at_audit_log (action, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS at_audit_log;
