package orchapi

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/userfs"
)

// The user's shared folder (shared-folder design). It is on by default: the console
// provisions every user's bucket by itself (the shared-folders job) and deletes it only with the
// account. GET shows it and POST retries a failed provisioning at once; neither carries a key (only
// fs.mount, on a node's live link, does).

// FsVolumeView is the user's folder without any secret.
type FsVolumeView struct {
	Bucket     string    `json:"bucket" doc:"The object store bucket holding the folder"`
	Prefix     string    `json:"prefix" doc:"The user's folder in the bucket (users/<id>/)"`
	ReadOnly   bool      `json:"read_only" doc:"Over its capacity: the nodes' key cannot write (reading and deleting still work) until the folder is back under it"`
	State      string    `json:"state" enum:"provisioning,ready,error,deleting"`
	Error      string    `json:"error"`
	CapacityGB int       `json:"capacity_gb"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// FsUsageView is what the user's bucket holds.
type FsUsageView struct {
	UsedBytes int64 `json:"used_bytes" doc:"Bytes the bucket's objects hold"`
	Objects   int64 `json:"objects" doc:"Objects: files, and folders geesefs keeps as markers"`
}

// FsView is GET /api/storage/fs.
type FsView struct {
	Configured bool          `json:"configured" doc:"The console has AGENT_TEAMS_FS_* (then every user's folder is switched on by itself)"`
	Volume     *FsVolumeView `json:"volume,omitempty" doc:"Absent until the console has started provisioning it"`
	ProjectDir string        `json:"project_dir" doc:"A project's folder inside the volume, with {project_id}"`
	SharedDir  string        `json:"shared_dir" doc:"The folder every project of the user shares"`
	Usage      *FsUsageView  `json:"usage,omitempty" doc:"What the ready bucket holds; absent before it is ready or when it cannot be counted"`
}

type fsOutput struct{ Body FsView }

func (a *API) registerUserFs(api huma.API) {
	t := "storage"
	huma.Register(api, op("get-shared-folder", "GET", "/api/storage/fs", t, summary("My shared folder (a bucket of the object store)")), a.getUserFs)
	huma.Register(api, op("enable-shared-folder", "POST", "/api/storage/fs", t,
		summary("Provision the shared folder now (it is on by default; this retries a failed attempt at once)")), a.enableUserFs)
}

func (a *API) fsView(ctx context.Context, userID string) (FsView, error) {
	v := FsView{Configured: a.e.FS.Enabled(), ProjectDir: userfs.ProjectDir("{project_id}"), SharedDir: userfs.SharedDir}
	if a.e.FS == nil {
		return v, nil
	}
	vol, err := a.e.FS.Get(ctx, userID)
	if userfs.IsNotFound(err) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	v.Volume = &FsVolumeView{Bucket: vol.Bucket, Prefix: vol.Prefix, ReadOnly: vol.ReadOnly, State: vol.State, Error: vol.Error, CapacityGB: vol.CapacityGB, CreatedAt: vol.CreatedAt, UpdatedAt: vol.UpdatedAt}
	if vol.State == userfs.StateReady {
		v.Usage = a.fsUsage(ctx, userID)
	}
	return v, nil
}

// fsUsage is the volume's counters, or nil when they cannot be read (the page then shows none).
func (a *API) fsUsage(ctx context.Context, userID string) *FsUsageView {
	u, err := a.e.FS.Usage(ctx, userID)
	if err != nil {
		slog.Warn("shared folder usage", "user_id", userID, "err", err)
		return nil
	}
	return &FsUsageView{UsedBytes: u.UsedBytes, Objects: u.Objects}
}

func (a *API) getUserFs(ctx context.Context, _ *struct{}) (*fsOutput, error) {
	u, err := a.user(ctx)
	if err != nil {
		return nil, err
	}
	v, err := a.fsView(ctx, u.ID)
	return &fsOutput{Body: v}, err
}

func fsErr(err error) error {
	if errors.Is(err, userfs.ErrDisabled) {
		return httpapi.FailErr(503, "fs_not_configured", err)
	}
	if userfs.IsNotFound(err) {
		return httpapi.FailErr(404, "not_found", err)
	}
	return err
}

func (a *API) enableUserFs(ctx context.Context, _ *struct{}) (*fsOutput, error) {
	u, err := a.user(ctx)
	if err != nil {
		return nil, err
	}
	if a.e.FS == nil {
		return nil, fsErr(userfs.ErrDisabled)
	}
	if _, err := a.e.FS.Enable(ctx, u.ID); err != nil {
		return nil, fsErr(err)
	}
	v, err := a.fsView(ctx, u.ID)
	return &fsOutput{Body: v}, err
}
