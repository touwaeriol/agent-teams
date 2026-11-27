package engine

import (
	"context"
	"encoding/json"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/scheduler"
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

// UseSharedFolder wires the shared-folder service (nil: none): volume changes reach the user's
// nodes and session.open carries the folders.
func (e *Engine) UseSharedFolder(s *userfs.Service) {
	if e.FS = s; s == nil {
		e.Orch.SharedFolder = nil
		e.Core.Volume = nil
		return
	}
	s.OnChange = e.pushFS
	if s.Enabled() {
		e.Core.Volume = s // deliverables then live only in the owners' folders
	}
	e.Orch.SharedFolder = e.sharedFolder
}

// fsSweepEvery is how often the automatic switch-on looks for users without a ready folder.
const fsSweepEvery = time.Minute

// runSharedFolders keeps every user's shared folder on (it is on by default): at start and every
// fsSweepEvery it provisions the missing ones, then moves deliverable versions still kept in the
// database into their owners' folders. One replica runs it (leader lock).
func (e *Engine) runSharedFolders(ctx context.Context) {
	if !e.FS.Enabled() {
		return
	}
	scheduler.RunAsLeader(ctx, e.Core.Pool, "agent-teams:shared-folders", 30*time.Second, func(ctx context.Context) {
		for {
			e.sweepSharedFolders(ctx)
			select {
			case <-ctx.Done():
				return
			case <-time.After(fsSweepEvery):
			}
		}
	})
}

func (e *Engine) sweepSharedFolders(ctx context.Context) {
	if _, err := e.FS.ProvisionAll(ctx); err != nil {
		e.log.Warn("shared folders: provisioning sweep", "err", err)
	}
	rep, err := e.Core.Artifacts().MoveToVolumes(ctx, e.log)
	if err != nil {
		e.log.Warn("shared folders: moving deliverables", "err", err)
	} else if rep.Moved > 0 || rep.Failed > 0 {
		e.log.Info("shared folders: deliverables moved into the folders", "moved", rep.Moved, "failed", rep.Failed)
	}
}

// sharedFolder is session.open fs: the project's folder and the user's shared folder, when the
// user has a ready volume and the node reports that very volume and generation mounted (a status
// left from another volume, or from before a re-enable, does not count; at_nodes.fs_status is
// reset whenever the node connects or drops).
func (e *Engine) sharedFolder(ctx context.Context, q core.DB, userID, nodeID, projectID string) (*protocol.SessionOpenFs, error) {
	vol, err := e.FS.Get(ctx, userID)
	if err != nil || vol.State != userfs.StateReady {
		return nil, nil
	}
	st, err := readFsStatus(ctx, q, nodeID)
	if err != nil || !st.mounts(vol) {
		return nil, err
	}
	return &protocol.SessionOpenFs{Project: protocol.RelPath(userfs.ProjectDir(projectID)), Shared: protocol.RelPath(userfs.SharedDir)}, nil
}

// mounts tells whether the status is vol mounted at its current generation. A volume mounted
// outside the daemon (Docker volume plugin) is reported without them; it counts unless it names
// another volume.
func (st nodeFsStatus) mounts(vol userfs.Volume) bool {
	switch {
	case st.State != string(protocol.FsStatusStateMounted):
		return false
	case st.Mode == string(protocol.FsStatusModeExternal):
		return st.Volume == "" || st.Volume == vol.Volume
	default:
		return st.Volume == vol.Volume && st.Generation == vol.Generation
	}
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
