-- +goose Up
-- Threads, roles, employees, sessions and the project event stream
-- (docs/v2/data-model.md §2.4–§2.7, §2.9). One-shot cut-over: there is no
-- legacy_guard check because Python v1 never runs against this database again.

ALTER TABLE at_projects
    ADD COLUMN IF NOT EXISTS work_mode TEXT NOT NULL DEFAULT 'worktree',
    ADD COLUMN IF NOT EXISTS auto_approve BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS require_acceptance BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS thread_seq INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS event_seq BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS delivery JSONB NOT NULL DEFAULT '{}';

ALTER TABLE at_conversations
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
CREATE UNIQUE INDEX IF NOT EXISTS at_conversations_one_coord
    ON at_conversations (project_id) WHERE kind = 'coordinator';

CREATE TABLE IF NOT EXISTS at_roles (
    id               TEXT PRIMARY KEY,
    user_id          TEXT NOT NULL REFERENCES at_users(id) ON DELETE CASCADE,
    project_id       TEXT REFERENCES at_projects(id) ON DELETE CASCADE,
    tier             TEXT NOT NULL,
    role             TEXT NOT NULL,
    title            TEXT NOT NULL DEFAULT '',
    runtime          TEXT NOT NULL DEFAULT 'claude',
    count            INT  NOT NULL DEFAULT 1,
    node_ids         TEXT[] NOT NULL DEFAULT '{}',
    description      TEXT NOT NULL DEFAULT '',
    instructions     TEXT NOT NULL DEFAULT '',
    model            TEXT NOT NULL DEFAULT '',
    effort           TEXT NOT NULL DEFAULT '',
    skip_permissions BOOLEAN NOT NULL DEFAULT FALSE,
    can_delegate     BOOLEAN NOT NULL DEFAULT FALSE,
    read_only        BOOLEAN NOT NULL DEFAULT FALSE,
    allowed_tools    TEXT[] NOT NULL DEFAULT '{}',
    disallowed_tools TEXT[] NOT NULL DEFAULT '{}',
    sandbox          TEXT NOT NULL DEFAULT '',
    max_turns        INT,
    gate             TEXT NOT NULL DEFAULT '',
    task_kinds       TEXT[] NOT NULL DEFAULT '{}',
    look             JSONB NOT NULL DEFAULT '{}',
    sort             INT NOT NULL DEFAULT 0,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (tier IN ('coordinator','lead','member')),
    CHECK (runtime IN ('claude','codex')),
    CHECK (NOT (tier = 'coordinator' AND runtime <> 'claude')),
    CHECK (NOT (read_only AND skip_permissions))
);
CREATE UNIQUE INDEX IF NOT EXISTS at_roles_key ON at_roles (user_id, COALESCE(project_id, ''), role);
CREATE UNIQUE INDEX IF NOT EXISTS at_roles_one_coord
    ON at_roles (user_id, COALESCE(project_id, '')) WHERE tier = 'coordinator';
CREATE UNIQUE INDEX IF NOT EXISTS at_roles_one_lead
    ON at_roles (user_id, COALESCE(project_id, '')) WHERE tier = 'lead';

CREATE TABLE IF NOT EXISTS at_threads (
    id             TEXT PRIMARY KEY REFERENCES at_conversations(id) ON DELETE CASCADE,
    project_id     TEXT NOT NULL REFERENCES at_projects(id) ON DELETE CASCADE,
    user_id        TEXT NOT NULL REFERENCES at_users(id) ON DELETE CASCADE,
    no             INT  NOT NULL,
    title          TEXT NOT NULL,
    brief          TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL DEFAULT 'queued',
    pause_reason   TEXT NOT NULL DEFAULT '',
    branch         TEXT NOT NULL DEFAULT '',
    base_branch    TEXT NOT NULL DEFAULT '',
    pr_number      INT,
    pr_url         TEXT,
    pr_state       TEXT NOT NULL DEFAULT '',
    summary        TEXT NOT NULL DEFAULT '',
    auto_approve   BOOLEAN NOT NULL DEFAULT FALSE,
    opened_by      TEXT NOT NULL DEFAULT 'coordinator',
    roles_snapshot JSONB NOT NULL DEFAULT '[]',
    waiting_count  INT NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at     TIMESTAMPTZ,
    finished_at    TIMESTAMPTZ,
    UNIQUE (project_id, no)
);
CREATE INDEX IF NOT EXISTS at_threads_project ON at_threads (project_id, status, updated_at DESC);

ALTER TABLE at_messages
    ADD COLUMN IF NOT EXISTS author TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'chat',
    ADD COLUMN IF NOT EXISTS delivered_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS meta JSONB NOT NULL DEFAULT '{}';

ALTER TABLE at_tasks
    ADD COLUMN IF NOT EXISTS thread_id TEXT REFERENCES at_threads(id) ON DELETE CASCADE,
    ADD COLUMN IF NOT EXISTS main_task_id TEXT,
    ADD COLUMN IF NOT EXISTS role TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS claimed_by TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'work',
    ADD COLUMN IF NOT EXISTS priority INT NOT NULL DEFAULT 100,
    ADD COLUMN IF NOT EXISTS attempt INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS summary TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS branch TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS head_sha TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN IF NOT EXISTS claimed_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS finished_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS at_tasks_pool
    ON at_tasks (thread_id, role, priority, created_at) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS at_tasks_claimed
    ON at_tasks (thread_id, claimed_by) WHERE status IN ('claimed','running');

CREATE TABLE IF NOT EXISTS at_main_tasks (
    id          TEXT PRIMARY KEY,
    thread_id   TEXT NOT NULL REFERENCES at_threads(id) ON DELETE CASCADE,
    pos         INT NOT NULL,
    title       TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'todo',
    auto_done   BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS at_main_tasks_thread ON at_main_tasks (thread_id, pos);

ALTER TABLE at_questions
    ADD COLUMN IF NOT EXISTS thread_id TEXT REFERENCES at_threads(id) ON DELETE CASCADE,
    ADD COLUMN IF NOT EXISTS project_id TEXT REFERENCES at_projects(id) ON DELETE CASCADE,
    ADD COLUMN IF NOT EXISTS session_id TEXT,
    ADD COLUMN IF NOT EXISTS approval_id TEXT,
    ADD COLUMN IF NOT EXISTS call_id TEXT,
    ADD COLUMN IF NOT EXISTS blocking BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS decided_by TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS at_questions_approval
    ON at_questions (approval_id) WHERE approval_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS at_questions_open
    ON at_questions (project_id, created_at) WHERE status = 'open';

CREATE TABLE IF NOT EXISTS at_employees (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL REFERENCES at_projects(id) ON DELETE CASCADE,
    thread_id   TEXT REFERENCES at_threads(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    tier        TEXT NOT NULL,
    role        TEXT NOT NULL,
    role_id     TEXT REFERENCES at_roles(id) ON DELETE SET NULL,
    runtime     TEXT NOT NULL,
    node_id     TEXT REFERENCES at_nodes(id) ON DELETE SET NULL,
    instance_no INT NOT NULL DEFAULT 1,
    look        JSONB NOT NULL DEFAULT '{}',
    spec        JSONB NOT NULL DEFAULT '{}',
    active      BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS at_employees_name
    ON at_employees (project_id, COALESCE(thread_id, ''), name);

CREATE TABLE IF NOT EXISTS at_agent_sessions (
    id              TEXT PRIMARY KEY,
    employee_id     TEXT NOT NULL REFERENCES at_employees(id) ON DELETE CASCADE,
    node_id         TEXT REFERENCES at_nodes(id) ON DELETE SET NULL,
    runtime         TEXT NOT NULL,
    sdk_session_id  TEXT,
    codex_thread_id TEXT,
    cwd             TEXT NOT NULL,
    branch          TEXT NOT NULL DEFAULT '',
    state           TEXT NOT NULL DEFAULT 'opening',
    open_gen        INT NOT NULL DEFAULT 0,
    permission_mode TEXT NOT NULL DEFAULT 'default',
    init            JSONB NOT NULL DEFAULT '{}',
    last_event_no   BIGINT NOT NULL DEFAULT 0,
    opened_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_active_at  TIMESTAMPTZ,
    closed_at       TIMESTAMPTZ,
    close_reason    TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS at_agent_sessions_live
    ON at_agent_sessions (employee_id) WHERE state <> 'closed';
CREATE INDEX IF NOT EXISTS at_agent_sessions_node
    ON at_agent_sessions (node_id) WHERE state <> 'closed';

CREATE TABLE IF NOT EXISTS at_tool_calls (
    session_id  TEXT NOT NULL REFERENCES at_agent_sessions(id) ON DELETE CASCADE,
    call_id     TEXT NOT NULL,
    tool_use_id TEXT,
    tool        TEXT NOT NULL,
    args        JSONB NOT NULL,
    status      TEXT NOT NULL DEFAULT 'running',
    result      JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    PRIMARY KEY (session_id, call_id)
);
CREATE INDEX IF NOT EXISTS at_tool_calls_waiting
    ON at_tool_calls (session_id) WHERE status IN ('running','waiting');

CREATE TABLE IF NOT EXISTS at_usage (
    id                 BIGSERIAL PRIMARY KEY,
    session_id         TEXT NOT NULL REFERENCES at_agent_sessions(id) ON DELETE CASCADE,
    project_id         TEXT NOT NULL,
    thread_id          TEXT,
    turn_id            TEXT NOT NULL,
    model              TEXT NOT NULL DEFAULT '',
    cost_usd           NUMERIC(12,6),
    input_tokens       BIGINT,
    output_tokens      BIGINT,
    cache_read_tokens  BIGINT,
    cache_write_tokens BIGINT,
    duration_ms        INT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (session_id, turn_id)
);
CREATE INDEX IF NOT EXISTS at_usage_project ON at_usage (project_id, created_at);

CREATE TABLE IF NOT EXISTS at_permission_rules (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL REFERENCES at_projects(id) ON DELETE CASCADE,
    thread_id   TEXT REFERENCES at_threads(id) ON DELETE CASCADE,
    employee    TEXT NOT NULL DEFAULT '',
    tool_name   TEXT NOT NULL,
    rule        TEXT NOT NULL DEFAULT '',
    sdk_update  JSONB,
    input_hash  TEXT NOT NULL DEFAULT '',
    created_by  TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS at_permission_rules_lookup
    ON at_permission_rules (project_id, thread_id, tool_name);

CREATE TABLE IF NOT EXISTS at_events (
    id          BIGINT GENERATED ALWAYS AS IDENTITY,
    project_id  TEXT NOT NULL,
    seq         BIGINT NOT NULL,
    thread_id   TEXT,
    type        TEXT NOT NULL,
    actor       TEXT NOT NULL DEFAULT '',
    payload     JSONB NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);
CREATE INDEX IF NOT EXISTS at_events_project ON at_events (project_id, seq);
CREATE INDEX IF NOT EXISTS at_events_thread ON at_events (thread_id, id) WHERE thread_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS at_status_events (
    id          BIGINT GENERATED ALWAYS AS IDENTITY,
    project_id  TEXT NOT NULL,
    seq         BIGINT NOT NULL,
    thread_id   TEXT,
    employee_id TEXT NOT NULL,
    tier        TEXT NOT NULL,
    role        TEXT NOT NULL,
    state       TEXT NOT NULL,
    substate    TEXT NOT NULL DEFAULT '',
    bubble      TEXT NOT NULL DEFAULT '',
    bubble_kind TEXT NOT NULL DEFAULT 'plain',
    task_id     TEXT NOT NULL DEFAULT '',
    task_kind   TEXT NOT NULL DEFAULT '',
    cause       TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);
CREATE INDEX IF NOT EXISTS at_status_events_project ON at_status_events (project_id, created_at);
CREATE INDEX IF NOT EXISTS at_status_events_seq ON at_status_events (project_id, seq);
CREATE INDEX IF NOT EXISTS at_status_events_employee ON at_status_events (employee_id, created_at DESC);

CREATE TABLE IF NOT EXISTS at_employee_status (
    employee_id  TEXT PRIMARY KEY REFERENCES at_employees(id) ON DELETE CASCADE,
    project_id   TEXT NOT NULL,
    thread_id    TEXT,
    state        TEXT NOT NULL,
    substate     TEXT NOT NULL DEFAULT '',
    bubble       TEXT NOT NULL DEFAULT '',
    bubble_kind  TEXT NOT NULL DEFAULT 'plain',
    bubble_until TIMESTAMPTZ,
    task_id      TEXT NOT NULL DEFAULT '',
    task_kind    TEXT NOT NULL DEFAULT '',
    since        TIMESTAMPTZ NOT NULL DEFAULT now(),
    seq          BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS at_employee_status_project ON at_employee_status (project_id);

-- Monthly partitions of the event tables: this month and `ahead` more.
-- atctl serve calls it daily; the migration creates the first batch.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION at_ensure_event_partitions(ahead INT) RETURNS VOID
LANGUAGE plpgsql AS $$
DECLARE
    month DATE := date_trunc('month', now())::date;
    parent TEXT;
    name TEXT;
BEGIN
    FOR i IN 0..ahead LOOP
        FOREACH parent IN ARRAY ARRAY['at_events', 'at_status_events'] LOOP
            name := format('%s_%s', parent, to_char(month, 'YYYY_MM'));
            EXECUTE format(
                'CREATE TABLE IF NOT EXISTS %I PARTITION OF %I FOR VALUES FROM (%L) TO (%L)',
                name, parent, month, (month + INTERVAL '1 month')::date);
        END LOOP;
        month := (month + INTERVAL '1 month')::date;
    END LOOP;
END;
$$;
-- +goose StatementEnd
SELECT at_ensure_event_partitions(2);

-- +goose Down
DROP FUNCTION IF EXISTS at_ensure_event_partitions(INT);
DROP TABLE IF EXISTS at_employee_status;
DROP TABLE IF EXISTS at_status_events;
DROP TABLE IF EXISTS at_events;
DROP TABLE IF EXISTS at_permission_rules;
DROP TABLE IF EXISTS at_usage;
DROP TABLE IF EXISTS at_tool_calls;
DROP TABLE IF EXISTS at_agent_sessions;
DROP TABLE IF EXISTS at_employees;
DROP TABLE IF EXISTS at_main_tasks;
ALTER TABLE at_questions DROP COLUMN IF EXISTS thread_id, DROP COLUMN IF EXISTS project_id,
    DROP COLUMN IF EXISTS session_id, DROP COLUMN IF EXISTS approval_id, DROP COLUMN IF EXISTS call_id,
    DROP COLUMN IF EXISTS blocking, DROP COLUMN IF EXISTS decided_by;
ALTER TABLE at_tasks DROP COLUMN IF EXISTS thread_id;
DROP TABLE IF EXISTS at_threads;
DROP TABLE IF EXISTS at_roles;
