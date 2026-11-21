-- +goose Up
-- The latest install and login state of each runtime CLI on each node, as the daemon reported it
-- with node.runtimes (protocol.md §9.2, hello.features runtime_status). One row per node and
-- runtime id; a report replaces the node's rows. Never holds credentials.
CREATE TABLE IF NOT EXISTS at_node_runtimes (
    node_id         TEXT NOT NULL REFERENCES at_nodes(id) ON DELETE CASCADE,
    runtime         TEXT NOT NULL,
    installed       BOOLEAN NOT NULL DEFAULT false,
    version         TEXT NOT NULL DEFAULT '',
    path            TEXT NOT NULL DEFAULT '',
    auth            TEXT NOT NULL DEFAULT 'unknown',
    auth_expires_at TIMESTAMPTZ,
    expired         BOOLEAN NOT NULL DEFAULT false,
    error           TEXT NOT NULL DEFAULT '',
    checked_at      TIMESTAMPTZ NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (node_id, runtime)
);

-- A session an employee could not open because no candidate node has its runtime installed and
-- logged in (orchestrator.pickNode): the messages wait here, with the reason shown to the user,
-- until a node reports the runtime ready (node.runtimes) or comes online; then the session opens
-- with the first message and the rest follow as session.send. One row per employee.
CREATE TABLE IF NOT EXISTS at_session_holds (
    employee_id TEXT PRIMARY KEY REFERENCES at_employees(id) ON DELETE CASCADE,
    project_id  TEXT NOT NULL REFERENCES at_projects(id) ON DELETE CASCADE,
    reason      TEXT NOT NULL DEFAULT '',
    first       JSONB NOT NULL,
    resume      JSONB,
    more        JSONB NOT NULL DEFAULT '[]',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS at_session_holds_project ON at_session_holds (project_id);

-- +goose Down
DROP TABLE IF EXISTS at_session_holds;
DROP TABLE IF EXISTS at_node_runtimes;
