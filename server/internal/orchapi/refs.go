package orchapi

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/touwaeriol/agent-teams/server/internal/artifacts"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/ghops"
	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Limits of @ references: how many a message carries and how much of one goes to the employee.
const (
	maxRefs        = 10
	maxRefText     = 30000
	maxFileMatches = 30
	maxOtherHits   = 8
)

// RefCandidate is one thing the @ popup offers.
type RefCandidate struct {
	Kind   string `json:"kind" enum:"file,thread,deliverable,memory"`
	Label  string `json:"label" doc:"What the chip shows: a path, #12 标题, a title or a file name"`
	Detail string `json:"detail"`
	Repo   string `json:"repo,omitempty" doc:"file: owner/name"`
	Path   string `json:"path,omitempty" doc:"file: path in the repository"`
	No     int    `json:"no,omitempty" doc:"thread: its number"`
	ID     string `json:"id,omitempty" doc:"deliverable: its id"`
	Name   string `json:"name,omitempty" doc:"memory: file name"`
}

// RefList is GET /api/projects/{pid}/references.
type RefList struct {
	Items     []RefCandidate `json:"items"`
	FilesNote string         `json:"files_note" doc:"Why repository files are missing or partial ('' when listed in full)"`
}

type refsInput struct {
	PID  string `path:"pid" maxLength:"64"`
	Q    string `query:"q" maxLength:"200"`
	Kind string `query:"kind" enum:"file,thread,deliverable,memory," doc:"Only this kind ('' = all)"`
}

type refsOutput struct{ Body RefList }

// MessageRef is a reference the user put in a message with @.
type MessageRef struct {
	Kind string `json:"kind" enum:"file,thread,deliverable,memory"`
	Repo string `json:"repo,omitempty" maxLength:"300"`
	Path string `json:"path,omitempty" maxLength:"1000"`
	No   int    `json:"no,omitempty" minimum:"0"`
	ID   string `json:"id,omitempty" maxLength:"64"`
	Name string `json:"name,omitempty" maxLength:"200"`
}

func (a *API) references(ctx context.Context, in *refsInput) (*refsOutput, error) {
	_, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	q := strings.TrimSpace(in.Q)
	out := RefList{Items: []RefCandidate{}}
	want := func(kind string) bool { return in.Kind == "" || in.Kind == kind }
	if want("thread") {
		if out.Items, err = a.threadRefs(ctx, p.ID, q, out.Items); err != nil {
			return nil, err
		}
	}
	if want("deliverable") {
		if out.Items, err = a.deliverableRefs(ctx, p.ID, q, out.Items); err != nil {
			return nil, err
		}
	}
	if want("memory") {
		if out.Items, err = a.memoryRefs(ctx, p.ID, q, out.Items); err != nil {
			return nil, err
		}
	}
	if want("file") {
		out.Items, out.FilesNote = a.fileRefs(ctx, p, q, out.Items)
	}
	return &refsOutput{Body: out}, nil
}

func (a *API) threadRefs(ctx context.Context, pid, q string, out []RefCandidate) ([]RefCandidate, error) {
	threads, err := core.Threads(ctx, a.e.Core.Pool, pid, nil)
	if err != nil {
		return nil, err
	}
	hits := []scored[RefCandidate]{}
	for _, t := range threads {
		label := fmt.Sprintf("#%d %s", t.No, t.Title)
		if s, ok := fuzzy(q, label); ok {
			hits = append(hits, scored[RefCandidate]{s, RefCandidate{Kind: "thread", Label: label, Detail: threadStatus(t.Status), No: t.No}})
		}
	}
	return append(out, best(hits, maxOtherHits)...), nil
}

func (a *API) deliverableRefs(ctx context.Context, pid, q string, out []RefCandidate) ([]RefCandidate, error) {
	list, err := core.Deliverables(ctx, a.e.Core.Pool, pid, core.DeliverableFilter{Limit: 200, NoImages: true})
	if err != nil {
		return nil, err
	}
	hits := []scored[RefCandidate]{}
	for _, d := range list {
		if s, ok := fuzzy(q, d.Title); ok {
			hits = append(hits, scored[RefCandidate]{s, RefCandidate{Kind: "deliverable", Label: d.Title, Detail: d.Kind, ID: d.ID}})
		}
	}
	return append(out, best(hits, maxOtherHits)...), nil
}

func (a *API) memoryRefs(ctx context.Context, pid, q string, out []RefCandidate) ([]RefCandidate, error) {
	files, err := store.ListMemoryFiles(ctx, a.e.Core.Pool, store.ProjectMemory(pid))
	if err != nil {
		return nil, err
	}
	hits := []scored[RefCandidate]{}
	for _, f := range files {
		if s, ok := fuzzy(q, f.Name); ok {
			hits = append(hits, scored[RefCandidate]{s, RefCandidate{Kind: "memory", Label: f.Name, Detail: f.Description, Name: f.Name}})
		}
	}
	return append(out, best(hits, maxOtherHits)...), nil
}

// fileRefs lists matching paths of every repository at its default branch; GitHub problems
// become a note instead of an error, so threads and memory still show.
func (a *API) fileRefs(ctx context.Context, p store.Project, q string, out []RefCandidate) ([]RefCandidate, string) {
	if !p.HasRepos() {
		return out, "项目没有关联仓库"
	}
	repos, err := a.e.GH.ReposOf(ctx, p)
	if err != nil {
		return out, "读不到仓库：" + err.Error()
	}
	hits := []scored[RefCandidate]{}
	notes := []string{}
	for _, r := range repos {
		tree, err := a.repoTree(ctx, r)
		if err != nil {
			notes = append(notes, fmt.Sprintf("%s 的文件列表读不到：%v", r.Slug, err))
			continue
		}
		if tree.Truncated {
			notes = append(notes, fmt.Sprintf("%s 文件太多，只列出前 %d 个", r.Slug, len(tree.Paths)))
		}
		for _, path := range tree.Paths {
			if s, ok := fuzzy(q, path); ok {
				hits = append(hits, scored[RefCandidate]{s, RefCandidate{Kind: "file", Label: path, Detail: r.Slug + "@" + tree.Ref, Repo: r.Slug, Path: path}})
			}
		}
	}
	return append(out, best(hits, maxFileMatches)...), strings.Join(notes, "；")
}

func (a *API) repoTree(ctx context.Context, r ghops.Repo) (ghops.Tree, error) {
	ref, err := a.e.GH.DefaultBranch(ctx, r)
	if err != nil {
		return ghops.Tree{}, err
	}
	return a.e.GH.Tree(ctx, r, ref)
}

var threadStatusText = map[string]string{"queued": "排队中", "running": "进行中", "waiting": "等你处理", "paused": "已暂停", "review": "待验收",
	"done": "已完成", "failed": "失败", "archived": "已归档"}

func threadStatus(s string) string {
	if t, ok := threadStatusText[s]; ok {
		return t
	}
	return s
}

// scored pairs an item with its match score (lower is better).
type scored[T any] struct {
	score int
	item  T
}

func best[T any](hits []scored[T], n int) []T {
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score < hits[j].score })
	if len(hits) > n {
		hits = hits[:n]
	}
	out := make([]T, len(hits))
	for i, h := range hits {
		out[i] = h.item
	}
	return out
}

// fuzzy matches q as a case-insensitive subsequence of text. A substring scores best (earlier and
// in the last path segment better); a scattered match scores by how spread out it is.
func fuzzy(q, text string) (int, bool) {
	if q == "" {
		return len(text), true
	}
	lq, lt := strings.ToLower(q), strings.ToLower(text)
	if i := strings.Index(lt, lq); i >= 0 {
		base := strings.LastIndex(lt, "/") + 1
		if i >= base {
			return i - base, true
		}
		return 1000 + i, true
	}
	pos, first, last := 0, -1, 0
	for _, r := range lq {
		j := strings.IndexRune(lt[pos:], r)
		if j < 0 {
			return 0, false
		}
		if first < 0 {
			first = pos + j
		}
		pos += j + utf8.RuneLen(r)
		last = pos
	}
	return 10000 + (last - first) + len(text), true
}

// resolveRefs turns the message's @ references into the context block delivered with it and the
// chips stored in meta.refs. Every lookup is scoped to the conversation's project (the user's).
func (a *API) resolveRefs(ctx context.Context, u store.User, c core.Conversation, refs []MessageRef) (string, []map[string]any, error) {
	if len(refs) == 0 {
		return "", nil, nil
	}
	if len(refs) > maxRefs {
		return "", nil, httpapi.Invalidf("refs", "一条消息最多引用 {n} 项", "n", maxRefs)
	}
	p, err := a.e.Store.ProjectOf(ctx, u.ID, deref(c.ProjectID))
	if err != nil {
		return "", nil, fail(err, "没有这个项目")
	}
	var b strings.Builder
	b.WriteString("\n\n---\n[用户用 @ 引用的上下文]")
	chips := make([]map[string]any, 0, len(refs))
	for _, r := range refs {
		section, chip, err := a.resolveRef(ctx, u, p, r)
		if err != nil {
			return "", nil, err
		}
		b.WriteString("\n\n" + section)
		chips = append(chips, chip)
	}
	return b.String(), chips, nil
}

// refContext resolves stored reference chips (meta.refs) again for a forwarded message;
// a reference that no longer resolves becomes a line saying so instead of failing the forward.
func (a *API) refContext(ctx context.Context, userID, projectID string, chips []map[string]any) string {
	p, err := a.e.Store.ProjectOf(ctx, userID, projectID)
	if err != nil || len(chips) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n---\n[用户用 @ 引用的上下文]")
	for _, chip := range chips {
		section, _, err := a.resolveRef(ctx, store.User{ID: userID}, p, refOfChip(chip))
		if err != nil {
			section = fmt.Sprintf("### %v\n（这个引用现在读不到了：%v）", chip["label"], err)
		}
		b.WriteString("\n\n" + section)
	}
	return b.String()
}

// refOfChip turns a stored chip back into the reference it was made from.
func refOfChip(chip map[string]any) MessageRef {
	str := func(k string) string { v, _ := chip[k].(string); return v }
	no := 0
	switch v := chip["no"].(type) {
	case float64:
		no = int(v)
	case int:
		no = v
	}
	return MessageRef{Kind: str("kind"), Repo: str("repo"), Path: str("path"), No: no, ID: str("id"), Name: str("name")}
}

func (a *API) resolveRef(ctx context.Context, u store.User, p store.Project, r MessageRef) (string, map[string]any, error) {
	switch r.Kind {
	case "file":
		return a.fileRef(ctx, p, r)
	case "thread":
		return a.threadRef(ctx, p, r)
	case "deliverable":
		return a.deliverableRef(ctx, u, p, r)
	case "memory":
		return a.memoryRef(ctx, p, r)
	}
	return "", nil, httpapi.Invalid("refs", "不认识的引用")
}

func (a *API) fileRef(ctx context.Context, p store.Project, r MessageRef) (string, map[string]any, error) {
	path := strings.Trim(r.Path, "/")
	if path == "" || strings.Contains(path, "..") {
		return "", nil, httpapi.Invalid("refs", "文件路径不对")
	}
	repos, err := a.e.GH.ReposOf(ctx, p)
	if err != nil {
		return "", nil, fail(err, "")
	}
	repo, ok := ghops.Find(repos, r.Repo)
	if !ok {
		return "", nil, httpapi.Invalidf("refs", "项目里没有仓库 {repo}", "repo", r.Repo)
	}
	chip := map[string]any{"kind": "file", "label": path, "repo": repo.Slug, "path": path}
	ref, err := a.e.GH.DefaultBranch(ctx, repo)
	if err != nil {
		return fmt.Sprintf("### 文件 %s（仓库 %s）\n（读取失败：%v）", path, repo.Slug, err), chip, nil
	}
	chip["ref"] = ref
	head := fmt.Sprintf("### 文件 %s（仓库 %s，分支 %s）", path, repo.Slug, ref)
	text, ok, err := a.e.GH.TextFile(ctx, repo, path, ref)
	switch {
	case err != nil:
		return head + fmt.Sprintf("\n（读取失败：%v）", err), chip, nil
	case !ok:
		return head + "\n（读不到内容：文件不存在、不是文本或太大，需要时请自己在仓库里打开）", chip, nil
	}
	body, note := clip(text, maxRefText)
	return head + note + "\n```\n" + body + "\n```", chip, nil
}

func (a *API) threadRef(ctx context.Context, p store.Project, r MessageRef) (string, map[string]any, error) {
	t, err := core.ThreadByNo(ctx, a.e.Core.Pool, p.ID, r.No)
	if errors.Is(err, core.ErrNotFound) {
		return "", nil, httpapi.Invalidf("refs", "没有讨论串 #{no}", "no", r.No)
	}
	if err != nil {
		return "", nil, err
	}
	v, err := a.threadView(ctx, t)
	if err != nil {
		return "", nil, err
	}
	var b strings.Builder
	merges, err := core.MergeQueue(ctx, a.e.Core.Pool, t.ID, true)
	if err != nil {
		return "", nil, err
	}
	fmt.Fprintf(&b, "### 讨论串 #%d %s\n状态：%s", t.No, t.Title, core.PhaseOf(v, len(merges)).Text())
	if t.Brief != "" {
		body, _ := clip(t.Brief, 4000)
		b.WriteString("\n任务说明：" + body)
	}
	if t.Summary != "" {
		body, _ := clip(t.Summary, maxRefText/2)
		b.WriteString("\n总结：" + body)
	}
	if v.LeadLast != nil {
		body, _ := clip(v.LeadLast.Text, maxRefText/2)
		b.WriteString("\n组长最新消息：" + body)
	}
	return b.String(), map[string]any{"kind": "thread", "label": fmt.Sprintf("#%d %s", t.No, t.Title), "no": t.No}, nil
}

func (a *API) deliverableRef(ctx context.Context, u store.User, p store.Project, r MessageRef) (string, map[string]any, error) {
	d, err := core.DeliverableByID(ctx, a.e.Core.Pool, u.ID, r.ID)
	if errors.Is(err, core.ErrNotFound) || (err == nil && d.ProjectID != p.ID) {
		return "", nil, httpapi.Invalid("refs", "没有这个交付物")
	}
	if err != nil {
		return "", nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "### 交付物 %s\nid：%s\n类型：%s", d.Title, d.ID, d.Kind)
	if d.URL != "" {
		fmt.Fprintf(&b, "\n地址：%s", d.URL)
	}
	for _, key := range []string{"summary", "description", "path"} {
		if s, ok := d.Detail[key].(string); ok && s != "" {
			body, _ := clip(s, 4000)
			fmt.Fprintf(&b, "\n%s：%s", key, body)
		}
	}
	if d.Detail["hosted"] == true {
		if err := a.hostedRef(ctx, &b, d); err != nil {
			return "", nil, err
		}
	}
	return b.String(), map[string]any{"kind": "deliverable", "label": d.Title, "id": d.ID}, nil
}

// hostedRef adds the latest version of a hosted deliverable: its files, the entry file's text when
// it is text-like (cut at maxRefText), and how to fetch the full version with read_deliverable.
func (a *API) hostedRef(ctx context.Context, b *strings.Builder, d core.Deliverable) error {
	versions, err := artifacts.Versions(ctx, a.e.Core.Pool, d.ID)
	if err != nil || len(versions) == 0 {
		return err
	}
	v := versions[0]
	files, err := artifacts.Files(ctx, a.e.Core.Pool, d.ID, v.Version)
	if err != nil {
		return err
	}
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Path
	}
	entry := artifacts.Entry(paths)
	fmt.Fprintf(b, "\n最新版本：第 %d 版（%s 发布），%d 个文件：%s", v.Version, v.PublishedBy, len(files), strings.Join(paths, "、"))
	fetch := fmt.Sprintf("read_deliverable(deliverable_id=%s)", d.ID)
	if !textLike(entry) {
		fmt.Fprintf(b, "\n%s 是%s，这里不附内容；需要时用 %s 把文件取到工作目录查看。", entry, binaryKind(entry), fetch)
		return nil
	}
	f, err := a.e.Core.Artifacts().Load(ctx, a.e.Core.Pool, d.ID, v.Version, entry)
	if errors.Is(err, artifacts.ErrNotFound) {
		fmt.Fprintf(b, "\n%s 的内容现在取不到（共享文件夹里的文件不在了或被改过），这里不附内容。", entry)
		return nil
	}
	if err != nil {
		return err
	}
	body, note := clip(string(f.Data), maxRefText)
	fmt.Fprintf(b, "\n%s 的内容%s：\n```\n%s\n```", entry, note, body)
	if note != "" || len(files) > 1 {
		fmt.Fprintf(b, "\n完整内容（含全部文件）用 %s 取到工作目录。", fetch)
	}
	fmt.Fprintf(b, "\n要修改：%s 取回后在本地改，再用 publish_file 带 deliverable_id=%s 发布新版本。", fetch, d.ID)
	return nil
}

// textLike is a file whose content goes into the message (html, md, svg, json, csv, txt, css, js).
func textLike(path string) bool {
	ct := artifacts.ContentType(path)
	return strings.HasPrefix(ct, "text/") || strings.HasPrefix(ct, "application/json") || strings.HasPrefix(ct, "image/svg+xml")
}

func binaryKind(path string) string {
	switch artifacts.Render(artifacts.ContentType(path)) {
	case "pdf":
		return "PDF 文件"
	case "image":
		return "图片"
	}
	return "二进制文件"
}

func (a *API) memoryRef(ctx context.Context, p store.Project, r MessageRef) (string, map[string]any, error) {
	f, err := store.GetMemoryFile(ctx, a.e.Core.Pool, store.ProjectMemory(p.ID), r.Name)
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, core.ErrNotFound) || store.IsMemoryError(err) {
		return "", nil, httpapi.Invalidf("refs", "没有记忆文件 {name}", "name", r.Name)
	}
	if err != nil {
		return "", nil, err
	}
	body, note := clip(f.Content, maxRefText)
	return fmt.Sprintf("### 记忆文件 %s%s\n```\n%s\n```", f.Name, note, body), map[string]any{"kind": "memory", "label": f.Name, "name": f.Name}, nil
}

// clip keeps the first n bytes of s on a rune boundary; the note says it was cut.
func clip(s string, n int) (string, string) {
	if len(s) <= n {
		return s, ""
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], fmt.Sprintf("（只取前 %d 字节）", n)
}
