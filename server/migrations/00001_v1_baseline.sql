-- +goose Up
-- The v1 schema (Python agent_teams.server), idempotent. On the production
-- database every statement is a no-op; on an empty database it builds v1.
CREATE TABLE IF NOT EXISTS at_users (
    id TEXT PRIMARY KEY,
    email TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'user',
    active BOOLEAN NOT NULL DEFAULT TRUE,
    max_nodes INTEGER,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS at_sessions (
    token_hash TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES at_users(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS at_nodes (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES at_users(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, name)
);
CREATE TABLE IF NOT EXISTS at_teams (
    user_id TEXT PRIMARY KEY REFERENCES at_users(id) ON DELETE CASCADE,
    config TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS at_settings (
    key TEXT PRIMARY KEY,
    value JSONB NOT NULL
);
CREATE TABLE IF NOT EXISTS at_git_connections (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES at_users(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    provider TEXT NOT NULL DEFAULT 'github',
    login TEXT NOT NULL DEFAULT '',
    token_secret TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, name)
);
ALTER TABLE at_git_connections ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'token';
ALTER TABLE at_git_connections ADD COLUMN IF NOT EXISTS installation_id BIGINT;
CREATE TABLE IF NOT EXISTS at_projects (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES at_users(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    context TEXT NOT NULL DEFAULT '',
    repo TEXT,
    branch TEXT NOT NULL DEFAULT '',
    git_connection_id TEXT REFERENCES at_git_connections(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, name)
);
ALTER TABLE at_projects ADD COLUMN IF NOT EXISTS pinned BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE at_projects ADD COLUMN IF NOT EXISTS archived BOOLEAN NOT NULL DEFAULT FALSE;
CREATE TABLE IF NOT EXISTS at_conversations (
    id TEXT PRIMARY KEY,
    user_id TEXT REFERENCES at_users(id) ON DELETE CASCADE,
    title TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    history JSONB NOT NULL DEFAULT '{}'::jsonb
);
ALTER TABLE at_conversations
    ADD COLUMN IF NOT EXISTS user_id TEXT REFERENCES at_users(id) ON DELETE CASCADE;
ALTER TABLE at_conversations
    ADD COLUMN IF NOT EXISTS project_id TEXT REFERENCES at_projects(id) ON DELETE CASCADE;
ALTER TABLE at_conversations ADD COLUMN IF NOT EXISTS plan JSONB NOT NULL DEFAULT '[]'::jsonb;
CREATE INDEX IF NOT EXISTS at_conversations_user ON at_conversations (user_id, created_at);
CREATE INDEX IF NOT EXISTS at_conversations_project ON at_conversations (project_id, created_at);
CREATE TABLE IF NOT EXISTS at_messages (
    id BIGSERIAL PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES at_conversations(id) ON DELETE CASCADE,
    role TEXT NOT NULL,
    content TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS at_messages_conversation ON at_messages (conversation_id, id);
CREATE TABLE IF NOT EXISTS at_tasks (
    conversation_id TEXT NOT NULL REFERENCES at_conversations(id) ON DELETE CASCADE,
    id TEXT NOT NULL,
    assignee TEXT NOT NULL,
    instruction TEXT NOT NULL,
    depends_on JSONB NOT NULL,
    status TEXT NOT NULL,
    output TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (conversation_id, id)
);
ALTER TABLE at_tasks ADD COLUMN IF NOT EXISTS parent TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS at_mail (
    conversation_id TEXT NOT NULL REFERENCES at_conversations(id) ON DELETE CASCADE,
    id BIGSERIAL,
    sender TEXT NOT NULL,
    recipient TEXT NOT NULL,
    body TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    read_by JSONB NOT NULL DEFAULT '[]'::jsonb,
    PRIMARY KEY (conversation_id, id)
);
CREATE TABLE IF NOT EXISTS at_questions (
    conversation_id TEXT NOT NULL REFERENCES at_conversations(id) ON DELETE CASCADE,
    id TEXT NOT NULL,
    asker TEXT NOT NULL,
    task_id TEXT,
    audience TEXT NOT NULL,
    kind TEXT NOT NULL,
    text TEXT NOT NULL,
    options JSONB NOT NULL DEFAULT '[]'::jsonb,
    detail JSONB NOT NULL DEFAULT '{}'::jsonb,
    answer TEXT,
    status TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    answered_at TIMESTAMPTZ,
    PRIMARY KEY (conversation_id, id)
);

-- +goose Down
-- The v1 baseline is never rolled back: it owns the production data.
SELECT 1;
