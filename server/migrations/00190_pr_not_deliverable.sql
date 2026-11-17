-- +goose Up
-- Pull requests are no longer deliverables (data-model.md §2.8): they live on the thread
-- (at_thread_repos.pr_*). The CI checks of a pull request count from the time it was opened,
-- which used to be the created_at of its kind='pr' deliverable; that time moves here, then the
-- pr deliverables go (their files and comments cascade).
ALTER TABLE at_thread_repos ADD COLUMN IF NOT EXISTS pr_opened_at TIMESTAMPTZ;

UPDATE at_thread_repos tr SET pr_opened_at = d.opened
  FROM (SELECT thread_id, url, max(created_at) AS opened FROM at_deliverables
         WHERE kind = 'pr' AND thread_id IS NOT NULL GROUP BY thread_id, url) d
 WHERE tr.pr_opened_at IS NULL AND tr.thread_id = d.thread_id AND tr.pr_url = d.url;

DELETE FROM at_deliverables WHERE kind = 'pr';

-- +goose Down
-- The deleted pr deliverables are not restored.
ALTER TABLE at_thread_repos DROP COLUMN IF EXISTS pr_opened_at;
