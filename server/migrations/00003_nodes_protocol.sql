-- +goose Up
CREATE TABLE IF NOT EXISTS at_node_commands (
    node_id     TEXT NOT NULL REFERENCES at_nodes(id) ON DELETE CASCADE,
    seq         BIGINT NOT NULL,
    type        TEXT NOT NULL,
    idem_key    TEXT NOT NULL,
    body        JSONB NOT NULL,
    status      TEXT NOT NULL DEFAULT 'pending',
    result      JSONB,
    session_id  TEXT,
    open_gen    INT,
    expires_at  TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    acked_at    TIMESTAMPTZ,
    PRIMARY KEY (node_id, seq),
    UNIQUE (node_id, idem_key)
);
CREATE INDEX IF NOT EXISTS at_node_commands_pending
    ON at_node_commands (node_id, seq) WHERE status IN ('pending', 'sent');

CREATE TABLE IF NOT EXISTS at_node_logs (
    id         BIGSERIAL PRIMARY KEY,
    node_id    TEXT NOT NULL REFERENCES at_nodes(id) ON DELETE CASCADE,
    level      TEXT NOT NULL,
    msg        TEXT NOT NULL,
    fields     JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS at_node_logs_node ON at_node_logs (node_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS at_node_logs;
DROP TABLE IF EXISTS at_node_commands;
