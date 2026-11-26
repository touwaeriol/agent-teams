-- +goose Up
-- Each repository bound to a project records the branch threads start from and open pull
-- requests against (at_project_repos.base_branch). It is looked up when the repository is added
-- (the upstream's default branch, or the one the user picked) and editable in project settings.
-- Before, the platform asked GitHub on every thread start and quietly used "main" when that
-- failed, so a repository whose default branch is develop could never start a thread.
ALTER TABLE at_project_repos ADD COLUMN IF NOT EXISTS base_branch TEXT NOT NULL DEFAULT '';

-- Standalone repositories: the default branch saved by their last connection test.
UPDATE at_project_repos r SET base_branch = g.default_branch
FROM at_git_repos g
WHERE r.git_repo_id = g.id AND r.base_branch = '' AND g.default_branch <> '';

-- A base branch 00070 wrote into the project context ("仓库 <repo> 的基准分支：<branch>").
UPDATE at_project_repos r SET base_branch = substring(
    substring(p.context FROM position(('仓库 ' || r.repo || ' 的基准分支：') IN p.context)
                           + char_length('仓库 ' || r.repo || ' 的基准分支：'))
    FROM '^[^[:space:]]+')
FROM at_projects p
WHERE p.id = r.project_id AND r.base_branch = ''
  AND position(('仓库 ' || r.repo || ' 的基准分支：') IN p.context) > 0;

-- GitHub repositories still without one are filled by the control plane when it starts
-- (orchestrator.WarmAll asks GitHub for each one's default branch).

-- +goose Down
ALTER TABLE at_project_repos DROP COLUMN IF EXISTS base_branch;
