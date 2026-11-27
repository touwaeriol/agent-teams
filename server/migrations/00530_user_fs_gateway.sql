-- +goose Up
-- The shared folder became one JuiceFS volume served by the console's gateway, a bucket per user
-- (u-<16 hex>, a top-level folder of the volume) reached with the user's own gateway key. A row
-- keeps the user's bucket, key and sealed secret; the volume's credentials moved to at_settings
-- ('user_fs_volume'). Rows from before name volumes of their own, which are not used any more:
-- they go, and the automatic switch-on provisions the users' buckets.
DELETE FROM at_user_fs;
ALTER TABLE at_user_fs DROP CONSTRAINT at_user_fs_volume_key;
ALTER TABLE at_user_fs DROP COLUMN meta_role;
ALTER TABLE at_user_fs ADD COLUMN bucket TEXT NOT NULL UNIQUE;

-- +goose Down
DELETE FROM at_user_fs;
DELETE FROM at_settings WHERE key = 'user_fs_volume';
ALTER TABLE at_user_fs DROP COLUMN bucket;
ALTER TABLE at_user_fs ADD COLUMN meta_role TEXT NOT NULL UNIQUE;
ALTER TABLE at_user_fs ADD CONSTRAINT at_user_fs_volume_key UNIQUE (volume);
