-- +goose Up
-- The user's shared folder (juicefs-shared-storage design): one JuiceFS volume per user. Its
-- metadata lives in a schema of the production database owned by a login role of the same name
-- (meta_role, also the schema), its data under <volume>/ in the shared bucket, reachable with an
-- object-storage account limited to that prefix (s3_access_key). The role's password and the
-- account's secret key are sealed together in secrets (credvault, AAD = user, volume, generation).
-- generation grows whenever the credentials change; nodes remount when it differs.
-- backend: platform (the platform's bucket and database, the default), s3 (the user's own bucket,
-- formatted by the console, metadata still in a jfs_ schema here) or juicefs (the user's own
-- formatted JuiceFS: only its metadata URL and password, sealed in secrets). summary is what the
-- settings page shows of it (endpoint, bucket, metadata host; never a secret).
CREATE TABLE at_user_fs (
  user_id       TEXT PRIMARY KEY REFERENCES at_users(id) ON DELETE CASCADE,
  volume        TEXT NOT NULL UNIQUE,
  meta_role     TEXT NOT NULL UNIQUE,
  s3_access_key TEXT NOT NULL UNIQUE,
  secrets       JSONB NOT NULL,
  backend       TEXT NOT NULL DEFAULT 'platform' CHECK (backend IN ('platform', 's3', 'juicefs')),
  summary       JSONB NOT NULL DEFAULT '{}',
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
