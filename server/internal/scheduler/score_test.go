package scheduler

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) time.Time { return t0.Add(-d) }

func until(d time.Duration) *time.Time {
	t := t0.Add(d)
	return &t
}

// dev builds a project member of role dev working in thread th.
func dev(name, th string, repos ...string) seat {
	return seat{Name: name, Role: "dev", Thread: th, Repos: repos}
}

// work builds a fresh pooled dev task of thread th.
func work(th string) job {
	return job{Thread: th, Role: "dev", Priority: 100, Ready: ago(time.Minute), Created: ago(time.Minute)}
}

// rev builds a project member of role reviewer working in thread th.
func rev(name, th string) seat {
	return seat{Name: name, Role: "reviewer", Thread: th}
}

// gate builds a platform review gate of thread th on work by notBy (ClaimableTasks sets NotBy
// only while a peer of the role exists).
func gate(th, notBy string) job {
	return job{Thread: th, Role: "reviewer", Gate: true, NotBy: notBy, Priority: 50, Ready: ago(time.Minute), Created: ago(time.Minute)}
}

// inTurn marks a member as mid-turn in thread th.
func inTurn(s seat, th string) seat {
	s.TurnThread = th
	return s
}

func with(j job, f func(*job)) job {
	f(&j)
	return j
}

func TestPick(t *testing.T) {
	cases := []struct {
		name  string
		seats []seat
		jobs  []job
		seat  int
		job   int
		none  bool
	}{
		{name: "nobody of the role", seats: []seat{{Name: "qa-1", Role: "qa"}}, jobs: []job{work("A")}, none: true},
		{name: "plain: first task to first member", seats: []seat{dev("dev-1", ""), dev("dev-2", "")},
			jobs: []job{work("A"), work("B")}, seat: 0, job: 0},
		{name: "named beats thread stickiness", seats: []seat{dev("dev-1", "A"), dev("dev-2", "")},
			jobs: []job{work("A"), with(work("B"), func(j *job) { j.Named = "dev-2" })}, seat: 1, job: 1},
		{name: "named task only for its member", seats: []seat{dev("dev-1", "")},
			jobs: []job{with(work("A"), func(j *job) { j.Named = "dev-2" })}, none: true},
		{name: "mid-turn member takes no task of another thread", seats: []seat{inTurn(dev("dev-1", "A"), "A")},
			jobs: []job{work("B")}, none: true},
		{name: "mid-turn member takes a task of its own thread", seats: []seat{inTurn(dev("dev-1", "A"), "A"), dev("dev-2", "")},
			jobs: []job{work("B"), work("A")}, seat: 0, job: 1},
		{name: "reserved for it", seats: []seat{dev("dev-1", "B"), dev("dev-2", "A")},
			jobs: []job{work("A"), with(work("B"), func(j *job) { j.ReservedFor, j.ReservedUntil = "dev-2", until(time.Minute) })},
			seat: 1, job: 1},
		{name: "gate goes to one of three reviewers", seats: []seat{rev("reviewer-1", ""), rev("reviewer-2", ""), rev("reviewer-3", "")},
			jobs: []job{gate("A", "")}, seat: 0, job: 0},
		{name: "gate skips the author of the checked work", seats: []seat{rev("reviewer-1", "A"), rev("reviewer-2", "")},
			jobs: []job{gate("A", "reviewer-1")}, seat: 1, job: 0},
		{name: "gate waits for a peer rather than the author", seats: []seat{rev("reviewer-1", "A")},
			jobs: []job{gate("A", "reviewer-1")}, none: true},
		{name: "starving gate still skips the author", seats: []seat{rev("reviewer-1", "A"), rev("reviewer-2", "")},
			jobs: []job{with(gate("A", "reviewer-1"), func(j *job) { j.Ready = ago(time.Hour) })}, seat: 1, job: 0},
		{name: "reserved for someone else is skipped",seats: []seat{dev("dev-1", "A")},
			jobs: []job{with(work("A"), func(j *job) { j.ReservedFor, j.ReservedUntil = "dev-2", until(time.Minute) })}, none: true},
		{name: "expired reservation is open", seats: []seat{dev("dev-1", "A")},
			jobs: []job{with(work("A"), func(j *job) { j.ReservedFor, j.ReservedUntil = "dev-2", until(-time.Second) })}, seat: 0, job: 0},
		{name: "single dependency by it", seats: []seat{dev("dev-1", "B"), dev("dev-2", "B")},
			jobs: []job{work("B"), with(work("A"), func(j *job) { j.DepWorkers = []string{"dev-2"} })}, seat: 1, job: 1},
		{name: "most dependencies by it", seats: []seat{dev("dev-1", ""), dev("dev-2", "")},
			jobs: []job{with(work("A"), func(j *job) { j.DepWorkers = []string{"dev-1", "dev-2", "dev-2"} })}, seat: 1, job: 0},
		{name: "dependency stickiness beats thread stickiness", seats: []seat{dev("dev-1", "A")},
			jobs: []job{work("A"), with(work("B"), func(j *job) { j.DepWorkers = []string{"dev-1"} })}, seat: 0, job: 1},
		{name: "thread stickiness", seats: []seat{dev("dev-1", "A"), dev("dev-2", "B")},
			jobs: []job{work("A"), work("B")}, seat: 0, job: 0},
		{name: "member sticky to the second thread gets it first", seats: []seat{dev("dev-1", ""), dev("dev-2", "B")},
			jobs: []job{work("A"), work("B")}, seat: 1, job: 1},
		{name: "same repository beats others", seats: []seat{dev("dev-1", "C", "o/api")},
			jobs: []job{with(work("A"), func(j *job) { j.Repos = []string{"o/web"} }), with(work("B"), func(j *job) { j.Repos = []string{"o/api"} })},
			seat: 0, job: 1},
		{name: "ties: priority", seats: []seat{dev("dev-1", "")},
			jobs: []job{work("A"), with(work("A"), func(j *job) { j.Priority = 50 })}, seat: 0, job: 1},
		{name: "ties: more downstream", seats: []seat{dev("dev-1", "")},
			jobs: []job{work("A"), with(work("A"), func(j *job) { j.Downstream = 2 })}, seat: 0, job: 1},
		{name: "ties: priority before downstream", seats: []seat{dev("dev-1", "")},
			jobs: []job{with(work("A"), func(j *job) { j.Downstream = 9 }), with(work("A"), func(j *job) { j.Priority = 50 })}, seat: 0, job: 1},
		{name: "ties: downstream before readiness", seats: []seat{dev("dev-1", "")},
			jobs: []job{with(work("A"), func(j *job) { j.Ready = ago(4 * time.Minute) }), with(work("A"), func(j *job) { j.Downstream = 1 })},
			seat: 0, job: 1},
		{name: "downstream does not beat stickiness", seats: []seat{dev("dev-1", "A")},
			jobs: []job{with(work("B"), func(j *job) { j.Downstream = 9 }), work("A")}, seat: 0, job: 1},
		{name: "ties: ready earlier", seats: []seat{dev("dev-1", "")},
			jobs: []job{work("A"), with(work("A"), func(j *job) { j.Ready = ago(2 * time.Minute) })}, seat: 0, job: 1},
		{name: "ties: created earlier", seats: []seat{dev("dev-1", "")},
			jobs: []job{work("A"), with(work("A"), func(j *job) { j.Created = ago(time.Hour) })}, seat: 0, job: 1},
		{name: "starving task beats stickiness", seats: []seat{dev("dev-1", "A")},
			jobs: []job{with(work("A"), func(j *job) { j.DepWorkers = []string{"dev-1"} }), with(work("B"), func(j *job) { j.Ready = ago(6 * time.Minute) })},
			seat: 0, job: 1},
		{name: "starving: oldest first, over priority", seats: []seat{dev("dev-1", "")},
			jobs: []job{with(work("A"), func(j *job) { j.Ready, j.Priority = ago(6*time.Minute), 10 }), with(work("B"), func(j *job) { j.Ready = ago(9 * time.Minute) })},
			seat: 0, job: 1},
		{name: "starving: any member, stickiness ignored", seats: []seat{dev("dev-1", ""), dev("dev-2", "")},
			jobs: []job{with(work("A"), func(j *job) { j.Ready, j.DepWorkers = ago(6*time.Minute), []string{"dev-2"} })}, seat: 0, job: 0},
		{name: "reservation beats starvation", seats: []seat{dev("dev-1", "")},
			jobs: []job{with(work("A"), func(j *job) { j.Ready = ago(time.Hour) }),
				with(work("B"), func(j *job) { j.ReservedFor, j.ReservedUntil = "dev-1", until(time.Minute) })}, seat: 0, job: 1},
		{name: "legacy member only in its thread", seats: []seat{{Name: "dev-1", Role: "dev", Legacy: "B", Thread: "B"}},
			jobs: []job{work("A")}, none: true},
		{name: "project member hidden by a legacy name", seats: []seat{dev("dev-1", ""), {Name: "dev-1", Role: "dev", Legacy: "A", Thread: "A"}},
			jobs: []job{with(work("A"), func(j *job) { j.Legacy = []string{"dev-1"} })}, seat: 1, job: 0},
		{name: "project member before a legacy member of the thread", seats: []seat{{Name: "dev-3", Role: "dev", Legacy: "A", Thread: "A"}, dev("dev-2", "")},
			jobs: []job{with(work("A"), func(j *job) { j.Legacy = []string{"dev-3"} })}, seat: 1, job: 0},
		{name: "legacy member takes what nobody else can", seats: []seat{{Name: "dev-3", Role: "dev", Legacy: "A", Thread: "A"}},
			jobs: []job{with(work("A"), func(j *job) { j.Legacy = []string{"dev-3"} })}, seat: 0, job: 0},
		{name: "thread role limit", seats: []seat{dev("dev-1", "")},
			jobs: []job{with(work("A"), func(j *job) { j.OnlyRoles = []string{"qa"} })}, none: true},
		{name: "gate ignores the role limit", seats: []seat{dev("dev-1", "")},
			jobs: []job{with(work("A"), func(j *job) { j.OnlyRoles, j.Gate = []string{"qa"}, true })}, seat: 0, job: 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			si, ji, ok := pick(c.seats, c.jobs, t0)
			if c.none {
				if ok {
					t.Fatalf("picked %d/%d, want none", si, ji)
				}
				return
			}
			if !ok || si != c.seat || ji != c.job {
				t.Fatalf("picked %d/%d ok=%v, want %d/%d", si, ji, ok, c.seat, c.job)
			}
		})
	}
}

func TestDepSticky(t *testing.T) {
	cases := []struct {
		name    string
		workers []string
		want    bool
	}{
		{"none", nil, false},
		{"single by it", []string{"dev-1"}, true},
		{"single by another", []string{"dev-2"}, false},
		{"tie counts", []string{"dev-1", "dev-2"}, true},
		{"fewer", []string{"dev-1", "dev-2", "dev-2"}, false},
		{"unclaimed ignored", []string{"", "", "dev-1"}, true},
	}
	for _, c := range cases {
		if got := depSticky("dev-1", c.workers); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}
