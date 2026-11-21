package orchestrator

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/platform"
	"github.com/touwaeriol/agent-teams/server/internal/runtimes"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// QuotaFull is the used percent at which a node's subscription window counts as exhausted: the
// node is passed over while another candidate still has quota.
const QuotaFull = 95.0

// unknownUsage ranks a node whose runtime reported no quota numbers (no report yet, an older
// daemon, an API key): between a fresh account and a busy one.
const unknownUsage = 50.0

// pickNode chooses the node and the runtime for an employee's new session. An employee is not
// tied to a node, and a role with several runtimes not to one of them: each new session goes to
// the (runtime, node) pair with the most quota left (pickRank) over every runtime of the role and
// its candidate nodes, and the employee stays on its current node and runtime only while it must
// (stay: it resumes a conversation or has a worktree in its thread) and that node is online,
// still a candidate and not out of quota. With no usable pair online it is the current node and
// runtime (the session waits for it), else the first candidate.
func (o *Orchestrator) pickNode(ctx context.Context, tx *core.Tx, e core.Employee, p store.Project, stay bool) (store.Node, string, error) {
	nodes, err := o.store.On(tx).ListNodes(ctx, p.UserID)
	if err != nil {
		return store.Node{}, "", err
	}
	current := nodeState{rt: e.Runtime}
	if e.NodeID != nil && slices.ContainsFunc(nodes, func(n store.Node) bool { return n.ID == *e.NodeID }) {
		current.id = *e.NodeID
	}
	var states []nodeState
	for _, rt := range employeeRuntimes(e) {
		as := e
		as.Runtime = rt
		states = append(states, o.nodeStates(ctx, tx, core.NodeCandidates(as, nodes), rt, Model(as, p))...)
	}
	if current.id == "" && len(states) == 0 {
		return store.Node{}, "", ErrNoNode
	}
	got := chooseNode(current, states, stay)
	i := slices.IndexFunc(nodes, func(n store.Node) bool { return n.ID == got.id })
	return nodes[i], got.rt, nil
}

// employeeRuntimes are the runtimes a session of the employee may run on: its role's, else the
// one it has. The coordinator and leads have one (CanLead runtimes only).
func employeeRuntimes(e core.Employee) []string {
	if len(e.Spec.Runtimes) > 0 {
		return e.Spec.Runtimes
	}
	return []string{e.Runtime}
}

// nodeState is what chooseNode knows about a candidate (runtime, node) pair.
type nodeState struct {
	id     string
	rt     string  // the runtime the session would run on ("" in tests of a single runtime)
	usable bool    // online and the runtime is logged in there
	free   int     // freeSlots
	used   float64 // quotaUsed of the runtime
}

func (s nodeState) same(o nodeState) bool { return s.id == o.id && s.rt == o.rt }

func (s nodeState) full() bool { return s.free <= 0 || s.used >= QuotaFull }

func (o *Orchestrator) nodeStates(ctx context.Context, tx *core.Tx, cands []store.Node, runtime, model string) []nodeState {
	now := time.Now()
	out := make([]nodeState, 0, len(cands))
	for _, n := range cands {
		st := nodeState{id: n.ID, rt: runtime}
		active, err := core.ActiveSessionCount(ctx, tx, n.ID)
		if o.gw.Online(n.ID) && err == nil {
			ru := runtimeUsage(o.gw.Usage(n.ID), runtime)
			st.usable = loggedIn(ru)
			st.free = freeSlots(o.gw.Capacity(n), active)
			st.used = quotaUsed(ru, model, now)
		}
		out = append(out, st)
	}
	return out
}

// chooseNode is pickNode's rule over the candidate pairs' states; current is the employee's node
// (id "" when it has none or it was deleted) and runtime, which may not be a candidate any more.
func chooseNode(current nodeState, cands []nodeState, stay bool) nodeState {
	cur := slices.IndexFunc(cands, func(c nodeState) bool { return c.same(current) })
	if stay && cur >= 0 && cands[cur].usable && !cands[cur].full() {
		return cands[cur]
	}
	best := -1
	for i, c := range cands {
		if c.usable && (best < 0 || pickRank(c, i == cur, cands[best], best == cur) < 0) {
			best = i
		}
	}
	switch {
	case best >= 0:
		return cands[best]
	case current.id != "":
		return current
	default:
		return cands[0]
	}
}

// pickRank orders two usable pairs, negative when a goes first: one with quota and a free slot
// before a full one, then the least used quota (5 h, weekly, the model's weekly window; Codex:
// every window), then the employee's current node and runtime (it keeps its clones), then the
// most free slots.
func pickRank(a nodeState, aCur bool, b nodeState, bCur bool) int {
	switch {
	case a.full() != b.full():
		return boolRank(!a.full())
	case a.used != b.used:
		return boolRank(a.used < b.used)
	case aCur != bCur:
		return boolRank(aCur)
	case a.free != b.free:
		return boolRank(a.free > b.free)
	}
	return 0
}

func boolRank(first bool) int {
	if first {
		return -1
	}
	return 1
}

// freeSlots ranks a node: an unlimited node (capacity 0) counts as far roomier than any capped one.
func freeSlots(capacity, active int) int {
	if capacity <= 0 {
		capacity = 1 << 20
	}
	return capacity - active
}

func runtimeUsage(u *platform.NodeUsage, runtime string) *platform.RuntimeUsage {
	if u == nil {
		return nil
	}
	return usageOf[runtimes.Of(runtime).ID](u)
}

// usageOf is where node.usage reports each runtime (TestRuntimeOptionsCoverRegistry).
var usageOf = map[string]func(*platform.NodeUsage) *platform.RuntimeUsage{
	runtimes.Claude: func(u *platform.NodeUsage) *platform.RuntimeUsage { return u.Claude },
	runtimes.Codex:  func(u *platform.NodeUsage) *platform.RuntimeUsage { return u.Codex },
}

// loggedIn: a node whose daemon reported the runtime as not logged in cannot run it; without a
// report (older daemon, none received yet) the node is given the benefit of the doubt.
func loggedIn(ru *platform.RuntimeUsage) bool {
	return ru == nil || ru.Auth != "none"
}

// quotaUsed is the fullest of the runtime's windows that apply to the model (Claude: five_hour,
// weekly, and the weekly window of the model's family; Codex: every window), ignoring windows
// that have already reset; unknownUsage without numbers.
func quotaUsed(ru *platform.RuntimeUsage, model string, now time.Time) float64 {
	if ru == nil || ru.Status != "ok" || len(ru.Windows) == 0 {
		return unknownUsage
	}
	used := 0.0
	for _, w := range ru.Windows {
		if (w.ResetsAt != nil && !w.ResetsAt.After(now)) || !windowApplies(w, model) {
			continue
		}
		used = max(used, w.UsedPercent)
	}
	return used
}

// windowApplies: the per-model weekly windows (weekly_opus, weekly_sonnet, weekly_model with the
// provider's label such as "Fable") count only for a model of that family; the others always.
func windowApplies(w platform.UsageWindow, model string) bool {
	model = strings.ToLower(model)
	switch w.Key {
	case "weekly_opus":
		return strings.Contains(model, "opus")
	case "weekly_sonnet":
		return strings.Contains(model, "sonnet")
	case "weekly_model":
		family := strings.Fields(strings.ToLower(w.Label)) // "Fable" or "Fable 5": the family is the first word
		return len(family) > 0 && strings.Contains(model, family[0])
	}
	return true
}
