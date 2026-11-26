package engine

import (
	"context"
	"encoding/json"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/userfs"
)

// Node features of the shared folder (juicefs-shared-storage design).
const (
	// FeatureFsMount: the daemon can run juicefs itself (fs.mount / fs.unmount).
	FeatureFsMount = "fs_mount"
	// FeatureFsExternal: the volume is mounted outside the daemon (Docker volume plugin, host
	// service) at AGENT_TEAMS_FS_DIR; the daemon only reports it.
	FeatureFsExternal = "fs_external"
)

// nodeFsStatus is at_nodes.fs_status.
type nodeFsStatus struct {
	State      string `json:"state"`
	Mode       string `json:"mode"`
	Volume     string `json:"volume,omitempty"`
	Generation int    `json:"generation,omitempty"`
	Error      string `json:"error,omitempty"`
}

func readFsStatus(ctx context.Context, q core.DB, nodeID string) (nodeFsStatus, error) {
	var raw []byte
	var st nodeFsStatus
	if err := store.QueryRow(ctx, q, `SELECT fs_status FROM at_nodes WHERE id = $1`, nodeID).Scan(&raw); err != nil {
		return st, core.MapErr(err)
	}
	return st, json.Unmarshal(raw, &st)
}

// sharedFolder is session.open fs: the project's folder and the user's shared folder, when the
// user has a ready volume and the node reports it mounted.
func (e *Engine) sharedFolder(ctx context.Context, q core.DB, userID, nodeID, projectID string) (*protocol.SessionOpenFs, error) {
	if !e.FS.Ready(ctx, userID) {
		return nil, nil
	}
	st, err := readFsStatus(ctx, q, nodeID)
	if err != nil || st.State != string(protocol.FsStatusStateMounted) {
		return nil, err
	}
	return &protocol.SessionOpenFs{Project: protocol.RelPath(userfs.ProjectDir(projectID)), Shared: protocol.RelPath(userfs.SharedDir)}, nil
}

// fsStatus stores a node's fs.status and refreshes its node page.
func (e *Engine) fsStatus(ctx context.Context, node store.Node, m *protocol.FsStatus) error {
	st := nodeFsStatus{State: string(m.State), Mode: string(m.Mode)}
	if m.Volume != nil {
		st.Volume = *m.Volume
	}
	if m.Generation != nil {
		st.Generation = *m.Generation
	}
	if m.Error != nil {
		st.Error = *m.Error
	}
	raw, _ := json.Marshal(st)
	if _, err := store.Exec(ctx, e.Core.Pool, `UPDATE at_nodes SET fs_status = $2 WHERE id = $1`, node.ID, raw); err != nil {
		return err
	}
	e.Events.User(ctx, node.UserID, core.EvNode, map[string]any{"node": map[string]any{"id": node.ID, "name": node.Name, "fs": st.State}})
	return nil
}

// pushFS sends the user's online nodes their mount settings (after a volume became ready or was removed).
func (e *Engine) pushFS(ctx context.Context, userID string) {
	rows, err := store.Query(ctx, e.Core.Pool, `SELECT id FROM at_nodes WHERE user_id = $1`, userID)
	if err != nil {
		e.log.Warn("shared folder push: nodes", "user_id", userID, "err", err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if e.Gateway.Online(id) {
			e.pushFSNode(ctx, id, userID)
		}
	}
}

// pushFSNode sends one node fs.mount (the volume is ready) or fs.unmount (it had one mounted and
// the volume is gone). Only to daemons that mount themselves; the credentials never touch the outbox.
func (e *Engine) pushFSNode(ctx context.Context, nodeID, userID string) {
	if e.FS == nil {
		return
	}
	ok, err := core.NodeHasFeature(ctx, e.Core.Pool, nodeID, FeatureFsMount)
	if err != nil || !ok {
		return
	}
	spec, err := e.FS.MountSpec(ctx, userID)
	if err == nil {
		if err := e.Gateway.SendNow(ctx, nodeID, protocol.TypeFsMount, spec); err != nil {
			e.log.Info("fs.mount not sent", "node_id", nodeID, "err", err)
		}
		return
	}
	if !userfs.IsNotFound(err) {
		e.log.Warn("shared folder mount spec", "user_id", userID, "err", err)
		return
	}
	st, err := readFsStatus(ctx, e.Core.Pool, nodeID)
	if err != nil || st.Volume == "" || st.Mode != string(protocol.FsStatusModeDaemon) {
		return
	}
	_ = e.Gateway.SendNow(ctx, nodeID, protocol.TypeFsUnmount, protocol.FsUnmount{Volume: st.Volume})
}
