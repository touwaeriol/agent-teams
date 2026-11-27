-- +goose Up
-- Background jobs (the admin console's 后台任务 page; they run as River job_run jobs): one row per
-- task, built in (registered by the code, not deletable) or added by the system later, with the
-- administrator's controls (paused, an interval or a cron schedule replacing the registered
-- interval, a run asked for) and its last run;
-- at_job_runs keeps the runs that did something or failed.
CREATE TABLE at_jobs (
  name             TEXT PRIMARY KEY,
  kind             TEXT NOT NULL DEFAULT 'builtin' CHECK (kind IN ('builtin', 'added')),
  paused           BOOLEAN NOT NULL DEFAULT false,
  every_seconds    INT CHECK (every_seconds IS NULL OR every_seconds > 0),
  cron             TEXT NOT NULL DEFAULT '',
  timezone         TEXT NOT NULL DEFAULT '',
  run_requested_at TIMESTAMPTZ,
  running_since    TIMESTAMPTZ,
  last_started_at  TIMESTAMPTZ,
  last_finished_at TIMESTAMPTZ,
  last_ok          BOOLEAN,
  last_summary     TEXT NOT NULL DEFAULT '',
  last_error       TEXT NOT NULL DEFAULT '',
  last_replica     TEXT NOT NULL DEFAULT '',
  runs             BIGINT NOT NULL DEFAULT 0,
  failures         BIGINT NOT NULL DEFAULT 0,
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE at_job_runs (
  id          BIGSERIAL PRIMARY KEY,
  job         TEXT NOT NULL,
  replica     TEXT NOT NULL DEFAULT '',
  trigger     TEXT NOT NULL DEFAULT '',
  started_at  TIMESTAMPTZ NOT NULL,
  finished_at TIMESTAMPTZ NOT NULL,
  ok          BOOLEAN NOT NULL,
  summary     TEXT NOT NULL DEFAULT '',
  error       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX at_job_runs_job ON at_job_runs (job, started_at DESC);

-- +goose Down
DROP TABLE at_job_runs;
DROP TABLE at_jobs;
