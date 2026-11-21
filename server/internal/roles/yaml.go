package roles

import (
	"bytes"
	"errors"
	"fmt"

	"go.yaml.in/yaml/v3"

	"github.com/touwaeriol/agent-teams/server/internal/runtimes"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// yamlRole is one role in the threads.md YAML format (type: claude_code | codex).
type yamlRole struct {
	Role            string        `yaml:"role,omitempty"`
	Title           string        `yaml:"title,omitempty"`
	Type            string        `yaml:"type,omitempty"`
	Count           int           `yaml:"count,omitempty"`
	Node            string        `yaml:"node,omitempty"`
	Nodes           []string      `yaml:"nodes,omitempty"`
	Description     string        `yaml:"description,omitempty"`
	Instructions    string        `yaml:"instructions,omitempty"`
	Model           string        `yaml:"model,omitempty"`
	Effort          string        `yaml:"effort,omitempty"`
	PermissionMode  string        `yaml:"permission_mode,omitempty"`
	SkipPermissions bool          `yaml:"skip_permissions,omitempty"` // legacy: true reads as permission_mode: bypass
	CanDelegate     bool          `yaml:"can_delegate,omitempty"`
	ReadOnly        *bool         `yaml:"read_only,omitempty"`
	AllowedTools    []string      `yaml:"allowed_tools,omitempty"`
	DisallowedTools []string      `yaml:"disallowed_tools,omitempty"`
	Sandbox         string        `yaml:"sandbox,omitempty"`
	MaxTurns        *int          `yaml:"max_turns,omitempty"`
	Gate            string        `yaml:"gate,omitempty"`
	TaskKinds       []string      `yaml:"task_kinds,omitempty"`
	Skills          []string      `yaml:"skills,omitempty"`
	Plugins         []string      `yaml:"plugins,omitempty"`
	HiddenCommands  []string      `yaml:"hidden_commands,omitempty"`
	Commands        []yamlCommand `yaml:"commands,omitempty"`
	Look            *yamlLook     `yaml:"look,omitempty"`
	Context         *yamlContext  `yaml:"context,omitempty"`
}

type yamlCommand struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
	Mode        string `yaml:"mode,omitempty"`
	Prompt      string `yaml:"prompt,omitempty"`
}

func commandsFromYAML(list []yamlCommand) []store.CustomCommand {
	out := make([]store.CustomCommand, len(list))
	for i, c := range list {
		out[i] = store.CustomCommand(c)
	}
	return out
}

func commandsToYAML(list []store.CustomCommand) []yamlCommand {
	var out []yamlCommand
	for _, c := range list {
		out = append(out, yamlCommand(c))
	}
	return out
}

type yamlLook struct {
	Color     string  `yaml:"color,omitempty"`
	Outfit    string  `yaml:"outfit,omitempty"`
	Accessory string  `yaml:"accessory,omitempty"`
	Hair      *string `yaml:"hair,omitempty"`
}

type yamlTeam struct {
	Coordinator *yamlRole  `yaml:"coordinator,omitempty"`
	Lead        *yamlRole  `yaml:"lead,omitempty"`
	Members     []yamlRole `yaml:"members"`
}

// Parsed is the result of reading YAML: roles plus a conversion report.
type Parsed struct {
	Roles  []store.Role
	Report []string // dropped settings, unknown nodes
}

// FromYAML reads the coordinator/lead/members format. nodeIDs maps node names to ids.
func FromYAML(text string, nodeIDs map[string]string) (Parsed, error) {
	var probe map[string]any
	if err := yaml.Unmarshal([]byte(text), &probe); err != nil {
		return Parsed{}, fmt.Errorf("YAML 格式错误：%w", err)
	}
	if probe == nil {
		return Parsed{}, errors.New("团队配置是空的")
	}
	if _, old := probe["employees"]; old {
		return Parsed{}, errors.New("旧平台的 leader/employees 格式已不再支持，请改用 coordinator/lead/members")
	}
	return fromNew(text, nodeIDs)
}

func fromNew(text string, nodeIDs map[string]string) (Parsed, error) {
	var team yamlTeam
	dec := yaml.NewDecoder(bytes.NewReader([]byte(text)))
	dec.KnownFields(true)
	if err := dec.Decode(&team); err != nil {
		return Parsed{}, fmt.Errorf("YAML 格式错误：%w", err)
	}
	var p Parsed
	if team.Coordinator != nil {
		p.add(fromYAMLRole(*team.Coordinator, Coordinator, "coordinator", nodeIDs, &p.Report))
	}
	if team.Lead != nil {
		p.add(fromYAMLRole(*team.Lead, Lead, "lead", nodeIDs, &p.Report))
	}
	for i, m := range team.Members {
		r := fromYAMLRole(m, Member, m.Role, nodeIDs, &p.Report)
		r.Sort = i + 1
		p.add(r)
	}
	return p, nil
}

func (p *Parsed) add(r store.Role) { p.Roles = append(p.Roles, r) }

// yamlMode reads permission_mode, or the legacy skip_permissions flag.
func yamlMode(y yamlRole) string {
	if y.PermissionMode == "" || y.PermissionMode == store.PermLegacyAuto || y.SkipPermissions {
		return store.PermBypass
	}
	return y.PermissionMode
}

func fromYAMLRole(y yamlRole, tier, key string, nodeIDs map[string]string, report *[]string) store.Role {
	r := store.Role{Tier: tier, Role: first(y.Role, key), Title: y.Title, Runtime: runtimeOf(y.Type), Count: y.Count,
		Description: y.Description, Instructions: y.Instructions, Model: y.Model, Effort: y.Effort,
		PermissionMode: yamlMode(y), CanDelegate: y.CanDelegate, ReadOnly: y.ReadOnly,
		AllowedTools: y.AllowedTools, DisallowedTools: y.DisallowedTools, Sandbox: y.Sandbox, MaxTurns: y.MaxTurns,
		Gate: y.Gate, TaskKinds: y.TaskKinds, Skills: y.Skills, Plugins: y.Plugins,
		HiddenCommands: y.HiddenCommands, Commands: commandsFromYAML(y.Commands)}
	contextFromYAML(y.Context, tier, y.Gate).apply(&r)
	r.NodeIDs = resolveNodes(append(nonEmpty(y.Node), y.Nodes...), nodeIDs, r.Role, report)
	if y.Look != nil {
		r.Look = store.Look{Color: y.Look.Color, Outfit: y.Look.Outfit, Accessory: y.Look.Accessory, Hair: y.Look.Hair}
	}
	return r
}

func runtimeOf(t string) string { return runtimes.FromYAMLType(t) }

func resolveNodes(names []string, nodeIDs map[string]string, role string, report *[]string) []string {
	ids := []string{}
	for _, name := range names {
		if id, ok := nodeIDs[name]; ok {
			ids = append(ids, id)
			continue
		}
		*report = append(*report, fmt.Sprintf("%s：没有叫 %s 的节点，已忽略", role, name))
	}
	return ids
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

// ToYAML renders roles in the threads.md format. nodeNames maps ids to names.
func ToYAML(roles []store.Role, nodeNames map[string]string) (string, error) {
	team := yamlTeam{Members: []yamlRole{}}
	for _, r := range roles {
		y := toYAMLRole(r, nodeNames)
		switch r.Tier {
		case Coordinator:
			y.Role, y.Count = "", 0
			team.Coordinator = &y
		case Lead:
			y.Role, y.Count = "", 0
			team.Lead = &y
		default:
			team.Members = append(team.Members, y)
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(team); err != nil {
		return "", err
	}
	return buf.String(), enc.Close()
}

// yamlModeOf leaves the default (bypass) out of the YAML.
func yamlModeOf(r store.Role) string {
	if r.PermissionMode == store.PermBypass {
		return ""
	}
	return r.PermissionMode
}

func toYAMLRole(r store.Role, nodeNames map[string]string) yamlRole {
	y := yamlRole{Role: r.Role, Title: r.Title, Type: runtimes.Of(r.Runtime).YAMLType, Count: r.Count, Description: r.Description,
		Instructions: r.Instructions, Model: r.Model, Effort: r.Effort, PermissionMode: yamlModeOf(r),
		CanDelegate: r.CanDelegate, AllowedTools: r.AllowedTools, DisallowedTools: r.DisallowedTools,
		Sandbox: r.Sandbox, MaxTurns: r.MaxTurns, Gate: r.Gate, TaskKinds: r.TaskKinds, Skills: r.Skills, Plugins: r.Plugins,
		HiddenCommands: r.HiddenCommands, Commands: commandsToYAML(r.Commands),
		Look:    &yamlLook{Color: r.Look.Color, Outfit: r.Look.Outfit, Accessory: r.Look.Accessory, Hair: r.Look.Hair},
		Context: contextToYAML(r)}
	if r.ReadOnly != nil && *r.ReadOnly != (r.Gate == "review" || r.Gate == "decision") {
		y.ReadOnly = r.ReadOnly
	}
	for _, id := range r.NodeIDs {
		if name, ok := nodeNames[id]; ok {
			y.Nodes = append(y.Nodes, name)
		}
	}
	return y
}
