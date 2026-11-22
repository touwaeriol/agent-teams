-- +goose Up
-- Scheduled tasks that agents create (schedule_* tools) and that wake a thread's lead, besides
-- the user's recurring messages to the coordinator (data-model.md §2.10). River runs them now
-- (00360): every active schedule has one pending schedule_fire job for its next_run_at.
--   kind        cron (recurring, cron + timezone) | once (run_at, cron stays '')
--   target      coordinator | lead (thread_id names the thread)
--   created_by  user | coordinator | lead: who made it; agents may change only their own
--   status      active | done (once fired / ends_at or max_runs reached) | cancelled | missed
--               (a once schedule more than a day late); ended rows are kept 7 days for the list
-- Existing rows are the user's recurring messages to the coordinator.
ALTER TABLE at_schedules
    ADD COLUMN kind TEXT NOT NULL DEFAULT 'cron' CHECK (kind IN ('cron', 'once')),
    ADD COLUMN run_at TIMESTAMPTZ,
    ADD COLUMN target TEXT NOT NULL DEFAULT 'coordinator' CHECK (target IN ('coordinator', 'lead')),
    ADD COLUMN thread_id TEXT REFERENCES at_threads(id) ON DELETE CASCADE,
    ADD COLUMN created_by TEXT NOT NULL DEFAULT 'user' CHECK (created_by IN ('user', 'coordinator', 'lead')),
    ADD COLUMN ends_at TIMESTAMPTZ,
    ADD COLUMN max_runs INTEGER CHECK (max_runs > 0),
    ADD COLUMN run_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'done', 'cancelled', 'missed')),
    ADD COLUMN ended_at TIMESTAMPTZ,
    ADD CONSTRAINT at_schedules_once_run_at CHECK (kind <> 'once' OR run_at IS NOT NULL),
    ADD CONSTRAINT at_schedules_lead_thread CHECK (target <> 'lead' OR thread_id IS NOT NULL);
CREATE INDEX at_schedules_thread ON at_schedules (thread_id) WHERE thread_id IS NOT NULL;

-- +goose Down
DELETE FROM at_schedules WHERE kind <> 'cron' OR target <> 'coordinator';
DROP INDEX IF EXISTS at_schedules_thread;
ALTER TABLE at_schedules
    DROP CONSTRAINT at_schedules_once_run_at,
    DROP CONSTRAINT at_schedules_lead_thread,
    DROP COLUMN kind, DROP COLUMN run_at, DROP COLUMN target, DROP COLUMN thread_id, DROP COLUMN created_by,
    DROP COLUMN ends_at, DROP COLUMN max_runs, DROP COLUMN run_count, DROP COLUMN status, DROP COLUMN ended_at;
