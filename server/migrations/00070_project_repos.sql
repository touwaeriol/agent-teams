-- +goose Up
-- A project binds several repositories (docs/v2/data-model.md §2.3a). The old
-- at_projects.repo / branch / git_connection_id columns stay for rollback but are
-- no longer read: base branches are written in the project context instead, and a
-- thread branches each repository lazily (at_thread_repos).

CREATE TABLE IF NOT EXISTS at_project_repos (
    project_id        TEXT NOT NULL REFERENCES at_projects(id) ON DELETE CASCADE,
    repo              TEXT NOT NULL,
    git_connection_id TEXT REFERENCES at_git_connections(id) ON DELETE SET NULL,
    position          INT  NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, repo)
);
CREATE INDEX IF NOT EXISTS at_project_repos_order ON at_project_repos (project_id, position);

INSERT INTO at_project_repos (project_id, repo, git_connection_id, position, created_at)
SELECT id, regexp_replace(regexp_replace(trim(repo), '^.*github\.com[/:]', ''), '\.git$', ''), git_connection_id, 0, created_at
FROM at_projects WHERE repo IS NOT NULL AND trim(repo) <> ''
ON CONFLICT DO NOTHING;

-- The base branch moves into the project context. Without GitHub at migration time the
-- usual default names are taken as "the default branch"; any other branch is written down.
UPDATE at_projects SET context = trim(BOTH E'\n' FROM context || E'\n\n' ||
    '仓库 ' || regexp_replace(regexp_replace(trim(repo), '^.*github\.com[/:]', ''), '\.git$', '') || ' 的基准分支：' || branch)
WHERE repo IS NOT NULL AND trim(repo) <> '' AND branch <> '' AND branch NOT IN ('main', 'master')
  AND position(('的基准分支：' || branch) IN context) = 0;

-- Per thread and repository: the base the thread branch was (or will be) cut from, whether
-- the thread branch exists on GitHub, and that repository's pull request.
CREATE TABLE IF NOT EXISTS at_thread_repos (
    thread_id      TEXT NOT NULL REFERENCES at_threads(id) ON DELETE CASCADE,
    repo           TEXT NOT NULL,
    base_branch    TEXT NOT NULL DEFAULT '',
    branch_created BOOLEAN NOT NULL DEFAULT FALSE,
    pr_number      INT,
    pr_url         TEXT,
    pr_state       TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (thread_id, repo)
);
CREATE INDEX IF NOT EXISTS at_thread_repos_pr ON at_thread_repos (pr_url) WHERE pr_url IS NOT NULL;

-- Existing threads worked in the project's single repository; started ones already have
-- their thread branch (it used to be created when the thread started).
INSERT INTO at_thread_repos (thread_id, repo, base_branch, branch_created, pr_number, pr_url, pr_state)
SELECT t.id, r.repo, t.base_branch, t.started_at IS NOT NULL, t.pr_number, t.pr_url, t.pr_state
FROM at_threads t JOIN at_project_repos r ON r.project_id = t.project_id
ON CONFLICT DO NOTHING;

ALTER TABLE at_merge_queue ADD COLUMN IF NOT EXISTS repo TEXT NOT NULL DEFAULT '';
UPDATE at_merge_queue q SET repo = r.repo FROM at_threads t JOIN at_project_repos r ON r.project_id = t.project_id
WHERE t.id = q.thread_id AND q.repo = '';

-- Which repositories a member's completed task pushed (repo → head sha).
ALTER TABLE at_tasks ADD COLUMN IF NOT EXISTS pushed JSONB NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE at_tasks DROP COLUMN IF EXISTS pushed;
ALTER TABLE at_merge_queue DROP COLUMN IF EXISTS repo;
DROP TABLE IF EXISTS at_thread_repos;
DROP TABLE IF EXISTS at_project_repos;
