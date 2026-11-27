-- +goose Up
-- The shared folder moved to the object store directly (no JuiceFS): one bucket of the platform's
-- store (Backblaze B2, or a RustFS / MinIO) holds every user's folder under its own prefix, and
-- each user has a key of that store limited to the prefix (nodes mount it with geesefs). A row
-- keeps the user's bucket and prefix, its key's id (s3_access_key, empty until the key is issued)
-- and sealed secret, and read_only: the folder is over its capacity and its key cannot write.
-- Rows from before name JuiceFS volumes, which are not used any more: they go, and the automatic
-- switch-on provisions the users' folders again.
DELETE FROM at_user_fs;
ALTER TABLE at_user_fs DROP COLUMN volume, DROP COLUMN meta_role, DROP CONSTRAINT at_user_fs_s3_access_key_key;
ALTER TABLE at_user_fs
  ADD COLUMN bucket TEXT NOT NULL,
  ADD COLUMN prefix TEXT NOT NULL,
  ADD COLUMN read_only BOOLEAN NOT NULL DEFAULT false,
  ADD CONSTRAINT at_user_fs_location UNIQUE (bucket, prefix);

-- +goose Down
DELETE FROM at_user_fs;
ALTER TABLE at_user_fs DROP CONSTRAINT at_user_fs_location, DROP COLUMN bucket, DROP COLUMN prefix, DROP COLUMN read_only;
ALTER TABLE at_user_fs
  ADD COLUMN volume TEXT NOT NULL UNIQUE,
  ADD COLUMN meta_role TEXT NOT NULL UNIQUE,
  ADD CONSTRAINT at_user_fs_s3_access_key_key UNIQUE (s3_access_key);
