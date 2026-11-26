package orchapi

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/userfs"
)

// The user's shared folder (juicefs-shared-storage design): GET shows it, POST switches it on
// (provisions the JuiceFS volume), DELETE removes it with every file in it, and docker-volume hands
// out the Compose volume a Docker node mounts it with (the only response carrying a credential).

// FsVolumeView is the user's volume without any secret.
type FsVolumeView struct {
	Volume     string    `json:"volume"`
	State      string    `json:"state" enum:"provisioning,ready,error,deleting"`
	Error      string    `json:"error"`
	CapacityGB int       `json:"capacity_gb"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// FsView is GET /api/storage/fs.
type FsView struct {
	Configured bool          `json:"configured" doc:"The console has AGENT_TEAMS_FS_* (otherwise the folder cannot be switched on)"`
	Volume     *FsVolumeView `json:"volume,omitempty" doc:"Absent until switched on"`
	ProjectDir string        `json:"project_dir" doc:"A project's folder inside the volume, with {project_id}"`
	SharedDir  string        `json:"shared_dir" doc:"The folder every project of the user shares"`
}

type fsOutput struct{ Body FsView }

type fsDeleteInput struct {
	Body struct {
		Confirm string `json:"confirm" maxLength:"64" doc:"The volume name, typed by the user: every file in it is deleted"`
	}
}

// FsDockerVolume is POST /api/storage/fs/docker-volume.
type FsDockerVolume struct {
	Compose string `json:"compose" doc:"Compose fragment: the juicedata/juicefs volume and how the node service mounts it"`
	Plugin  string `json:"plugin" doc:"Host command installing the volume plugin (once per host)"`
	Mount   string `json:"mount" doc:"Where the node container sees the folder (AGENT_TEAMS_FS_DIR)"`
}

type fsDockerOutput struct{ Body FsDockerVolume }

// FsDockerMount is where the node image mounts the folder.
const FsDockerMount = "/home/agent/fs"

func (a *API) registerUserFs(api huma.API) {
	t := "storage"
	huma.Register(api, op("get-shared-folder", "GET", "/api/storage/fs", t, summary("My shared folder (JuiceFS volume)")), a.getUserFs)
	huma.Register(api, op("enable-shared-folder", "POST", "/api/storage/fs", t,
		summary("Switch the shared folder on (or retry a failed provisioning)")), a.enableUserFs)
	huma.Register(api, op("delete-shared-folder", "DELETE", "/api/storage/fs", t, status(204),
		summary("Delete the shared folder and every file in it")), a.deleteUserFs)
	huma.Register(api, op("shared-folder-docker-volume", "POST", "/api/storage/fs/docker-volume", t,
		summary("Compose volume for a Docker node (carries the metadata password)")), a.userFsDockerVolume)
}

func (a *API) fsView(ctx context.Context, userID string) (FsView, error) {
	v := FsView{Configured: a.e.FS.Enabled(), ProjectDir: userfs.ProjectDir("{project_id}"), SharedDir: userfs.SharedDir}
	vol, err := a.e.FS.Get(ctx, userID)
	if userfs.IsNotFound(err) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	v.Volume = &FsVolumeView{Volume: vol.Volume, State: vol.State, Error: vol.Error, CapacityGB: vol.CapacityGB, CreatedAt: vol.CreatedAt, UpdatedAt: vol.UpdatedAt}
	return v, nil
}

func (a *API) getUserFs(ctx context.Context, _ *struct{}) (*fsOutput, error) {
	u, err := a.user(ctx)
	if err != nil {
		return nil, err
	}
	if a.e.FS == nil {
		return &fsOutput{Body: FsView{ProjectDir: userfs.ProjectDir("{project_id}"), SharedDir: userfs.SharedDir}}, nil
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

func (a *API) deleteUserFs(ctx context.Context, in *fsDeleteInput) (*struct{}, error) {
	u, err := a.user(ctx)
	if err != nil {
		return nil, err
	}
	if a.e.FS == nil {
		return nil, fsErr(userfs.ErrDisabled)
	}
	vol, err := a.e.FS.Get(ctx, u.ID)
	if err != nil {
		return nil, fsErr(err)
	}
	if in.Body.Confirm != vol.Volume {
		return nil, httpapi.Invalid("confirm", "请输入卷名 "+vol.Volume+" 确认删除")
	}
	return nil, fsErr(a.e.FS.Disable(ctx, u.ID))
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
	meta, err := url.Parse(spec.MetaURL)
	if err != nil {
		return nil, err
	}
	meta.User = url.UserPassword(meta.User.Username(), spec.MetaPassword)
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
`, spec.Volume, meta.String(), FsDockerMount)
	return &fsDockerOutput{Body: FsDockerVolume{Compose: compose, Mount: FsDockerMount,
		Plugin: "docker plugin install juicedata/juicefs --alias juicedata/juicefs --grant-all-permissions"}}, nil
}
