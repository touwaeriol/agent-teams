-- +goose Up
-- A thread whose end is due waits for its lead's turn (threads.md "结束"):
--   at_threads.done_pending_at   when the end became due while the lead was in a turn (or after
--                                finish / the platform's prompt): the lead's next turn end moves
--                                the thread to done; past the grace period it ends regardless
--   at_threads.finish_nudged_at  when the lead was prompted to write its final report and call
--                                finish (sent once per thread)
ALTER TABLE at_threads ADD COLUMN done_pending_at TIMESTAMPTZ, ADD COLUMN finish_nudged_at TIMESTAMPTZ;
CREATE INDEX at_threads_done_pending ON at_threads (done_pending_at) WHERE done_pending_at IS NOT NULL;

-- +goose Down
DROP INDEX at_threads_done_pending;
ALTER TABLE at_threads DROP COLUMN finish_nudged_at, DROP COLUMN done_pending_at;
