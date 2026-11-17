// Package composer holds what the chat composer's "/" offers besides the session's own skills:
// the platform commands the web runs itself, custom commands (per role, and platform built-ins
// the administrators keep) with their validation and server-side expansion, and the model lists
// /model and the role model field choose from.
package composer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// PlatformCommands are the commands the web handles itself (web composer/assist.ts
// PLATFORM_COMMANDS). A role may hide them; custom commands may not take their names.
var PlatformCommands = []string{"model", "effort", "status", "context", "skills", "usage", "memory", "config"}

// BuiltinSetting is the at_settings key of the platform's built-in custom commands.
const BuiltinSetting = "composer_commands"

// Limits of custom commands.
const (
	MaxCommands    = 30
	MaxPromptChars = 8000
	MaxDescChars   = 200
	argsToken      = "{{args}}"
)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}(:[a-z0-9][a-z0-9_-]{0,31})?$`)

// Problem is one validation error; Path is relative to the list ("commands[1].name").
type Problem struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// NormalizeCommands trims every field and fills the default mode (prompt).
func NormalizeCommands(list []store.CustomCommand) []store.CustomCommand {
	out := make([]store.CustomCommand, 0, len(list))
	for _, c := range list {
		c.Name = strings.TrimPrefix(strings.TrimSpace(c.Name), "/")
		c.Description = strings.TrimSpace(c.Description)
		c.Prompt = strings.TrimSpace(c.Prompt)
		if c.Mode == "" {
			c.Mode = store.CommandPrompt
		}
		if c.Mode == store.CommandPassthrough {
			c.Prompt = ""
		}
		out = append(out, c)
	}
	return out
}

// Where a command list belongs, for the passthrough rule.
const (
	ForBuiltin = ""       // platform built-ins: both modes (passthrough ones skip prompt-only employees)
	ForClaude  = "claude" // a Claude Code coordinator or lead: both modes
	ForCodex   = "codex"  // Codex has no slash commands over app-server: prompt only
	ForMember  = "member" // members get commands inside platform messages: prompt only
)

// PromptOnly reports whether an employee can run prompt commands only: Codex (no slash commands
// over app-server) and members (their messages arrive wrapped by the platform, so "/name"
// would not start the text Claude Code sees).
func PromptOnly(runtime, tier string) bool { return runtime == "codex" || tier == "member" }

// For is the list kind of a role.
func For(runtime, tier string) string {
	switch {
	case runtime == "codex":
		return ForCodex
	case tier == "member":
		return ForMember
	}
	return ForClaude
}

// ValidateCommands checks a normalized list of one kind (For*).
func ValidateCommands(path string, list []store.CustomCommand, kind string) []Problem {
	if len(list) > MaxCommands {
		return []Problem{{path, fmt.Sprintf("最多 %d 个自定义命令", MaxCommands)}}
	}
	var out []Problem
	seen := map[string]bool{}
	for i, c := range list {
		p := fmt.Sprintf("%s[%d]", path, i)
		if seen[c.Name] {
			out = append(out, Problem{p + ".name", "命令重名：/" + c.Name})
		}
		seen[c.Name] = true
		out = append(out, commandProblems(p, c, kind)...)
	}
	return out
}

func commandProblems(p string, c store.CustomCommand, kind string) []Problem {
	checks := []struct {
		field string
		bad   bool
		msg   string
	}{
		{"name", !namePattern.MatchString(c.Name), "命令名用小写字母开头的小写字母、数字、- 和 _，最多 32 个字符（插件命令可写 插件:命令）"},
		{"name", slices.Contains(PlatformCommands, c.Name), "/" + c.Name + " 是平台命令，换个名字"},
		{"mode", c.Mode != store.CommandPrompt && c.Mode != store.CommandPassthrough, "方式只能是 prompt（提示词模板）或 passthrough（透传给 Claude Code）"},
		{"mode", kind == ForCodex && c.Mode == store.CommandPassthrough, "Codex 没有斜杠命令，只能用提示词模板"},
		{"mode", kind == ForMember && c.Mode == store.CommandPassthrough, "成员收到的是平台转达的消息，只能用提示词模板"},
		{"prompt", c.Mode == store.CommandPrompt && c.Prompt == "", "提示词模板不能为空"},
		{"prompt", c.Mode == store.CommandPrompt && strings.HasPrefix(c.Prompt, "/"), "提示词模板不能以 / 开头（要运行斜杠命令请用透传）"},
		{"prompt", len([]rune(c.Prompt)) > MaxPromptChars, fmt.Sprintf("提示词模板最多 %d 字", MaxPromptChars)},
		{"description", len([]rune(c.Description)) > MaxDescChars, fmt.Sprintf("说明最多 %d 字", MaxDescChars)},
	}
	var out []Problem
	for _, ch := range checks {
		if ch.bad {
			out = append(out, Problem{p + "." + ch.field, ch.msg})
		}
	}
	return out
}

// HiddenProblems checks a role's hidden platform commands.
func HiddenProblems(path string, hidden []string) []Problem {
	for _, h := range hidden {
		if !slices.Contains(PlatformCommands, h) {
			return []Problem{{path, "没有这个平台命令：/" + h}}
		}
	}
	return nil
}

var slashPattern = regexp.MustCompile(`^/([A-Za-z0-9_:.-]+)(?:\s+|$)`)

// Split reads "/name args" at the start of a message; ok is false when it does not start with a command.
func Split(content string) (name, args string, ok bool) {
	m := slashPattern.FindStringSubmatchIndex(content)
	if m == nil {
		return "", "", false
	}
	return content[m[2]:m[3]], strings.TrimSpace(content[m[1]:]), true
}

// Expand fills a prompt template: every {{args}} becomes the arguments, literally (one pass, so
// arguments that contain {{args}} are not expanded again). A template without the placeholder
// gets the arguments appended after a blank line.
func Expand(prompt, args string) string {
	if strings.Contains(prompt, argsToken) {
		return strings.ReplaceAll(prompt, argsToken, args)
	}
	if args == "" {
		return prompt
	}
	return prompt + "\n\n" + args
}

// Resolved is a custom command as an employee's chat offers it.
type Resolved struct {
	store.CustomCommand
	Source string `json:"source" enum:"role,platform" doc:"role: the employee's role; platform: a built-in every role gets"`
}

// Merge is the employee's custom commands: the role's own first, then the platform built-ins
// whose names the role does not use. Passthrough built-ins are left out for prompt-only employees.
func Merge(role, builtin []store.CustomCommand, promptOnly bool) []Resolved {
	out := make([]Resolved, 0, len(role)+len(builtin))
	seen := map[string]bool{}
	for _, c := range role {
		seen[c.Name] = true
		out = append(out, Resolved{c, "role"})
	}
	for _, c := range builtin {
		if seen[c.Name] || (promptOnly && c.Mode == store.CommandPassthrough) {
			continue
		}
		out = append(out, Resolved{c, "platform"})
	}
	return out
}

// Find is the custom command a message names, if any.
func Find(list []Resolved, name string) (Resolved, bool) {
	for _, c := range list {
		if c.Name == name {
			return c, true
		}
	}
	return Resolved{}, false
}

type builtinDoc struct {
	Commands []store.CustomCommand `json:"commands"`
}

// Builtin is the platform's built-in custom commands (at_settings.composer_commands).
func Builtin(ctx context.Context, db store.DB) ([]store.CustomCommand, error) {
	var raw []byte
	err := store.QueryRow(ctx, db, `SELECT value FROM at_settings WHERE key = $1`, BuiltinSetting).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, store.ErrNotFound) {
		return []store.CustomCommand{}, nil
	}
	var doc builtinDoc
	if err == nil {
		err = json.Unmarshal(raw, &doc)
	}
	if err != nil {
		return nil, err
	}
	if doc.Commands == nil {
		doc.Commands = []store.CustomCommand{}
	}
	return doc.Commands, nil
}

// BuiltinDoc wraps a list for SaveSetting.
func BuiltinDoc(list []store.CustomCommand) any { return builtinDoc{Commands: list} }

// RoleOf is the employee's role template as saved now (so command edits reach running threads),
// else the snapshot the employee was created from.
func RoleOf(ctx context.Context, db store.DB, e core.Employee) store.Role {
	if e.RoleID == nil {
		return e.Spec
	}
	var r store.Role
	if err := db.Gorm(ctx).Where("id = ?", *e.RoleID).Take(&r).Error; err != nil {
		return e.Spec
	}
	return r
}

// EmployeeCommands is the employee's custom commands: its role's, then the platform built-ins.
func EmployeeCommands(ctx context.Context, db store.DB, e core.Employee, role store.Role) ([]Resolved, error) {
	builtin, err := Builtin(ctx, db)
	if err != nil {
		return nil, err
	}
	return Merge(role.Commands, builtin, PromptOnly(e.Runtime, e.Tier)), nil
}

// ExpandFor reads a message to the employee that names one of its custom commands ("/name
// args"): a prompt command gives the text to deliver instead (deliver), a passthrough command
// goes as typed (deliver ""). meta describes the command for the stored message; nil when the
// message names no custom command.
func ExpandFor(ctx context.Context, db store.DB, e core.Employee, content string) (deliver string, meta map[string]any, err error) {
	name, args, ok := Split(content)
	if !ok {
		return "", nil, nil
	}
	list, err := EmployeeCommands(ctx, db, e, RoleOf(ctx, db, e))
	if err != nil {
		return "", nil, err
	}
	cmd, found := Find(list, name)
	if !found {
		return "", nil, nil
	}
	meta = map[string]any{"name": cmd.Name, "mode": cmd.Mode, "source": cmd.Source}
	if cmd.Mode != store.CommandPrompt {
		return "", meta, nil
	}
	return Expand(cmd.Prompt, args), meta, nil
}

// Names lists the command names ("/a /b") for a line of text; "" when there are none.
func Names(list []Resolved) string {
	parts := make([]string, len(list))
	for i, c := range list {
		parts[i] = "/" + c.Name
	}
	return strings.Join(parts, " ")
}
