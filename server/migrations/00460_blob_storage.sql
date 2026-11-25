-- +goose Up
-- #58: attachments and shared files move to the object store (RustFS, S3-compatible; design
-- dl_01m89bx7jg5wdj464tkyhqp7ja §2). This one migration carries every schema change of the
-- feature (D2 adds none). Only metadata changes and constant defaults: no table holding BYTEA
-- is rewritten (size stays INT on at_attachments: 200 MB fits).
--
-- Bytes: a row whose storage_key is NULL still keeps its bytes in data (rows from before #58,
-- until `atctl storage migrate` copies them); storage_key set = the object holds the bytes. data
-- is cleared only by `atctl storage purge-data` after `atctl storage verify`; a later migration
-- drops the column.

-- Attachments.
--   sha256      NULL until the blob_hash job has streamed the object (uploads finished after #58)
--   state       uploading (multipart upload open, upload_id set) | ready | failed | expired
--               (expired: the retention ran out, bytes and object deleted, the row stays so
--               messages can show "已过期" and reads answer 410)
--   upload_id   S3 multipart upload id while uploading
--   expires_at  retention (user decision #58: 24 h after the last use, AGENT_TEAMS_ATTACHMENT_TTL):
--               set on upload, pushed to now()+TTL by every use (send, forward, assign, node or
--               page download…, core.TouchAttachments); blobgc expires rows past it. Existing
--               rows get now()+24h once (a stable default is evaluated once: no table rewrite).
ALTER TABLE at_attachments
    ALTER COLUMN data DROP NOT NULL,
    ALTER COLUMN sha256 DROP NOT NULL,
    ADD COLUMN storage_key TEXT,
    ADD COLUMN state TEXT NOT NULL DEFAULT 'ready' CHECK (state IN ('uploading', 'ready', 'failed', 'expired')),
    ADD COLUMN upload_id TEXT,
    ADD COLUMN expires_at TIMESTAMPTZ NOT NULL DEFAULT now() + interval '24 hours';
CREATE INDEX at_attachments_expiry ON at_attachments (expires_at) WHERE state IN ('ready', 'failed');
CREATE INDEX at_attachments_unhashed ON at_attachments (created_at) WHERE sha256 IS NULL AND state = 'ready';

-- Shared files.
--   object_id   sf_… id of the object (key projects/<pid>/shared/<object_id>); a new upload of a
--               path gets a new id, the old object goes to at_blob_deletions
--   sha256      NULL until the blob_hash job has streamed the object; read_shared_file then only
--               checks the size
--   state       ready | failed (the hash did not match what the uploader declared: the row stays
--               so the uploader and the page can see it, reads are refused)
ALTER TABLE at_shared_files
    ALTER COLUMN data DROP NOT NULL,
    ALTER COLUMN sha256 DROP NOT NULL,
    ADD COLUMN storage_key TEXT,
    ADD COLUMN object_id TEXT,
    ADD COLUMN declared_sha256 TEXT,
    ADD COLUMN state TEXT NOT NULL DEFAULT 'ready' CHECK (state IN ('ready', 'failed'));
CREATE INDEX at_shared_files_unhashed ON at_shared_files (updated_at) WHERE sha256 IS NULL AND state = 'ready';

-- Uploads of shared files in flight (browser multipart, or a node's single presigned PUT): the
-- path only changes when the upload completes, so readers keep seeing the old file until then.
--   id            up_… (the API's upload_id)
--   s3_upload_id  S3 multipart upload id; NULL for a single PUT
--   declared_sha256  what the uploader said (nodes send it; browsers may not)
CREATE TABLE at_shared_uploads (
    id                 TEXT PRIMARY KEY,
    project_id         TEXT NOT NULL REFERENCES at_projects(id) ON DELETE CASCADE,
    path               TEXT NOT NULL,
    object_id          TEXT NOT NULL,
    storage_key        TEXT NOT NULL,
    s3_upload_id       TEXT,
    size               BIGINT NOT NULL,
    content_type       TEXT NOT NULL,
    declared_sha256    TEXT,
    overwrite          BOOLEAN NOT NULL DEFAULT false,
    uploaded_by        TEXT NOT NULL,
    uploader_id        TEXT REFERENCES at_employees(id) ON DELETE SET NULL,
    uploader_thread_id TEXT REFERENCES at_threads(id) ON DELETE SET NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX at_shared_uploads_project ON at_shared_uploads (project_id, created_at);

-- Attachments a task carries (assign_task attachments=[…]): [{id, name, content_type, size}].
ALTER TABLE at_tasks ADD COLUMN attachments JSONB NOT NULL DEFAULT '[]';

-- Objects to delete after the deleting transaction commits (blob_gc retries failures).
--   prefix  true = every object under key (a deleted project's projects/<pid>/)
CREATE TABLE at_blob_deletions (
    key        TEXT PRIMARY KEY,
    prefix     BOOLEAN NOT NULL DEFAULT false,
    attempts   INT NOT NULL DEFAULT 0,
    last_error TEXT,
    next_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX at_blob_deletions_next ON at_blob_deletions (next_at);

-- +goose Down
-- Refuses while any bytes live only in the object store (run `atctl storage restore` first).
-- Expired attachments (their bytes are gone for good, by the retention rule) and uploads that
-- never finished are deleted first: the schema before #58 has no way to hold them. Messages that
-- referenced an expired attachment then show it as missing instead of "已过期".
DELETE FROM at_attachments WHERE state = 'expired' OR (state = 'uploading' AND data IS NULL);
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM at_attachments WHERE data IS NULL) OR EXISTS (SELECT 1 FROM at_shared_files WHERE data IS NULL) THEN
        RAISE EXCEPTION 'rows without data: run atctl storage restore before migrating down';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE at_blob_deletions;
ALTER TABLE at_tasks DROP COLUMN attachments;
DROP TABLE at_shared_uploads;
DROP INDEX at_shared_files_unhashed;
UPDATE at_shared_files SET sha256 = '' WHERE sha256 IS NULL;
ALTER TABLE at_shared_files
    DROP COLUMN state, DROP COLUMN declared_sha256, DROP COLUMN object_id, DROP COLUMN storage_key,
    ALTER COLUMN sha256 SET NOT NULL,
    ALTER COLUMN data SET NOT NULL;
DROP INDEX at_attachments_unhashed;
DROP INDEX at_attachments_expiry;
UPDATE at_attachments SET sha256 = '' WHERE sha256 IS NULL;
ALTER TABLE at_attachments
    DROP COLUMN expires_at, DROP COLUMN upload_id, DROP COLUMN state, DROP COLUMN storage_key,
    ALTER COLUMN sha256 SET NOT NULL,
    ALTER COLUMN data SET NOT NULL;
