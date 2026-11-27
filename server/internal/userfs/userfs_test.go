package userfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/touwaeriol/agent-teams/server/internal/credvault"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/testdb"
)

type fakeMeta struct {
	mu      sync.Mutex
	roles   map[string]string
	ensures int
	fail    error
}

func (f *fakeMeta) Ensure(_ context.Context, role, password string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.roles[role] = password
	f.ensures++
	return nil
}

func (f *fakeMeta) Usage(_ context.Context, schema, dir string) (Usage, error) {
	if schema != DefaultMetaRole || !strings.HasPrefix(dir, UsersDir+"/") {
		return Usage{}, errors.New("usage of another folder")
	}
	return Usage{UsedBytes: 4096, Inodes: 2}, nil
}

type fakeIAM struct {
	mu    sync.Mutex
	users map[string]string
}

func (f *fakeIAM) Ensure(_ context.Context, n Names, secret string, policy []byte) error {
	if !strings.Contains(string(policy), "arn:aws:s3:::bkt/"+n.Volume+"/*") {
		return errors.New("policy not limited to the volume prefix")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[n.AccessKey] = secret
	return nil
}

type fakeVolumes struct {
	mu        sync.Mutex
	formatted map[string]FormatSpec
	formats   int
	quotas    map[string]int // folder path → capacity
	root      string
	opened    []string // metaURL of each Files
	filesErr  error    // Files fails with it (the volume cannot be reached)
}

func (f *fakeVolumes) Files(_ context.Context, volume, metaURL, _ string) (Files, error) {
	f.mu.Lock()
	f.opened = append(f.opened, metaURL)
	ferr := f.filesErr
	f.mu.Unlock()
	if ferr != nil {
		return nil, ferr
	}
	return DirFiles{Root: filepath.Join(f.root, volume)}, os.MkdirAll(filepath.Join(f.root, volume), 0o755)
}

func (f *fakeVolumes) Format(_ context.Context, s FormatSpec) error {
	time.Sleep(20 * time.Millisecond) // long enough for concurrent Enables to overlap
	f.mu.Lock()
	defer f.mu.Unlock()
	f.formatted[s.Volume] = s
	f.formats++
	return nil
}

func (f *fakeVolumes) Quota(_ context.Context, _, _, path string, capacityGB int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.quotas[path] = capacityGB
	return nil
}

// userPath is a path in the user's folder as the fake volume stores it.
func (f *fakeVolumes) userPath(userID string, rel ...string) string {
	return filepath.Join(append([]string{f.root, DefaultVolume, UserDir(userID)}, rel...)...)
}

func testConfig() Config {
	return Config{AdminURL: "postgres://admin:pw@db.internal:5432/agent_teams", MetaHost: "db.example.net:15432", MetaSSLMode: "verify-full",
		S3Endpoint: "https://s3.example.net", Bucket: "bkt", AdminAccessKey: "root", AdminSecretKey: "rootpw", CapacityGB: 20,
		JuiceFS: "juicefs", ClientVersion: DefaultClientVersion, ClientBase: DefaultClientBase, Assets: DefaultAssets}
}

func newTest(t *testing.T) (*Service, *fakeMeta, *fakeIAM, *fakeVolumes, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), testdb.Migrated(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if _, err := store.Exec(context.Background(), st.Pool, `INSERT INTO at_users (id, email, password_hash) VALUES ('u1', 'a@example.com', 'x')`); err != nil {
		t.Fatal(err)
	}
	v, _ := credvault.New("k1")
	m, i, vols := &fakeMeta{roles: map[string]string{}}, &fakeIAM{users: map[string]string{}}, &fakeVolumes{formatted: map[string]FormatSpec{}, quotas: map[string]int{}, root: t.TempDir()}
	s := New(testConfig(), st.Pool, v, m, i, vols, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.UnmountPoll = 10 * time.Millisecond
	return s, m, i, vols, st
}

func TestNames(t *testing.T) {
	n := testConfig().SharedNames()
	if n.Volume != DefaultVolume || n.Role != DefaultMetaRole || len(n.AccessKey) > 20 || n.Policy == "" {
		t.Fatalf("%+v", n)
	}
	other := Config{Volume: "other"}.SharedNames()
	if other.AccessKey == n.AccessKey || other.Role != DefaultMetaRole {
		t.Fatalf("%+v", other)
	}
	d := UserDir("u_01JD3V0000000000000000000A")
	if !strings.HasPrefix(d, UsersDir+"/") || len(d) != len(UsersDir)+17 || strings.Contains(d, "u_01") || UserDir("u1") == UserDir("u2") {
		t.Fatalf("user dir %q", d)
	}
}

func TestMetaURL(t *testing.T) {
	u, err := url.Parse(MetaURL("juicefs.ref123", "juicefs", "db.example.net:15432", "agent_teams", "verify-full"))
	if err != nil {
		t.Fatal(err)
	}
	if u.User.Username() != "juicefs.ref123" || u.Query().Get("search_path") != "juicefs" || u.Path != "/agent_teams" {
		t.Fatalf("%s", u)
	}
	if _, has := u.User.Password(); has {
		t.Fatal("password in meta url")
	}
}

func TestEnableMountDisable(t *testing.T) {
	s, m, i, vols, st := newTest(t)
	ctx := context.Background()
	var changes []string
	s.OnChange = func(_ context.Context, uid string) { changes = append(changes, uid) }

	if _, err := s.MountSpec(ctx, "u1"); !IsNotFound(err) {
		t.Fatalf("mount spec before enable: %v", err)
	}
	vol, err := s.Enable(ctx, "u1")
	if err != nil || vol.State != StateReady || vol.CapacityGB != 20 || vol.Subdir != UserDir("u1") || vol.Volume != DefaultVolume {
		t.Fatalf("enable: %+v %v", vol, err)
	}
	n := s.cfg.SharedNames()
	f, ok := vols.formatted[n.Volume]
	if !ok || f.Bucket != "https://s3.example.net/bkt" || f.AccessKey != n.AccessKey || !strings.Contains(f.MetaURL, "juicefs@db.internal:5432/") {
		t.Fatalf("format: %+v", f)
	}
	if vols.quotas["/"+UserDir("u1")] != 20 {
		t.Fatalf("quotas %v", vols.quotas)
	}
	for _, d := range []string{"projects", SharedDir} {
		if fi, err := os.Stat(vols.userPath("u1", d)); err != nil || !fi.IsDir() {
			t.Fatalf("folder %s: %v", d, err)
		}
	}
	spec, err := s.MountSpec(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if spec.MetaPassword != m.roles[n.Role] || spec.MetaPassword == "" || !strings.Contains(spec.MetaURL, "juicefs@db.example.net:15432/") ||
		spec.Volume != n.Volume || spec.Subdir != UserDir("u1") || len(spec.Client.Assets) != 4 ||
		!strings.HasSuffix(spec.Client.Assets[0].URL, "/v1.4.2/juicefs-1.4.2-linux-amd64.tar.gz") {
		t.Fatalf("spec: %+v", spec)
	}
	if !s.Ready(ctx, "u1") || len(changes) != 1 {
		t.Fatalf("ready %v changes %v", s.Ready(ctx, "u1"), changes)
	}
	if u, err := s.Usage(ctx, "u1"); err != nil || u.UsedBytes != 4096 {
		t.Fatalf("usage %+v %v", u, err)
	}
	// Enabling again is a no-op.
	if _, err := s.Enable(ctx, "u1"); err != nil || len(changes) != 1 {
		t.Fatalf("second enable: %v %v", err, changes)
	}
	// A second user gets a folder of its own in the same volume, provisioned once per process.
	if _, err := store.Exec(ctx, st.Pool, `INSERT INTO at_users (id, email, password_hash) VALUES ('u2', 'b@example.com', 'x')`); err != nil {
		t.Fatal(err)
	}
	if vol2, err := s.Enable(ctx, "u2"); err != nil || vol2.Subdir == vol.Subdir || vol2.Volume != vol.Volume {
		t.Fatalf("u2: %+v %v", vol2, err)
	}
	spec2, _ := s.MountSpec(ctx, "u2")
	if vols.formats != 1 || m.ensures != 1 || len(i.users) != 1 || spec2.MetaPassword != spec.MetaPassword || spec2.Subdir != UserDir("u2") {
		t.Fatalf("formats=%d ensures=%d users=%v", vols.formats, m.ensures, i.users)
	}
	if err := s.Disable(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	if _, err := s.Get(ctx, "u1"); !IsNotFound(err) || len(changes) != 3 {
		t.Fatalf("after disable: %v changes %v", err, changes)
	}
	if _, err := os.Stat(vols.userPath("u1")); !os.IsNotExist(err) {
		t.Fatalf("u1 folder left: %v", err)
	}
	if _, err := os.Stat(vols.userPath("u2", "projects")); err != nil {
		t.Fatalf("u2 folder touched: %v", err)
	}
}

// TestSharedSecretsAcrossReplicas: a second console (another replica, or after a restart) reads
// the credentials the first one stored instead of making new ones; another volume name is refused.
func TestSharedSecretsAcrossReplicas(t *testing.T) {
	s, _, _, _, st := newTest(t)
	ctx := context.Background()
	if _, err := s.Enable(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	other := New(testConfig(), st.Pool, s.vault, s.meta, s.iam, s.volumes, s.log)
	a, _ := s.MountSpec(ctx, "u1")
	b, err := other.MountSpec(ctx, "u1")
	if err != nil || a.MetaPassword != b.MetaPassword {
		t.Fatalf("second replica: %v", err)
	}
	cfg := testConfig()
	cfg.Volume = "renamed"
	renamed := New(cfg, st.Pool, s.vault, s.meta, s.iam, s.volumes, s.log)
	if _, err := renamed.MountSpec(ctx, "u1"); err == nil || !strings.Contains(err.Error(), "renamed") {
		t.Fatalf("renamed volume: %v", err)
	}
}

func TestEnableFailureThenRetry(t *testing.T) {
	s, m, _, _, _ := newTest(t)
	ctx := context.Background()
	m.fail = errors.New("connection refused")
	vol, err := s.Enable(ctx, "u1")
	if err != nil || vol.State != StateError || !strings.Contains(vol.Error, "connection refused") {
		t.Fatalf("%+v %v", vol, err)
	}
	m.fail = nil
	if vol, err = s.Enable(ctx, "u1"); err != nil || vol.State != StateReady || vol.Error != "" {
		t.Fatalf("retry: %+v %v", vol, err)
	}
}

func TestDisabledConfig(t *testing.T) {
	s, _, _, _, _ := newTest(t)
	s.cfg.MetaHost = ""
	if _, err := s.Enable(context.Background(), "u1"); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
}

// TestPGMeta runs the role and schema SQL against a real PostgreSQL, as a role that may create
// roles but is no superuser (managed PostgreSQL): the role logs in, creates tables in its own
// schema only, cannot read the console's tables, and its folders' usage is read from the quotas.
func TestPGMeta(t *testing.T) {
	admin := testdb.Migrated(t)
	ctx := context.Background()
	adminStore := mustStore(t, admin)
	if _, err := store.Exec(ctx, adminStore.Pool, `CREATE TABLE at_secret_probe (x int); INSERT INTO at_secret_probe VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	creator, role := "jfs_creator_"+suffix, "jfs_test_"+suffix
	u, _ := url.Parse(admin)
	if _, err := store.Exec(ctx, adminStore.Pool, fmt.Sprintf(`CREATE ROLE %s LOGIN CREATEROLE PASSWORD 'cpw'; GRANT CREATE ON DATABASE %s TO %s`,
		creator, strings.TrimPrefix(u.Path, "/"), creator)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = store.Exec(context.Background(), adminStore.Pool, fmt.Sprintf(`DROP SCHEMA IF EXISTS %[1]s CASCADE; DROP OWNED BY %[1]s; DROP ROLE IF EXISTS %[1]s;
DROP OWNED BY %[2]s; DROP ROLE IF EXISTS %[2]s`, role, creator))
	})
	cu := *u
	cu.User = url.UserPassword(creator, "cpw")
	m := &PGMeta{URL: cu.String(), ConnLimit: 7}
	t.Cleanup(func() {
		if m.pool != nil {
			m.pool.Close()
		}
	})
	if got, err := m.Usage(ctx, role, UserDir("u1")); err != nil || got != (Usage{}) {
		t.Fatalf("usage before ensure = %+v %v", got, err)
	}
	if err := m.Ensure(ctx, role, "pw-1"); err != nil {
		t.Fatal(err)
	}
	var stored string
	var limit int
	if err := store.QueryRow(ctx, adminStore.Pool, `SELECT rolpassword, rolconnlimit FROM pg_authid WHERE rolname = $1`, role).Scan(&stored, &limit); err != nil ||
		!strings.HasPrefix(stored, "SCRAM-SHA-256$4096:") || limit != 7 {
		t.Fatalf("stored password %q limit %d %v", stored, limit, err)
	}
	if err := m.Ensure(ctx, role, "pw-2"); err != nil { // idempotent, password reset
		t.Fatal(err)
	}
	ru := *u
	ru.User = url.UserPassword(role, "pw-2")
	conn, err := pgx.Connect(ctx, ru.String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `CREATE TABLE jfs_probe (x int)`); err != nil {
		t.Fatalf("create in own schema: %v", err)
	}
	var schema string
	if err := conn.QueryRow(ctx, `SELECT table_schema FROM information_schema.tables WHERE table_name = 'jfs_probe'`).Scan(&schema); err != nil || schema != role {
		t.Fatalf("table landed in %q: %v", schema, err)
	}
	if _, err := conn.Exec(ctx, `SELECT * FROM public.at_secret_probe`); err == nil {
		t.Fatal("volume role can read the console's tables")
	}
	if _, err := conn.Exec(ctx, `SELECT * FROM public.at_users`); err == nil {
		t.Fatal("volume role can read at_users")
	}
	// What JuiceFS keeps: users (inode 2) → <id> (inode 5) with a directory quota.
	dir := UserDir("u1")
	if _, err := conn.Exec(ctx, `CREATE TABLE jfs_edge (id bigserial PRIMARY KEY, parent bigint NOT NULL, name bytea NOT NULL, inode bigint NOT NULL, type smallint NOT NULL);
CREATE TABLE jfs_dir_quota (inode bigint PRIMARY KEY, max_space bigint NOT NULL, max_inodes bigint NOT NULL, used_space bigint NOT NULL, used_inodes bigint NOT NULL);
INSERT INTO jfs_dir_quota VALUES (5, 0, 0, 8192, 3), (9, 0, 0, 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO jfs_edge (parent, name, inode, type) VALUES (1, 'users', 2, 2), (2, $1, 5, 2), (1, 'other', 9, 2)`,
		[]byte(strings.TrimPrefix(dir, UsersDir+"/"))); err != nil {
		t.Fatal(err)
	}
	if got, err := m.Usage(ctx, role, dir); err != nil || got != (Usage{UsedBytes: 8192, Inodes: 3}) {
		t.Fatalf("usage = %+v %v", got, err)
	}
	if got, err := m.Usage(ctx, role, UserDir("u2")); err != nil || got != (Usage{}) {
		t.Fatalf("usage of a missing folder = %+v %v", got, err)
	}
}

func mustStore(t *testing.T, url string) *store.Store {
	st, err := store.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return st
}
