-- +goose Up
-- Scheduled tasks that agents create (schedule_* tools) and that wake a thread's lead or a
-- member, besides the user's recurring messages to the coordinator (data-model.md §2.10). River
-- runs them now (00380): every active schedule has one pending schedule_fire job for its
-- next_run_at.
--   kind        cron (recurring, cron + timezone) | once (run_at, cron stays '')
--   target      coordinator | lead (thread_id names the thread) | member (employee_id, bound to
--               its task task_id in thread thread_id; ends when the task does)
--   created_by  user | coordinator | lead | member: who made it; agents may change only their own
--   status      active | done (once fired / ends_at or max_runs reached) | cancelled | missed
--               (a once schedule more than a day late); ended rows are kept 7 days for the list
-- Existing rows are the user's recurring messages to the coordinator.
ALTER TABLE at_schedules
    ADD COLUMN kind TEXT NOT NULL DEFAULT 'cron' CHECK (kind IN ('cron', 'once')),
    ADD COLUMN run_at TIMESTAMPTZ,
    ADD COLUMN target TEXT NOT NULL DEFAULT 'coordinator' CHECK (target IN ('coordinator', 'lead', 'member')),
    ADD COLUMN thread_id TEXT REFERENCES at_threads(id) ON DELETE CASCADE,
    ADD COLUMN employee_id TEXT REFERENCES at_employees(id) ON DELETE CASCADE,
    ADD COLUMN task_id TEXT,
    ADD COLUMN created_by TEXT NOT NULL DEFAULT 'user' CHECK (created_by IN ('user', 'coordinator', 'lead', 'member')),
    ADD COLUMN ends_at TIMESTAMPTZ,
    ADD COLUMN max_runs INTEGER CHECK (max_runs > 0),
    ADD COLUMN run_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'done', 'cancelled', 'missed')),
    ADD COLUMN ended_at TIMESTAMPTZ,
    ADD CONSTRAINT at_schedules_once_run_at CHECK (kind <> 'once' OR run_at IS NOT NULL),
    ADD CONSTRAINT at_schedules_lead_thread CHECK (target <> 'lead' OR thread_id IS NOT NULL),
    ADD CONSTRAINT at_schedules_member_task CHECK (target <> 'member'
        OR (kind = 'once' AND thread_id IS NOT NULL AND employee_id IS NOT NULL AND task_id IS NOT NULL));
CREATE INDEX at_schedules_thread ON at_schedules (thread_id) WHERE thread_id IS NOT NULL;
CREATE INDEX at_schedules_task ON at_schedules (thread_id, task_id) WHERE task_id IS NOT NULL AND status = 'active';

-- One row per run of a schedule (the last 100 are kept): planned_at is the run's planned time
-- (NULL for the "立即运行" button, manual), outcome delivered | skipped (the target was gone or
-- the previous run's message had not reached it yet) | failed (every try failed) | missed;
-- message_id is the message it posted.
CREATE TABLE at_schedule_runs (
    id          BIGSERIAL PRIMARY KEY,
    schedule_id TEXT NOT NULL REFERENCES at_schedules(id) ON DELETE CASCADE,
    planned_at  TIMESTAMPTZ,
    fired_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    manual      BOOLEAN NOT NULL DEFAULT false,
    outcome     TEXT NOT NULL CHECK (outcome IN ('delivered', 'skipped', 'failed', 'missed')),
    message_id  BIGINT,
    error       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX at_schedule_runs_schedule ON at_schedule_runs (schedule_id, id DESC);

-- +goose Down
DROP TABLE at_schedule_runs;
DELETE FROM at_schedules WHERE kind <> 'cron' OR target <> 'coordinator';
DROP INDEX IF EXISTS at_schedules_task;
DROP INDEX IF EXISTS at_schedules_thread;
ALTER TABLE at_schedules
    DROP CONSTRAINT at_schedules_once_run_at,
    DROP CONSTRAINT at_schedules_lead_thread,
    DROP CONSTRAINT at_schedules_member_task,
    DROP COLUMN kind, DROP COLUMN run_at, DROP COLUMN target, DROP COLUMN thread_id, DROP COLUMN employee_id,
    DROP COLUMN task_id, DROP COLUMN created_by, DROP COLUMN ends_at, DROP COLUMN max_runs, DROP COLUMN run_count,
    DROP COLUMN status, DROP COLUMN ended_at;
