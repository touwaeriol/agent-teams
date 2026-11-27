package userfs

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
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

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/madmin-go/v3"
)

var roleRE = regexp.MustCompile(`^jfs_[0-9a-f]{16}$`)

// PGMeta is Meta on the PostgreSQL of Config.AdminURL (a role allowed to create roles).
type PGMeta struct {
	URL  string
	once sync.Once
	pool *pgxpool.Pool
	err  error
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
// the console's tables (they are not granted to PUBLIC) nor on the other volumes' schemas. The
// password goes as a SCRAM-SHA-256 verifier, so the plain password never reaches the server (nor
// its statement log).
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
ALTER ROLE %[1]s LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION CONNECTION LIMIT 30 PASSWORD '%[2]s';
ALTER ROLE %[1]s SET search_path = %[1]s;
ALTER ROLE %[1]s SET statement_timeout = '60s';
ALTER ROLE %[1]s SET idle_in_transaction_session_timeout = '60s';
CREATE SCHEMA IF NOT EXISTS %[1]s AUTHORIZATION %[1]s;
REVOKE ALL ON SCHEMA %[1]s FROM PUBLIC;`, role, verifier))
	return err
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

// Exists implements Meta.
func (m *PGMeta) Exists(ctx context.Context, role string) (bool, error) {
	pool, err := m.open(ctx)
	if err != nil {
		return false, err
	}
	var ok bool
	err = pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1) AND EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)`, role).Scan(&ok)
	return ok, err
}

// Usage implements Meta: JuiceFS keeps the totals in <schema>.jfs_counter.
func (m *PGMeta) Usage(ctx context.Context, role string) (Usage, error) {
	var u Usage
	if !roleRE.MatchString(role) {
		return u, fmt.Errorf("bad role %q", role)
	}
	pool, err := m.open(ctx)
	if err != nil {
		return u, err
	}
	var formatted bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, role+".jfs_counter").Scan(&formatted); err != nil || !formatted {
		return u, err
	}
	err = pool.QueryRow(ctx, fmt.Sprintf(`SELECT
	COALESCE(MAX(value) FILTER (WHERE name = 'usedSpace'), 0),
	COALESCE(MAX(value) FILTER (WHERE name = 'totalInodes'), 0)
FROM %s.jfs_counter`, role)).Scan(&u.UsedBytes, &u.Inodes)
	return u, err
}

// Roles implements Meta.
func (m *PGMeta) Roles(ctx context.Context) ([]string, error) {
	pool, err := m.open(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, `SELECT r.rolname FROM pg_roles r JOIN pg_namespace n ON n.nspname = r.rolname
		WHERE r.rolname ~ '^jfs_[0-9a-f]{16}$' ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Drop implements Meta.
func (m *PGMeta) Drop(ctx context.Context, role string) error {
	if !roleRE.MatchString(role) {
		return fmt.Errorf("bad role %q", role)
	}
	pool, err := m.open(ctx)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, fmt.Sprintf(`
SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = '%[1]s';
DROP SCHEMA IF EXISTS %[1]s CASCADE;
DROP ROLE IF EXISTS %[1]s;`, role))
	return err
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

// Remove implements IAM.
func (m MinioIAM) Remove(ctx context.Context, n Names) error {
	c, err := m.client()
	if err != nil {
		return err
	}
	if err := c.RemoveUser(ctx, n.AccessKey); err != nil && !notFound(err) {
		return fmt.Errorf("remove user: %w", err)
	}
	if err := c.RemoveCannedPolicy(ctx, n.Policy); err != nil && !notFound(err) {
		return fmt.Errorf("remove policy: %w", err)
	}
	return nil
}

func notFound(err error) bool {
	var e madmin.ErrorResponse
	if errors.As(err, &e) && strings.Contains(e.Code, "NoSuch") {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "not found") || strings.Contains(s, "does not exist") || strings.Contains(s, "nosuch")
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

// Close implements Volumes.
func (c CLI) Close(volume string) { c.Gateways.Stop(volume) }

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

// Format implements Volumes.
func (c CLI) Format(ctx context.Context, f FormatSpec) error {
	_, err := c.run(ctx, []string{"META_PASSWORD=" + f.MetaPassword, "SECRET_KEY=" + f.SecretKey},
		"format", "--storage", "s3", "--bucket", f.Bucket, "--access-key", f.AccessKey,
		"--capacity", strconv.Itoa(f.CapacityGB), "--trash-days", "1", "--no-update=false", f.MetaURL, f.Volume)
	return err
}

// Destroy implements Volumes. A volume that was never formatted, or whose role is gone or no
// longer logs in, has nothing left to destroy.
func (c CLI) Destroy(ctx context.Context, metaURL, metaPassword string) error {
	env := []string{"META_PASSWORD=" + metaPassword}
	out, err := c.run(ctx, env, "status", metaURL)
	if err != nil {
		if nothingToDestroy(err) {
			return nil
		}
		return err
	}
	var st struct {
		Setting struct {
			UUID string
		}
	}
	if err := json.Unmarshal(out, &st); err != nil || st.Setting.UUID == "" {
		return fmt.Errorf("juicefs status: no volume uuid (%v)", err)
	}
	_, err = c.run(ctx, env, "destroy", "--yes", "--force", metaURL, st.Setting.UUID)
	return err
}

// nothingToDestroy tells the juicefs status errors of a volume without metadata.
func nothingToDestroy(err error) bool {
	s := err.Error()
	roleGone := strings.Contains(s, `role "`) && strings.Contains(s, "does not exist")
	return roleGone || strings.Contains(s, "not formatted") || strings.Contains(s, "password authentication failed")
}
