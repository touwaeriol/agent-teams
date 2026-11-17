package httpapi

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/composer"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Model lists (/model, the role model field) and the platform's built-in custom commands.

// ModelsBody is GET /api/models: the platform list in order, then the user's own entries.
type ModelsBody struct {
	Models []composer.Model `json:"models"`
}

type modelsOutput struct{ Body ModelsBody }

type modelsInput struct {
	Body struct {
		Models []composer.Model `json:"models" maxItems:"50"`
	}
}

// CommandsBody is GET /api/commands and PUT /api/admin/commands.
type CommandsBody struct {
	Commands []store.CustomCommand `json:"commands" doc:"Built-in custom commands every role gets (a role's own command of the same name wins; passthrough ones skip Codex roles)"`
	Platform []string              `json:"platform" readOnly:"true" doc:"Names of the platform commands (taken)"`
}

type commandsOutput struct{ Body CommandsBody }

type commandsInput struct {
	Body struct {
		Commands []store.CustomCommand `json:"commands" maxItems:"30"`
	}
}

func (s *Server) registerModels(api huma.API) {
	tags := []string{"team"}
	huma.Register(api, huma.Operation{OperationID: "list-models", Method: "GET", Path: "/api/models", Tags: tags,
		Summary: "Models to choose from: the platform list, then the user's own"}, s.listModels)
	huma.Register(api, huma.Operation{OperationID: "save-my-models", Method: "PUT", Path: "/api/models/mine", Tags: tags,
		Summary: "Replace the user's own model entries (in order)"}, s.saveMyModels)
	huma.Register(api, huma.Operation{OperationID: "builtin-commands", Method: "GET", Path: "/api/commands", Tags: tags,
		Summary: "The platform's built-in custom commands"}, s.builtinCommands)
	admin := []string{"admin"}
	huma.Register(api, huma.Operation{OperationID: "admin-save-models", Method: "PUT", Path: "/api/admin/models", Tags: admin,
		Summary: "Replace the platform model list (order, one default per runtime)"}, s.adminSaveModels)
	huma.Register(api, huma.Operation{OperationID: "admin-save-commands", Method: "PUT", Path: "/api/admin/commands", Tags: admin,
		Summary: "Replace the platform's built-in custom commands"}, s.adminSaveCommands)
}

func (s *Server) listModels(ctx context.Context, _ *struct{}) (*modelsOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	list, err := composer.ListModels(ctx, s.store.Pool, p.User.ID)
	return &modelsOutput{Body: ModelsBody{Models: list}}, err
}

func (s *Server) saveModels(ctx context.Context, userID, owner string, in []composer.Model) (*modelsOutput, error) {
	list := composer.NormalizeModels(in, owner == "")
	if problems := composer.ValidateModels(list); len(problems) > 0 {
		return nil, composerInvalid(problems)
	}
	if err := composer.ReplaceModels(ctx, s.store.Pool, owner, list); err != nil {
		return nil, err
	}
	action := "models_change"
	if owner == "" {
		action = "platform_models_change"
	}
	s.audit(ctx, userID, action, "models", owner, map[string]int{"count": len(list)})
	out, err := composer.ListModels(ctx, s.store.Pool, userID)
	return &modelsOutput{Body: ModelsBody{Models: out}}, err
}

func (s *Server) saveMyModels(ctx context.Context, in *modelsInput) (*modelsOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	return s.saveModels(ctx, p.User.ID, p.User.ID, in.Body.Models)
}

func (s *Server) adminSaveModels(ctx context.Context, in *modelsInput) (*modelsOutput, error) {
	p, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	return s.saveModels(ctx, p.User.ID, "", in.Body.Models)
}

func (s *Server) builtinCommands(ctx context.Context, _ *struct{}) (*commandsOutput, error) {
	if _, err := requireUser(ctx); err != nil {
		return nil, err
	}
	list, err := composer.Builtin(ctx, s.store.Pool)
	return &commandsOutput{Body: CommandsBody{Commands: list, Platform: composer.PlatformCommands}}, err
}

func (s *Server) adminSaveCommands(ctx context.Context, in *commandsInput) (*commandsOutput, error) {
	p, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	list := composer.NormalizeCommands(in.Body.Commands)
	if problems := composer.ValidateCommands("commands", list, composer.ForBuiltin); len(problems) > 0 {
		return nil, composerInvalid(problems)
	}
	if err := s.store.SaveSetting(ctx, composer.BuiltinSetting, composer.BuiltinDoc(list)); err != nil {
		return nil, err
	}
	s.audit(ctx, p.User.ID, "builtin_commands_change", "settings", composer.BuiltinSetting, map[string]int{"count": len(list)})
	return &commandsOutput{Body: CommandsBody{Commands: list, Platform: composer.PlatformCommands}}, nil
}

func composerInvalid(problems []composer.Problem) error {
	fields := make([]FieldError, len(problems))
	for i, p := range problems {
		fields[i] = FieldError{Path: p.Path, Message: p.Message}
	}
	return invalid(fields, problems[0].Message)
}
