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
	Volume     string                `json:"volume"`
	Backend    string                `json:"backend" enum:"platform,s3,juicefs" doc:"platform: the platform's storage; s3: the user's bucket; juicefs: the user's own JuiceFS"`
	Summary    userfs.StorageSummary `json:"summary" doc:"Where the user's own storage is (no secret)"`
	State      string                `json:"state" enum:"provisioning,ready,error,deleting"`
	Error      string                `json:"error"`
	CapacityGB int                   `json:"capacity_gb"`
	CreatedAt  time.Time             `json:"created_at"`
	UpdatedAt  time.Time             `json:"updated_at"`
}

// FsView is GET /api/storage/fs.
type FsView struct {
	Configured bool          `json:"configured" doc:"The console has AGENT_TEAMS_FS_* (otherwise the folder cannot be switched on)"`
	Volume     *FsVolumeView `json:"volume,omitempty" doc:"Absent until switched on"`
	ProjectDir string        `json:"project_dir" doc:"A project's folder inside the volume, with {project_id}"`
	SharedDir  string        `json:"shared_dir" doc:"The folder every project of the user shares"`
}

type fsOutput struct{ Body FsView }

// fsEnableInput picks the storage; no body (or kind platform) = the platform's.
type fsEnableInput struct {
	Body *struct {
		Kind         string `json:"kind,omitempty" enum:"platform,s3,juicefs" doc:"Default platform"`
		S3Endpoint   string `json:"s3_endpoint,omitempty" maxLength:"300" doc:"s3: https://host[:port]"`
		Bucket       string `json:"bucket,omitempty" maxLength:"63"`
		AccessKey    string `json:"access_key,omitempty" maxLength:"256"`
		SecretKey    string `json:"secret_key,omitempty" maxLength:"256"`
		MetaURL      string `json:"meta_url,omitempty" maxLength:"1000" doc:"juicefs: the metadata engine URL of the user's formatted volume"`
		MetaPassword string `json:"meta_password,omitempty" maxLength:"256" doc:"juicefs: its password when not in the URL"`
	}
}

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
	v.Volume = &FsVolumeView{Volume: vol.Volume, Backend: vol.Backend, Summary: vol.Summary, State: vol.State, Error: vol.Error, CapacityGB: vol.CapacityGB, CreatedAt: vol.CreatedAt, UpdatedAt: vol.UpdatedAt}
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
	if errors.Is(err, userfs.ErrSwitch) {
		return httpapi.Fail(409, "fs_exists", err.Error())
	}
	if errors.Is(err, userfs.ErrInvalidBackend) {
		return httpapi.Fail(422, "invalid_storage", err.Error())
	}
	return err
}

func (a *API) enableUserFs(ctx context.Context, in *fsEnableInput) (*fsOutput, error) {
	u, err := a.user(ctx)
	if err != nil {
		return nil, err
	}
	if a.e.FS == nil {
		return nil, fsErr(userfs.ErrDisabled)
	}
	var b userfs.Backend
	if in.Body != nil {
		b = userfs.Backend{Kind: in.Body.Kind, S3Endpoint: in.Body.S3Endpoint, Bucket: in.Body.Bucket, AccessKey: in.Body.AccessKey,
			SecretKey: in.Body.SecretKey, MetaURL: in.Body.MetaURL, MetaPassword: in.Body.MetaPassword}
	}
	if _, err := a.e.FS.Enable(ctx, u.ID, b); err != nil {
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

// withPassword puts the metadata password back into the URL (the plugin takes one URL); a URL
// that already carries it, or needs none, is kept.
func withPassword(raw, password string) (string, error) {
	if password == "" {
		return raw, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	u.User = url.UserPassword(u.User.Username(), password)
	return u.String(), nil
}
