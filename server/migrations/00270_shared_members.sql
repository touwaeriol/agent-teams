-- +goose Up
-- Project-level shared members (shared-members.md §2–§4, §8). Members become project staff
-- (at_employees.thread_id NULL) shared by every thread; a thread creates only its lead.
--   at_employees.current_thread_id   the thread of the member's current task or latest session
--   at_agent_sessions.thread_id      the thread whose worktree the session was opened in
--   at_tasks.reserved_for/_until     rework reserved for the original member until a deadline
--   at_tasks.ready_at                when the task's dependencies were met (claimable from then)
--   at_threads.only_roles            open_thread(roles): member roles the lead may assign to (NULL = all)
ALTER TABLE at_employees ADD COLUMN current_thread_id TEXT REFERENCES at_threads(id) ON DELETE SET NULL;
ALTER TABLE at_agent_sessions ADD COLUMN thread_id TEXT REFERENCES at_threads(id) ON DELETE SET NULL;
UPDATE at_agent_sessions a SET thread_id = e.thread_id FROM at_employees e
    WHERE e.id = a.employee_id AND e.thread_id IS NOT NULL;
ALTER TABLE at_tasks
    ADD COLUMN reserved_for TEXT NOT NULL DEFAULT '',
    ADD COLUMN reserved_until TIMESTAMPTZ,
    ADD COLUMN ready_at TIMESTAMPTZ;
ALTER TABLE at_threads ADD COLUMN only_roles JSONB;
CREATE INDEX at_tasks_queue ON at_tasks (thread_id, status, role);

-- +goose Down
DROP INDEX at_tasks_queue;
ALTER TABLE at_threads DROP COLUMN only_roles;
ALTER TABLE at_tasks DROP COLUMN reserved_for, DROP COLUMN reserved_until, DROP COLUMN ready_at;
ALTER TABLE at_agent_sessions DROP COLUMN thread_id;
ALTER TABLE at_employees DROP COLUMN current_thread_id;
