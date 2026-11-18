-- +goose Up
-- Task hand-over (shared-members.md §4.2): a task's worktree is synced before its brief goes out,
-- and a follow-up task's brief carries what its dependencies changed and left for it.
--   sync          per repository, what the pre-task sync did ({repo: {action, head_sha, conflicts, error}})
--   sync_pending  sync jobs still out; the brief waits for them (or a timeout)
--   handoff       complete_task's note for whoever builds on this task
--   handoff_asked complete_task already asked once for a missing handoff
--   merged        per repository, the range the task's merge added to the thread branch ({repo: {base, head, files}})
-- at_merge_queue.base_sha is the thread branch before the merge, for that range.
ALTER TABLE at_tasks
    ADD COLUMN sync JSONB NOT NULL DEFAULT '{}',
    ADD COLUMN sync_pending INT NOT NULL DEFAULT 0,
    ADD COLUMN handoff TEXT NOT NULL DEFAULT '',
    ADD COLUMN handoff_asked BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN merged JSONB NOT NULL DEFAULT '{}';
ALTER TABLE at_merge_queue ADD COLUMN base_sha TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE at_merge_queue DROP COLUMN base_sha;
ALTER TABLE at_tasks DROP COLUMN sync, DROP COLUMN sync_pending, DROP COLUMN handoff,
    DROP COLUMN handoff_asked, DROP COLUMN merged;
