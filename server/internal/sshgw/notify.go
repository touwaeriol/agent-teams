package sshgw

import (
	"context"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// SessionServers is what session.open carries in ssh_servers (protocol.md §8.1): the servers the
// session's role is granted in its project now; nil when the node does not speak ssh_gateway or
// the session may not use servers (coordinator, lead, read-only roles). q may be the transaction
// that just created the session.
func SessionServers(ctx context.Context, st *store.Store, q core.DB, nodeID, sessionID string) (protocol.SshServers, error) {
	ok, err := core.NodeHasFeature(ctx, q, nodeID, Feature)
	if err != nil || !ok {
		return nil, err
	}
	x, servers, err := st.On(q).SessionServers(ctx, sessionID)
	if err != nil || !x.MayUseSSH() || len(servers) == 0 {
		return nil, err
	}
	return toList(servers), nil
}

func toList(servers []store.RemoteServer) protocol.SshServers {
	out := make(protocol.SshServers, len(servers))
	for i, s := range servers {
		out[i] = protocol.SshServer{Name: s.Name}
	}
	return out
}

// nextVersion is the version of the next ssh.servers_update: milliseconds since the epoch, strictly
// growing in this process (so a session's versions grow whichever replica sends them).
func (g *Gateway) nextVersion() int {
	now := g.now().UnixMilli()
	for {
		last := g.version.Load()
		v := max(now, last+1)
		if g.version.CompareAndSwap(last, v) {
			return int(v)
		}
	}
}

// ServersChanged tells every live session of the project that may use servers which servers it has
// now (reliable ssh.servers_update), after the project's servers or role grants changed. Call it
// after the change committed; revoking certificates (Revoked) is what actually cuts access.
func (g *Gateway) ServersChanged(ctx context.Context, projectID string) error {
	live, err := g.store.LiveSSHSessions(ctx, projectID)
	if err != nil || len(live) == 0 {
		return err
	}
	return g.core.InTx(ctx, func(tx *core.Tx) error {
		for _, x := range live {
			if err := g.queueUpdate(ctx, tx, x); err != nil {
				return err
			}
		}
		return nil
	})
}

// NodeOnline sends each live session of a node that just connected its current server list once
// (protocol.md §8.1: the daemon may have missed updates while offline).
func (g *Gateway) NodeOnline(ctx context.Context, node store.Node) error {
	ok, err := core.NodeHasFeature(ctx, g.core.Pool, node.ID, Feature)
	if err != nil || !ok {
		return err
	}
	sessions, err := core.NodeLiveSessions(ctx, g.core.Pool, node.ID)
	if err != nil || len(sessions) == 0 {
		return err
	}
	return g.core.InTx(ctx, func(tx *core.Tx) error {
		for _, s := range sessions {
			x, err := g.store.On(tx).SSHSession(ctx, s.ID)
			if err != nil || !x.MayUseSSH() || x.NodeID != node.ID {
				continue
			}
			if err := g.queueUpdate(ctx, tx, x); err != nil {
				return err
			}
		}
		return nil
	})
}

func (g *Gateway) queueUpdate(ctx context.Context, tx *core.Tx, x store.SSHSession) error {
	if x.NodeID == "" {
		return nil
	}
	ok, err := core.NodeHasFeature(ctx, tx, x.NodeID, Feature)
	if err != nil || !ok {
		return err
	}
	_, servers, err := g.store.On(tx).SessionServers(ctx, x.SessionID)
	if err != nil {
		return err
	}
	body := protocol.SshServersUpdate{SessionID: protocol.SessionID(x.SessionID), OpenGen: protocol.OpenGen(x.OpenGen),
		Version: g.nextVersion(), Servers: toList(servers)}
	_, err = g.nodes.Enqueue(ctx, tx, x.NodeID, nodegw.Command{Type: protocol.TypeSshServersUpdate, Body: body,
		SessionID: x.SessionID, OpenGen: x.OpenGen})
	return err
}
