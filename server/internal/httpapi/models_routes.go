package httpapi

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/composer"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Model lists (/model, the role model field) and the platform's built-in custom commands.

// ModelsBody is GET /api/models and PUT /api/models/mine: the user's model list in order.
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
		Summary: "The user's model list (a new user starts with Opus 5.5, the Claude default)"}, s.listModels)
	huma.Register(api, huma.Operation{OperationID: "save-my-models", Method: "PUT", Path: "/api/models/mine", Tags: tags,
		Summary: "Replace the user's model list (in order, at least one model, at most one default per runtime)"}, s.saveMyModels)
	huma.Register(api, huma.Operation{OperationID: "builtin-commands", Method: "GET", Path: "/api/commands", Tags: tags,
		Summary: "The platform's built-in custom commands"}, s.builtinCommands)
	admin := []string{"admin"}
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

func (s *Server) saveMyModels(ctx context.Context, in *modelsInput) (*modelsOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	list := composer.NormalizeModels(in.Body.Models)
	if problems := composer.ValidateModels(list); len(problems) > 0 {
		return nil, composerInvalid(problems)
	}
	if err := composer.ReplaceModels(ctx, s.store.Pool, p.User.ID, list); err != nil {
		return nil, err
	}
	s.audit(ctx, p.User.ID, "models_change", "models", p.User.ID, map[string]int{"count": len(list)})
	out, err := composer.ListModels(ctx, s.store.Pool, p.User.ID)
	return &modelsOutput{Body: ModelsBody{Models: out}}, err
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
