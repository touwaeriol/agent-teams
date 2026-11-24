-- +goose Up
-- Per-user scheduling thresholds (shared-members.md §4.3): a node whose 5-hour window has used at
-- least quota_five_hour_pct percent, or any weekly window at least quota_weekly_pct, gets no new
-- session while another candidate node is under them. NULL = the built-in default (85 / 95).
ALTER TABLE at_users
    ADD COLUMN IF NOT EXISTS quota_five_hour_pct SMALLINT CHECK (quota_five_hour_pct BETWEEN 1 AND 100),
    ADD COLUMN IF NOT EXISTS quota_weekly_pct SMALLINT CHECK (quota_weekly_pct BETWEEN 1 AND 100);

-- +goose Down
ALTER TABLE at_users DROP COLUMN IF EXISTS quota_five_hour_pct, DROP COLUMN IF EXISTS quota_weekly_pct;
