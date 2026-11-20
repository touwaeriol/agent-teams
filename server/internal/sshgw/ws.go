package sshgw

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Request headers of /nodes/ssh (remote-servers.md §6.4).
const (
	HeaderSession = "X-AT-Session"
	HeaderOpenGen = "X-AT-Open-Gen"
	HeaderServer  = "X-AT-Server"
)

// wsReadLimit: one SSH packet is at most ~35 KB; the daemon may batch several in a frame.
const wsReadLimit = 1 << 20

// ServeWS is GET /nodes/ssh: node token, the session (this node's, live, current open_gen) and a
// server the session may use now are checked before the upgrade; then the WebSocket carries one
// SSH connection (binary frames) that the gateway terminates.
func (g *Gateway) ServeWS(w http.ResponseWriter, r *http.Request) {
	node, err := g.nodes.Authenticate(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !g.vault.Ready() {
		http.Error(w, "ssh gateway unavailable", http.StatusServiceUnavailable)
		return
	}
	sid, name := r.Header.Get(HeaderSession), r.Header.Get(HeaderServer)
	gen, err := strconv.Atoi(r.Header.Get(HeaderOpenGen))
	if sid == "" || name == "" || err != nil {
		http.Error(w, "X-AT-Session, X-AT-Open-Gen and X-AT-Server are required", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	x, srv, err := g.store.SessionServerGrant(ctx, sid, name)
	if err != nil && !errors.Is(err, store.ErrNotGranted) {
		if !errors.Is(err, store.ErrNotFound) {
			g.log.Warn("ssh: grant lookup", "session_id", sid, "err", err)
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}
	if x.NodeID != node.ID || x.UserID != node.UserID || x.Ended() || x.OpenGen != gen {
		// Not this node's session (or not its current generation): say nothing more.
		g.log.Info("ssh: session refused", "node_id", node.ID, "session_id", sid)
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}
	if errors.Is(err, store.ErrNotGranted) {
		g.audit(ctx, auditBase(x, node.ID, nil, "", name, ""), store.SSHAuditDenied, "not_granted", nil)
		http.Error(w, "this session may not use "+name, http.StatusForbidden)
		return
	}
	release, ok := g.reserve(x, srv.ID)
	if !ok {
		g.audit(ctx, auditBase(x, node.ID, nil, srv.ID, name, ""), store.SSHAuditDenied, "too_many_connections", nil)
		http.Error(w, "too many connections", http.StatusTooManyRequests)
		return
	}
	defer release()
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{Subprotocol},
		CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	if ws.Subprotocol() != Subprotocol {
		_ = ws.Close(websocket.StatusPolicyViolation, "subprotocol "+Subprotocol+" required")
		return
	}
	ws.SetReadLimit(wsReadLimit)
	// The connection outlives nothing but itself: its context ends when the SSH connection does.
	cctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	nc := websocket.NetConn(cctx, ws, websocket.MessageBinary)
	go g.pingWS(cctx, ws)
	c := newConn(g, nc, node, x, srv.RemoteServer, gen)
	c.serve(cctx)
	_ = ws.Close(websocket.StatusNormalClosure, "")
}

// pingWS keeps the WebSocket alive through proxies (the SSH keepalive covers the SSH layer).
func (g *Gateway) pingWS(ctx context.Context, ws *websocket.Conn) {
	t := time.NewTicker(g.cfg.Keepalive)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, g.cfg.Keepalive)
			_ = ws.Ping(pctx)
			cancel()
		}
	}
}

// auditBase is an audit row with the session's identity.
func auditBase(x store.SSHSession, nodeID string, serial *int64, serverID, serverName, connID string) store.SSHAudit {
	return store.SSHAudit{UserID: x.UserID, ProjectID: x.ProjectID, ThreadID: x.ThreadID, SessionID: x.SessionID,
		EmployeeID: x.EmployeeID, Employee: x.Employee, Role: x.Role, NodeID: nodeID, ServerID: serverID,
		ServerName: serverName, CertSerial: serial, ConnID: connID}
}

// audit writes one at_ssh_audit row (command already redacted); a failure is only logged.
func (g *Gateway) audit(ctx context.Context, a store.SSHAudit, kind, reason string, detail map[string]any) int64 {
	a.Kind, a.Reason, a.Detail = kind, reason, detail
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	row, err := g.store.InsertSSHAudit(actx, a)
	if err != nil {
		g.log.Warn("ssh audit", "kind", kind, "session_id", a.SessionID, "err", err)
		return 0
	}
	return row.ID
}

// emitServerEvent writes the thread-visible summary of a finished connection (at_events, type server).
func (g *Gateway) emitServerEvent(ctx context.Context, x store.SSHSession, payload map[string]any) {
	ectx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	var thread *string
	if x.ThreadID != "" {
		thread = &x.ThreadID
	}
	err := g.core.InTx(ectx, func(tx *core.Tx) error {
		_, err := tx.Emit(ectx, x.ProjectID, thread, core.EvServer, x.Employee, payload)
		return err
	})
	if err != nil {
		g.log.Warn("ssh: server event", "session_id", x.SessionID, "err", err)
	}
}
