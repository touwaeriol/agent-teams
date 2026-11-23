package scheduler

import (
	"cmp"
	"slices"
	"time"
)

// Who takes which task (shared-members.md §4.1). pick is pure: the claim pass loads the idle
// members (seats) and claimable tasks (jobs) and claims the pair it returns.

// StarveAfter is how long a task may stay claimable before stickiness stops counting for it.
const StarveAfter = 5 * time.Minute

// ReserveFor is how long rework stays reserved for the member whose task it continues.
const ReserveFor = 10 * time.Minute

// Tiers of a (member, task) pair, best first.
const (
	tierNamed    = iota // assigned to this instance
	tierReserved        // rework reserved for it
	tierStarving        // claimable for StarveAfter: anyone of the role, oldest first
	tierDeps            // it did the task's dependency (the most of them)
	tierThread          // the thread it works in now
	tierRepo            // a repository its thread works in
	tierOther
	tierLegacy // a legacy member's pooled work: only what no project member takes (shared-members.md §8)
	tierAuthor // a starving gate for the author of the work it checks: only when no peer takes it
)

// seat is an idle member.
type seat struct {
	Name, Role string
	Legacy     string   // a legacy member's own thread (it works only there); "" for project members
	Thread     string   // the thread it works in now ("" none)
	TurnThread string   // the thread of its session while a turn runs there ("" none)
	Repos      []string // the repositories of that thread
}

// job is a claimable task.
type job struct {
	Thread, Role  string
	Named         string // the instance it is assigned to ("" = the role's pool)
	Gate          bool   // a platform gate or fix task: the thread's role limit does not apply
	OnlyRoles     []string
	NotBy         string // a gate's author while a peer of the gate role exists: it does not check its own work until the gate starves
	ReservedFor   string
	ReservedUntil *time.Time
	DepWorkers    []string // who did each dependency
	Repos         []string // the repositories of its thread
	Legacy        []string // the thread's own member names (project members of these names stay out)
	Priority      int      // lower first
	Downstream    int      // unfinished tasks waiting on it, more first
	Ready         time.Time
	Created       time.Time
}

// pick returns the best (member, task) pair, or ok=false when no idle member can take any task.
// Pairs compare by tier, then by the task's urgency within the tier, then by member order.
func pick(seats []seat, jobs []job, now time.Time) (int, int, bool) {
	best := pair{tier: -1}
	for si, s := range seats {
		for ji, j := range jobs {
			t, ok := tier(s, j, now)
			if !ok {
				continue
			}
			p := pair{seat: si, job: ji, tier: t}
			if best.tier < 0 || p.before(best, jobs) {
				best = p
			}
		}
	}
	return best.seat, best.job, best.tier >= 0
}

type pair struct{ seat, job, tier int }

func (p pair) before(q pair, jobs []job) bool {
	if p.tier != q.tier {
		return p.tier < q.tier
	}
	if c := urgency(p.tier, jobs[p.job], jobs[q.job]); c != 0 {
		return c < 0
	}
	if p.job != q.job {
		return p.job < q.job
	}
	return p.seat < q.seat
}

// urgency orders tasks of one tier: starving ones by how long they waited; the others by
// priority, the number of tasks waiting on them, when they became claimable, creation.
func urgency(tier int, a, b job) int {
	if tier == tierStarving {
		return cmp.Or(a.Ready.Compare(b.Ready), cmp.Compare(a.Priority, b.Priority))
	}
	return cmp.Or(cmp.Compare(a.Priority, b.Priority), cmp.Compare(b.Downstream, a.Downstream),
		a.Ready.Compare(b.Ready), a.Created.Compare(b.Created))
}

// tier ranks what taking job j means for member s (ok=false: s may not take it).
func tier(s seat, j job, now time.Time) (int, bool) {
	if !eligible(s, j, now) {
		return 0, false
	}
	if j.NotBy != "" && j.NotBy == s.Name {
		return tierAuthor, true // eligible only once the gate starves
	}
	t := stickiness(s, j, now)
	if s.Legacy != "" && t > tierReserved {
		return tierLegacy, true
	}
	return t, true
}

func stickiness(s seat, j job, now time.Time) int {
	switch {
	case j.Named != "":
		return tierNamed
	case j.ReservedFor == s.Name && reserved(j, now):
		return tierReserved
	case now.Sub(j.Ready) >= StarveAfter:
		return tierStarving
	case depSticky(s.Name, j.DepWorkers):
		return tierDeps
	case s.Thread != "" && s.Thread == j.Thread:
		return tierThread
	case overlaps(s.Repos, j.Repos):
		return tierRepo
	}
	return tierOther
}

// eligible: the named instance or a member of the role; a legacy member only in its own thread
// and a project member only where no legacy member has its name; not mid-turn in another thread
// (switching would close the session under the running turn: it claims once the turn ended);
// not reserved for someone else; not the author of the work a gate checks (while a peer can,
// until the gate starves);
// a role the thread uses.
func eligible(s seat, j job, now time.Time) bool {
	switch {
	case j.Named != "" && j.Named != s.Name, j.Named == "" && j.Role != s.Role:
		return false
	case j.NotBy != "" && j.NotBy == s.Name && now.Sub(j.Ready) < StarveAfter:
		return false
	case s.Legacy != "" && s.Legacy != j.Thread, s.Legacy == "" && slices.Contains(j.Legacy, s.Name):
		return false
	case s.TurnThread != "" && s.TurnThread != j.Thread:
		return false
	case reserved(j, now) && j.ReservedFor != s.Name:
		return false
	}
	return j.Gate || len(j.OnlyRoles) == 0 || slices.Contains(j.OnlyRoles, s.Role)
}

func reserved(j job, now time.Time) bool {
	return j.ReservedFor != "" && j.ReservedUntil != nil && now.Before(*j.ReservedUntil)
}

// depSticky: the member did the task's dependency, or the most of its dependencies (ties count
// for everyone tied).
func depSticky(name string, workers []string) bool {
	n := map[string]int{}
	for _, w := range workers {
		if w != "" {
			n[w]++
		}
	}
	mine := n[name]
	if mine == 0 {
		return false
	}
	for _, c := range n {
		if c > mine {
			return false
		}
	}
	return true
}

func overlaps(a, b []string) bool {
	for _, x := range a {
		if slices.Contains(b, x) {
			return true
		}
	}
	return false
}
