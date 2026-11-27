package orchapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/danielgtaylor/huma/v2"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/userfs"
)

// The user's shared folder (juicefs-shared-storage design). It is on by default: the console
// provisions every user's bucket by itself (the shared-folders job) and deletes it only with the
// account. GET shows it, POST retries a failed provisioning at once, and docker-volume hands out the
// Compose volume a Docker node mounts it with (the only response carrying a credential).

// FsVolumeView is the user's folder without any secret.
type FsVolumeView struct {
	Volume     string    `json:"volume" doc:"The platform's shared volume"`
	Bucket     string    `json:"bucket" doc:"The user's bucket of the volume's gateway (its top-level folder)"`
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
	Compose string `json:"compose" doc:"Compose fragment: the rclone volume over the user's bucket and how the node service mounts it"`
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
		summary("Compose volume for a Docker node (carries the user's gateway key)")), a.userFsDockerVolume)
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
	v.Volume = &FsVolumeView{Volume: vol.Volume, Bucket: vol.Bucket, State: vol.State, Error: vol.Error, CapacityGB: vol.CapacityGB, CreatedAt: vol.CreatedAt, UpdatedAt: vol.UpdatedAt}
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
	return &fsDockerOutput{Body: dockerVolume(spec)}, nil
}

// dockerVolume is the Compose volume of the rclone volume plugin over the user's bucket.
func dockerVolume(spec protocol.FsMount) FsDockerVolume {
	compose := fmt.Sprintf(`# 共享文件夹（网关 bucket %[1]s）。卷参数里有这个账号的密钥，只放在这台宿主机上。
services:
  node:
    environment:
      AGENT_TEAMS_FS_DIR: %[5]s
    volumes:
      - agent-teams-fs:%[5]s

volumes:
  agent-teams-fs:
    driver: rclone
    driver_opts:
      remote: ":s3:%[1]s"
      s3-provider: Other
      s3-endpoint: "%[2]s"
      s3-access-key-id: "%[3]s"
      s3-secret-access-key: "%[4]s"
      s3-force-path-style: "true"
      vfs-cache-mode: writes
      allow-other: "true"
`, spec.Bucket, spec.Endpoint, spec.AccessKey, spec.SecretKey, FsDockerMount)
	return FsDockerVolume{Compose: compose, Mount: FsDockerMount,
		Plugin: "docker plugin install rclone/docker-volume-rclone:amd64 args=\"-v\" --alias rclone --grant-all-permissions"}
}
