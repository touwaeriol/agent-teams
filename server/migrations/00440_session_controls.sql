-- +goose Up
-- Session controls (#53): the user of a session (多仓库会话) switches its permission mode, model
-- and thinking effort while it runs. The pick is the session's own, kept on its employee so that a
-- reopened session keeps it (the role is left alone): session_mode is a session.set_mode value
-- ('' = the session's initial mode), session_effort an effort level ('' = project / role setting);
-- the model goes into the existing at_employees.model. at_agent_sessions.controls holds what the
-- live session runs (model, effort), the switch waiting for session.mode_set and the last failure.
ALTER TABLE at_employees ADD COLUMN IF NOT EXISTS session_mode TEXT NOT NULL DEFAULT '';
ALTER TABLE at_employees ADD COLUMN IF NOT EXISTS session_effort TEXT NOT NULL DEFAULT '';
ALTER TABLE at_agent_sessions ADD COLUMN IF NOT EXISTS controls JSONB NOT NULL DEFAULT '{}'::jsonb;

-- +goose Down
ALTER TABLE at_agent_sessions DROP COLUMN IF EXISTS controls;
ALTER TABLE at_employees DROP COLUMN IF EXISTS session_effort;
ALTER TABLE at_employees DROP COLUMN IF EXISTS session_mode;
