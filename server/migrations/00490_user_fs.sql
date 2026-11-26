-- +goose Up
-- The user's shared folder (juicefs-shared-storage design): one JuiceFS volume per user. Its
-- metadata lives in a schema of the production database owned by a login role of the same name
-- (meta_role, also the schema), its data under <volume>/ in the shared bucket, reachable with an
-- object-storage account limited to that prefix (s3_access_key). The role's password and the
-- account's secret key are sealed together in secrets (credvault, AAD = user, volume, generation).
-- generation grows whenever the credentials change; nodes remount when it differs. It comes from a
-- sequence, so a volume switched off and on again (same deterministic name) never repeats a
-- generation a node may still have mounted.
CREATE SEQUENCE at_user_fs_generation;
CREATE TABLE at_user_fs (
  user_id       TEXT PRIMARY KEY REFERENCES at_users(id) ON DELETE CASCADE,
  volume        TEXT NOT NULL UNIQUE,
  meta_role     TEXT NOT NULL UNIQUE,
  s3_access_key TEXT NOT NULL UNIQUE,
  secrets       JSONB NOT NULL,
  generation    BIGINT NOT NULL DEFAULT nextval('at_user_fs_generation'),
  state         TEXT NOT NULL DEFAULT 'provisioning' CHECK (state IN ('provisioning', 'ready', 'error', 'deleting')),
  error         TEXT NOT NULL DEFAULT '',
  capacity_gb   INT  NOT NULL,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Last fs.status of each node ({} = never reported): the node page shows it, session.open sends a
-- shared folder only to a node whose state is mounted.
ALTER TABLE at_nodes ADD COLUMN fs_status JSONB NOT NULL DEFAULT '{}';

-- Deliverable versions published while the owner's shared folder is on live there
-- (projects/<pid>/.deliverables/<id>/v<n>/<path>): their rows keep the metadata, without data.
ALTER TABLE at_deliverable_files ADD COLUMN in_volume BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE at_deliverable_files ALTER COLUMN data DROP NOT NULL;
ALTER TABLE at_deliverable_files ADD CONSTRAINT at_deliverable_files_data CHECK (in_volume OR data IS NOT NULL);

-- +goose Down
-- Files kept only in a volume cannot come back into the database: their rows go.
DELETE FROM at_deliverable_files WHERE in_volume;
ALTER TABLE at_deliverable_files DROP CONSTRAINT at_deliverable_files_data;
ALTER TABLE at_deliverable_files ALTER COLUMN data SET NOT NULL;
ALTER TABLE at_deliverable_files DROP COLUMN in_volume;
ALTER TABLE at_nodes DROP COLUMN fs_status;
DROP TABLE at_user_fs;
DROP SEQUENCE at_user_fs_generation;
