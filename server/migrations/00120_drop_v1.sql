-- +goose Up
-- Drop what only the retired Python v1 platform used (docs/v2/data-model.md §3.1).
-- v1 has been off since 2026-11-15 and the read-only "旧对话" viewer is gone, so:
--   * v1 chat conversations go, with their messages, tasks, mail and questions
--     (ON DELETE CASCADE from at_conversations); only coordinator and thread
--     conversations remain, and kind has no default any more;
--   * at_teams (v1 YAML teams; role templates live in at_roles);
--   * at_conversations.history (v1 Claude session ids) and .plan (v1 plan; v2 uses at_main_tasks);
--   * at_tasks.output (v1 result text), folded into summary where summary is empty;
--   * at_users.platform_version (v1/v2 routing during the migration);
--   * at_projects.repo / branch / git_connection_id (moved to at_project_repos by 00070,
--     no longer read).

DELETE FROM at_conversations WHERE kind NOT IN ('coordinator', 'thread');
ALTER TABLE at_conversations
    DROP COLUMN IF EXISTS history,
    DROP COLUMN IF EXISTS plan,
    ALTER COLUMN kind DROP DEFAULT,
    ADD CONSTRAINT at_conversations_kind CHECK (kind IN ('coordinator', 'thread'));

DROP TABLE IF EXISTS at_teams;

UPDATE at_tasks SET summary = output WHERE summary = '' AND output <> '';
ALTER TABLE at_tasks DROP COLUMN IF EXISTS output;

ALTER TABLE at_users DROP COLUMN IF EXISTS platform_version;

ALTER TABLE at_projects
    DROP COLUMN IF EXISTS repo,
    DROP COLUMN IF EXISTS branch,
    DROP COLUMN IF EXISTS git_connection_id;

-- +goose Down
-- Not reversible: the v1 conversations and the dropped columns' data are gone, and no
-- code reads them any more. Restore from a database backup if v1 data is ever needed.
SELECT 1;
