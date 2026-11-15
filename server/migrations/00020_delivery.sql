-- +goose Up
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE IF NOT EXISTS at_merge_queue (
    id           TEXT PRIMARY KEY,
    thread_id    TEXT NOT NULL REFERENCES at_threads(id) ON DELETE CASCADE,
    task_id      TEXT,
    employee     TEXT NOT NULL,
    head_branch  TEXT NOT NULL,
    head_sha     TEXT NOT NULL DEFAULT '',
    trial_branch TEXT NOT NULL DEFAULT '',
    trial_sha    TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'queued',
    attempts     INT NOT NULL DEFAULT 0,
    error        TEXT NOT NULL DEFAULT '',
    position     BIGSERIAL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS at_merge_queue_active
    ON at_merge_queue (thread_id, position) WHERE status IN ('queued','merging','ci_running');

CREATE TABLE IF NOT EXISTS at_ci_runs (
    id           BIGSERIAL PRIMARY KEY,
    project_id   TEXT NOT NULL REFERENCES at_projects(id) ON DELETE CASCADE,
    thread_id    TEXT REFERENCES at_threads(id) ON DELETE CASCADE,
    queue_id     TEXT REFERENCES at_merge_queue(id) ON DELETE CASCADE,
    sha          TEXT NOT NULL,
    check_name   TEXT NOT NULL,
    check_run_id BIGINT,
    status       TEXT NOT NULL,
    conclusion   TEXT NOT NULL DEFAULT '',
    url          TEXT NOT NULL DEFAULT '',
    summary      TEXT NOT NULL DEFAULT '',
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (sha, check_name, queue_id)
);

CREATE TABLE IF NOT EXISTS at_deliverables (
    id           TEXT PRIMARY KEY,
    project_id   TEXT NOT NULL REFERENCES at_projects(id) ON DELETE CASCADE,
    thread_id    TEXT REFERENCES at_threads(id) ON DELETE SET NULL,
    kind         TEXT NOT NULL,
    title        TEXT NOT NULL,
    url          TEXT NOT NULL DEFAULT '',
    detail       JSONB NOT NULL DEFAULT '{}',
    published_by TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS at_deliverables_project ON at_deliverables (project_id, kind, created_at DESC);
CREATE INDEX IF NOT EXISTS at_deliverables_title_trgm ON at_deliverables USING gin (title gin_trgm_ops);

CREATE TABLE IF NOT EXISTS at_project_memory (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL REFERENCES at_projects(id) ON DELETE CASCADE,
    text        TEXT NOT NULL,
    source      TEXT NOT NULL,
    pinned      BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS at_project_memory_project ON at_project_memory (project_id, created_at DESC);

CREATE TABLE IF NOT EXISTS at_thread_nodes (
    thread_id  TEXT NOT NULL REFERENCES at_threads(id) ON DELETE CASCADE,
    node_id    TEXT NOT NULL REFERENCES at_nodes(id) ON DELETE CASCADE,
    paths      TEXT[] NOT NULL DEFAULT '{}',
    cleaned_at TIMESTAMPTZ,
    PRIMARY KEY (thread_id, node_id)
);

-- +goose Down
DROP TABLE IF EXISTS at_thread_nodes;
DROP TABLE IF EXISTS at_project_memory;
DROP TABLE IF EXISTS at_deliverables;
DROP TABLE IF EXISTS at_ci_runs;
DROP TABLE IF EXISTS at_merge_queue;
