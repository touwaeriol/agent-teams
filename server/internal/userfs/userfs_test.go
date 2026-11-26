package userfs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/touwaeriol/agent-teams/server/internal/credvault"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/testdb"
)

type fakeMeta struct {
	roles map[string]string
	fail  error
}

func (f *fakeMeta) Ensure(_ context.Context, role, password string) error {
	if f.fail != nil {
		return f.fail
	}
	f.roles[role] = password
	return nil
}

func (f *fakeMeta) Drop(_ context.Context, role string) error { delete(f.roles, role); return nil }

type fakeIAM struct{ users map[string]string }

func (f *fakeIAM) Ensure(_ context.Context, n Names, secret string, policy []byte) error {
	if !strings.Contains(string(policy), "arn:aws:s3:::bkt/"+n.Volume+"/*") {
		return errors.New("policy not limited to the volume prefix")
	}
	f.users[n.AccessKey] = secret
	return nil
}

func (f *fakeIAM) Remove(_ context.Context, n Names) error { delete(f.users, n.AccessKey); return nil }

type fakeVolumes struct {
	formatted map[string]FormatSpec
	destroyed int
}

func (f *fakeVolumes) Format(_ context.Context, s FormatSpec) error {
	f.formatted[s.Volume] = s
	return nil
}
func (f *fakeVolumes) Destroy(context.Context, string, string) error {
	f.destroyed++
	return nil
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
	m, i, vols := &fakeMeta{roles: map[string]string{}}, &fakeIAM{users: map[string]string{}}, &fakeVolumes{formatted: map[string]FormatSpec{}}
	s := New(testConfig(), st.Pool, v, m, i, vols, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return s, m, i, vols, st
}

func TestNames(t *testing.T) {
	n := NamesFor("u_01JD3V0000000000000000000A")
	if !roleRE.MatchString(n.Role) || len(n.AccessKey) > 20 || !strings.HasPrefix(n.Volume, "at-") || strings.Contains(n.Volume, "u_01") {
		t.Fatalf("%+v", n)
	}
	if NamesFor("u1") == NamesFor("u2") {
		t.Fatal("names collide")
	}
}

func TestMetaURL(t *testing.T) {
	u, err := url.Parse(MetaURL("jfs_0123456789abcdef", "db.example.net:15432", "agent_teams", "verify-full"))
	if err != nil {
		t.Fatal(err)
	}
	if u.User.Username() != "jfs_0123456789abcdef" || u.Query().Get("search_path") != "jfs_0123456789abcdef" || u.Path != "/agent_teams" {
		t.Fatalf("%s", u)
	}
	if _, has := u.User.Password(); has {
		t.Fatal("password in meta url")
	}
}

func TestEnableMountDisable(t *testing.T) {
	s, m, i, vols, _ := newTest(t)
	ctx := context.Background()
	var changes []string
	s.OnChange = func(_ context.Context, uid string) { changes = append(changes, uid) }

	if _, err := s.MountSpec(ctx, "u1"); !IsNotFound(err) {
		t.Fatalf("mount spec before enable: %v", err)
	}
	vol, err := s.Enable(ctx, "u1", Backend{})
	if err != nil || vol.State != StateReady || vol.CapacityGB != 20 {
		t.Fatalf("enable: %+v %v", vol, err)
	}
	n := NamesFor("u1")
	f, ok := vols.formatted[n.Volume]
	if !ok || f.Bucket != "https://s3.example.net/bkt" || f.AccessKey != n.AccessKey || !strings.Contains(f.MetaURL, "@db.internal:5432/") {
		t.Fatalf("format: %+v", f)
	}
	spec, err := s.MountSpec(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if spec.MetaPassword != m.roles[n.Role] || spec.SecretKey != i.users[n.AccessKey] || !strings.Contains(spec.MetaURL, "@db.example.net:15432/") ||
		spec.Volume != n.Volume || len(spec.Client.Assets) != 4 || !strings.HasSuffix(spec.Client.Assets[0].URL, "/v1.4.2/juicefs-1.4.2-linux-amd64.tar.gz") {
		t.Fatalf("spec: %+v", spec)
	}
	if !s.Ready(ctx, "u1") || len(changes) != 1 {
		t.Fatalf("ready %v changes %v", s.Ready(ctx, "u1"), changes)
	}
	// Enabling again is a no-op.
	if _, err := s.Enable(ctx, "u1", Backend{}); err != nil || len(changes) != 1 {
		t.Fatalf("second enable: %v %v", err, changes)
	}
	if err := s.Disable(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "u1"); !IsNotFound(err) || vols.destroyed != 1 || len(m.roles) != 0 || len(i.users) != 0 || len(changes) != 2 {
		t.Fatalf("after disable: %v destroyed=%d roles=%v users=%v", err, vols.destroyed, m.roles, i.users)
	}
}

func TestEnableFailureThenRetry(t *testing.T) {
	s, m, _, _, _ := newTest(t)
	ctx := context.Background()
	m.fail = errors.New("connection refused")
	vol, err := s.Enable(ctx, "u1", Backend{})
	if err != nil || vol.State != StateError || !strings.Contains(vol.Error, "connection refused") {
		t.Fatalf("%+v %v", vol, err)
	}
	m.fail = nil
	if vol, err = s.Enable(ctx, "u1", Backend{}); err != nil || vol.State != StateReady || vol.Error != "" {
		t.Fatalf("retry: %+v %v", vol, err)
	}
}

func TestDisabledConfig(t *testing.T) {
	s, _, _, _, _ := newTest(t)
	s.cfg.MetaHost = ""
	if _, err := s.Enable(context.Background(), "u1", Backend{}); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
}

// TestPGMeta runs the role and schema SQL against a real PostgreSQL: the role logs in, can create
// tables in its own schema only, and cannot read the console's tables.
func TestPGMeta(t *testing.T) {
	admin := testdb.Migrated(t)
	ctx := context.Background()
	if _, err := store.Exec(ctx, mustStore(t, admin).Pool, `CREATE TABLE at_secret_probe (x int); INSERT INTO at_secret_probe VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	m := &PGMeta{URL: admin}
	role := NamesFor("u1").Role
	if err := m.Ensure(ctx, role, "pw-1"); err != nil {
		t.Fatal(err)
	}
	if err := m.Ensure(ctx, role, "pw-2"); err != nil { // idempotent, password reset
		t.Fatal(err)
	}
	u, _ := url.Parse(admin)
	u.User = url.UserPassword(role, "pw-2")
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
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
	conn.Close(ctx)
	if err := m.Drop(ctx, role); err != nil {
		t.Fatal(err)
	}
	if err := m.Drop(ctx, role); err != nil { // missing is fine
		t.Fatal(err)
	}
	if _, err := pgx.Connect(ctx, u.String()); err == nil {
		t.Fatal("role still logs in after drop")
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

// TestOwnS3: the user's bucket is formatted with their keys and metadata in our PostgreSQL; no
// platform account is made; nodes get their keys; deleting destroys the volume and drops the role.
func TestOwnS3(t *testing.T) {
	s, m, i, vols, _ := newTest(t)
	ctx := context.Background()
	s.CheckEndpoint = func(context.Context, string) error { return nil }
	b := Backend{Kind: BackendS3, S3Endpoint: "https://s3.user.example/", Bucket: "mine", AccessKey: "UAK", SecretKey: "USK"}
	vol, err := s.Enable(ctx, "u1", b)
	if err != nil || vol.State != StateReady || vol.Backend != BackendS3 || vol.CapacityGB != 0 || vol.Summary.Bucket != "mine" || vol.Summary.Endpoint != "https://s3.user.example" {
		t.Fatalf("enable: %+v %v", vol, err)
	}
	n := NamesFor("u1")
	f := vols.formatted[n.Volume]
	if f.Bucket != "https://s3.user.example/mine" || f.AccessKey != "UAK" || f.SecretKey != "USK" || len(i.users) != 0 || m.roles[n.Role] == "" {
		t.Fatalf("format %+v users %v", f, i.users)
	}
	spec, err := s.MountSpec(ctx, "u1")
	if err != nil || spec.AccessKey != "UAK" || spec.SecretKey != "USK" || !strings.Contains(spec.MetaURL, n.Role) {
		t.Fatalf("spec %+v %v", spec, err)
	}
	if _, err := s.Enable(ctx, "u1", Backend{}); !errors.Is(err, ErrSwitch) {
		t.Fatalf("switch without delete: %v", err)
	}
	if err := s.Disable(ctx, "u1"); err != nil || vols.destroyed != 1 || len(m.roles) != 0 {
		t.Fatalf("disable: %v destroyed %d roles %v", err, vols.destroyed, m.roles)
	}
}

// TestOwnJuiceFS: nothing is provisioned or destroyed; nodes get the user's URL with the password split out.
func TestOwnJuiceFS(t *testing.T) {
	s, m, _, vols, _ := newTest(t)
	ctx := context.Background()
	vol, err := s.Enable(ctx, "u1", Backend{Kind: BackendJuiceFS, MetaURL: "redis://:sekret@redis.user.example:6379/1"})
	if err != nil || vol.State != StateReady || vol.Summary.Meta != "redis://redis.user.example:6379/1" || len(vols.formatted) != 0 || len(m.roles) != 0 {
		t.Fatalf("enable: %+v %v", vol, err)
	}
	spec, err := s.MountSpec(ctx, "u1")
	if err != nil || spec.MetaURL != "redis://redis.user.example:6379/1" || spec.MetaPassword != "sekret" || spec.AccessKey != "" {
		t.Fatalf("spec %+v %v", spec, err)
	}
	if err := s.Disable(ctx, "u1"); err != nil || vols.destroyed != 0 {
		t.Fatalf("disable: %v destroyed %d", err, vols.destroyed)
	}
}

func TestBackendNormalize(t *testing.T) {
	bad := []Backend{
		{Kind: "ftp"},
		{Kind: BackendS3, S3Endpoint: "s3.example", Bucket: "b", AccessKey: "a", SecretKey: "s"},
		{Kind: BackendS3, S3Endpoint: "https://s3.example/path", Bucket: "b-1", AccessKey: "a", SecretKey: "s"},
		{Kind: BackendS3, S3Endpoint: "https://s3.example", Bucket: "Bad_Bucket", AccessKey: "a", SecretKey: "s"},
		{Kind: BackendS3, S3Endpoint: "https://s3.example", Bucket: "bucket", AccessKey: "a"},
		{Kind: BackendJuiceFS, MetaURL: "sqlite3:///tmp/x.db"},
	}
	for _, b := range bad {
		if _, err := b.Normalize(); !errors.Is(err, ErrInvalidBackend) {
			t.Errorf("%+v accepted: %v", b, err)
		}
	}
	if b, err := (Backend{Kind: BackendJuiceFS, MetaURL: "mysql://u:p@(db:3306)/jfs"}).Normalize(); err != nil || b.MetaURL != "mysql://u:p@(db:3306)/jfs" {
		t.Errorf("mysql url: %+v %v", b, err)
	}
}

func TestPublicEndpoint(t *testing.T) {
	for _, e := range []string{"http://127.0.0.1:9000", "https://10.0.0.5", "http://localhost:9000", "http://169.254.169.254"} {
		if err := PublicEndpoint(context.Background(), e); !errors.Is(err, ErrInvalidBackend) {
			t.Errorf("%s allowed: %v", e, err)
		}
	}
	if err := PublicEndpoint(context.Background(), "https://1.1.1.1"); err != nil {
		t.Errorf("public address refused: %v", err)
	}
}
