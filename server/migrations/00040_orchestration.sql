-- +goose Up
-- Columns and tables the orchestration engines need beyond data-model.md.
--   at_node_commands.env_id: envelope id of the command, reused on every resend
--     (the daemon's idempotency key of id-keyed commands is the envelope id).
--   at_node_commands.meta: what to do when the command's result arrives
--     (e.g. a git.push run for complete_task carries the pending tool call).
--   at_tasks.nudges: turns that ended while the task was still running (§7.3).
--   at_tasks.created_by: who created the task (lead, or a member for subtasks/reviews).
--   at_user_events: the user event stream (/api/events) with at_users.event_seq.
ALTER TABLE at_node_commands
    ADD COLUMN IF NOT EXISTS env_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS meta JSONB NOT NULL DEFAULT '{}';

ALTER TABLE at_tasks
    ADD COLUMN IF NOT EXISTS nudges INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS created_by TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS at_messages_msg_id ON at_messages ((meta->>'msg_id')) WHERE meta ? 'msg_id';
CREATE INDEX IF NOT EXISTS at_tool_calls_status ON at_tool_calls (status) WHERE status IN ('running','waiting');

CREATE TABLE IF NOT EXISTS at_user_events (
    id         BIGSERIAL PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES at_users(id) ON DELETE CASCADE,
    seq        BIGINT NOT NULL,
    type       TEXT NOT NULL,
    payload    JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS at_user_events_user ON at_user_events (user_id, seq);

-- +goose Down
DROP TABLE IF EXISTS at_user_events;
DROP INDEX IF EXISTS at_tool_calls_status;
DROP INDEX IF EXISTS at_messages_msg_id;
ALTER TABLE at_tasks DROP COLUMN IF EXISTS created_by, DROP COLUMN IF EXISTS nudges;
ALTER TABLE at_node_commands DROP COLUMN IF EXISTS meta, DROP COLUMN IF EXISTS env_id;
