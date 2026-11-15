package httpapi

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"go.yaml.in/yaml/v3"

	"github.com/touwaeriol/agent-teams/server/internal/office"
	"github.com/touwaeriol/agent-teams/server/internal/plugins"
	"github.com/touwaeriol/agent-teams/server/internal/roles"
	"github.com/touwaeriol/agent-teams/server/internal/skills"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// RolesBody is GET/PUT /api/roles.
type RolesBody struct {
	Roles    []store.Role    `json:"roles"`
	Warnings []roles.Warning `json:"warnings"`
	Report   []string        `json:"report,omitempty" doc:"Conversion notes (dropped v1 settings, unknown nodes)"`
}

type rolesOutput struct{ Body RolesBody }

type rolesInput struct {
	Body struct {
		Roles []store.Role `json:"roles" maxItems:"40"`
	}
}

type randomLookInput struct {
	Body struct {
		Role string `json:"role,omitempty" maxLength:"24"`
	}
}

type lookOutput struct{ Body store.Look }

type yamlOutput struct {
	Body struct {
		YAML string `json:"yaml"`
	}
}

type yamlInput struct {
	Body struct {
		YAML string `json:"yaml" maxLength:"200000"`
	}
}

// TeamV1 is the v1 /api/team shape.
type TeamV1 struct {
	Config string         `json:"config"`
	Team   map[string]any `json:"team"`
	Saved  bool           `json:"saved"`
	Error  string         `json:"error"`
}

type teamOutput struct{ Body TeamV1 }

type teamInput struct {
	Body struct {
		Config string         `json:"config,omitempty" maxLength:"200000"`
		Team   map[string]any `json:"team,omitempty" doc:"Structured form; wins over config"`
	}
}

func (s *Server) registerRoles(api huma.API) {
	tags := []string{"team"}
	huma.Register(api, huma.Operation{OperationID: "get-roles", Method: "GET", Path: "/api/roles", Tags: tags,
		Summary: "Role templates (converted from the v1 team on first use)"}, s.getRoles)
	huma.Register(api, huma.Operation{OperationID: "save-roles", Method: "PUT", Path: "/api/roles", Tags: tags,
		Summary: "Save the whole set (matched by id; missing ones are deleted)"}, s.saveRoles)
	huma.Register(api, huma.Operation{OperationID: "random-look", Method: "POST", Path: "/api/roles/look/random", Tags: tags}, s.randomLook)
	huma.Register(api, huma.Operation{OperationID: "get-roles-yaml", Method: "GET", Path: "/api/roles/yaml", Tags: tags}, s.getRolesYAML)
	huma.Register(api, huma.Operation{OperationID: "save-roles-yaml", Method: "PUT", Path: "/api/roles/yaml", Tags: tags}, s.saveRolesYAML)
	huma.Register(api, huma.Operation{OperationID: "role-presets", Method: "GET", Path: "/api/roles/presets", Tags: tags,
		Summary: "Ready-made member roles (designer …) the Team page offers"}, s.rolePresets)
	s.registerSkills(api)
	s.registerPlugins(api)
	huma.Register(api, huma.Operation{OperationID: "get-team", Method: "GET", Path: "/api/team", Tags: tags, Summary: "v1 compatible YAML view"}, s.getTeam)
	huma.Register(api, huma.Operation{OperationID: "save-team", Method: "PUT", Path: "/api/team", Tags: tags, Summary: "v1 compatible; same as PUT /api/roles/yaml"}, s.saveTeam)
}

func (s *Server) currentLooks(ctx context.Context) office.Looks {
	layout, err := s.office.Get(s.officeDefault(ctx))
	if err != nil {
		layout, _ = s.office.Get(office.DefaultID)
	}
	if layout == nil {
		return office.Looks{}
	}
	return office.LooksOf(layout.Doc)
}

func (s *Server) nodeMaps(ctx context.Context, userID string) (map[string]store.Node, map[string]string, error) {
	nodes, err := s.store.ListNodes(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	byID := map[string]store.Node{}
	byName := map[string]string{}
	for _, n := range nodes {
		byID[n.ID] = n
		byName[n.Name] = n.ID
	}
	return byID, byName, nil
}

// ensureRoles returns the user's templates, creating them from at_teams (or defaults) on first use.
func (s *Server) ensureRoles(ctx context.Context, userID string) ([]store.Role, []string, error) {
	existing, err := s.store.Roles(ctx, userID)
	if err != nil || len(existing) > 0 {
		return existing, nil, err
	}
	seed, report, err := s.seedRoles(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	err = s.store.Tx(ctx, func(tx *store.Store) error { return tx.ReplaceRoles(ctx, userID, seed) })
	if err != nil && !errors.Is(err, store.ErrConflict) {
		return nil, nil, err
	}
	saved, err := s.store.Roles(ctx, userID)
	return saved, report, err
}

func (s *Server) seedRoles(ctx context.Context, userID string) ([]store.Role, []string, error) {
	looks := s.currentLooks(ctx)
	config, found, err := s.store.Team(ctx, userID)
	if err != nil || !found {
		return roles.Defaults(looks), nil, err
	}
	_, byName, err := s.nodeMaps(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	parsed, err := roles.FromYAML(config, byName)
	if err != nil || len(parsed.Roles) == 0 {
		return roles.Defaults(looks), []string{"旧团队配置无法转换，已使用默认岗位"}, nil
	}
	roles.Normalize(parsed.Roles, looks)
	return parsed.Roles, parsed.Report, nil
}

func (s *Server) rolesBody(ctx context.Context, userID string, list []store.Role, report []string) (RolesBody, error) {
	byID, _, err := s.nodeMaps(ctx, userID)
	if err != nil {
		return RolesBody{}, err
	}
	return RolesBody{Roles: list, Warnings: roles.Warnings(list, byID), Report: report}, nil
}

func (s *Server) getRoles(ctx context.Context, _ *struct{}) (*rolesOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	list, report, err := s.ensureRoles(ctx, p.User.ID)
	if err != nil {
		return nil, err
	}
	body, err := s.rolesBody(ctx, p.User.ID, list, report)
	return &rolesOutput{Body: body}, err
}

func (s *Server) saveRoles(ctx context.Context, in *rolesInput) (*rolesOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.replaceRoles(ctx, p.User.ID, in.Body.Roles); err != nil {
		return nil, err
	}
	return s.getRoles(ctx, nil)
}

// replaceRoles validates and saves a full set; unknown ids are treated as new rows.
func (s *Server) replaceRoles(ctx context.Context, userID string, list []store.Role) error {
	current, err := s.store.Roles(ctx, userID)
	if err != nil {
		return err
	}
	byID, _, err := s.nodeMaps(ctx, userID)
	if err != nil {
		return err
	}
	known := map[string]store.Role{}
	for _, r := range current {
		known[r.ID] = r
	}
	for i := range list {
		if _, ok := known[list[i].ID]; !ok {
			list[i].ID = ""
		}
		list[i].Sort = i // display order is the order of the saved list
	}
	looks := s.currentLooks(ctx)
	roles.Normalize(list, looks)
	skillNames, err := skills.Names(ctx, s.store.Pool, userID)
	if err != nil {
		return err
	}
	pluginChoices, err := plugins.Choices(ctx, s.store.Pool, userID)
	if err != nil {
		return err
	}
	if problems := roles.Validate(list, roles.Context{Nodes: byID, Looks: looks, Skills: skillNames, Plugins: pluginChoices}); len(problems) > 0 {
		return rolesInvalid(problems)
	}
	if err := s.store.Tx(ctx, func(tx *store.Store) error { return tx.ReplaceRoles(ctx, userID, list) }); err != nil {
		return err
	}
	s.auditBypass(ctx, userID, known, list)
	return nil
}

func rolesInvalid(problems []roles.Problem) error {
	fields := make([]FieldError, len(problems))
	for i, p := range problems {
		fields[i] = FieldError{Path: p.Path, Message: p.Message}
	}
	return invalid(fields, problems[0].Message)
}

// auditBypass records every role switched to bypass (skip_permissions_on, the action name of v2.0).
func (s *Server) auditBypass(ctx context.Context, userID string, before map[string]store.Role, after []store.Role) {
	for _, r := range after {
		if r.PermissionMode == store.PermBypass && before[r.ID].PermissionMode != store.PermBypass {
			s.audit(ctx, userID, "skip_permissions_on", "role", r.ID, map[string]string{"role": r.Role})
		}
	}
}

type presetsOutput struct {
	Body struct {
		Roles []store.Role `json:"roles"`
	}
}

func (s *Server) rolePresets(ctx context.Context, _ *struct{}) (*presetsOutput, error) {
	if _, err := requireUser(ctx); err != nil {
		return nil, err
	}
	out := &presetsOutput{}
	out.Body.Roles = roles.Presets(s.currentLooks(ctx))
	return out, nil
}

func (s *Server) randomLook(ctx context.Context, _ *randomLookInput) (*lookOutput, error) {
	if _, err := requireUser(ctx); err != nil {
		return nil, err
	}
	return &lookOutput{Body: roles.RandomLook(s.currentLooks(ctx), rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())))}, nil
}

func (s *Server) rolesYAML(ctx context.Context, userID string) (string, error) {
	list, _, err := s.ensureRoles(ctx, userID)
	if err != nil {
		return "", err
	}
	byID, _, err := s.nodeMaps(ctx, userID)
	if err != nil {
		return "", err
	}
	names := map[string]string{}
	for id, n := range byID {
		names[id] = n.Name
	}
	return roles.ToYAML(list, names)
}

func (s *Server) getRolesYAML(ctx context.Context, _ *struct{}) (*yamlOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	text, err := s.rolesYAML(ctx, p.User.ID)
	out := &yamlOutput{}
	out.Body.YAML = text
	return out, err
}

func (s *Server) saveYAML(ctx context.Context, userID, text string) ([]string, error) {
	_, byName, err := s.nodeMaps(ctx, userID)
	if err != nil {
		return nil, err
	}
	parsed, err := roles.FromYAML(text, byName)
	if err != nil {
		return nil, fail(http.StatusUnprocessableEntity, "validation", err.Error())
	}
	current, err := s.store.Roles(ctx, userID)
	if err != nil {
		return nil, err
	}
	keepIDs(parsed.Roles, current)
	return parsed.Report, s.replaceRoles(ctx, userID, parsed.Roles)
}

// keepIDs matches YAML roles to saved ones by role key so links survive a YAML save.
func keepIDs(next, current []store.Role) {
	byKey := map[string]string{}
	for _, r := range current {
		byKey[r.Role] = r.ID
	}
	for i := range next {
		next[i].ID = byKey[next[i].Role]
	}
}

func (s *Server) saveRolesYAML(ctx context.Context, in *yamlInput) (*rolesOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	report, err := s.saveYAML(ctx, p.User.ID, in.Body.YAML)
	if err != nil {
		return nil, err
	}
	out, err := s.getRoles(ctx, nil)
	if out != nil {
		out.Body.Report = report
	}
	return out, err
}

func (s *Server) getTeam(ctx context.Context, _ *struct{}) (*teamOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	text, err := s.rolesYAML(ctx, p.User.ID)
	if err != nil {
		return nil, err
	}
	return &teamOutput{Body: TeamV1{Config: text, Team: structured(text), Saved: true}}, nil
}

func (s *Server) saveTeam(ctx context.Context, in *teamInput) (*teamOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	text := in.Body.Config
	if in.Body.Team != nil {
		raw, err := yaml.Marshal(in.Body.Team)
		if err != nil {
			return nil, fail(http.StatusUnprocessableEntity, "validation", err.Error())
		}
		text = string(raw)
	}
	if strings.TrimSpace(text) == "" {
		return nil, fail(http.StatusUnprocessableEntity, "validation", "团队配置是空的")
	}
	if _, err := s.saveYAML(ctx, p.User.ID, text); err != nil {
		return nil, err
	}
	return s.getTeam(ctx, nil)
}

func structured(text string) map[string]any {
	var out map[string]any
	if yaml.Unmarshal([]byte(text), &out) != nil {
		return nil
	}
	return out
}
