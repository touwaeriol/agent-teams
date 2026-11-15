package httpapi

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/plugins"
	"github.com/touwaeriol/agent-teams/server/internal/skills"
)

// PluginsBody is GET /api/plugins and GET /api/admin/plugins.
type PluginsBody struct {
	Plugins []plugins.Plugin `json:"plugins"`
}

type pluginsOutput struct{ Body PluginsBody }

type pluginOutput struct{ Body plugins.Plugin }

type newPluginInput struct {
	Body struct {
		Zip string `json:"zip" minLength:"1" maxLength:"2900000" doc:"Base64 of a zip of the plugin folder (.claude-plugin/plugin.json at the root or in one top folder); the same name again uploads a new version"`
	}
}

type pluginIDInput struct {
	ID string `path:"id" maxLength:"64"`
}

type pluginPatchInput struct {
	ID   string `path:"id" maxLength:"64"`
	Body struct {
		Enabled bool `json:"enabled" doc:"Disabled plugins stay on roles but are not loaded"`
	}
}

func (s *Server) registerPlugins(api huma.API) {
	team, admin := []string{"team"}, []string{"admin"}
	huma.Register(api, huma.Operation{OperationID: "list-plugins", Method: "GET", Path: "/api/plugins", Tags: team,
		Summary: "The user's plugins and the enabled built-in plugins"}, s.listPlugins)
	huma.Register(api, huma.Operation{OperationID: "upload-plugin", Method: "POST", Path: "/api/plugins", Tags: team,
		DefaultStatus: http.StatusCreated, MaxBodyBytes: 4 << 20,
		Summary: "Upload a plugin zip (a new plugin, or a new version of one with the same name)"}, s.uploadPlugin)
	huma.Register(api, huma.Operation{OperationID: "update-plugin", Method: "PATCH", Path: "/api/plugins/{id}", Tags: team,
		Summary: "Enable or disable one of the user's plugins"}, s.patchPlugin)
	huma.Register(api, huma.Operation{OperationID: "delete-plugin", Method: "DELETE", Path: "/api/plugins/{id}", Tags: team,
		DefaultStatus: http.StatusNoContent, Summary: "Delete one of the user's plugins (all versions) and remove it from the roles"}, s.deletePlugin)

	huma.Register(api, huma.Operation{OperationID: "admin-list-plugins", Method: "GET", Path: "/api/admin/plugins", Tags: admin,
		Summary: "Every built-in plugin, disabled ones included"}, s.adminListPlugins)
	huma.Register(api, huma.Operation{OperationID: "admin-upload-plugin", Method: "POST", Path: "/api/admin/plugins", Tags: admin,
		DefaultStatus: http.StatusCreated, MaxBodyBytes: 4 << 20, Summary: "Upload a built-in plugin or a new version of one"}, s.adminUploadPlugin)
	huma.Register(api, huma.Operation{OperationID: "admin-update-plugin", Method: "PATCH", Path: "/api/admin/plugins/{id}", Tags: admin,
		Summary: "Enable or disable a built-in plugin"}, s.adminPatchPlugin)
	huma.Register(api, huma.Operation{OperationID: "admin-delete-plugin", Method: "DELETE", Path: "/api/admin/plugins/{id}", Tags: admin,
		DefaultStatus: http.StatusNoContent, Summary: "Delete a built-in plugin and remove it from every role"}, s.adminDeletePlugin)
	huma.Register(api, huma.Operation{OperationID: "admin-create-skill", Method: "POST", Path: "/api/admin/skills", Tags: admin,
		DefaultStatus: http.StatusCreated, MaxBodyBytes: 4 << 20, Summary: "Upload a built-in skill every user may tick"}, s.adminCreateSkill)
	huma.Register(api, huma.Operation{OperationID: "admin-delete-skill", Method: "DELETE", Path: "/api/admin/skills/{id}", Tags: admin,
		DefaultStatus: http.StatusNoContent, Summary: "Delete an admin-created built-in skill and remove it from every role"}, s.adminDeleteSkill)
}

// pluginCaller is the signed-in user and the library scope they write to.
func pluginCaller(ctx context.Context, admin bool) (string, plugins.Owner, error) {
	if admin {
		p, err := requireAdmin(ctx)
		if err != nil {
			return "", plugins.Owner{}, err
		}
		return p.User.ID, plugins.Builtins, nil
	}
	p, err := requireUser(ctx)
	if err != nil {
		return "", plugins.Owner{}, err
	}
	return p.User.ID, plugins.Owner{UserID: p.User.ID}, nil
}

func (s *Server) listPlugins(ctx context.Context, _ *struct{}) (*pluginsOutput, error) {
	userID, _, err := pluginCaller(ctx, false)
	if err != nil {
		return nil, err
	}
	list, err := plugins.List(ctx, s.store.Pool, userID)
	return &pluginsOutput{Body: PluginsBody{Plugins: list}}, err
}

func (s *Server) adminListPlugins(ctx context.Context, _ *struct{}) (*pluginsOutput, error) {
	if _, _, err := pluginCaller(ctx, true); err != nil {
		return nil, err
	}
	list, err := plugins.ListBuiltin(ctx, s.store.Pool)
	return &pluginsOutput{Body: PluginsBody{Plugins: list}}, err
}

func (s *Server) uploadPlugin(ctx context.Context, in *newPluginInput) (*pluginOutput, error) {
	return s.upload(ctx, false, in.Body.Zip)
}

func (s *Server) adminUploadPlugin(ctx context.Context, in *newPluginInput) (*pluginOutput, error) {
	return s.upload(ctx, true, in.Body.Zip)
}

func (s *Server) upload(ctx context.Context, admin bool, zipB64 string) (*pluginOutput, error) {
	userID, owner, err := pluginCaller(ctx, admin)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(zipB64)
	if err != nil {
		return nil, fail(http.StatusUnprocessableEntity, "validation", "zip 不是有效的 base64")
	}
	files, err := plugins.FromZip(raw)
	if err != nil {
		return nil, pluginError(err)
	}
	p, err := plugins.Upload(ctx, s.store.Pool, owner, userID, files)
	if err != nil {
		return nil, pluginError(err)
	}
	s.audit(ctx, userID, "plugin_upload", "plugin", p.ID, map[string]any{"name": p.Name, "version": p.Latest, "builtin": admin})
	return &pluginOutput{Body: p}, nil
}

func pluginError(err error) error {
	if plugins.IsProblem(err) {
		return fail(http.StatusUnprocessableEntity, "validation", err.Error())
	}
	if errors.Is(err, plugins.ErrNotFound) {
		return fail(http.StatusNotFound, "not_found", "没有这个插件")
	}
	return err
}

func (s *Server) patchPlugin(ctx context.Context, in *pluginPatchInput) (*pluginOutput, error) {
	return s.setPluginEnabled(ctx, false, in)
}

func (s *Server) adminPatchPlugin(ctx context.Context, in *pluginPatchInput) (*pluginOutput, error) {
	return s.setPluginEnabled(ctx, true, in)
}

func (s *Server) setPluginEnabled(ctx context.Context, admin bool, in *pluginPatchInput) (*pluginOutput, error) {
	userID, owner, err := pluginCaller(ctx, admin)
	if err != nil {
		return nil, err
	}
	p, err := plugins.SetEnabled(ctx, s.store.Pool, owner, in.ID, in.Body.Enabled)
	if err != nil {
		return nil, pluginError(err)
	}
	s.audit(ctx, userID, "plugin_update", "plugin", p.ID, map[string]any{"name": p.Name, "enabled": p.Enabled})
	return &pluginOutput{Body: p}, nil
}

func (s *Server) deletePlugin(ctx context.Context, in *pluginIDInput) (*struct{}, error) {
	return nil, s.removePlugin(ctx, false, in.ID)
}

func (s *Server) adminDeletePlugin(ctx context.Context, in *pluginIDInput) (*struct{}, error) {
	return nil, s.removePlugin(ctx, true, in.ID)
}

func (s *Server) removePlugin(ctx context.Context, admin bool, id string) error {
	userID, owner, err := pluginCaller(ctx, admin)
	if err != nil {
		return err
	}
	name, err := plugins.Delete(ctx, s.store.Pool, owner, id)
	if err != nil {
		return pluginError(err)
	}
	s.audit(ctx, userID, "plugin_delete", "plugin", id, map[string]any{"name": name, "builtin": admin})
	return nil
}

func (s *Server) adminCreateSkill(ctx context.Context, in *newSkillInput) (*skillOutput, error) {
	p, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	files, err := uploadedFiles(in.Body.Zip, in.Body.Files)
	if err != nil {
		return nil, skillError(err)
	}
	sk, err := skills.Create(ctx, s.store.Pool, "", files)
	if err != nil {
		return nil, skillError(err)
	}
	s.audit(ctx, p.User.ID, "skill_create", "skill", sk.ID, map[string]any{"name": sk.Name, "builtin": true})
	return &skillOutput{Body: sk}, nil
}

func (s *Server) adminDeleteSkill(ctx context.Context, in *skillIDInput) (*struct{}, error) {
	p, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if err := skills.Delete(ctx, s.store.Pool, "", in.ID); errors.Is(err, skills.ErrNotFound) {
		return nil, fail(http.StatusNotFound, "not_found", "没有这个内置技能（随平台发布的技能不能删除）")
	} else if err != nil {
		return nil, err
	}
	s.audit(ctx, p.User.ID, "skill_delete", "skill", in.ID, map[string]any{"builtin": true})
	return nil, nil
}
