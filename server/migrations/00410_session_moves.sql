-- +goose Up
-- Session moves between nodes (protocol.md §4.11, architecture.md §6.2): an employee whose node ran
-- out of subscription quota continues on another node with the same conversation. The source node
-- uploads the archive (transcript + checkout snapshots) into archive; the target downloads it when
-- its session opens; the bytes are dropped once that session answered (done / failed).
CREATE TABLE IF NOT EXISTS at_session_moves (
    id              TEXT PRIMARY KEY,                -- mv_ + ULID: session.export.export_id
    employee_id     TEXT NOT NULL REFERENCES at_employees(id) ON DELETE CASCADE,
    project_id      TEXT NOT NULL REFERENCES at_projects(id) ON DELETE CASCADE,
    from_session_id TEXT NOT NULL,
    from_node_id    TEXT NOT NULL REFERENCES at_nodes(id) ON DELETE CASCADE,
    to_node_id      TEXT NOT NULL REFERENCES at_nodes(id) ON DELETE CASCADE,
    to_session_id   TEXT,
    runtime         TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'exporting'
        CHECK (status IN ('exporting', 'importing', 'done', 'failed')),
    error           TEXT NOT NULL DEFAULT '',
    archive         BYTEA,
    size            BIGINT,
    sha256          TEXT,
    reset_at        TIMESTAMPTZ,                     -- when the source node's limit resets: no move goes back there before
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS at_session_moves_employee ON at_session_moves (employee_id, created_at DESC);
CREATE INDEX IF NOT EXISTS at_session_moves_pending ON at_session_moves (updated_at) WHERE status IN ('exporting', 'importing');
CREATE INDEX IF NOT EXISTS at_session_moves_to_session ON at_session_moves (to_session_id) WHERE to_session_id IS NOT NULL;

-- +goose Down
DROP TABLE IF EXISTS at_session_moves;
