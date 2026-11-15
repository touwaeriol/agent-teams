package roles

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// yamlRole is one role in the threads.md YAML format (type: claude_code | codex).
type yamlRole struct {
	Role            string    `yaml:"role,omitempty"`
	Title           string    `yaml:"title,omitempty"`
	Type            string    `yaml:"type,omitempty"`
	Count           int       `yaml:"count,omitempty"`
	Node            string    `yaml:"node,omitempty"`
	Nodes           []string  `yaml:"nodes,omitempty"`
	Description     string    `yaml:"description,omitempty"`
	Instructions    string    `yaml:"instructions,omitempty"`
	Model           string    `yaml:"model,omitempty"`
	Effort          string    `yaml:"effort,omitempty"`
	PermissionMode  string    `yaml:"permission_mode,omitempty"`
	SkipPermissions bool      `yaml:"skip_permissions,omitempty"` // legacy: true reads as permission_mode: bypass
	CanDelegate     bool      `yaml:"can_delegate,omitempty"`
	ReadOnly        *bool     `yaml:"read_only,omitempty"`
	AllowedTools    []string  `yaml:"allowed_tools,omitempty"`
	DisallowedTools []string  `yaml:"disallowed_tools,omitempty"`
	Sandbox         string    `yaml:"sandbox,omitempty"`
	MaxTurns        *int      `yaml:"max_turns,omitempty"`
	Gate            string    `yaml:"gate,omitempty"`
	TaskKinds       []string  `yaml:"task_kinds,omitempty"`
	Skills          []string  `yaml:"skills,omitempty"`
	Plugins         []string  `yaml:"plugins,omitempty"`
	Look            *yamlLook `yaml:"look,omitempty"`
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

// v1Team is the old platform format: leader + employees.
type v1Team struct {
	Leader    string       `yaml:"leader"`
	Employees []v1Employee `yaml:"employees"`
}

type v1Employee struct {
	Name            string   `yaml:"name"`
	Type            string   `yaml:"type"`
	Node            string   `yaml:"node"`
	Description     string   `yaml:"description"`
	Instructions    string   `yaml:"instructions"`
	Model           string   `yaml:"model"`
	PermissionMode  string   `yaml:"permission_mode"`
	Sandbox         string   `yaml:"sandbox"`
	AllowedTools    []string `yaml:"allowed_tools"`
	DisallowedTools []string `yaml:"disallowed_tools"`
	MaxTurns        *int     `yaml:"max_turns"`
	CanDelegate     bool     `yaml:"can_delegate"`
	ClaudeCommand   string   `yaml:"claude_command"`
	CodexCommand    string   `yaml:"codex_command"`
	ExtraArgs       []string `yaml:"extra_args"`
	Workdir         string   `yaml:"workdir"`
}

// Parsed is the result of reading YAML: roles plus a conversion report.
type Parsed struct {
	Roles  []store.Role
	Report []string // dropped settings, unknown nodes
}

// FromYAML reads either format. nodeIDs maps node names to ids.
func FromYAML(text string, nodeIDs map[string]string) (Parsed, error) {
	var probe map[string]any
	if err := yaml.Unmarshal([]byte(text), &probe); err != nil {
		return Parsed{}, fmt.Errorf("YAML 格式错误：%w", err)
	}
	if probe == nil {
		return Parsed{}, errors.New("团队配置是空的")
	}
	if _, old := probe["employees"]; old {
		return fromV1(text, nodeIDs)
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
	if y.PermissionMode == "" && y.SkipPermissions {
		return store.PermBypass
	}
	return y.PermissionMode
}

func fromYAMLRole(y yamlRole, tier, key string, nodeIDs map[string]string, report *[]string) store.Role {
	r := store.Role{Tier: tier, Role: first(y.Role, key), Title: y.Title, Runtime: runtimeOf(y.Type), Count: y.Count,
		Description: y.Description, Instructions: y.Instructions, Model: y.Model, Effort: y.Effort,
		PermissionMode: yamlMode(y), CanDelegate: y.CanDelegate, ReadOnly: y.ReadOnly,
		AllowedTools: y.AllowedTools, DisallowedTools: y.DisallowedTools, Sandbox: y.Sandbox, MaxTurns: y.MaxTurns,
		Gate: y.Gate, TaskKinds: y.TaskKinds, Skills: y.Skills, Plugins: y.Plugins}
	r.NodeIDs = resolveNodes(append(nonEmpty(y.Node), y.Nodes...), nodeIDs, r.Role, report)
	if y.Look != nil {
		r.Look = store.Look{Color: y.Look.Color, Outfit: y.Look.Outfit, Accessory: y.Look.Accessory, Hair: y.Look.Hair}
	}
	return r
}

func runtimeOf(t string) string {
	if t == "codex" {
		return Codex
	}
	return Claude
}

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

// fromV1 converts leader + employees (data-model.md §3, at_teams row).
func fromV1(text string, nodeIDs map[string]string) (Parsed, error) {
	var team v1Team
	if err := yaml.Unmarshal([]byte(text), &team); err != nil {
		return Parsed{}, fmt.Errorf("YAML 格式错误：%w", err)
	}
	var p Parsed
	leader := findEmployee(team, team.Leader)
	if leader == nil {
		return Parsed{}, fmt.Errorf("leader %q 不在 employees 里", team.Leader)
	}
	lead := v1Role(*leader, Lead, "lead", nodeIDs, &p.Report)
	coord := lead
	coord.Tier, coord.Role, coord.Title, coord.Runtime = Coordinator, "coordinator", "项目统筹", Claude
	coord.PermissionMode, coord.CanDelegate, coord.Sandbox = store.PermAuto, false, ""
	lead.PermissionMode, lead.Title = store.PermAuto, first(lead.Title, "组长")
	p.add(coord)
	p.add(lead)
	taken := map[string]bool{"coordinator": true, "lead": true}
	for _, e := range team.Employees {
		if e.Name == team.Leader {
			continue
		}
		r := v1Role(e, Member, uniqueKey(e.Name, taken), nodeIDs, &p.Report)
		r.Sort = len(p.Roles) - 1
		p.add(r)
	}
	return p, nil
}

func findEmployee(team v1Team, name string) *v1Employee {
	for i := range team.Employees {
		if team.Employees[i].Name == name {
			return &team.Employees[i]
		}
	}
	return nil
}

func v1Role(e v1Employee, tier, key string, nodeIDs map[string]string, report *[]string) store.Role {
	mode := store.PermAuto
	if (e.PermissionMode == "bypassPermissions" || e.Sandbox == "danger-full-access") && tier == Member {
		mode = store.PermBypass
	}
	r := store.Role{Tier: tier, Role: key, Title: e.Name, Runtime: runtimeOf(e.Type), Count: 1,
		Description: e.Description, Instructions: e.Instructions, Model: e.Model, PermissionMode: mode,
		CanDelegate: e.CanDelegate, AllowedTools: e.AllowedTools, DisallowedTools: e.DisallowedTools,
		MaxTurns: e.MaxTurns}
	if r.Runtime == Codex {
		r.Sandbox = e.Sandbox
	}
	r.NodeIDs = resolveNodes(nonEmpty(e.Node), nodeIDs, key, report)
	dropped := droppedFields(e)
	if len(dropped) > 0 {
		*report = append(*report, fmt.Sprintf("%s：v2 不支持 %s，已丢弃", e.Name, strings.Join(dropped, "、")))
	}
	return r
}

func droppedFields(e v1Employee) []string {
	var out []string
	if e.ClaudeCommand != "" && e.ClaudeCommand != "claude" {
		out = append(out, "claude_command")
	}
	if e.CodexCommand != "" && e.CodexCommand != "codex" {
		out = append(out, "codex_command")
	}
	if len(e.ExtraArgs) > 0 {
		out = append(out, "extra_args")
	}
	if e.Workdir != "" {
		out = append(out, "workdir")
	}
	return out
}

var notKey = regexp.MustCompile(`[^a-z0-9-]+`)

// uniqueKey turns a v1 employee name into a role key matching ^[a-z][a-z0-9-]{0,23}$.
func uniqueKey(name string, taken map[string]bool) string {
	key := strings.Trim(notKey.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if key == "" || key[0] < 'a' || key[0] > 'z' {
		key = "r-" + key
	}
	key = strings.TrimRight(truncate(key, 24), "-")
	base := truncate(key, 21)
	for i := 2; taken[key]; i++ {
		key = fmt.Sprintf("%s-%d", base, i)
	}
	taken[key] = true
	return key
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
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

// yamlModeOf leaves the default (auto) out of the YAML.
func yamlModeOf(r store.Role) string {
	if r.PermissionMode == store.PermAuto {
		return ""
	}
	return r.PermissionMode
}

func toYAMLRole(r store.Role, nodeNames map[string]string) yamlRole {
	y := yamlRole{Role: r.Role, Title: r.Title, Type: "claude_code", Count: r.Count, Description: r.Description,
		Instructions: r.Instructions, Model: r.Model, Effort: r.Effort, PermissionMode: yamlModeOf(r),
		CanDelegate: r.CanDelegate, AllowedTools: r.AllowedTools, DisallowedTools: r.DisallowedTools,
		Sandbox: r.Sandbox, MaxTurns: r.MaxTurns, Gate: r.Gate, TaskKinds: r.TaskKinds, Skills: r.Skills, Plugins: r.Plugins,
		Look: &yamlLook{Color: r.Look.Color, Outfit: r.Look.Outfit, Accessory: r.Look.Accessory, Hair: r.Look.Hair}}
	if r.Runtime == Codex {
		y.Type = "codex"
	}
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
