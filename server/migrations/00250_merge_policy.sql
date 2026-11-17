-- +goose Up
-- How a thread with pull requests ends (architecture.md §8.4) is a choice of three instead of
-- require_acceptance: pr (default) — done once the PRs' checks pass, the PRs stay open for a
-- person to merge; auto — merged into the base once the checks pass; manual — the user accepts.
-- Projects on the old default (acceptance required) move to pr, the others keep merging on their own.
ALTER TABLE at_projects ADD COLUMN merge_policy TEXT NOT NULL DEFAULT 'pr'
    CHECK (merge_policy IN ('pr', 'auto', 'manual'));
UPDATE at_projects SET merge_policy = CASE WHEN require_acceptance THEN 'pr' ELSE 'auto' END;
ALTER TABLE at_projects DROP COLUMN require_acceptance;

-- +goose Down
ALTER TABLE at_projects ADD COLUMN require_acceptance BOOLEAN NOT NULL DEFAULT TRUE;
UPDATE at_projects SET require_acceptance = merge_policy <> 'auto';
ALTER TABLE at_projects DROP COLUMN merge_policy;
