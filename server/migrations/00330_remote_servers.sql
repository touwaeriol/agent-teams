-- +goose Up
-- Remote server access (讨论串 #38, remote-servers.md; data-model.md §2.14). The user keeps servers in
-- their settings (owned by at_users, not by a project); a project picks some of them and grants each
-- to roles. Sessions never see a credential: the console's SSH gateway logs in for them.
--
-- Credentials are envelope-encrypted (server/internal/credvault): AES-256-GCM under a random per-row
-- DEK whose AAD binds user_id, server id, auth_kind, host, port and username, so a credential copied
-- to another row or left behind after host/port/username/auth_kind change no longer opens; the DEK is
-- wrapped with a KEK derived from AGENT_TEAMS_SECRET_KEYS (kek_id names which one, for atctl admin
-- reseal). The plaintext never leaves the console: no API returns it, it can only be replaced.
CREATE TABLE at_remote_servers (
  id                    TEXT PRIMARY KEY,                                   -- srv_…
  user_id               TEXT NOT NULL REFERENCES at_users(id) ON DELETE CASCADE,
  name                  TEXT NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9-]{0,62}$'), -- ssh Host alias in sessions
  host                  TEXT NOT NULL CHECK (host <> ''),
  port                  INT  NOT NULL DEFAULT 22 CHECK (port BETWEEN 1 AND 65535),
  username              TEXT NOT NULL CHECK (username <> ''),
  auth_kind             TEXT NOT NULL CHECK (auth_kind IN ('password', 'private_key')),
  cred_ciphertext       BYTEA NOT NULL,                                     -- AES-256-GCM(DEK, JSON {password} | {private_key, passphrase?})
  cred_nonce            BYTEA NOT NULL,
  wrapped_dek           BYTEA NOT NULL,                                     -- AES-256-GCM(KEK, DEK)
  dek_nonce             BYTEA NOT NULL,
  kek_id                TEXT  NOT NULL,
  has_passphrase        BOOLEAN NOT NULL DEFAULT FALSE,                     -- private_key: the key has a passphrase
  key_fingerprint       TEXT NOT NULL DEFAULT '',                           -- private_key: SHA256:… of its public key
  host_key              TEXT NOT NULL DEFAULT '',                           -- confirmed target host key (authorized_keys line); '' = not confirmed
  host_key_fp           TEXT NOT NULL DEFAULT '',                           -- SHA256:…
  host_key_mac          BYTEA,                                              -- HMAC over (id, host, port, host_key) with a KEK-derived key; the gateway trusts only a valid one
  host_key_confirmed_at TIMESTAMPTZ,
  created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  cred_updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (user_id, name),
  UNIQUE (id, user_id)                                                      -- target of the same-owner foreign key below
);

-- Lets at_project_servers require that a project and a server belong to the same user.
CREATE UNIQUE INDEX at_projects_id_user ON at_projects (id, user_id);

-- Servers a project uses. user_id is the owner of both (enforced by the two composite keys).
CREATE TABLE at_project_servers (
  project_id TEXT NOT NULL,
  server_id  TEXT NOT NULL,
  user_id    TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (project_id, server_id),
  FOREIGN KEY (project_id, user_id) REFERENCES at_projects (id, user_id) ON DELETE CASCADE,
  FOREIGN KEY (server_id, user_id) REFERENCES at_remote_servers (id, user_id) ON DELETE CASCADE
);
CREATE INDEX at_project_servers_server ON at_project_servers (server_id);

-- Roles of the project that may use a server. No row = nobody. The store refuses the coordinator,
-- the lead and read-only roles, and the gateway checks the session's employee again.
CREATE TABLE at_project_server_grants (
  project_id TEXT NOT NULL,
  server_id  TEXT NOT NULL,
  role       TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (project_id, server_id, role),
  FOREIGN KEY (project_id, server_id) REFERENCES at_project_servers (project_id, server_id) ON DELETE CASCADE
);
CREATE INDEX at_project_server_grants_role ON at_project_server_grants (project_id, role);

-- Session certificates the console signed (remote-servers.md §4). The gateway looks the serial up on
-- every login: revoked_at set, or the session closed / its open_gen moved on, means no. Rows whose
-- valid_before is more than 30 days ago are removed by the daily cleanup.
CREATE TABLE at_ssh_certs (
  serial        BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  session_id    TEXT NOT NULL,                     -- no foreign key: kept for tracing after the session row goes
  open_gen      INT  NOT NULL,
  node_id       TEXT NOT NULL,
  project_id    TEXT NOT NULL REFERENCES at_projects(id) ON DELETE CASCADE,
  thread_id     TEXT,
  employee_id   TEXT NOT NULL DEFAULT '',
  role          TEXT NOT NULL,
  pubkey_fp     TEXT NOT NULL,                     -- SHA256:… of the session's ephemeral key
  principals    TEXT[] NOT NULL,                   -- server names the certificate names
  ca_id         TEXT NOT NULL DEFAULT '',          -- which session CA signed it (CA rotation)
  valid_after   TIMESTAMPTZ NOT NULL,
  valid_before  TIMESTAMPTZ NOT NULL,
  revoked_at    TIMESTAMPTZ,
  revoke_reason TEXT NOT NULL DEFAULT '',          -- session_closed | gen_advanced | server_removed | grant_removed | …
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX at_ssh_certs_session ON at_ssh_certs (session_id) WHERE revoked_at IS NULL;
CREATE INDEX at_ssh_certs_project ON at_ssh_certs (project_id) WHERE revoked_at IS NULL;
CREATE INDEX at_ssh_certs_valid_before ON at_ssh_certs (valid_before);

-- Gateway audit (remote-servers.md §9.4): one row per event, kept 365 days (daily cleanup). No
-- foreign key to the project, server or session so the trail outlives them; deleting the user
-- removes it.
CREATE TABLE at_ssh_audit (
  id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  user_id           TEXT NOT NULL REFERENCES at_users(id) ON DELETE CASCADE,
  project_id        TEXT NOT NULL DEFAULT '',
  thread_id         TEXT NOT NULL DEFAULT '',
  session_id        TEXT NOT NULL DEFAULT '',
  employee_id       TEXT NOT NULL DEFAULT '',
  employee          TEXT NOT NULL DEFAULT '',     -- name snapshot (dev-1)
  role              TEXT NOT NULL DEFAULT '',
  node_id           TEXT NOT NULL DEFAULT '',
  server_id         TEXT NOT NULL DEFAULT '',
  server_name       TEXT NOT NULL DEFAULT '',     -- snapshot: readable after the server is deleted
  cert_serial       BIGINT,
  conn_id           TEXT NOT NULL DEFAULT '',     -- rows of one connection share it
  kind              TEXT NOT NULL CHECK (kind IN ('connect', 'disconnect', 'exec', 'shell', 'subsystem',
                                                  'denied', 'auth_fail', 'host_key_mismatch', 'test')),
  command           TEXT NOT NULL DEFAULT '',     -- redacted, at most 8 KB
  command_truncated BOOLEAN NOT NULL DEFAULT FALSE,
  exit_code         INT,
  duration_ms       BIGINT,
  bytes_in          BIGINT NOT NULL DEFAULT 0,    -- node → target
  bytes_out         BIGINT NOT NULL DEFAULT 0,    -- target → node
  reason            TEXT NOT NULL DEFAULT '',
  detail            JSONB NOT NULL DEFAULT '{}'
);
CREATE INDEX at_ssh_audit_project ON at_ssh_audit (project_id, created_at DESC);
CREATE INDEX at_ssh_audit_server ON at_ssh_audit (server_id, created_at DESC);
CREATE INDEX at_ssh_audit_user ON at_ssh_audit (user_id, created_at DESC);
CREATE INDEX at_ssh_audit_created ON at_ssh_audit (created_at);

-- +goose Down
DROP TABLE at_ssh_audit;
DROP TABLE at_ssh_certs;
DROP TABLE at_project_server_grants;
DROP TABLE at_project_servers;
DROP INDEX at_projects_id_user;
DROP TABLE at_remote_servers;
DELETE FROM at_settings WHERE key IN ('ssh_session_ca', 'ssh_gateway_host_key');
