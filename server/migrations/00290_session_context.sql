-- +goose Up
-- Session bookkeeping for the claim-time decision (§5): the context the session used at its last
-- turn end (tokens; NULL = not reported) and how many tasks it was briefed with.
ALTER TABLE at_agent_sessions
    ADD COLUMN context_used INT,
    ADD COLUMN context_max INT,
    ADD COLUMN tasks_done INT NOT NULL DEFAULT 0;

-- Claim-time compaction (§5.3): a claimed task waits for session.compacted of this compact_id;
-- wait_since is when its current wait (compaction, then the pre-task sync) began.
-- progress is the member's latest report_progress (part of the task anchor, §5.2).
-- Per-task metrics (§5.2 item 5): compactions during the task, context at its start and end (tokens),
-- and whether the lead was told the task is too long.
ALTER TABLE at_tasks
    ADD COLUMN compact_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN wait_since TIMESTAMPTZ,
    ADD COLUMN progress TEXT NOT NULL DEFAULT '',
    ADD COLUMN compactions INT NOT NULL DEFAULT 0,
    ADD COLUMN ctx_start INT,
    ADD COLUMN ctx_end INT,
    ADD COLUMN split_hinted BOOLEAN NOT NULL DEFAULT false;
CREATE INDEX at_tasks_compact ON at_tasks (compact_id) WHERE compact_id <> '';

-- +goose Down
DROP INDEX at_tasks_compact;
ALTER TABLE at_tasks DROP COLUMN compact_id, DROP COLUMN wait_since, DROP COLUMN progress, DROP COLUMN compactions, DROP COLUMN ctx_start, DROP COLUMN ctx_end,
    DROP COLUMN split_hinted;
ALTER TABLE at_agent_sessions DROP COLUMN context_used, DROP COLUMN context_max, DROP COLUMN tasks_done;
