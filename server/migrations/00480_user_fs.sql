-- +goose Up
-- The user's shared folder (juicefs-shared-storage design): one JuiceFS volume per user. Its
-- metadata lives in a schema of the production database owned by a login role of the same name
-- (meta_role, also the schema), its data under <volume>/ in the shared bucket, reachable with an
-- object-storage account limited to that prefix (s3_access_key). The role's password and the
-- account's secret key are sealed together in secrets (credvault, AAD = user, volume, generation).
-- generation grows whenever the credentials change; nodes remount when it differs.
CREATE TABLE at_user_fs (
  user_id       TEXT PRIMARY KEY REFERENCES at_users(id) ON DELETE CASCADE,
  volume        TEXT NOT NULL UNIQUE,
  meta_role     TEXT NOT NULL UNIQUE,
  s3_access_key TEXT NOT NULL UNIQUE,
  secrets       JSONB NOT NULL,
  generation    INT  NOT NULL DEFAULT 1,
  state         TEXT NOT NULL DEFAULT 'provisioning' CHECK (state IN ('provisioning', 'ready', 'error', 'deleting')),
  error         TEXT NOT NULL DEFAULT '',
  capacity_gb   INT  NOT NULL,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Last fs.status of each node ({} = never reported): the node page shows it, session.open sends a
-- shared folder only to a node whose state is mounted.
ALTER TABLE at_nodes ADD COLUMN fs_status JSONB NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE at_nodes DROP COLUMN fs_status;
DROP TABLE at_user_fs;
