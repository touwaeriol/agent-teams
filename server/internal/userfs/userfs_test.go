package userfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/credvault"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/testdb"
)

// fakeStore keeps each folder as a directory under root (bucket/prefix), keys in a map. hard
// picks the layout: a bucket per user with a quota (RustFS) or a prefix of one bucket (B2).
type fakeStore struct {
	mu       sync.Mutex
	root     string
	hard     bool
	keys     map[string]fakeKey // key id → key
	quotas   map[string]int     // bucket → GB
	n        int
	prepares int
	usage    int64 // when set, Usage reports it instead of counting
	fail     error // Prepare and IssueKey fail with it (the store cannot be reached)
	filesErr error // every Files call fails with it
}

type fakeKey struct {
	name   string
	folder Folder
	secret string
	write  bool
}

func newFakeStore(root string) *fakeStore {
	return &fakeStore{root: root, keys: map[string]fakeKey{}, quotas: map[string]int{}}
}

func (f *fakeStore) dir(fo Folder) string { return filepath.Join(f.root, fo.Bucket, fo.Prefix) }

func (f *fakeStore) Folder(n UserNames) Folder {
	if f.hard {
		return Folder{Bucket: n.Name}
	}
	return Folder{Bucket: "fs", Prefix: n.Prefix}
}

func (f *fakeStore) HardQuota() bool { return f.hard }

func (f *fakeStore) Files(fo Folder) Files {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.filesErr != nil {
		return failingFiles{f.filesErr}
	}
	return DirFiles{Root: f.dir(fo)}
}

func (f *fakeStore) Prepare(_ context.Context, fo Folder, capacityGB int) error {
	time.Sleep(20 * time.Millisecond) // long enough for concurrent Enables to overlap
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.prepares++
	if f.hard {
		f.quotas[fo.Bucket] = capacityGB
	}
	return nil
}

func (f *fakeStore) Remove(_ context.Context, fo Folder) error {
	return os.RemoveAll(f.dir(fo))
}

func (f *fakeStore) Folders(context.Context) ([]Folder, error) {
	var out []Folder
	if f.hard {
		es, err := os.ReadDir(f.root)
		for _, e := range es {
			out = append(out, Folder{Bucket: e.Name()})
		}
		return out, err
	}
	es, err := os.ReadDir(filepath.Join(f.root, "fs", "users"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	for _, e := range es {
		out = append(out, Folder{Bucket: "fs", Prefix: "users/" + e.Name() + "/"})
	}
	return out, err
}

func (f *fakeStore) Usage(_ context.Context, fo Folder) (Usage, error) {
	f.mu.Lock()
	fixed := f.usage
	f.mu.Unlock()
	if fixed > 0 {
		return Usage{UsedBytes: fixed, Objects: 1}, nil
	}
	var u Usage
	err := filepath.Walk(f.dir(fo), func(_ string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			u.UsedBytes += fi.Size()
			u.Objects++
		}
		return err
	})
	return u, err
}

func (f *fakeStore) IssueKey(_ context.Context, n UserNames, fo Folder, write bool) (Key, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return Key{}, f.fail
	}
	f.n++
	k := Key{ID: fmt.Sprintf("kid%d", f.n), Secret: fmt.Sprintf("secret%d", f.n)}
	f.keys[k.ID] = fakeKey{name: n.Name, folder: fo, secret: k.Secret, write: write}
	return k, nil
}

func (f *fakeStore) RevokeKey(_ context.Context, _ UserNames, keyID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.keys, keyID)
	return nil
}

func (f *fakeStore) Keys(context.Context) ([]KeyInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []KeyInfo
	for id, k := range f.keys {
		out = append(out, KeyInfo{ID: id, Name: k.name})
	}
	return out, nil
}

// keyCount is how many keys the store holds.
func (f *fakeStore) keyCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.keys)
}

// key is a key the store holds (ok false when revoked).
func (f *fakeStore) key(id string) (fakeKey, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k, ok := f.keys[id]
	return k, ok
}

// userPath is a path in the user's folder as the fake stores it.
func (f *fakeStore) userPath(userID string, rel ...string) string {
	return filepath.Join(append([]string{f.dir(f.Folder(NamesOf(userID)))}, rel...)...)
}

type failingFiles struct{ err error }

func (f failingFiles) List(context.Context, string) ([]Entry, error) { return nil, f.err }
func (f failingFiles) Open(context.Context, string) (io.ReadCloser, Entry, error) {
	return nil, Entry{}, f.err
}
func (f failingFiles) Put(context.Context, string, io.Reader, int64) error { return f.err }
func (f failingFiles) Mkdir(context.Context, string) error                 { return f.err }
func (f failingFiles) Remove(context.Context, string) error                { return f.err }

func testConfig() Config {
	return Config{S3Endpoint: "http://s3.internal:9000", NodeEndpoint: "https://s3.example.net", AccessKey: "kid", SecretKey: "ksecret",
		CapacityGB: 20, ClientVersion: DefaultClientVersion, ClientBase: DefaultClientBase, Assets: DefaultAssets}
}

func newTest(t *testing.T) (*Service, *fakeStore, *store.Store) {
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
	fs := newFakeStore(t.TempDir())
	s := New(testConfig(), st.Pool, v, fs, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.UnmountPoll = 10 * time.Millisecond
	return s, fs, st
}

func TestNames(t *testing.T) {
	n := NamesOf("u_01JD3V0000000000000000000A")
	if n.Prefix != "users/"+n.ID+"/" || n.Name != "atu-"+n.ID || n.User != "atu"+n.ID || len(n.ID) != 16 ||
		strings.Contains(n.Prefix, "u_01") || prefixID(n.Prefix) != n.ID || keyID(n.Name) != n.ID || keyID(n.User) != n.ID {
		t.Fatalf("%+v", n)
	}
	if NamesOf("u1") == NamesOf("u2") || prefixID("users/xyz/") != "" || prefixID("users/0123456789abcdef") != "" ||
		prefixID("other/0123456789abcdef/") != "" || keyID("agent-teams") != "" || keyID("atu-0123456789ABCDEF") != "" {
		t.Fatal("names")
	}
	rw, ro := string(FolderPolicy(Folder{"fs", n.Prefix}, true)), string(FolderPolicy(Folder{"fs", n.Prefix}, false))
	if !strings.Contains(rw, "arn:aws:s3:::fs/"+n.Prefix+"*") || !strings.Contains(rw, `"s3:prefix":["`+n.Prefix+`","`+n.Prefix+`*"]`) ||
		!strings.Contains(rw, "s3:PutObject") || strings.Contains(rw, "s3:*") || strings.Contains(rw, "DeleteBucket") {
		t.Fatalf("policy %s", rw)
	}
	if strings.Contains(ro, "s3:PutObject") || !strings.Contains(ro, "s3:DeleteObject") {
		t.Fatalf("read-only policy %s", ro)
	}
	whole := string(FolderPolicy(Folder{Bucket: n.Name}, true))
	if !strings.Contains(whole, "arn:aws:s3:::"+n.Name+"/*") || strings.Contains(whole, "s3:prefix") || strings.Contains(whole, "DeleteBucket") {
		t.Fatalf("bucket policy %s", whole)
	}
	if folderID(Folder{Bucket: n.Name}) != n.ID || folderID(Folder{Bucket: "fs", Prefix: n.Prefix}) != n.ID || folderID(Folder{Bucket: "attachments"}) != "" {
		t.Fatal("folder ids")
	}
	assets := testConfig().ClientAssets()
	if len(assets) != 4 || !strings.HasSuffix(assets[0].URL, "/v"+DefaultClientVersion+"/geesefs-linux-amd64") || !strings.HasSuffix(assets[3].URL, "/geesefs-mac-arm64") {
		t.Fatalf("assets %+v", assets)
	}
}

func TestEnableMountDisable(t *testing.T) {
	s, fs, st := newTest(t)
	ctx := context.Background()
	var changes []string
	s.OnChange = func(_ context.Context, uid string) { changes = append(changes, uid) }

	if _, err := s.MountSpec(ctx, "u1"); !IsNotFound(err) {
		t.Fatalf("mount spec before enable: %v", err)
	}
	vol, err := s.Enable(ctx, "u1")
	n := NamesOf("u1")
	if err != nil || vol.State != StateReady || vol.CapacityGB != 20 || vol.Bucket != "fs" || vol.Prefix != n.Prefix || vol.AccessKey == "" || vol.ReadOnly {
		t.Fatalf("enable: %+v %v", vol, err)
	}
	for _, d := range []string{"projects", SharedDir} {
		if fi, err := os.Stat(fs.userPath("u1", d)); err != nil || !fi.IsDir() {
			t.Fatalf("folder %s: %v", d, err)
		}
	}
	key, ok := fs.key(vol.AccessKey)
	if !ok || key.folder != (Folder{"fs", n.Prefix}) || key.name != n.Name || !key.write || fs.keyCount() != 1 {
		t.Fatalf("key %+v %v (%d keys)", key, ok, fs.keyCount())
	}
	spec, err := s.MountSpec(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Name != n.Name || spec.Bucket != "fs" || spec.Prefix != n.Prefix || spec.AccessKey != vol.AccessKey || spec.SecretKey != key.secret ||
		spec.Endpoint != "https://s3.example.net" || spec.Region == nil || *spec.Region != "us-east-1" || spec.Client.Tool != "geesefs" ||
		len(spec.Client.Assets) != 4 || spec.Generation != vol.Generation {
		t.Fatalf("spec: %+v", spec)
	}
	if !s.Ready(ctx, "u1") || len(changes) != 1 {
		t.Fatalf("ready %v changes %v", s.Ready(ctx, "u1"), changes)
	}
	if err := os.WriteFile(fs.userPath("u1", "projects", "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if u, err := s.Usage(ctx, "u1"); err != nil || u.UsedBytes != 5 || u.Objects != 1 {
		t.Fatalf("usage %+v %v", u, err)
	}
	// Enabling again is a no-op.
	if _, err := s.Enable(ctx, "u1"); err != nil || len(changes) != 1 || fs.keyCount() != 1 {
		t.Fatalf("second enable: %v %v", err, changes)
	}
	// A second user gets a folder and key of its own.
	if _, err := store.Exec(ctx, st.Pool, `INSERT INTO at_users (id, email, password_hash) VALUES ('u2', 'b@example.com', 'x')`); err != nil {
		t.Fatal(err)
	}
	spec2 := mustReady(t, s, "u2")
	if spec2.Prefix == spec.Prefix || spec2.Name == spec.Name || spec2.AccessKey == spec.AccessKey || spec2.SecretKey == spec.SecretKey {
		t.Fatalf("u2 shares u1's folder or key: %+v", spec2)
	}
	if err := s.Disable(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	if _, err := s.Get(ctx, "u1"); !IsNotFound(err) || len(changes) != 3 {
		t.Fatalf("after disable: %v changes %v", err, changes)
	}
	if _, err := os.Stat(fs.userPath("u1")); !os.IsNotExist(err) {
		t.Fatalf("u1 folder left: %v", err)
	}
	if _, ok := fs.key(vol.AccessKey); ok {
		t.Fatal("u1's key not revoked")
	}
	if _, err := os.Stat(fs.userPath("u2", "projects")); err != nil || fs.keyCount() != 1 {
		t.Fatalf("u2 touched: %v %d keys", err, fs.keyCount())
	}
}

func mustReady(t *testing.T, s *Service, userID string) protocol.FsMount {
	t.Helper()
	if vol, err := s.Enable(context.Background(), userID); err != nil || vol.State != StateReady {
		t.Fatalf("enable %s: %+v %v", userID, vol, err)
	}
	spec, err := s.MountSpec(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestEnableFailureThenRetry(t *testing.T) {
	s, fs, _ := newTest(t)
	ctx := context.Background()
	fs.fail = errors.New("connection refused")
	vol, err := s.Enable(ctx, "u1")
	if err != nil || vol.State != StateError || !strings.Contains(vol.Error, "connection refused") {
		t.Fatalf("%+v %v", vol, err)
	}
	fs.fail = nil
	if vol, err = s.Enable(ctx, "u1"); err != nil || vol.State != StateReady || vol.Error != "" {
		t.Fatalf("retry: %+v %v", vol, err)
	}
}

func TestDisabledConfig(t *testing.T) {
	s, _, _ := newTest(t)
	s.cfg.SecretKey = ""
	if _, err := s.Enable(context.Background(), "u1"); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
}

// TestEnableFailureReplacesKey: a provisioning that failed after issuing a key gets a new one on
// retry and the earlier key is revoked.
func TestEnableFailureReplacesKey(t *testing.T) {
	s, fs, st := newTest(t)
	ctx := context.Background()
	vol := mustVolume(t, s, "u1")
	first := vol.AccessKey
	if _, err := store.Exec(ctx, st.Pool, `UPDATE at_user_fs SET state = 'error' WHERE user_id = 'u1'`); err != nil {
		t.Fatal(err)
	}
	vol, err := s.Enable(ctx, "u1")
	if err != nil || vol.State != StateReady || vol.AccessKey == first {
		t.Fatalf("retry: %+v %v", vol, err)
	}
	if _, ok := fs.key(first); ok || fs.keyCount() != 1 {
		t.Fatalf("earlier key kept (%d keys)", fs.keyCount())
	}
}

func mustVolume(t *testing.T, s *Service, userID string) Volume {
	t.Helper()
	vol, err := s.Enable(context.Background(), userID)
	if err != nil || vol.State != StateReady {
		t.Fatalf("enable %s: %+v %v", userID, vol, err)
	}
	return vol
}

// TestEnforceQuotas: a folder over its capacity gets a key that cannot write (new generation,
// nodes told, old key revoked); back under it, a full key again; within it, nothing changes.
func TestEnforceQuotas(t *testing.T) {
	s, fs, _ := newTest(t)
	ctx := context.Background()
	var changes int
	s.OnChange = func(context.Context, string) { changes++ }
	vol := mustVolume(t, s, "u1")
	changes = 0
	if r, err := s.EnforceQuotas(ctx); err != nil || r != (QuotaResult{Checked: 1}) || changes != 0 {
		t.Fatalf("within capacity: %+v %v %d", r, err, changes)
	}
	fs.usage = 21 << 30
	r, err := s.EnforceQuotas(ctx)
	locked, _ := s.Get(ctx, "u1")
	if err != nil || r.Locked != 1 || changes != 1 || !locked.ReadOnly || locked.Generation <= vol.Generation || locked.AccessKey == vol.AccessKey {
		t.Fatalf("over capacity: %+v %v %d %+v", r, err, changes, locked)
	}
	if k, ok := fs.key(locked.AccessKey); !ok || k.write {
		t.Fatalf("locked key %+v %v", k, ok)
	}
	if _, ok := fs.key(vol.AccessKey); ok {
		t.Fatal("full key kept after the lock")
	}
	spec, err := s.MountSpec(ctx, "u1")
	if err != nil || spec.AccessKey != locked.AccessKey || spec.Generation != locked.Generation {
		t.Fatalf("spec after lock: %+v %v", spec, err)
	}
	if r, _ := s.EnforceQuotas(ctx); r.Locked != 0 || changes != 1 {
		t.Fatalf("locked twice: %+v", r)
	}
	fs.usage = 1 << 30
	r, err = s.EnforceQuotas(ctx)
	back, _ := s.Get(ctx, "u1")
	if err != nil || r.Unlocked != 1 || changes != 2 || back.ReadOnly {
		t.Fatalf("back under: %+v %v %+v", r, err, back)
	}
	if k, ok := fs.key(back.AccessKey); !ok || !k.write || fs.keyCount() != 1 {
		t.Fatalf("unlocked key %+v %v", k, ok)
	}
}

// TestHardQuotaStore: on a store with quotas (RustFS) each user gets a bucket of its own with the
// capacity as its quota, the mount names the bucket without a prefix, and no soft-quota round runs.
func TestHardQuotaStore(t *testing.T) {
	s, fs, _ := newTest(t)
	fs.hard = true
	ctx := context.Background()
	vol := mustVolume(t, s, "u1")
	n := NamesOf("u1")
	if vol.Bucket != n.Name || vol.Prefix != "" || fs.quotas[n.Name] != 20 || !s.Enabled() || s.SoftQuota() {
		t.Fatalf("%+v quotas %v", vol, fs.quotas)
	}
	spec, err := s.MountSpec(ctx, "u1")
	if err != nil || spec.Bucket != n.Name || spec.Prefix != "" || spec.Name != n.Name {
		t.Fatalf("spec %+v %v", spec, err)
	}
	if k, _ := fs.key(vol.AccessKey); k.folder != (Folder{Bucket: n.Name}) {
		t.Fatalf("key %+v", k)
	}
	fs.usage = 100 << 30
	if r, err := s.EnforceQuotas(ctx); err != nil || r != (QuotaResult{}) {
		t.Fatalf("soft quota ran: %+v %v", r, err)
	}
	if err := s.Disable(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	if exists(fs.userPath("u1")) || fs.keyCount() != 0 {
		t.Fatal("bucket or key left")
	}
}
