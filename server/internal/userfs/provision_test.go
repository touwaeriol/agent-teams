package userfs

import (
	"context"
	"errors"
	"os"
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

// TestGenerationGrowsAcrossReenable: a folder deleted and made again keeps its deterministic
// folder but never its generation (nodes remount), and its new secret opens under the new one.
func TestGenerationGrowsAcrossReenable(t *testing.T) {
	s, _, _ := newTest(t)
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
	if second.Prefix != first.Prefix || second.Generation <= first.Generation {
		t.Fatalf("generation %d after %d", second.Generation, first.Generation)
	}
	if spec, err := s.MountSpec(ctx, "u1"); err != nil || spec.Generation != second.Generation {
		t.Fatalf("mount spec %+v %v", spec, err)
	}
}

// TestConcurrentEnableProvisionsOnce: only the call that created (or claimed) the row provisions.
func TestConcurrentEnableProvisionsOnce(t *testing.T) {
	s, fs, _ := newTest(t)
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
	if fs.prepares != 1 || fs.keyCount() != 1 {
		t.Fatalf("provisioned %d times, %d keys", fs.prepares, fs.keyCount())
	}
}

// TestEnableSurvivesClientCancel: a cancelled request still finishes provisioning.
func TestEnableSurvivesClientCancel(t *testing.T) {
	s, _, _ := newTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if vol, err := s.Enable(ctx, "u1"); err != nil || vol.State != StateReady {
		t.Fatalf("%+v %v", vol, err)
	}
}

// TestDisableWhileProvisioningIsBusy: a deletion does not race a provisioning; one already
// running is not started twice.
func TestDisableWhileProvisioningIsBusy(t *testing.T) {
	s, fs, st := newTest(t)
	ctx := context.Background()
	if _, _, err := insert(ctx, st.Pool, Folder{Bucket: "fs", Prefix: NamesOf("u1").Prefix}, "u1", 20); err != nil {
		t.Fatal(err)
	}
	if err := s.Disable(ctx, "u1"); !errors.Is(err, ErrBusy) {
		t.Fatalf("disable while provisioning: %v", err)
	}
	if vol, err := s.Enable(ctx, "u1"); err != nil || vol.State != StateProvisioning || fs.prepares != 0 {
		t.Fatalf("enable while another provisions: %+v %v prepares %d", vol, err, fs.prepares)
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
	if err := s.Disable(ctx, "u1"); err != nil || !exists(fs.userPath("u1")) {
		t.Fatalf("disable while deleting: %v", err)
	}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// TestDisableWaitsForUnmount: the key is revoked at once, the folder removed only once the daemon
// that mounted it reports it unmounted (another folder does not count); a node mounting it outside
// the daemon cannot unmount, so the deletion goes on after the bounded wait.
func TestDisableWaitsForUnmount(t *testing.T) {
	s, fs, st := newTest(t)
	ctx := context.Background()
	vol, err := s.Enable(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	setNodeFs(t, st, "n1", `{"state":"mounted","mode":"daemon","name":"`+vol.Names().Name+`","generation":1}`)
	setNodeFs(t, st, "n2", `{"state":"mounted","mode":"daemon","name":"atu-other"}`)
	if err := s.Disable(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, "u1"); got.State != StateDeleting {
		t.Fatalf("state %q", got.State)
	}
	time.Sleep(100 * time.Millisecond)
	if !exists(fs.userPath("u1")) {
		t.Fatal("removed while a node still mounts it")
	}
	if fs.keyCount() != 0 {
		t.Fatal("key not revoked at once")
	}
	setNodeFs(t, st, "n1", `{"state":"unmounted","mode":"daemon"}`)
	s.Wait()
	if _, err := s.Get(ctx, "u1"); !IsNotFound(err) || exists(fs.userPath("u1")) {
		t.Fatalf("after unmount: %v", err)
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
	if exists(fs.userPath("u1")) {
		t.Fatal("external mount held the deletion")
	}
}

// TestDisableWithoutKey: a folder whose provisioning failed before it had a key is
// deleted all the same.
func TestDisableWithoutKey(t *testing.T) {
	s, fs, _ := newTest(t)
	ctx := context.Background()
	fs.fail = errors.New("connection refused")
	if vol, _ := s.Enable(ctx, "u1"); vol.State != StateError {
		t.Fatalf("state %q", vol.State)
	}
	fs.fail = nil
	if err := s.Disable(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	if _, err := s.Get(ctx, "u1"); !IsNotFound(err) {
		t.Fatal(err)
	}
}

// TestProvisionAll: the folder is on by default: every active user without one gets it; a failed
// attempt waits retryAfter before the next sweep tries again; a deactivated account gets none.
func TestProvisionAll(t *testing.T) {
	s, fs, st := newTest(t)
	ctx := context.Background()
	if _, err := store.Exec(ctx, st.Pool, `INSERT INTO at_users (id, email, password_hash) VALUES ('u2', 'b@example.com', 'x');
		INSERT INTO at_users (id, email, password_hash, active) VALUES ('u3', 'c@example.com', 'x', false)`); err != nil {
		t.Fatal(err)
	}
	fs.fail = errors.New("connection refused")
	if n, err := s.ProvisionAll(ctx); err != nil || n != 0 {
		t.Fatalf("failing sweep: %d %v", n, err)
	}
	fs.fail = nil
	if n, err := s.ProvisionAll(ctx); err != nil || n != 0 {
		t.Fatalf("a failed volume is retried at once: %d %v", n, err)
	}
	if _, err := store.Exec(ctx, st.Pool, `UPDATE at_user_fs SET updated_at = now() - interval '11 minutes'`); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ProvisionAll(ctx); err != nil || n != 2 {
		t.Fatalf("retry: %d %v", n, err)
	}
	for _, id := range []string{"u1", "u2"} {
		if !s.Ready(ctx, id) {
			t.Fatalf("%s not ready", id)
		}
	}
	if n, err := s.ProvisionAll(ctx); err != nil || n != 0 {
		t.Fatalf("nothing left: %d %v", n, err)
	}
	if _, err := s.Get(ctx, "u3"); !IsNotFound(err) {
		t.Fatalf("a deactivated account got a folder: %v", err)
	}
}
