-- +goose Up
-- Project chrome (data-model.md §2.5, §2.10): a project icon, project-level model overrides
-- for the coordinator and for thread employees, "continue threads after the usage limit
-- resets", the rate-limit waits that feed it, and scheduled messages to the coordinator.

ALTER TABLE at_projects
  ADD COLUMN IF NOT EXISTS icon TEXT NOT NULL DEFAULT '',               -- '' = initial on the default color; an emoji; or color:#RRGGBB
  ADD COLUMN IF NOT EXISTS coordinator_model TEXT NOT NULL DEFAULT '',  -- '' = the coordinator role's model
  ADD COLUMN IF NOT EXISTS thread_model TEXT NOT NULL DEFAULT '',       -- '' = each lead/member role's model
  ADD COLUMN IF NOT EXISTS thread_effort TEXT NOT NULL DEFAULT '',      -- '' = each role's effort; low | medium | high | xhigh | max
  ADD COLUMN IF NOT EXISTS auto_resume BOOLEAN NOT NULL DEFAULT FALSE;  -- continue paused threads once the usage limit resets

CREATE TABLE IF NOT EXISTS at_rate_limit_waits (   -- an employee whose Claude usage limit was hit, until it resets
  employee_id TEXT PRIMARY KEY REFERENCES at_employees(id) ON DELETE CASCADE,
  project_id  TEXT NOT NULL REFERENCES at_projects(id) ON DELETE CASCADE,
  thread_id   TEXT REFERENCES at_threads(id) ON DELETE CASCADE,
  reset_at    TIMESTAMPTZ NOT NULL,
  detail      TEXT NOT NULL DEFAULT '',
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS at_rate_limit_waits_reset ON at_rate_limit_waits (reset_at);

CREATE TABLE IF NOT EXISTS at_schedules (          -- scheduled messages to the project coordinator
  id          TEXT PRIMARY KEY,                    -- sc_…
  project_id  TEXT NOT NULL REFERENCES at_projects(id) ON DELETE CASCADE,
  name        TEXT NOT NULL,
  cron        TEXT NOT NULL,                       -- five fields: minute hour day-of-month month day-of-week
  timezone    TEXT NOT NULL DEFAULT 'UTC',         -- IANA name the cron is read in
  message     TEXT NOT NULL,
  enabled     BOOLEAN NOT NULL DEFAULT TRUE,
  next_run_at TIMESTAMPTZ,                         -- NULL while disabled
  last_run_at TIMESTAMPTZ,
  last_error  TEXT NOT NULL DEFAULT '',
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS at_schedules_project ON at_schedules (project_id, created_at);
CREATE INDEX IF NOT EXISTS at_schedules_due ON at_schedules (next_run_at) WHERE enabled;

-- +goose Down
DROP TABLE IF EXISTS at_schedules;
DROP TABLE IF EXISTS at_rate_limit_waits;
ALTER TABLE at_projects
  DROP COLUMN IF EXISTS icon,
  DROP COLUMN IF EXISTS coordinator_model,
  DROP COLUMN IF EXISTS thread_model,
  DROP COLUMN IF EXISTS thread_effort,
  DROP COLUMN IF EXISTS auto_resume;
