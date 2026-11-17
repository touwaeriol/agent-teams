// Package roles holds the role templates (at_roles): validation for
// PUT /api/roles, defaults and the YAML view (threads.md format).
package roles

import (
	"fmt"
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"

	"github.com/touwaeriol/agent-teams/server/internal/composer"
	"github.com/touwaeriol/agent-teams/server/internal/office"
	"github.com/touwaeriol/agent-teams/server/internal/plugins"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Tiers and runtimes.
const (
	Coordinator = "coordinator"
	Lead        = "lead"
	Member      = "member"
	Claude      = "claude"
	Codex       = "codex"
	MaxCount    = 8
)

var (
	keyPattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)
	colorPattern = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
	gates        = []string{"", "review", "decision"}
	permModes    = []string{store.PermBypass, store.PermAsk}
	sandboxes    = []string{"", "read-only", "workspace-write", "danger-full-access"}
)

// Problem is one validation error; Path is like "roles[2].count".
type Problem struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Warning is shown on the Team page for a saved role that cannot run as configured.
type Warning struct {
	Role    string `json:"role"`
	Message string `json:"message"`
}

// Context is what validation needs besides the roles themselves.
type Context struct {
	Nodes map[string]store.Node // the user's nodes by id
	Looks office.Looks          // the current global layout's looks
	// Skills are the skill names the user may give roles (built-in + uploaded); nil skips the check.
	Skills map[string]bool
	// Plugins maps the plugin names the user may tick to their versions; nil skips the check.
	Plugins map[string]map[int]bool
}

// Normalize fills defaults in place: fixed counts, read_only from gate, look from the layout.
func Normalize(roles []store.Role, looks office.Looks) {
	for i := range roles {
		normalizeOne(&roles[i], looks)
	}
}

func normalizeOne(r *store.Role, looks office.Looks) {
	r.Role = strings.TrimSpace(r.Role)
	if r.Runtime == "" {
		r.Runtime = Claude
	}
	if r.Tier != Member {
		r.Count = 1
	}
	if r.Count == 0 {
		r.Count = 1
	}
	if r.PermissionMode == "" || r.PermissionMode == store.PermLegacyAuto || r.Tier != Member {
		r.PermissionMode = store.PermBypass
	}
	if r.ReadOnly == nil {
		ro := r.Gate == "review" || r.Gate == "decision"
		r.ReadOnly = &ro
	}
	r.Look = fillLook(r.Look, defaultLook(looks, r.Role, r.Tier))
	r.NodeIDs = dedupe(r.NodeIDs)
	r.Skills = dedupe(r.Skills)
	r.Plugins = dedupe(r.Plugins)
	r.HiddenCommands = dedupe(r.HiddenCommands)
	r.Commands = composer.NormalizeCommands(r.Commands)
}

func defaultLook(looks office.Looks, role, tier string) office.LookDefault {
	for _, key := range []string{role, tier, "*"} {
		if d, ok := looks.Defaults[key]; ok {
			return d
		}
	}
	return office.LookDefault{Color: "#4C7BD0", Outfit: "tee", Accessory: "none"}
}

func fillLook(l store.Look, d office.LookDefault) store.Look {
	if l.Color == "" {
		l.Color = first(d.Color, "#4C7BD0")
	}
	if l.Outfit == "" {
		l.Outfit = first(d.Outfit, "tee")
	}
	if l.Accessory == "" {
		l.Accessory = first(d.Accessory, "none")
	}
	if l.Hair == nil && d.Hair != nil {
		l.Hair = d.Hair
	}
	return l
}

// Validate checks a normalized set (api.md §3.7 PUT /api/roles).
func Validate(roles []store.Role, c Context) []Problem {
	var out []Problem
	out = append(out, tierCounts(roles)...)
	seen := map[string]bool{}
	for i := range roles {
		path := fmt.Sprintf("roles[%d]", i)
		if seen[roles[i].Role] {
			out = append(out, Problem{path + ".role", "岗位键重复：" + roles[i].Role})
		}
		seen[roles[i].Role] = true
		out = append(out, validateOne(path, &roles[i], c)...)
	}
	return out
}

func tierCounts(roles []store.Role) []Problem {
	count := map[string]int{}
	for _, r := range roles {
		count[r.Tier]++
	}
	var out []Problem
	if count[Coordinator] != 1 {
		out = append(out, Problem{"roles", "需要恰好一个统筹（coordinator）岗位"})
	}
	if count[Lead] != 1 {
		out = append(out, Problem{"roles", "需要恰好一个组长（lead）岗位"})
	}
	return out
}

type check struct {
	field string
	bad   bool
	msg   string
}

func validateOne(path string, r *store.Role, c Context) []Problem {
	checks := []check{
		{"tier", !slices.Contains([]string{Coordinator, Lead, Member}, r.Tier), "tier 只能是 coordinator、lead 或 member"},
		{"role", !keyPattern.MatchString(r.Role), "岗位键只能用小写字母开头的小写字母、数字和 -，最多 24 个字符"},
		{"runtime", r.Runtime != Claude && r.Runtime != Codex, "runtime 只能是 claude 或 codex"},
		{"runtime", r.Tier == Coordinator && r.Runtime != Claude, "统筹只能用 Claude"},
		{"count", r.Count < 1 || r.Count > MaxCount, fmt.Sprintf("人数在 1 到 %d 之间", MaxCount)},
		{"gate", !slices.Contains(gates, r.Gate), "gate 只能是空、review 或 decision"},
		{"sandbox", !slices.Contains(sandboxes, r.Sandbox), "sandbox 只能是 read-only、workspace-write 或 danger-full-access"},
		{"max_turns", r.MaxTurns != nil && *r.MaxTurns < 1, "max_turns 至少为 1"},
		{"permission_mode", !slices.Contains(permModes, r.PermissionMode), "权限模式只能是 bypass 或 ask"},
		{"look.color", !colorPattern.MatchString(r.Look.Color), "颜色格式是 #RRGGBB"},
		{"look.outfit", !slices.Contains(c.Looks.Outfits, r.Look.Outfit), "当前办公室布局里没有这种服装：" + r.Look.Outfit},
		{"look.accessory", !slices.Contains(c.Looks.Accessories, r.Look.Accessory), "当前办公室布局里没有这种配饰：" + r.Look.Accessory},
	}
	var out []Problem
	for _, ch := range checks {
		if ch.bad {
			out = append(out, Problem{path + "." + ch.field, ch.msg})
		}
	}
	out = append(out, skillProblems(path, r, c.Skills)...)
	out = append(out, pluginProblems(path, r, c.Plugins)...)
	out = append(out, commandProblems(path, r)...)
	return append(out, nodeProblems(path, r, c.Nodes)...)
}

func commandProblems(path string, r *store.Role) []Problem {
	var out []Problem
	for _, p := range composer.HiddenProblems(path+".hidden_commands", r.HiddenCommands) {
		out = append(out, Problem(p))
	}
	for _, p := range composer.ValidateCommands(path+".commands", r.Commands, composer.For(r.Runtime, r.Tier)) {
		out = append(out, Problem(p))
	}
	return out
}

// MaxSkills is how many skills one role may load.
const MaxSkills = 16

func skillProblems(path string, r *store.Role, known map[string]bool) []Problem {
	switch {
	case len(r.Skills) == 0:
		return nil
	case r.Tier == Coordinator:
		return []Problem{{path + ".skills", "统筹只能用平台工具，不能加载技能"}}
	case len(r.Skills) > MaxSkills:
		return []Problem{{path + ".skills", fmt.Sprintf("一个岗位最多 %d 个技能", MaxSkills)}}
	}
	for _, name := range r.Skills {
		if known != nil && !known[name] {
			return []Problem{{path + ".skills", "没有这个技能：" + name}}
		}
	}
	return nil
}

func pluginProblems(path string, r *store.Role, choices map[string]map[int]bool) []Problem {
	if len(r.Plugins) == 0 {
		return nil
	}
	if r.Tier == Coordinator {
		return []Problem{{path + ".plugins", "统筹只能用平台工具，不能加载插件"}}
	}
	if msg := plugins.CheckRefs(r.Plugins, choices); msg != "" {
		return []Problem{{path + ".plugins", msg}}
	}
	return nil
}

func nodeProblems(path string, r *store.Role, nodes map[string]store.Node) []Problem {
	for _, id := range r.NodeIDs {
		if _, ok := nodes[id]; !ok {
			return []Problem{{path + ".node_ids", "候选节点不存在或不属于你：" + id}}
		}
	}
	if r.Runtime == Codex && !candidatesQualify(r.NodeIDs, nodes) {
		return []Problem{{path + ".node_ids", "Codex 岗位不能派到 sandbox 或 none 隔离级别的节点（需要 agent_user 或 isolated）"}}
	}
	return nil
}

// Isolated reports whether a node may run Codex employees (architecture.md §10.3).
func Isolated(n store.Node) bool { return n.Isolated || n.Isolation == "agent_user" }

// candidatesQualify: an explicit list must all qualify; an empty list (any node) needs at least one.
func candidatesQualify(ids []string, nodes map[string]store.Node) bool {
	if len(ids) == 0 {
		for _, n := range nodes {
			if Isolated(n) {
				return true
			}
		}
		return false
	}
	for _, id := range ids {
		if !Isolated(nodes[id]) {
			return false
		}
	}
	return true
}

// Warnings lists problems with saved roles that appeared later (deleted nodes, isolation).
func Warnings(roles []store.Role, nodes map[string]store.Node) []Warning {
	out := []Warning{}
	for _, r := range roles {
		out = append(out, roleWarnings(r, nodes)...)
	}
	return out
}

func roleWarnings(r store.Role, nodes map[string]store.Node) []Warning {
	var out []Warning
	var live []string
	for _, id := range r.NodeIDs {
		if _, ok := nodes[id]; ok {
			live = append(live, id)
		}
	}
	if len(live) < len(r.NodeIDs) {
		out = append(out, Warning{r.Role, "候选节点已删除"})
	}
	if r.Runtime == Codex && !candidatesQualify(live, nodes) {
		out = append(out, Warning{r.Role, "Codex 岗位没有 agent_user 或 isolated 节点"})
	}
	return out
}

// Defaults is the starting team for a user with no templates.
func Defaults(looks office.Looks) []store.Role {
	roles := []store.Role{
		{Tier: Coordinator, Role: "coordinator", Title: "项目统筹", Runtime: Claude,
			Description: "和你对话，开讨论串，跟进进度"},
		{Tier: Lead, Role: "lead", Title: "组长", Runtime: Claude,
			Description: "拆任务、审查、合并、开 PR", CanDelegate: true},
		{Tier: Member, Role: "dev", Title: "开发", Runtime: Claude, Count: 2,
			Description: "改代码、跑测试", Sort: 1},
	}
	Normalize(roles, looks)
	return roles
}

// Presets are ready-made member roles the Team page offers ("从预设添加"); the designer
// gets the built-in design and docs skills.
func Presets(looks office.Looks) []store.Role {
	presets := []store.Role{
		{Tier: Member, Role: "designer", Title: "设计师", Runtime: Claude, Count: 1,
			Description: "做 HTML 原型、界面稿、示意图和设计文档，发布成交付物",
			Instructions: "你是设计师。按 design 技能做单文件 HTML 原型或示意图（SVG 内嵌在 HTML 里，不单独发布），按 docs 技能写设计文档；" +
				"做完用 publish_file 发布，收到批注后改稿并用同一个 deliverable_id 发布新版本；改已有的稿（包括别人做的）先 read_deliverable 取回文件，在原处修改后再发布。不要改产品代码，除非任务明确要求。",
			TaskKinds: []string{"design", "doc"}, Skills: []string{"design", "docs"},
			Look: store.Look{Accessory: "glasses"}},
	}
	Normalize(presets, looks)
	return presets
}

// RandomLook picks a look from the layout's palette, outfits and accessories.
func RandomLook(looks office.Looks, rng *rand.Rand) store.Look {
	pick := func(xs []string, fallback string) string {
		if len(xs) == 0 {
			return fallback
		}
		return xs[rng.IntN(len(xs))]
	}
	return store.Look{
		Color:     pick(looks.Colors, "#4C7BD0"),
		Outfit:    pick(looks.Outfits, "tee"),
		Accessory: pick(looks.Accessories, "none"),
	}
}

func dedupe(ids []string) []string {
	out := []string{}
	for _, id := range ids {
		if id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
