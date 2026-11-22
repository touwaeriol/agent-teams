-- +goose Up
-- Subthreads (threads.md "子讨论串"): a lead's open_subthread records the opening thread in
-- parent_id; one level only (a subthread has no subthreads), checked by the tool. Deleting the
-- parent keeps the subthread as a top-level thread. opened_by gains 'lead'.
ALTER TABLE at_threads ADD COLUMN parent_id TEXT REFERENCES at_threads(id) ON DELETE SET NULL;
CREATE INDEX at_threads_parent ON at_threads (parent_id) WHERE parent_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS at_threads_parent;
ALTER TABLE at_threads DROP COLUMN IF EXISTS parent_id;
