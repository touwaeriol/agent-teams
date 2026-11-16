-- +goose Up
-- When the platform last sent a thread's lead the "nothing is moving" note after a node
-- daemon reconnect. The note goes out once per stall: again only after something happened
-- in the thread since (a user message, a task, plan, merge or PR change, a resume), and
-- never twice within ten minutes (reconnect bursts).
ALTER TABLE at_threads ADD COLUMN IF NOT EXISTS stalled_note_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE at_threads DROP COLUMN IF EXISTS stalled_note_at;
