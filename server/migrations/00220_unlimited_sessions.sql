-- +goose Up
-- Threads and node sessions are no longer capped by default (0 / NULL = no limit): a thread runs as
-- soon as it opens and a node starts every session it is given. Limits stored so far (mostly the
-- old defaults that the settings page saved back) are cleared; an administrator can set one again.
UPDATE at_settings SET value = jsonb_set(value, '{max_running_threads}', '0')
 WHERE key = 'platform' AND value ? 'max_running_threads';
UPDATE at_users SET max_running_threads = NULL WHERE max_running_threads IS NOT NULL;
UPDATE at_nodes SET max_active_sessions = NULL WHERE max_active_sessions IS NOT NULL;

-- +goose Down
-- The cleared limits are not kept anywhere, so there is nothing to restore.
SELECT 1;
