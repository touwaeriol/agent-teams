-- +goose Up
-- The user's message that led the coordinator to open the thread (the last user message of the
-- project chat before open_thread, or the first forwarded user message). The thread panel shows
-- it with the coordinator's brief; its images and PDFs go to the lead with the brief. NULL for
-- threads the user opened by hand, older threads and when the message is gone.
ALTER TABLE at_threads ADD COLUMN IF NOT EXISTS source_message_id BIGINT
    REFERENCES at_messages(id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE at_threads DROP COLUMN IF EXISTS source_message_id;
