package userfs

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/madmin-go/v3"
)

// roleRE is a role and schema name the console creates (an unquoted SQL identifier).
var roleRE = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// PGMeta is Meta on the PostgreSQL of Config.AdminURL (a role allowed to create roles).
type PGMeta struct {
	URL string
	// ConnLimit caps the volume role's connections (0 = 30): every node's mount holds some.
	ConnLimit int
	once      sync.Once
	pool      *pgxpool.Pool
	err       error
}

func (m *PGMeta) open(ctx context.Context) (*pgxpool.Pool, error) {
	m.once.Do(func() {
		cfg, err := pgxpool.ParseConfig(m.URL)
		if err != nil {
			m.err = err
			return
		}
		cfg.MaxConns = 2
		m.pool, m.err = pgxpool.NewWithConfig(ctx, cfg)
	})
	return m.pool, m.err
}

// Ensure implements Meta. The role can log in and owns its schema only: it gets no privilege on
// the console's tables (they are not granted to PUBLIC). The password goes as a SCRAM-SHA-256
// verifier, so the plain password never reaches the server (nor its statement log). It needs only
// CREATEROLE, not superuser (managed PostgreSQL such as Supabase), so it leaves the superuser and
// replication attributes alone (and CREATEDB, CREATEROLE, which only a role holding them may
// change): a role created without them has none. Such an admin is made a
// member of the role first: PostgreSQL 16+ gives the creator only ADMIN on it, and CREATE SCHEMA
// ... AUTHORIZATION needs to be able to SET ROLE to it.
func (m *PGMeta) Ensure(ctx context.Context, role, password string) error {
	if !roleRE.MatchString(role) {
		return fmt.Errorf("bad role %q", role)
	}
	verifier, err := scramVerifier(password)
	if err != nil {
		return err
	}
	pool, err := m.open(ctx)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, fmt.Sprintf(`
DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '%[1]s') THEN CREATE ROLE %[1]s LOGIN; END IF; END $$;
ALTER ROLE %[1]s LOGIN CONNECTION LIMIT %[3]d PASSWORD '%[2]s';
ALTER ROLE %[1]s SET search_path = %[1]s;
ALTER ROLE %[1]s SET statement_timeout = '60s';
ALTER ROLE %[1]s SET idle_in_transaction_session_timeout = '60s';
DO $$ BEGIN IF NOT (SELECT rolsuper FROM pg_roles WHERE rolname = current_user) THEN
  EXECUTE format('GRANT %[1]s TO %%I', current_user);
END IF; END $$;
CREATE SCHEMA IF NOT EXISTS %[1]s AUTHORIZATION %[1]s;
REVOKE ALL ON SCHEMA %[1]s FROM PUBLIC;`, role, verifier, m.connLimit()))
	return err
}

func (m *PGMeta) connLimit() int {
	if m.ConnLimit > 0 {
		return m.ConnLimit
	}
	return 30
}

// scramIterations is PostgreSQL's default scram_iterations.
const scramIterations = 4096

// scramVerifier is the SCRAM-SHA-256 verifier PostgreSQL stores for password (RFC 5802/7677:
// SCRAM-SHA-256$<iterations>:<salt>$<StoredKey>:<ServerKey>, base64). The passwords are ASCII
// (randomSecret), so SASLprep leaves them unchanged.
func scramVerifier(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	salted, err := pbkdf2.Key(sha256.New, password, salt, scramIterations, sha256.Size)
	if err != nil {
		return "", err
	}
	clientKey := hmacSHA256(salted, "Client Key")
	storedKey := sha256.Sum256(clientKey)
	b64 := base64.StdEncoding.EncodeToString
	return fmt.Sprintf("SCRAM-SHA-256$%d:%s$%s:%s", scramIterations, b64(salt), b64(storedKey[:]), b64(hmacSHA256(salted, "Server Key"))), nil
}

func hmacSHA256(key []byte, msg string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(msg))
	return h.Sum(nil)
}

// Usage implements Meta: JuiceFS keeps a quota folder's totals in <schema>.jfs_dir_quota, keyed
// by its inode; the path is walked from the root (inode 1) through <schema>.jfs_edge.
func (m *PGMeta) Usage(ctx context.Context, schema, dir string) (Usage, error) {
	var u Usage
	if !roleRE.MatchString(schema) {
		return u, fmt.Errorf("bad schema %q", schema)
	}
	pool, err := m.open(ctx)
	if err != nil {
		return u, err
	}
	var formatted bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, schema+".jfs_dir_quota").Scan(&formatted); err != nil || !formatted {
		return u, err
	}
	inode := int64(1)
	for _, seg := range strings.Split(strings.Trim(dir, "/"), "/") {
		err := pool.QueryRow(ctx, fmt.Sprintf(`SELECT inode FROM %s.jfs_edge WHERE parent = $1 AND name = $2`, schema), inode, []byte(seg)).Scan(&inode)
		if errors.Is(err, pgx.ErrNoRows) {
			return u, nil
		}
		if err != nil {
			return u, err
		}
	}
	err = pool.QueryRow(ctx, fmt.Sprintf(`SELECT used_space, used_inodes FROM %s.jfs_dir_quota WHERE inode = $1`, schema), inode).Scan(&u.UsedBytes, &u.Inodes)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, nil
	}
	return u, err
}

// MinioIAM is IAM on a RustFS / MinIO admin API.
type MinioIAM struct {
	Endpoint  string // https://host[:port]
	AccessKey string
	SecretKey string
}

func (m MinioIAM) client() (*madmin.AdminClient, error) {
	u, err := url.Parse(m.Endpoint)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("bad object store endpoint %q", m.Endpoint)
	}
	return madmin.New(u.Host, m.AccessKey, m.SecretKey, u.Scheme == "https")
}

// Ensure implements IAM.
func (m MinioIAM) Ensure(ctx context.Context, n Names, secretKey string, policy []byte) error {
	c, err := m.client()
	if err != nil {
		return err
	}
	if err := c.AddCannedPolicy(ctx, n.Policy, policy); err != nil {
		return fmt.Errorf("add policy: %w", err)
	}
	if err := c.AddUser(ctx, n.AccessKey, secretKey); err != nil {
		return fmt.Errorf("add user: %w", err)
	}
	if _, err := c.AttachPolicy(ctx, madmin.PolicyAssociationReq{Policies: []string{n.Policy}, User: n.AccessKey}); err != nil && !alreadyAttached(err) {
		if err2 := c.SetPolicy(ctx, n.Policy, n.AccessKey, false); err2 != nil {
			return fmt.Errorf("attach policy: %w (set policy: %v)", err, err2)
		}
	}
	return nil
}

func alreadyAttached(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "already") && strings.Contains(s, "attach")
}

// CLI is Volumes with the juicefs binary. Secrets go in the environment (META_PASSWORD, SECRET_KEY),
// never in the arguments.
type CLI struct {
	Bin     string
	Timeout time.Duration
	// Gateways serve Files (juicefs gateway children).
	Gateways *Gateways
}

// Files implements Volumes.
func (c CLI) Files(ctx context.Context, volume, metaURL, metaPassword string) (Files, error) {
	return c.Gateways.Files(ctx, volume, metaURL, metaPassword)
}

// Shutdown stops every gateway (Service.Close).
func (c CLI) Shutdown() { c.Gateways.Close() }

func (c CLI) run(ctx context.Context, env []string, args ...string) ([]byte, error) {
	t := c.Timeout
	if t == 0 {
		t = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, t)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	cmd.Env = append(os.Environ(), env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("%s %s: %w: %s", c.Bin, args[0], err, lastLines(errb.String(), 6))
	}
	return out.Bytes(), nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// Format implements Volumes. The volume has no capacity of its own: each user's folder has a quota.
func (c CLI) Format(ctx context.Context, f FormatSpec) error {
	_, err := c.run(ctx, []string{"META_PASSWORD=" + f.MetaPassword, "SECRET_KEY=" + f.SecretKey},
		"format", "--storage", "s3", "--bucket", f.Bucket, "--access-key", f.AccessKey,
		"--trash-days", "1", "--no-update=false", f.MetaURL, f.Volume)
	return err
}

// Quota implements Volumes (capacityGB <= 0: no quota is set).
func (c CLI) Quota(ctx context.Context, metaURL, metaPassword, path string, capacityGB int) error {
	if capacityGB <= 0 {
		return nil
	}
	_, err := c.run(ctx, []string{"META_PASSWORD=" + metaPassword}, "quota", "set", metaURL, "--path", path, "--capacity", strconv.Itoa(capacityGB))
	return err
}
