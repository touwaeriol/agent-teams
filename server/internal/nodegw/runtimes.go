package nodegw

import (
	"context"
	"regexp"
	"sort"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/platform"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// FeatureRuntimeStatus: the daemon reports its runtime CLIs with node.runtimes and answers
// node.runtimes_refresh (protocol.md §9.2).
const FeatureRuntimeStatus = "runtime_status"

// RuntimesRefreshWait is how long POST /api/nodes/{id}/recheck waits for node.runtimes.
const RuntimesRefreshWait = 15 * time.Second

// Limits of a node.runtimes report (the console keeps only these).
const (
	maxRuntimesPerReport = 16
	maxRuntimeVersion    = 200
	maxRuntimePath       = 1024
	maxRuntimeError      = 500
)

var runtimeIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// RuntimesHandler is implemented by a Handler that wants to know when a node's runtime states
// changed (stored report differs from the previous one); ready lists the runtimes that became
// ready (installed and logged in) with this report.
type RuntimesHandler interface {
	RuntimesChanged(ctx context.Context, node store.Node, ready []string)
}

// runtimesWait is one in-flight node.runtimes_refresh, answered by the node.runtimes whose
// in_reply_to is id.
type runtimesWait struct {
	id     string
	nodeID string
	done   chan struct{}
	got    bool // guarded by Gateway.mu
}

// RuntimesReported reports whether the node's live link has delivered its first node.runtimes:
// until then its stored states (maybe from an earlier connection) do not count.
func (g *Gateway) RuntimesReported(nodeID string) bool {
	l := g.link(nodeID)
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.reported
}

// SupportsRuntimeStatus reports whether the node's live link announced runtime_status: only then
// do its stored runtime states decide where sessions go.
func (g *Gateway) SupportsRuntimeStatus(nodeID string) bool {
	l := g.link(nodeID)
	return l != nil && hasFeature(l.hello.Features, FeatureRuntimeStatus)
}

// recordRuntimes stores a node.runtimes report, ends the refresh it answers and tells the
// handler when something changed.
func (g *Gateway) recordRuntimes(ctx context.Context, l *link, b *protocol.NodeRuntimes) error {
	node := l.node
	rows := sanitizeRuntimes(b)
	var before []store.NodeRuntime
	err := g.core.InTx(ctx, func(tx *core.Tx) error {
		var err error
		before, err = g.store.On(tx).ReplaceNodeRuntimes(ctx, node.ID, rows)
		return err
	})
	if b.InReplyTo != nil {
		g.answerRuntimesWait(node.ID, *b.InReplyTo)
	}
	if err != nil {
		return err
	}
	l.mu.Lock()
	first := !l.reported
	l.reported = true
	l.mu.Unlock()
	changed, ready := diffRuntimes(before, rows, g.now())
	if first {
		ready = readyNow(rows, g.now()) // the rows stored before this link did not count
	}
	// The first report after a (re)connect always counts: sessions held while the node was being
	// checked are placed again even when nothing differs from what was stored.
	changed = changed || first || b.Reason == protocol.NodeRuntimesReasonHello
	if h, ok := g.handler.(RuntimesHandler); ok && changed {
		h.RuntimesChanged(ctx, node, ready)
	}
	return nil
}

// diffRuntimes compares the stored states with a report: changed when any runtime was added,
// removed or says something else; ready lists the runtimes that are ready now and were not.
func diffRuntimes(before, after []store.NodeRuntime, now time.Time) (bool, []string) {
	changed := len(before) != len(after)
	var ready []string
	for _, r := range after {
		old, ok := findStored(before, r.Runtime)
		if !ok || !old.Same(r) {
			changed = true
		}
		if r.Ready(now) && (!ok || !old.Ready(now)) {
			ready = append(ready, r.Runtime)
		}
	}
	return changed, ready
}

// readyNow lists the runtimes of a report that are ready.
func readyNow(rows []store.NodeRuntime, now time.Time) []string {
	var out []string
	for _, r := range rows {
		if r.Ready(now) {
			out = append(out, r.Runtime)
		}
	}
	return out
}

func findStored(rows []store.NodeRuntime, runtime string) (store.NodeRuntime, bool) {
	for _, r := range rows {
		if r.Runtime == runtime {
			return r, true
		}
	}
	return store.NodeRuntime{}, false
}

// sanitizeRuntimes keeps the valid runtime ids (at most maxRuntimesPerReport, sorted) and cuts
// the strings; the decoder already checked the schema.
func sanitizeRuntimes(b *protocol.NodeRuntimes) []store.NodeRuntime {
	ids := make([]string, 0, len(b.Runtimes))
	for id := range b.Runtimes {
		if runtimeIDPattern.MatchString(id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) > maxRuntimesPerReport {
		ids = ids[:maxRuntimesPerReport]
	}
	out := make([]store.NodeRuntime, 0, len(ids))
	for _, id := range ids {
		s := b.Runtimes[id]
		r := store.NodeRuntime{Runtime: id, Installed: s.Installed, Auth: string(s.Auth), CheckedAt: s.CheckedAt.UTC(),
			Version: cleanString(ptrValue(s.Version), maxRuntimeVersion), Path: cleanString(ptrValue(s.Path), maxRuntimePath),
			Error: cleanString(ptrValue(s.Error), maxRuntimeError), AuthExpiresAt: utcTime(s.AuthExpiresAt)}
		if s.Expired != nil {
			r.Expired = *s.Expired
		}
		out = append(out, r)
	}
	return out
}

// RecheckRuntimes implements platform.NodeRegistry: node.runtimes_refresh (and node.usage_refresh
// when the daemon reports usage), then waits for the node.runtimes answering it (stale = no
// answer in time). ErrNodeOffline when not connected, ErrRuntimeStatusUnsupported when the daemon
// predates runtime_status. No transaction is held.
func (g *Gateway) RecheckRuntimes(ctx context.Context, nodeID string) (bool, error) {
	l := g.link(nodeID)
	if l == nil {
		return false, platform.ErrNodeOffline
	}
	if !hasFeature(l.hello.Features, FeatureRuntimeStatus) {
		return false, platform.ErrRuntimeStatusUnsupported
	}
	wait := g.RefreshWait
	if wait <= 0 {
		wait = RuntimesRefreshWait
	}
	w := &runtimesWait{id: core.NewID("c"), nodeID: nodeID, done: make(chan struct{})}
	g.mu.Lock()
	g.runtimesWaits[w.id] = w
	g.mu.Unlock()
	timer := time.AfterFunc(wait, func() { g.endRuntimesWait(w) })
	defer timer.Stop()
	defer g.endRuntimesWait(w)
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), wait)
	err := l.sendUnreliableID(sendCtx, w.id, protocol.TypeNodeRuntimesRefresh, protocol.NodeRuntimesRefresh{})
	cancel()
	if err != nil {
		g.log.Warn("node.runtimes_refresh", "node_id", nodeID, "err", err)
		return true, nil
	}
	if hasFeature(l.hello.Features, "usage") {
		go g.refreshUsageQuietly(context.WithoutCancel(ctx), nodeID)
	}
	select {
	case <-w.done:
	case <-ctx.Done():
	}
	g.mu.Lock()
	got := w.got
	g.mu.Unlock()
	return !got, nil
}

// refreshUsageQuietly asks for fresh quota alongside a recheck; the answer lands in the usage cache.
func (g *Gateway) refreshUsageQuietly(ctx context.Context, nodeID string) {
	if _, _, err := g.RefreshUsage(ctx, nodeID); err != nil {
		g.log.Debug("usage refresh with recheck", "node_id", nodeID, "err", err)
	}
}

// answerRuntimesWait ends the refresh with this command id when it was sent to this node.
func (g *Gateway) answerRuntimesWait(nodeID, id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	w := g.runtimesWaits[id]
	if w == nil || w.nodeID != nodeID {
		return
	}
	delete(g.runtimesWaits, id)
	w.got = true
	close(w.done)
}

// endRuntimesWait gives up on w; a no-op when it already ended.
func (g *Gateway) endRuntimesWait(w *runtimesWait) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.runtimesWaits[w.id] == w {
		delete(g.runtimesWaits, w.id)
		close(w.done)
	}
}
