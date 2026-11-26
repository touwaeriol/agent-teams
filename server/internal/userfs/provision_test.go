package userfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func setNodeFs(t *testing.T, st *store.Store, nodeID, status string) {
	t.Helper()
	if _, err := store.Exec(context.Background(), st.Pool, `INSERT INTO at_nodes (id, user_id, name, token_hash, fs_status)
		VALUES ($1, 'u1', $1, $1, $2) ON CONFLICT (id) DO UPDATE SET fs_status = EXCLUDED.fs_status`, nodeID, status); err != nil {
		t.Fatal(err)
	}
}

// TestGenerationGrowsAcrossReenable: a volume switched off and on again keeps its deterministic
// name but never its generation, and its secrets open under the new one.
func TestGenerationGrowsAcrossReenable(t *testing.T) {
	s, _, _, _, _ := newTest(t)
	ctx := context.Background()
	first, err := s.Enable(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Disable(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	second, err := s.Enable(ctx, "u1")
	if err != nil || second.State != StateReady {
		t.Fatalf("re-enable: %+v %v", second, err)
	}
	if second.Volume != first.Volume || second.Generation <= first.Generation {
		t.Fatalf("generation %d after %d", second.Generation, first.Generation)
	}
	if spec, err := s.MountSpec(ctx, "u1"); err != nil || spec.Generation != second.Generation {
		t.Fatalf("mount spec %+v %v", spec, err)
	}
}

// TestConcurrentEnableFormatsOnce: only the call that created (or claimed) the row provisions.
func TestConcurrentEnableFormatsOnce(t *testing.T) {
	s, _, _, vols, _ := newTest(t)
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Enable(context.Background(), "u1"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if vols.formats != 1 {
		t.Fatalf("formatted %d times", vols.formats)
	}
}

// TestEnableSurvivesClientCancel: a cancelled request still finishes provisioning.
func TestEnableSurvivesClientCancel(t *testing.T) {
	s, _, _, _, _ := newTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if vol, err := s.Enable(ctx, "u1"); err != nil || vol.State != StateReady {
		t.Fatalf("%+v %v", vol, err)
	}
}

// TestDisableWhileProvisioningIsBusy: a deletion does not race a provisioning; one already
// running is not started twice.
func TestDisableWhileProvisioningIsBusy(t *testing.T) {
	s, _, _, vols, st := newTest(t)
	ctx := context.Background()
	if _, _, err := insert(ctx, st.Pool, s.vault, "u1", 20); err != nil {
		t.Fatal(err)
	}
	if err := s.Disable(ctx, "u1"); !errors.Is(err, ErrBusy) {
		t.Fatalf("disable while provisioning: %v", err)
	}
	if vol, err := s.Enable(ctx, "u1"); err != nil || vol.State != StateProvisioning || vols.formats != 0 {
		t.Fatalf("enable while another provisions: %+v %v formats %d", vol, err, vols.formats)
	}
	// An abandoned provisioning is taken over.
	if _, err := store.Exec(ctx, st.Pool, `UPDATE at_user_fs SET updated_at = now() - interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	if vol, err := s.Enable(ctx, "u1"); err != nil || vol.State != StateReady {
		t.Fatalf("take over: %+v %v", vol, err)
	}
	if err := setState(ctx, st.Pool, "u1", StateDeleting, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Disable(ctx, "u1"); err != nil || vols.destroys() != 0 {
		t.Fatalf("disable while deleting: %v destroyed %d", err, vols.destroys())
	}
}

// TestDisableWaitsForUnmount: the volume is destroyed only once the daemon that mounted it
// reports it unmounted; a Docker-mounted node cannot unmount, so the deletion goes on after the
// bounded wait.
func TestDisableWaitsForUnmount(t *testing.T) {
	s, _, _, vols, st := newTest(t)
	ctx := context.Background()
	vol, err := s.Enable(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	setNodeFs(t, st, "n1", `{"state":"mounted","mode":"daemon","volume":"`+vol.Volume+`","generation":1}`)
	setNodeFs(t, st, "n2", `{"state":"mounted","mode":"daemon","volume":"at-other"}`)
	if err := s.Disable(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, "u1"); got.State != StateDeleting {
		t.Fatalf("state %q", got.State)
	}
	time.Sleep(100 * time.Millisecond)
	if vols.destroys() != 0 {
		t.Fatal("destroyed while a node still mounts it")
	}
	setNodeFs(t, st, "n1", `{"state":"unmounted","mode":"daemon"}`)
	s.Wait()
	if _, err := s.Get(ctx, "u1"); !IsNotFound(err) || vols.destroys() != 1 {
		t.Fatalf("after unmount: %v destroyed %d", err, vols.destroys())
	}

	s.UnmountWait = 50 * time.Millisecond
	if _, err := s.Enable(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	setNodeFs(t, st, "n1", `{"state":"mounted","mode":"external"}`)
	if err := s.Disable(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	if vols.destroys() != 2 {
		t.Fatalf("external mount held the deletion: destroyed %d", vols.destroys())
	}
}

// TestDisableWithoutMetadata: a volume whose provisioning never created its role (state error)
// is deleted without juicefs destroy.
func TestDisableWithoutMetadata(t *testing.T) {
	s, m, _, vols, _ := newTest(t)
	ctx := context.Background()
	m.fail = errors.New("connection refused")
	if vol, _ := s.Enable(ctx, "u1"); vol.State != StateError {
		t.Fatalf("state %q", vol.State)
	}
	if err := s.Disable(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	if _, err := s.Get(ctx, "u1"); !IsNotFound(err) || vols.destroys() != 0 {
		t.Fatalf("%v destroyed %d", err, vols.destroys())
	}
}

// TestBeforeTeardown: the hook reads the volume (read-only) before it is destroyed; its failure
// stops the deletion with the reason.
func TestBeforeTeardown(t *testing.T) {
	s, _, _, vols, _ := newTest(t)
	ctx := context.Background()
	if _, err := s.Enable(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	f, _ := s.Files(ctx, "u1")
	if err := f.Put(ctx, "projects/p1/.deliverables/d1/v1/a.md", strings.NewReader("a"), 1); err != nil {
		t.Fatal(err)
	}
	var read string
	fail := errors.New("database full")
	s.BeforeTeardown = func(ctx context.Context, userID string) error {
		f, err := s.Files(ctx, userID)
		if err != nil {
			return err
		}
		if err := f.Put(ctx, "x", strings.NewReader("x"), 1); !errors.Is(err, ErrReadOnly) {
			return errors.New("writable while deleting")
		}
		r, _, err := f.Open(ctx, "projects/p1/.deliverables/d1/v1/a.md")
		if err != nil {
			return err
		}
		defer r.Close()
		b := make([]byte, 1)
		_, _ = r.Read(b)
		read = string(b)
		return fail
	}
	if err := s.Disable(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	vol, err := s.Get(ctx, "u1")
	if err != nil || vol.State != StateError || !strings.Contains(vol.Error, "database full") || vols.destroys() != 0 || read != "a" {
		t.Fatalf("%+v %v destroyed %d read %q", vol, err, vols.destroys(), read)
	}
	if _, err := s.Files(ctx, "u1"); !IsNotFound(err) {
		t.Fatalf("volume opens after the hook: %v", err)
	}
	s.BeforeTeardown = func(context.Context, string) error { return nil }
	if err := s.Disable(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	if _, err := s.Get(ctx, "u1"); !IsNotFound(err) || vols.destroys() != 1 {
		t.Fatalf("retry: %v destroyed %d", err, vols.destroys())
	}
}

// TestConsoleMetaTLS: the console's metadata URLs connect like AGENT_TEAMS_FS_META_ADMIN_URL.
func TestConsoleMetaTLS(t *testing.T) {
	s, _, _, vols, _ := newTest(t)
	s.cfg.AdminURL = "postgres://admin:pw@db.internal:5432/agent_teams?sslmode=verify-full&sslrootcert=/etc/ca.pem"
	ctx := context.Background()
	if _, err := s.Enable(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Files(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Disable(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	urls := append([]string{vols.formatted[NamesFor("u1").Volume].MetaURL}, append(vols.opened, vols.destroyAt...)...)
	for _, u := range urls {
		if !strings.Contains(u, "sslmode=verify-full") || !strings.Contains(u, "sslrootcert=%2Fetc%2Fca.pem") || strings.Contains(u, "pw") {
			t.Fatalf("meta url %q", u)
		}
	}
	if len(urls) != 3 {
		t.Fatalf("urls %v", urls)
	}
}

// TestGatewaysStartOutsideTheLock: a slow start of one volume's gateway does not hold up another
// volume; Close stops them and refuses new ones.
func TestGatewaysStartOutsideTheLock(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "juicefs")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	g := &Gateways{Bin: bin, StartTimeout: 10 * time.Second}
	go func() { _, _ = g.Files(context.Background(), "at-a", "sqlite3://a", "") }()
	time.Sleep(100 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := g.Files(ctx, "at-b", "sqlite3://b", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second volume: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("second volume waited for the first one's start")
	}
	done := make(chan struct{})
	go func() { g.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Close hung")
	}
	if _, err := g.Files(context.Background(), "at-c", "sqlite3://c", ""); !errors.Is(err, errGatewaysClosed) {
		t.Fatalf("after close: %v", err)
	}
}

// TestGatewayReaperSkipsInUse: an idle gateway is stopped unless a call or an open file holds it.
func TestGatewayReaperSkipsInUse(t *testing.T) {
	ready := make(chan struct{})
	close(ready)
	gw := &gateway{ready: ready, done: make(chan struct{}), last: time.Now().Add(-time.Hour)}
	g := &Gateways{running: map[string]*gateway{"at-a": gw}}
	g.acquire(gw)
	gw.last = time.Now().Add(-time.Hour)
	g.reapIdle(time.Minute)
	if g.running["at-a"] == nil {
		t.Fatal("reaped while in use")
	}
	g.release(gw)
	gw.last = time.Now().Add(-time.Hour)
	g.reapIdle(time.Minute)
	if g.running["at-a"] != nil {
		t.Fatal("idle gateway kept")
	}
}
