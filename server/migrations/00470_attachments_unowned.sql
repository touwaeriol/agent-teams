-- +goose Up
-- #58 D6: attachments in more places.
--
-- Attachments without a project: the first message of a new plain session is written before
-- its project exists (POST /api/uploads). Such a row has project_id NULL and its object lives at
-- users/<user_id>/attachments/<id>; POST /api/sessions moves it into the new project (the key
-- stays). Unowned rows expire like any other (expires_at, blobgc). The partial index serves
-- blobgc's "is this users/ key still referenced" check.
ALTER TABLE at_attachments ALTER COLUMN project_id DROP NOT NULL;
CREATE INDEX at_attachments_user_keys ON at_attachments (storage_key) WHERE storage_key LIKE 'users/%';

-- A session's first message is delivered when its thread starts (maybe later, within the running
-- limit): the attachments the user gave it wait here ([] = none).
ALTER TABLE at_threads ADD COLUMN brief_attachments JSONB NOT NULL DEFAULT '[]';

-- Attachments of a comment on a deliverable (core.AttachmentRef, as in a message's meta).
ALTER TABLE at_deliverable_comments ADD COLUMN attachments JSONB NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE at_deliverable_comments DROP COLUMN attachments;
ALTER TABLE at_threads DROP COLUMN brief_attachments;
DROP INDEX at_attachments_user_keys;
-- Unowned attachments cannot stay: their objects go with blobgc's users/ cleanup.
INSERT INTO at_blob_deletions (key) SELECT storage_key FROM at_attachments WHERE project_id IS NULL AND storage_key IS NOT NULL
    ON CONFLICT (key) DO NOTHING;
DELETE FROM at_attachments WHERE project_id IS NULL;
ALTER TABLE at_attachments ALTER COLUMN project_id SET NOT NULL;
