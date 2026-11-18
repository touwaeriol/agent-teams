-- +goose Up
-- Scheduling by events (shared-members.md §4.1, plan phase 4):
--   at_tasks.downstream   unfinished tasks that depend on the task, directly or through others
--                         (the critical-path tie-break of the queue; kept on insert and on status
--                         changes into or out of a final state)
-- The backfill counts every task's distinct transitive dependents that are not done, failed or
-- cancelled. Pending tasks whose dependencies are all done but that have no ready_at (none should
-- exist: ready_at is stamped on insert, requeue and the last dependency's done) get it now, as
-- the claim query no longer checks dependencies.
ALTER TABLE at_tasks ADD COLUMN downstream INT NOT NULL DEFAULT 0;
WITH RECURSIVE down(conversation_id, anc, id) AS (
    SELECT t.conversation_id, dep.id, t.id FROM at_tasks t, jsonb_array_elements_text(t.depends_on) dep(id)
    UNION
    SELECT d.conversation_id, d.anc, t.id FROM down d
        JOIN at_tasks t ON t.conversation_id = d.conversation_id AND jsonb_exists(t.depends_on, d.id)
), counts AS (
    SELECT d.conversation_id, d.anc, count(*) AS n FROM down d
        JOIN at_tasks t ON t.conversation_id = d.conversation_id AND t.id = d.id
        WHERE t.status NOT IN ('done','failed','cancelled') GROUP BY d.conversation_id, d.anc
)
UPDATE at_tasks a SET downstream = c.n FROM counts c WHERE a.conversation_id = c.conversation_id AND a.id = c.anc;
UPDATE at_tasks t SET ready_at = now() WHERE t.status = 'pending' AND t.ready_at IS NULL AND NOT EXISTS (
    SELECT 1 FROM jsonb_array_elements_text(t.depends_on) dep(id)
    LEFT JOIN at_tasks d ON d.conversation_id = t.conversation_id AND d.id = dep.id WHERE d.status IS DISTINCT FROM 'done');
-- The claim query reads the claimable tasks of a project's threads; the sweep's safety net only
-- the pending tasks still waiting for their dependencies; a thread's legacy members are looked
-- up per claimable task.
CREATE INDEX at_tasks_ready ON at_tasks (thread_id, role) WHERE status = 'pending' AND ready_at IS NOT NULL;
CREATE INDEX at_tasks_unready ON at_tasks (updated_at) WHERE status = 'pending' AND ready_at IS NULL;
CREATE INDEX at_employees_thread ON at_employees (thread_id) WHERE thread_id IS NOT NULL;

-- +goose Down
DROP INDEX at_employees_thread;
DROP INDEX at_tasks_unready;
DROP INDEX at_tasks_ready;
ALTER TABLE at_tasks DROP COLUMN downstream;
