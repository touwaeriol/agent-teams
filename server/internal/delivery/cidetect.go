package delivery

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/ghops"
)

// Workflow is one .github/workflows file and its push branch filters.
type Workflow struct {
	Path     string   `json:"path"`
	Triggers []string `json:"triggers"`
	Push     []string `json:"push_branches"`
}

// Ruleset is the GitHub ruleset advice of the settings page.
type Ruleset struct {
	Present     bool           `json:"present"`
	Recommended map[string]any `json:"recommended"`
}

// Detection is GET /api/projects/{pid}/ci/detect (one repository of the project).
type Detection struct {
	Repo                   string     `json:"repo"`
	Branch                 string     `json:"branch" doc:"The repository's default branch the workflows were read from"`
	Workflows              []Workflow `json:"workflows"`
	CoversThreadBranches   bool       `json:"covers_thread_branches"`
	CoversMQBranches       bool       `json:"covers_mq_branches"`
	AppHasChecksPermission bool       `json:"app_has_checks_permission"`
	Ruleset                Ruleset    `json:"ruleset"`
}

// ErrNoWorkflow means there is no workflow with a push trigger to extend.
var ErrNoWorkflow = errors.New("仓库里没有带 push 触发的 workflow，无法自动加触发条件")

// ciRepo picks the repository a CI request is about ("" = the project's first one) and its default branch.
func (e *Engine) ciRepo(ctx context.Context, projectID, slug string) (ghops.Repo, string, error) {
	_, repos, err := e.reposOf(ctx, projectID)
	if err != nil {
		return ghops.Repo{}, "", err
	}
	repo := repos[0]
	if slug != "" {
		var ok bool
		if repo, ok = ghops.Find(repos, slug); !ok {
			return repo, "", fmt.Errorf("%w：%s", ErrNoRepo, slug)
		}
	}
	base, err := e.gh.DefaultBranch(ctx, repo)
	return repo, base, err
}

// DetectCI inspects a repository's workflows on its default branch (§8.3) and its rulesets (§8.1).
func (e *Engine) DetectCI(ctx context.Context, projectID, slug string) (Detection, error) {
	repo, base, err := e.ciRepo(ctx, projectID, slug)
	if err != nil {
		return Detection{}, err
	}
	files, err := e.gh.Workflows(ctx, repo, base)
	if err != nil {
		return Detection{}, err
	}
	d := Detection{Repo: repo.Slug, Branch: base, Workflows: []Workflow{}, AppHasChecksPermission: repo.HasChecks()}
	for _, path := range sortedKeys(files) {
		w := parseWorkflow(path, files[path])
		d.Workflows = append(d.Workflows, w)
		d.CoversThreadBranches = d.CoversThreadBranches || covers(w, "thread/12-x", "thread/12/dev-1")
		d.CoversMQBranches = d.CoversMQBranches || covers(w, "mq/12/mq_x")
	}
	present, err := e.gh.Rulesets(ctx, repo)
	if err != nil {
		return d, err
	}
	d.Ruleset = Ruleset{Present: present, Recommended: recommendedRuleset(base)}
	return d, nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// parseWorkflow reads the "on" triggers and push branch filters of a workflow.
func parseWorkflow(path, text string) Workflow {
	w := Workflow{Path: path, Triggers: []string{}, Push: []string{}}
	var doc map[string]any
	if yaml.Unmarshal([]byte(text), &doc) != nil {
		return w
	}
	switch v := doc["on"].(type) {
	case string:
		w.Triggers = []string{v}
	case []any:
		for _, t := range v {
			w.Triggers = append(w.Triggers, fmt.Sprint(t))
		}
	case map[string]any:
		w.Triggers, w.Push = triggersOf(v)
	}
	sort.Strings(w.Triggers)
	return w
}

func triggersOf(on map[string]any) ([]string, []string) {
	triggers, push := []string{}, []string{}
	for k, v := range on {
		triggers = append(triggers, k)
		if k != "push" {
			continue
		}
		cfg, _ := v.(map[string]any)
		list, _ := cfg["branches"].([]any)
		for _, b := range list {
			push = append(push, fmt.Sprint(b))
		}
	}
	return triggers, push
}

// covers reports whether a push of every sample branch runs the workflow.
func covers(w Workflow, samples ...string) bool {
	if !slices.Contains(w.Triggers, "push") {
		return false
	}
	if len(w.Push) == 0 {
		return true
	}
	for _, s := range samples {
		if !slices.ContainsFunc(w.Push, func(p string) bool { return globMatch(p, s) }) {
			return false
		}
	}
	return true
}

// globMatch implements GitHub's branch filter globs: * (no slash) and ** (anything).
func globMatch(pattern, name string) bool {
	if pattern == "" {
		return name == ""
	}
	if strings.HasPrefix(pattern, "**") {
		rest := pattern[2:]
		for i := 0; i <= len(name); i++ {
			if globMatch(rest, name[i:]) {
				return true
			}
		}
		return false
	}
	if pattern[0] == '*' {
		for i := 0; i <= len(name); i++ {
			if globMatch(pattern[1:], name[i:]) {
				return true
			}
			if i < len(name) && name[i] == '/' {
				return false
			}
		}
		return false
	}
	return name != "" && pattern[0] == name[0] && globMatch(pattern[1:], name[1:])
}

func recommendedRuleset(base string) map[string]any {
	return map[string]any{"name": "agent-teams", "target": "branch", "enforcement": "active",
		"conditions":    map[string]any{"ref_name": map[string]any{"include": []string{"refs/heads/" + base, "refs/heads/thread/*"}}},
		"rules":         []map[string]string{{"type": "non_fast_forward"}, {"type": "update"}},
		"bypass_actors": []map[string]any{{"actor_type": "Integration", "bypass_mode": "always"}}}
}

// SetupCIPR opens a PR adding thread/** and mq/** to the push branch filters of a repository's workflows.
func (e *Engine) SetupCIPR(ctx context.Context, projectID, slug string) (string, error) {
	repo, base, err := e.ciRepo(ctx, projectID, slug)
	if err != nil {
		return "", err
	}
	files, err := e.gh.Workflows(ctx, repo, base)
	if err != nil {
		return "", err
	}
	changed := map[string]string{}
	for path, text := range files {
		if next, ok := addPushBranches(text); ok {
			changed[path] = next
		}
	}
	if len(changed) == 0 {
		return "", ErrNoWorkflow
	}
	return e.openCIPR(ctx, repo, base, changed)
}

func (e *Engine) openCIPR(ctx context.Context, repo ghops.Repo, base string, files map[string]string) (string, error) {
	branch := "agent-teams/ci-triggers-" + core.NewHex(3)
	if _, err := e.gh.EnsureBranch(ctx, repo, branch, base); err != nil {
		return "", err
	}
	for _, path := range sortedKeys(files) {
		sha, err := e.gh.FileSHA(ctx, repo, path, branch)
		if err != nil {
			return "", err
		}
		if err := e.gh.PutFile(ctx, repo, path, branch, "ci: run on thread/** and mq/** pushes", files[path], sha); err != nil {
			return "", err
		}
	}
	pr, err := e.gh.CreatePR(ctx, repo, branch, base, "CI：讨论串和合并队列分支也运行检查",
		"Agent Teams 的合并队列在 mq/** 试合并分支上等待 CI，讨论串分支是 thread/**。这个 PR 把它们加进 workflow 的 push 触发条件。")
	return pr.HTMLURL, err
}

// addPushBranches adds thread/** and mq/** to an existing push branch filter.
func addPushBranches(text string) (string, bool) {
	var root yaml.Node
	if yaml.Unmarshal([]byte(text), &root) != nil || len(root.Content) == 0 {
		return "", false
	}
	branches := pushBranchesNode(root.Content[0])
	if branches == nil {
		return "", false
	}
	added := false
	for _, b := range []string{"thread/**", "mq/**"} {
		if !hasScalar(branches, b) {
			branches.Content = append(branches.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: b})
			added = true
		}
	}
	if !added {
		return "", false
	}
	out, err := yaml.Marshal(&root)
	return string(out), err == nil
}

func pushBranchesNode(doc *yaml.Node) *yaml.Node {
	on := mapValue(doc, "on")
	if on == nil || on.Kind != yaml.MappingNode {
		return nil
	}
	push := mapValue(on, "push")
	if push == nil || push.Kind != yaml.MappingNode {
		return nil
	}
	branches := mapValue(push, "branches")
	if branches == nil || branches.Kind != yaml.SequenceNode {
		return nil
	}
	return branches
}

func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func hasScalar(seq *yaml.Node, v string) bool {
	for _, n := range seq.Content {
		if n.Value == v {
			return true
		}
	}
	return false
}
