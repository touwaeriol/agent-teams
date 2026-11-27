package orchapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/userfs"
)

// The user's shared folder (juicefs-shared-storage design). It is on by default: the console
// provisions every user's volume by itself (engine.runSharedFolders) and destroys it only with the
// account. GET shows it, POST retries a failed provisioning at once, and docker-volume hands out the
// Compose volume a Docker node mounts it with (the only response carrying a credential).

// FsVolumeView is the user's volume without any secret.
type FsVolumeView struct {
	Volume     string    `json:"volume"`
	State      string    `json:"state" enum:"provisioning,ready,error,deleting"`
	Error      string    `json:"error"`
	CapacityGB int       `json:"capacity_gb"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// FsUsageView is what the user's volume holds.
type FsUsageView struct {
	UsedBytes int64 `json:"used_bytes" doc:"Bytes in use (JuiceFS counts in 4 KiB blocks)"`
	Inodes    int64 `json:"inodes" doc:"Files and folders"`
}

// FsView is GET /api/storage/fs.
type FsView struct {
	Configured bool          `json:"configured" doc:"The console has AGENT_TEAMS_FS_* (then every user's folder is switched on by itself)"`
	Volume     *FsVolumeView `json:"volume,omitempty" doc:"Absent until the console has started provisioning it"`
	ProjectDir string        `json:"project_dir" doc:"A project's folder inside the volume, with {project_id}"`
	SharedDir  string        `json:"shared_dir" doc:"The folder every project of the user shares"`
	Usage      *FsUsageView  `json:"usage,omitempty" doc:"What the ready volume holds; absent before it is ready or when the metadata cannot be read"`
}

type fsOutput struct{ Body FsView }

// FsDockerVolume is POST /api/storage/fs/docker-volume.
type FsDockerVolume struct {
	Compose string `json:"compose" doc:"Compose fragment: the juicedata/juicefs volume and how the node service mounts it"`
	Plugin  string `json:"plugin" doc:"Host command installing the volume plugin (once per host)"`
	Mount   string `json:"mount" doc:"Where the node container sees the folder (AGENT_TEAMS_FS_DIR)"`
}

type fsDockerOutput struct{ Body FsDockerVolume }

// FsDockerMount is where the node image mounts the folder.
const FsDockerMount = "/home/agent/.fs"

func (a *API) registerUserFs(api huma.API) {
	t := "storage"
	huma.Register(api, op("get-shared-folder", "GET", "/api/storage/fs", t, summary("My shared folder (JuiceFS volume)")), a.getUserFs)
	huma.Register(api, op("enable-shared-folder", "POST", "/api/storage/fs", t,
		summary("Provision the shared folder now (it is on by default; this retries a failed attempt at once)")), a.enableUserFs)
	huma.Register(api, op("shared-folder-docker-volume", "POST", "/api/storage/fs/docker-volume", t,
		summary("Compose volume for a Docker node (carries the metadata password)")), a.userFsDockerVolume)
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
	v.Volume = &FsVolumeView{Volume: vol.Volume, State: vol.State, Error: vol.Error, CapacityGB: vol.CapacityGB, CreatedAt: vol.CreatedAt, UpdatedAt: vol.UpdatedAt}
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
	return &FsUsageView{UsedBytes: u.UsedBytes, Inodes: u.Inodes}
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
		return httpapi.Fail(503, "fs_not_configured", err.Error())
	}
	if userfs.IsNotFound(err) {
		return httpapi.Fail(404, "not_found", err.Error())
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

func (a *API) userFsDockerVolume(ctx context.Context, _ *struct{}) (*fsDockerOutput, error) {
	u, err := a.user(ctx)
	if err != nil {
		return nil, err
	}
	if a.e.FS == nil {
		return nil, fsErr(userfs.ErrDisabled)
	}
	spec, err := a.e.FS.MountSpec(ctx, u.ID)
	if err != nil {
		return nil, fsErr(err)
	}
	metaURL, err := withPassword(spec.MetaURL, spec.MetaPassword)
	if err != nil {
		return nil, err
	}
	compose := fmt.Sprintf(`# 共享文件夹（JuiceFS 卷 %[1]s）。卷参数里有元数据库密码，只放在这台宿主机上。
services:
  node:
    environment:
      AGENT_TEAMS_FS_DIR: %[3]s
    volumes:
      - agent-teams-fs:%[3]s

volumes:
  agent-teams-fs:
    driver: juicedata/juicefs
    driver_opts:
      name: %[1]s
      metaurl: "%[2]s"
`, spec.Volume, metaURL, FsDockerMount)
	return &fsDockerOutput{Body: FsDockerVolume{Compose: compose, Mount: FsDockerMount,
		Plugin: "docker plugin install juicedata/juicefs --alias juicedata/juicefs --grant-all-permissions"}}, nil
}

// withPassword puts the metadata password back into the URL (the plugin takes one URL).
func withPassword(raw, password string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	u.User = url.UserPassword(u.User.Username(), password)
	return u.String(), nil
}
