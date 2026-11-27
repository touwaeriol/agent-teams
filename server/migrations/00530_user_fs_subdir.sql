-- +goose Up
-- The shared folder became one JuiceFS volume with a folder per user (users/<16 hex>/, mounted by
-- the user's nodes with --subdir): a user's row keeps its folder, state and quota; the volume's
-- credentials (metadata role password, object-store secret key) moved to at_settings
-- ('user_fs_volume'), shared by every user. Rows from before name their own volumes, which are
-- not used any more: they are marked failed, so the automatic switch-on provisions the folder.
ALTER TABLE at_user_fs DROP CONSTRAINT at_user_fs_volume_key;
ALTER TABLE at_user_fs DROP COLUMN meta_role, DROP COLUMN s3_access_key, DROP COLUMN secrets;
ALTER TABLE at_user_fs ADD COLUMN subdir TEXT NOT NULL DEFAULT '';
UPDATE at_user_fs SET subdir = 'users/' || left(encode(sha256(convert_to('agent-teams-user-fs|' || user_id, 'UTF8')), 'hex'), 16),
  state = 'error', error = '共享文件夹改为单卷，需要重新开通', updated_at = now() - interval '1 day';
ALTER TABLE at_user_fs ALTER COLUMN subdir DROP DEFAULT;
ALTER TABLE at_user_fs ADD CONSTRAINT at_user_fs_subdir_key UNIQUE (subdir);

-- +goose Down
-- Per-user volumes need their own credentials, which are gone: the rows go too.
DELETE FROM at_user_fs;
DELETE FROM at_settings WHERE key = 'user_fs_volume';
ALTER TABLE at_user_fs DROP COLUMN subdir;
ALTER TABLE at_user_fs ADD COLUMN meta_role TEXT NOT NULL UNIQUE, ADD COLUMN s3_access_key TEXT NOT NULL UNIQUE,
  ADD COLUMN secrets JSONB NOT NULL;
ALTER TABLE at_user_fs ADD CONSTRAINT at_user_fs_volume_key UNIQUE (volume);
