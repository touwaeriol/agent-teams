-- +goose Up
-- Each repository bound to a project records the branch threads start from and open pull
-- requests against (at_project_repos.base_branch). It is looked up when the repository is added
-- (the upstream's default branch, or the one the user picked) and editable in project settings.
-- Before, the platform asked GitHub on every thread start and quietly used "main" when that
-- failed, so a repository whose default branch is develop could never start a thread.
ALTER TABLE at_project_repos ADD COLUMN IF NOT EXISTS base_branch TEXT NOT NULL DEFAULT '';

-- A base branch written into the project context, first: it is what the user asked for. 00070
-- wrote "仓库 <repo> 的基准分支：<branch>"; users copying it by hand also wrote a half-width colon,
-- other case or a trailing full stop, so the match is loose and keeps only git ref characters.
UPDATE at_project_repos r SET base_branch = m.base
FROM (
    SELECT r2.project_id, r2.repo,
           (regexp_match(p.context,
               '仓库\s*' || regexp_replace(r2.repo, '([.^$*+?()\[\]{}|\\/-])', '\\\1', 'g') || '\s*的?\s*基准分支\s*[：:]?\s*([A-Za-z0-9._/-]+)',
               'i'))[1] AS base
    FROM at_project_repos r2 JOIN at_projects p ON p.id = r2.project_id
) m
WHERE m.project_id = r.project_id AND m.repo = r.repo AND r.base_branch = ''
  AND m.base IS NOT NULL AND m.base NOT LIKE '%.' AND m.base NOT LIKE '%/';

-- Standalone repositories: the default branch saved by their last connection test.
UPDATE at_project_repos r SET base_branch = g.default_branch
FROM at_git_repos g
WHERE r.git_repo_id = g.id AND r.base_branch = '' AND g.default_branch <> '';

-- GitHub repositories still without one are filled by the control plane when it starts
-- (orchestrator.WarmAll asks GitHub for each one's default branch).

-- +goose Down
ALTER TABLE at_project_repos DROP COLUMN IF EXISTS base_branch;
