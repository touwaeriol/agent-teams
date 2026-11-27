package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/testdb"
)

func newRegistry(t *testing.T) *Registry {
	t.Helper()
	st, err := store.Open(context.Background(), testdb.Migrated(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	r := New(st.Pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.Poll = 20 * time.Millisecond
	r.FetchPoll = 20 * time.Millisecond
	return r
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (r *Registry) state(t *testing.T, name string) Control {
	t.Helper()
	ctl, err := r.Controls(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return ctl[name]
}

// TestRegistry: on River, a job runs at start and on its interval, records its last run and the
// runs that did something; a wake runs it at once; paused it runs only when asked; an interval
// override applies; a failing or panicking run is recorded as failed and the schedule goes on.
func TestRegistry(t *testing.T) {
	r := newRegistry(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var runs, woken atomic.Int32
	fail := atomic.Value{}
	fail.Store("")
	wake := make(chan struct{}, 1)
	r.Add(Job{Name: "probe", Title: "探针", Every: time.Hour, Wake: wake,
		Run: func(_ context.Context, t Trigger) (string, error) {
			runs.Add(1)
			if t == Woken {
				woken.Add(1)
			}
			switch fail.Load() {
			case "error":
				return "", errors.New("boom")
			case "panic":
				panic("kaboom")
			}
			return "做了 1 件事", nil
		}})
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the run at start", func() bool { return r.state(t, "probe").Runs == 1 })
	if c := r.state(t, "probe"); c.LastOK == nil || !*c.LastOK || c.LastSummary != "做了 1 件事" || c.LastReplica != r.Replica {
		t.Fatalf("after the first run %+v", c)
	}

	wake <- struct{}{}
	eventually(t, "the woken run", func() bool { return woken.Load() == 1 })

	if err := r.SetPaused(ctx, "probe", true); err != nil {
		t.Fatal(err)
	}
	if err := r.SetEvery(ctx, "probe", 1); err != nil { // due every second, but paused
		t.Fatal(err)
	}
	before := runs.Load()
	time.Sleep(1300 * time.Millisecond)
	wake <- struct{}{}
	time.Sleep(100 * time.Millisecond)
	if runs.Load() != before {
		t.Fatalf("a paused job ran: %d → %d", before, runs.Load())
	}

	fail.Store("error")
	if err := r.RequestRun(ctx, "probe"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the asked-for run", func() bool { return runs.Load() == before+1 })
	eventually(t, "the failure recorded", func() bool {
		c := r.state(t, "probe")
		return c.LastOK != nil && !*c.LastOK && c.LastError == "boom" && c.Failures == 1
	})

	fail.Store("panic")
	if err := r.SetPaused(ctx, "probe", false); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the panic recorded", func() bool { return r.state(t, "probe").Failures == 2 })
	fail.Store("")
	eventually(t, "the interval override", func() bool { c := r.state(t, "probe"); return c.LastOK != nil && *c.LastOK })

	list, err := r.Runs(ctx, "probe", 50)
	if err != nil || len(list) < 4 {
		t.Fatalf("runs %d %v", len(list), err)
	}
	triggers := map[string]bool{}
	for _, x := range list {
		triggers[x.Trigger] = true
	}
	if !triggers["manual"] || !triggers["woken"] || !triggers["scheduled"] {
		t.Fatalf("triggers %v", triggers)
	}
	if _, err := r.Runs(ctx, "nope", 10); !errors.Is(err, ErrUnknown) {
		t.Fatalf("unknown job: %v", err)
	}
	if err := r.SetPaused(ctx, "nope", true); !errors.Is(err, ErrUnknown) {
		t.Fatalf("unknown job: %v", err)
	}
	if n, err := r.Prune(ctx, -time.Hour); err != nil || n == 0 {
		t.Fatalf("prune %d %v", n, err)
	}
}

// TestNothingWorthKeeping: a scheduled run that did nothing updates the job's row only.
func TestNothingWorthKeeping(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()
	if _, err := store.Exec(ctx, r.DB, `INSERT INTO at_jobs (name) VALUES ('idle')`); err != nil {
		t.Fatal(err)
	}
	j := Job{Name: "idle", Every: time.Minute, Run: func(context.Context, Trigger) (string, error) { return "", nil }}
	r.Add(j)
	r.RunOnce(ctx, j, Scheduled)
	if c := r.state(t, "idle"); c.Runs != 1 || c.RunningSince != nil {
		t.Fatalf("row %+v", c)
	}
	if list, _ := r.Runs(ctx, "idle", 10); len(list) != 0 {
		t.Fatalf("kept %d runs", len(list))
	}
}

// TestCronSchedule: a cron schedule replaces the interval (read in its zone), an interval drops
// it again; malformed, too frequent or never-running schedules and unknown zones are refused.
func TestCronSchedule(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()
	j := Job{Name: "nightly", Every: time.Hour, Run: func(context.Context, Trigger) (string, error) { return "", nil }}
	r.Add(j)
	if _, err := store.Exec(ctx, r.DB, `INSERT INTO at_jobs (name) VALUES ('nightly')`); err != nil {
		t.Fatal(err)
	}
	if err := r.SetCron(ctx, "nightly", " 0  3 * * * ", "Asia/Shanghai"); err != nil {
		t.Fatal(err)
	}
	c := r.state(t, "nightly")
	if c.Cron != "0 3 * * *" || c.Timezone != "Asia/Shanghai" {
		t.Fatalf("stored %+v", c)
	}
	sh, _ := time.LoadLocation("Asia/Shanghai")
	from := time.Date(2026, 11, 27, 12, 0, 0, 0, sh)
	if next := c.Next(j, time.Time{}, from); !next.Equal(time.Date(2026, 11, 28, 3, 0, 0, 0, sh)) {
		t.Fatalf("next %v", next)
	}
	last := time.Date(2026, 11, 28, 3, 0, 1, 0, sh)
	if next := c.Next(j, last, from); !next.Equal(time.Date(2026, 11, 29, 3, 0, 0, 0, sh)) {
		t.Fatalf("next after a run %v", next)
	}
	for _, bad := range [][2]string{{"61 * * * *", ""}, {"@every 1s", ""}, {"0 0 31 2 *", ""}, {"0 3 * * *", "Mars/Base"}} {
		if err := r.SetCron(ctx, "nightly", bad[0], bad[1]); err == nil {
			t.Fatalf("%q in %q accepted", bad[0], bad[1])
		}
	}
	if err := r.SetEvery(ctx, "nightly", 120); err != nil {
		t.Fatal(err)
	}
	if c := r.state(t, "nightly"); c.Cron != "" || c.Interval(j) != 2*time.Minute {
		t.Fatalf("interval did not replace cron %+v", c)
	}
	if err := r.SetCron(ctx, "nightly", "", ""); err != nil {
		t.Fatal(err)
	}
	if c := r.state(t, "nightly"); c.Cron != "" || c.EverySeconds != nil || c.Interval(j) != time.Hour {
		t.Fatalf("reset %+v", c)
	}
}

// TestOneAtATime: runs asked for together wait for each other (the task's lock); a task on a
// cron schedule does not run at start.
func TestOneAtATime(t *testing.T) {
	r := newRegistry(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var now, peak, done atomic.Int32
	r.Add(Job{Name: "slow", Every: time.Hour, Run: func(context.Context, Trigger) (string, error) {
		n := now.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		time.Sleep(300 * time.Millisecond)
		now.Add(-1)
		done.Add(1)
		return "", nil
	}})
	var cronRuns atomic.Int32
	r.Add(Job{Name: "cron", Every: time.Hour, Run: func(context.Context, Trigger) (string, error) { cronRuns.Add(1); return "", nil }})
	if _, err := store.Exec(ctx, r.DB, `INSERT INTO at_jobs (name, cron) VALUES ('cron', '0 3 1 1 *')`); err != nil {
		t.Fatal(err)
	}
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the run at start", func() bool { return done.Load() == 1 })
	for range 2 {
		if err := r.RequestRun(ctx, "slow"); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "both asked-for runs", func() bool { return done.Load() == 3 })
	if peak.Load() != 1 {
		t.Fatalf("%d runs at once", peak.Load())
	}
	if cronRuns.Load() != 0 {
		t.Fatal("a cron task ran at start")
	}
}
