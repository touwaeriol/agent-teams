-- +goose Up
-- Why the platform could not merge a thread's pull requests after the user accepted them
-- (checks failed or still running, a conflict, no permission…). Shown on the acceptance card
-- so the user can retry; cleared when the user accepts or rejects again and when the PRs merge.
ALTER TABLE at_threads ADD COLUMN IF NOT EXISTS merge_error TEXT;

-- +goose Down
ALTER TABLE at_threads DROP COLUMN IF EXISTS merge_error;
