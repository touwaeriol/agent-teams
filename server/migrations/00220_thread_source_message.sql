-- +goose Up
-- source_message_id: the user's project-chat message the coordinator opened the thread for
-- (open_thread's message_id, else the user's newest message delivered to the coordinator after
-- its previous reply; scheduled-task posts never count). The thread panel shows it with the
-- brief and its images and PDFs go to the lead with the brief. NULL for threads the user opened
-- by hand, threads opened by a forward, older threads, when no message qualifies and when the
-- message is gone.
ALTER TABLE at_threads ADD COLUMN IF NOT EXISTS source_message_id BIGINT
    REFERENCES at_messages(id) ON DELETE SET NULL;
-- forwarded_brief: the brief is the user's forwarded messages (forward into a new thread); their
-- copies are already in the thread conversation, so the panel shows no separate dispatch.
ALTER TABLE at_threads ADD COLUMN IF NOT EXISTS forwarded_brief BOOLEAN NOT NULL DEFAULT FALSE;
UPDATE at_threads SET forwarded_brief = TRUE
 WHERE brief LIKE '[用户把用户在项目对话里说的 %' OR brief LIKE '[项目统筹把用户在项目对话里说的 %';

-- +goose Down
ALTER TABLE at_threads DROP COLUMN IF EXISTS forwarded_brief;
ALTER TABLE at_threads DROP COLUMN IF EXISTS source_message_id;
