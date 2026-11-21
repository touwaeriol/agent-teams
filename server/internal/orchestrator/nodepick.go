package orchestrator

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/platform"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// QuotaFull is the used percent at which a node's subscription window counts as exhausted: the
// node is passed over while another candidate still has quota.
const QuotaFull = 95.0

// unknownUsage ranks a node whose runtime reported no quota numbers (no report yet, an older
// daemon, an API key): between a fresh account and a busy one.
const unknownUsage = 50.0

// pickNode chooses the node for an employee's new session. An employee is not tied to a node:
// each new session goes where its runtime has the most quota left (pickRank), and the employee
// stays on its current node only while it must (stay: it resumes a conversation or has a
// worktree in its thread) and that node is online, still a candidate and not out of quota.
// With no usable candidate online it is the current node (the session waits for it), else the
// first candidate.
func (o *Orchestrator) pickNode(ctx context.Context, tx *core.Tx, e core.Employee, p store.Project, stay bool) (store.Node, error) {
	nodes, err := o.store.On(tx).ListNodes(ctx, p.UserID)
	if err != nil {
		return store.Node{}, err
	}
	cands := core.NodeCandidates(e, nodes)
	current := ""
	if e.NodeID != nil && slices.ContainsFunc(nodes, func(n store.Node) bool { return n.ID == *e.NodeID }) {
		current = *e.NodeID
	}
	if current == "" && len(cands) == 0 {
		return store.Node{}, ErrNoNode
	}
	id := chooseNode(current, o.nodeStates(ctx, tx, cands, e.Runtime, Model(e, p)), stay)
	i := slices.IndexFunc(nodes, func(n store.Node) bool { return n.ID == id })
	return nodes[i], nil
}

// nodeState is what chooseNode knows about a candidate node.
type nodeState struct {
	id     string
	usable bool    // online and the employee's runtime is logged in there
	free   int     // freeSlots
	used   float64 // quotaUsed of the employee's runtime
}

func (s nodeState) full() bool { return s.free <= 0 || s.used >= QuotaFull }

func (o *Orchestrator) nodeStates(ctx context.Context, tx *core.Tx, cands []store.Node, runtime, model string) []nodeState {
	now := time.Now()
	out := make([]nodeState, 0, len(cands))
	for _, n := range cands {
		st := nodeState{id: n.ID}
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

// chooseNode is pickNode's rule over the candidates' states; current is the employee's node
// ("" when it has none or it was deleted), which may not be a candidate any more.
func chooseNode(current string, cands []nodeState, stay bool) string {
	cur := slices.IndexFunc(cands, func(c nodeState) bool { return c.id == current })
	if stay && cur >= 0 && cands[cur].usable && !cands[cur].full() {
		return current
	}
	best := -1
	for i, c := range cands {
		if c.usable && (best < 0 || pickRank(c, i == cur, cands[best], best == cur) < 0) {
			best = i
		}
	}
	switch {
	case best >= 0:
		return cands[best].id
	case current != "":
		return current
	default:
		return cands[0].id
	}
}

// pickRank orders two usable nodes, negative when a goes first: one with quota and a free slot
// before a full one, then the least used quota (5 h, weekly, the model's weekly window), then the
// employee's current node (it keeps its clones), then the most free slots.
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
	switch {
	case u == nil:
		return nil
	case runtime == "codex":
		return u.Codex
	}
	return u.Claude
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
