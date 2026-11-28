package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/platform"
	"github.com/touwaeriol/agent-teams/server/internal/runtimes"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// QuotaHardFull is the used percent (of any window that applies) at which a node's quota counts as
// used up whatever the user's thresholds: a session that must stay (stay) leaves such a node even
// when every other node is tight too, and a session being moved (moveTarget) skips it. The user's
// lower thresholds (quotaTight) keep new sessions off a node, and a session that would stay leaves
// a node over them while another usable node is under them (openElsewhere).
const QuotaHardFull = 95.0

// shortWindowMinutes: a usage window up to this long counts as a 5-hour window for the thresholds
// (store.QuotaLimits.FiveHourPct), a longer one as a weekly window (WeeklyPct).
const shortWindowMinutes = 24 * 60

// unknownUsage ranks a node whose runtime reported no quota numbers (no report yet, an older
// daemon, an API key): between a fresh account and a busy one.
const unknownUsage = 50.0

// SessionWeight is how many percent of used quota one live session on a node counts as in the
// load score (loadScore = used + SessionWeight × active): new sessions spread over the usable
// nodes, the ones with less quota used getting more of them, instead of all going to the least
// used node until its quota report (refreshed every minute) catches up.
const SessionWeight = 10.0

// pickNode chooses the node and the runtime for an employee's new session. An employee is not
// tied to a node, and a role with several runtimes not to one of them: each new session goes to
// the (runtime, node) pair with the lowest load score (pickRank: used quota plus SessionWeight per
// live session on the node, so sessions spread over the nodes and the less used ones get more)
// over every runtime of the role and its candidate nodes, and the employee stays on its current node and runtime only while it must
// (stay: it resumes a conversation or has a worktree in its thread) and that node is online,
// still a candidate, not hardFull (a free slot, every window under QuotaHardFull) and either not
// tight (under the user's thresholds) or tight with no other usable pair under them (openElsewhere). With no usable pair online it is the current node and
// runtime (the session waits for it), else the first candidate; when that node is online and
// reports its runtime CLIs (runtime_status) but the runtime is not ready there, the pick is held
// instead (hold = why no node can take the session, see holdReason).
// purpose names the caller in the "pick node" log: pickOpen (a new session) or pickSync (the
// pre-task sync of a review).
func (o *Orchestrator) pickNode(ctx context.Context, tx *core.Tx, e core.Employee, p store.Project, stay bool, purpose string) (nodePick, error) {
	nodes, err := o.store.On(tx).ListNodes(ctx, p.UserID)
	if err != nil {
		return nodePick{}, err
	}
	current := nodeState{rt: e.Runtime}
	if e.NodeID != nil && slices.ContainsFunc(nodes, func(n store.Node) bool { return n.ID == *e.NodeID }) {
		current.id = *e.NodeID
	}
	reports, err := o.store.On(tx).NodeRuntimes(ctx, nodeIDs(nodes))
	if err != nil {
		return nodePick{}, err
	}
	limits, err := o.quotaLimits(ctx, tx, p.UserID)
	if err != nil {
		return nodePick{}, err
	}
	var states []nodeState
	rts := employeeRuntimes(e)
	for _, rt := range rts {
		as := e
		as.Runtime = rt
		states = append(states, o.nodeStates(ctx, tx, core.NodeCandidates(as, nodes), rt, Model(as, p), reports, limits)...)
	}
	if current.id == "" && len(states) == 0 {
		return nodePick{}, ErrNoNode
	}
	got := chooseNode(current, states, stay)
	i := slices.IndexFunc(nodes, func(n store.Node) bool { return n.ID == got.id })
	pick := nodePick{node: nodes[i], rt: got.rt}
	if held(got, states) {
		pick.hold = holdReason(rts, states)
	}
	o.logPick(e, p, stay, purpose, nodes, states, pick)
	return pick, nil
}

// quotaLimits are the user's scheduling thresholds (defaults when the user row is gone).
func (o *Orchestrator) quotaLimits(ctx context.Context, tx *core.Tx, userID string) (store.QuotaLimits, error) {
	l, err := o.store.On(tx).UserQuotaLimits(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return store.DefaultQuotaLimits(), nil
	}
	return l, err
}

// pickNode purposes (the "purpose" field of the "pick node" log).
const (
	pickOpen = "open"
	pickSync = "sync"
)

// pickCandidate is one candidate pair in the "pick node" log line.
type pickCandidate struct {
	Node   string  `json:"node"`
	NodeID string  `json:"node_id"`
	RT     string  `json:"rt"`
	Usable bool    `json:"usable"`
	Online bool    `json:"online"`
	Used   float64 `json:"used"`
	Active int     `json:"active"`
	Free   int     `json:"free"`
	Score  float64 `json:"score"`
	Tight  string  `json:"tight,omitempty"`
}

// logPick records why pickNode chose the node: every candidate pair with its load score (no
// account details: no email, no token).
func (o *Orchestrator) logPick(e core.Employee, p store.Project, stay bool, purpose string, nodes []store.Node, states []nodeState, pick nodePick) {
	if o.log == nil {
		return
	}
	names := make(map[string]string, len(nodes))
	for _, n := range nodes {
		names[n.ID] = n.Name
	}
	cands := make([]pickCandidate, len(states))
	for i, s := range states {
		cands[i] = pickCandidate{Node: names[s.id], NodeID: s.id, RT: s.rt, Usable: s.usable, Online: s.online,
			Used: s.used, Active: s.active, Free: s.free, Score: s.score(), Tight: s.tight}
	}
	thread := ""
	if e.CurrentThreadID != nil {
		thread = *e.CurrentThreadID
	} else if e.ThreadID != nil {
		thread = *e.ThreadID
	}
	o.log.Info("pick node", "employee", e.Name, "employee_id", e.ID, "project_id", p.ID, "thread_id", thread,
		"purpose", purpose, "stay", stay, "candidates", cands, "picked", pick.node.Name, "picked_id", pick.node.ID, "picked_rt", pick.rt,
		"hold", pick.hold != "", "hold_reason", pick.hold)
}

// nodePick is pickNode's answer: the node and runtime, or why the session must wait (hold).
type nodePick struct {
	node store.Node
	rt   string
	hold string
}

func nodeIDs(nodes []store.Node) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.ID
	}
	return out
}

// held: the chosen pair cannot run the session and the console knows it (the node is online and
// reports its runtime CLIs) rather than waiting for an offline node or trusting an old daemon.
func held(got nodeState, states []nodeState) bool {
	if got.usable {
		return false
	}
	i := slices.IndexFunc(states, func(c nodeState) bool { return c.same(got) })
	return i >= 0 && states[i].online && states[i].gated
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
	rt     string // the runtime the session would run on ("" in tests of a single runtime)
	usable bool   // online and the runtime is logged in there
	online bool   // the node has a live link
	gated  bool   // online with runtime_status: usable only when the runtime is ready (rs)
	unseen bool   // gated, but the node's first node.runtimes has not arrived yet (no rows)
	rs     *store.NodeRuntime
	free   int     // freeSlots
	active int     // live sessions on the node (core.ActiveSessionCount)
	used   float64 // quotaUsed of the runtime
	tight  string  // quotaTight: which window is over the user's threshold ("" = none)
}

// score is the pair's load score, lower first: used quota plus SessionWeight per live session.
func (s nodeState) score() float64 { return s.used + SessionWeight*float64(s.active) }

func (s nodeState) same(o nodeState) bool { return s.id == o.id && s.rt == o.rt }

// full: no free slot, or the quota is tight (a window over the user's threshold). A full pair is
// only picked when every usable pair is full.
func (s nodeState) full() bool { return s.free <= 0 || s.tight != "" }

// hardFull: no free slot, or a window at QuotaHardFull. A session that must stay leaves its node
// then even when every other node is over the user's thresholds too.
func (s nodeState) hardFull() bool { return s.free <= 0 || s.used >= QuotaHardFull }

func (o *Orchestrator) nodeStates(ctx context.Context, tx *core.Tx, cands []store.Node, runtime, model string,
	reports map[string][]store.NodeRuntime, limits store.QuotaLimits) []nodeState {
	now := time.Now()
	out := make([]nodeState, 0, len(cands))
	for _, n := range cands {
		st := nodeState{id: n.ID, rt: runtime}
		active, err := core.ActiveSessionCount(ctx, tx, n.ID)
		if o.gw.Online(n.ID) && err == nil {
			ru := runtimeUsage(o.gw.Usage(n.ID), runtime)
			st.online = true
			// Old daemons (no runtime_status): login from usage, plus the hello's runtimes entry for
			// runtimes without a usage API (Antigravity).
			st.usable = loggedIn(ru) && o.gw.RuntimeReady(n.ID, runtime)
			if o.gw.SupportsRuntimeStatus(n.ID) {
				// A daemon with runtime_status decides by its report only; until the first one of
				// this connection arrives (right after welcome) the runtime counts as not ready,
				// whatever an earlier connection left in at_node_runtimes.
				st.gated, st.unseen = true, !o.gw.RuntimesReported(n.ID) || len(reports[n.ID]) == 0
				if !st.unseen {
					st.rs = runtimeReport(reports[n.ID], runtime)
				}
				st.usable = st.rs != nil && st.rs.Ready(now)
			}
			st.free = freeSlots(o.gw.Capacity(n), active)
			st.active = active
			st.used = quotaUsed(ru, model, now)
			st.tight = quotaTight(ru, model, now, limits)
		}
		out = append(out, st)
	}
	return out
}

// runtimeReport is the node's stored state of the runtime (nil when the daemon does not know it).
func runtimeReport(rows []store.NodeRuntime, runtime string) *store.NodeRuntime {
	id := runtimes.Of(runtime).ID
	for i := range rows {
		if rows[i].Runtime == id {
			return &rows[i]
		}
	}
	return nil
}

// holdReason tells the user why no candidate node can take the session, one clause per runtime
// of the role (Claude Code, Codex…), e.g. "没有已登录 Codex 的在线节点".
func holdReason(rts []string, states []nodeState) string {
	parts := make([]string, 0, len(rts))
	for _, rt := range rts {
		parts = append(parts, runtimeHoldReason(rt, states))
	}
	return strings.Join(parts, "；") + "（在节点上安装并登录后会自动开始）"
}

func runtimeHoldReason(rt string, states []nodeState) string {
	title := runtimes.Of(rt).Title
	var online, installed, expired, unseen bool
	for _, s := range states {
		if s.rt != rt || !s.online {
			continue
		}
		online = true
		installed = installed || !s.gated || (s.rs != nil && s.rs.Installed)
		expired = expired || (s.rs != nil && s.rs.Installed && s.rs.Expired)
		unseen = unseen || s.unseen
	}
	switch {
	case !online:
		return "没有能运行 " + title + " 的在线节点"
	case unseen:
		return "正在检测节点上 " + title + " 的安装和登录状态"
	case !installed:
		return title + " 没有安装在任何在线的候选节点上"
	case expired:
		return title + " 在在线节点上的登录已过期"
	}
	return "没有已登录 " + title + " 的在线节点"
}

// chooseNode is pickNode's rule over the candidate pairs' states; current is the employee's node
// (id "" when it has none or it was deleted) and runtime, which may not be a candidate any more.
func chooseNode(current nodeState, cands []nodeState, stay bool) nodeState {
	cur := slices.IndexFunc(cands, func(c nodeState) bool { return c.same(current) })
	if stay && cur >= 0 && cands[cur].usable && !cands[cur].hardFull() && (!cands[cur].full() || !openElsewhere(cands, cur)) {
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

// openElsewhere: a usable pair other than cands[cur] has a free slot and quota that is not tight,
// so a session that would stay on a tight node (over the user's thresholds) goes there instead.
func openElsewhere(cands []nodeState, cur int) bool {
	for i, c := range cands {
		if i != cur && c.usable && !c.full() {
			return true
		}
	}
	return false
}

// pickRank orders two usable pairs, negative when a goes first: one with a free slot and quota
// that is not tight (quotaTight: under the user's 5-hour and weekly thresholds) before a full one, then the lowest load score (score: the used quota — 5 h, weekly, the model's
// weekly window; Codex: every window — plus SessionWeight per live session on the node, so a
// lightly used node gets more new sessions without taking all of them), then the employee's
// current node and runtime (it keeps its clones), then the most free slots. Equal pairs keep
// the candidates' order (chooseNode takes the first), so the pick is deterministic.
func pickRank(a nodeState, aCur bool, b nodeState, bCur bool) int {
	switch {
	case a.full() != b.full():
		return boolRank(!a.full())
	case a.score() != b.score():
		return boolRank(a.score() < b.score())
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
	// Antigravity has no usage API: its quota is unknown (unknownUsage) and its login comes from
	// node.runtimes (at_node_runtimes), or from hello (Gateway.RuntimeReady) on an old daemon.
	runtimes.Antigravity: func(*platform.NodeUsage) *platform.RuntimeUsage { return nil },
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

// quotaTight tells which of the runtime's windows that apply to the model and have not reset is
// at or over the user's threshold, e.g. "five_hour 86% ≥ 85%" ("" when none is): windows up to a
// day long (Claude five_hour, Codex primary) against FiveHourPct, longer ones (weekly, the
// per-model weekly windows, Codex secondary) against WeeklyPct.
func quotaTight(ru *platform.RuntimeUsage, model string, now time.Time, limits store.QuotaLimits) string {
	if ru == nil || ru.Status != "ok" {
		return ""
	}
	for _, w := range ru.Windows {
		if (w.ResetsAt != nil && !w.ResetsAt.After(now)) || !windowApplies(w, model) {
			continue
		}
		limit := limits.WeeklyPct
		if shortWindow(w) {
			limit = limits.FiveHourPct
		}
		if w.UsedPercent >= float64(limit) {
			return fmt.Sprintf("%s %g%% ≥ %d%%", w.Key, w.UsedPercent, limit)
		}
	}
	return ""
}

// shortWindow: the window counts as a 5-hour one (its length when reported, else its key).
func shortWindow(w platform.UsageWindow) bool {
	if w.WindowMinutes != nil && *w.WindowMinutes > 0 {
		return *w.WindowMinutes <= shortWindowMinutes
	}
	return w.Key == "five_hour" || w.Key == "primary"
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
