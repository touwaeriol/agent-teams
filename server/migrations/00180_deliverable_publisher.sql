-- +goose Up
-- Who published each version of a hosted deliverable (data-model.md §2.8): any employee of the
-- project may publish the next version (read_deliverable → edit → publish_file with the id), so the
-- version's employee id is kept next to its name; comments on a version go to that employee, else
-- to the lead of that employee's thread. '' for versions published before this migration.
ALTER TABLE at_deliverable_files ADD COLUMN IF NOT EXISTS publisher_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE at_deliverable_files DROP COLUMN IF EXISTS publisher_id;
