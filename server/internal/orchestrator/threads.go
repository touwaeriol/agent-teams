package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/office"
	"github.com/touwaeriol/agent-teams/server/internal/roles"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// DefaultMaxRunningThreads applies when at_settings.platform has no value.
const DefaultMaxRunningThreads = 3

// RoleCount adjusts how many instances of a role a thread gets.
type RoleCount struct {
	Role  string `json:"role"`
	Count int    `json:"count" minimum:"1" maximum:"8"`
}

// ThreadRequest opens a thread.
type ThreadRequest struct {
	UserID    string
	ProjectID string
	Title     string
	Brief     string
	Roles     []RoleCount // nil = template counts; Count 0 = template count
	OnlyRoles []string    // open_thread(roles): member roles to include (nil = all)
	OpenedBy  string      // coordinator | user
}

// OpenThread creates a thread (queued), its employees, then starts queued threads within the limit.
func (o *Orchestrator) OpenThread(ctx context.Context, req ThreadRequest) (core.Thread, error) {
	var t core.Thread
	err := o.core.InTx(ctx, func(tx *core.Tx) error {
		var err error
		t, err = o.OpenThreadTx(ctx, tx, req)
		return err
	})
	if err != nil {
		return t, err
	}
	o.StartQueued(ctx, req.UserID)
	return core.ThreadByID(ctx, o.core.Pool, t.ID)
}

// OpenThreadTx creates the thread inside tx; the caller starts queued threads after the commit (StartQueued).
func (o *Orchestrator) OpenThreadTx(ctx context.Context, tx *core.Tx, req ThreadRequest) (core.Thread, error) {
	p, err := o.store.ProjectOf(ctx, req.UserID, req.ProjectID)
	if err != nil {
		return core.Thread{}, err
	}
	roles, err := o.threadRoles(ctx, req)
	if err != nil {
		return core.Thread{}, err
	}
	t, err := core.CreateThread(ctx, tx, core.NewThread{ProjectID: p.ID, UserID: p.UserID, Title: req.Title,
		Brief: req.Brief, BaseBranch: p.Branch, AutoApprove: p.AutoApprove, OpenedBy: req.OpenedBy, Roles: roles})
	if err != nil {
		return t, err
	}
	if _, err := core.InstantiateThread(ctx, tx, t, o.hairFor); err != nil {
		return t, err
	}
	return t, tx.EmitThread(ctx, t.ID)
}

// threadRoles snapshots the user's lead and member templates with the requested counts.
func (o *Orchestrator) threadRoles(ctx context.Context, req ThreadRequest) ([]store.Role, error) {
	all, err := o.userRoles(ctx, req.UserID)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, rc := range req.Roles {
		counts[rc.Role] = rc.Count
	}
	if err := unknownRoles(all, counts, req.OnlyRoles); err != nil {
		return nil, err
	}
	var out []store.Role
	hasLead := false
	for _, r := range all {
		if !keepRole(r, counts, req.OnlyRoles) {
			continue
		}
		if n := counts[r.Role]; n > 0 && r.Tier == "member" {
			r.Count = n
		}
		hasLead = hasLead || r.Tier == "lead"
		out = append(out, r)
	}
	if !hasLead {
		out = append([]store.Role{defaultLead()}, out...)
	}
	return out, nil
}

func keepRole(r store.Role, counts map[string]int, only []string) bool {
	switch {
	case r.Tier == "coordinator":
		return false
	case r.Tier == "lead":
		return true
	case len(only) > 0:
		return contains(only, r.Role)
	}
	n, set := counts[r.Role]
	return !set || n > 0
}

func unknownRoles(all []store.Role, counts map[string]int, only []string) error {
	known := map[string]bool{}
	for _, r := range all {
		known[r.Role] = true
	}
	for role := range counts {
		if !known[role] {
			return fmt.Errorf("%w：%s", ErrRoleNotFound, role)
		}
	}
	for _, role := range only {
		if !known[role] {
			return fmt.Errorf("%w：%s", ErrRoleNotFound, role)
		}
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func defaultLead() store.Role {
	return store.Role{Tier: "lead", Role: "lead", Title: "组长", Runtime: "claude", Count: 1, NodeIDs: []string{}}
}

func defaultCoordinator() store.Role {
	return store.Role{Tier: "coordinator", Role: "coordinator", Title: "项目统筹", Runtime: "claude", Count: 1, NodeIDs: []string{}}
}

func (o *Orchestrator) hairFor(i int) *string {
	if o.office == nil {
		return nil
	}
	hairs := hairPalette(o)
	if len(hairs) == 0 {
		return nil
	}
	h := hairs[i%len(hairs)]
	return &h
}

// maxRunning is the user's running-thread limit.
func (o *Orchestrator) maxRunning(ctx context.Context, userID string) int {
	u, err := o.store.UserByID(ctx, userID)
	if err == nil && u.MaxRunningThreads != nil {
		return *u.MaxRunningThreads
	}
	var platform struct {
		MaxRunningThreads *int `json:"max_running_threads"`
	}
	if found, err := o.store.Setting(ctx, "platform", &platform); err == nil && found && platform.MaxRunningThreads != nil {
		return *platform.MaxRunningThreads
	}
	return DefaultMaxRunningThreads
}

// StartQueued starts the user's queued threads in creation order while slots are free.
func (o *Orchestrator) StartQueued(ctx context.Context, userID string) {
	for {
		running, err := core.RunningThreadCount(ctx, o.core.Pool, userID)
		if err != nil || running >= o.maxRunning(ctx, userID) {
			o.logErr("count running threads", err)
			return
		}
		queued, err := core.QueuedThreads(ctx, o.core.Pool, userID)
		if err != nil || len(queued) == 0 {
			o.logErr("list queued threads", err)
			return
		}
		if err := o.start(ctx, queued[0]); err != nil {
			o.log.Warn("start thread failed", "thread_id", queued[0].ID, "err", err)
			return
		}
	}
}

// start creates the thread branch on GitHub, marks the thread running and briefs the lead.
func (o *Orchestrator) start(ctx context.Context, t core.Thread) error {
	note := o.ensureThreadBranch(ctx, t)
	return o.core.InTx(ctx, func(tx *core.Tx) error {
		cur, err := core.LockThread(ctx, tx, t.ID)
		if err != nil || cur.Status != "queued" {
			return err
		}
		if err := o.setStatus(ctx, tx, cur, "running", ""); err != nil || cur.StartedAt != nil {
			return err
		}
		lead, err := core.Lead(ctx, tx, t.ID)
		if err != nil {
			return err
		}
		_, err = o.Deliver(ctx, tx, lead, Delivery{Text: briefText(cur, note), Kind: "user", Priority: "next"})
		return err
	})
}

func (o *Orchestrator) ensureThreadBranch(ctx context.Context, t core.Thread) string {
	if o.gh == nil {
		return ""
	}
	p, err := o.project(ctx, o.core.Pool, t.ProjectID)
	if err != nil {
		return ""
	}
	repo, err := o.gh.RepoOf(ctx, p)
	if err != nil {
		return ""
	}
	if _, err := o.gh.EnsureBranch(ctx, repo, t.Branch, t.BaseBranch); err != nil {
		o.log.Warn("create thread branch", "thread_id", t.ID, "err", err)
		return fmt.Sprintf("（平台没能在 GitHub 上创建讨论串分支 %s：%v）", t.Branch, err)
	}
	return ""
}

func briefText(t core.Thread, note string) string {
	text := fmt.Sprintf("[Thread #%d · %s]\n%s\n\n讨论串分支：%s（基于 %s）", t.No, t.Title, t.Brief, t.Branch, orDefault(t.BaseBranch, "默认分支"))
	if note != "" {
		text += "\n" + note
	}
	return text
}

// notifyStatuses are the transitions reported to the coordinator (threads.md).
var notifyStatuses = map[string]bool{"waiting": true, "review": true, "done": true, "failed": true}

// setStatus changes a thread's status, emits it, reports upward and frees slots.
func (o *Orchestrator) setStatus(ctx context.Context, tx *core.Tx, t core.Thread, next, reason string) error {
	if t.Status == next {
		return nil
	}
	if _, err := core.SetThreadStatus(ctx, tx, t.ID, next, reason); err != nil {
		return err
	}
	if err := tx.EmitThread(ctx, t.ID); err != nil {
		return err
	}
	if err := o.afterStatus(ctx, tx, t, next); err != nil {
		return err
	}
	if !notifyStatuses[next] {
		return nil
	}
	// Reporting upward must never block the status change itself (e.g. no node for the coordinator).
	if err := tx.Savepoint(ctx, func(sub *core.Tx) error { return o.notifyCoordinator(ctx, sub, t, next) }); err != nil {
		o.log.Warn("notify coordinator", "thread_id", t.ID, "status", next, "err", err)
	}
	return nil
}

// SetStatus exposes setStatus to the other engines.
func (o *Orchestrator) SetStatus(ctx context.Context, tx *core.Tx, t core.Thread, next, reason string) error {
	return o.setStatus(ctx, tx, t, next, reason)
}

func (o *Orchestrator) afterStatus(ctx context.Context, tx *core.Tx, t core.Thread, next string) error {
	freesSlot := (t.Status == "running" || t.Status == "waiting") && next != "running" && next != "waiting"
	if freesSlot {
		tx.AfterCommit(func(ctx context.Context) { go o.StartQueued(context.WithoutCancel(ctx), t.UserID) })
	}
	switch next {
	case "done", "failed", "archived":
		if err := o.closeThreadSessions(ctx, tx, t.ID, "thread_done"); err != nil {
			return err
		}
		tx.AfterCommit(func(ctx context.Context) { o.ThreadEnded(ctx, t.ID) })
	case "running":
		tx.AfterCommit(func(ctx context.Context) { o.Kick(ctx, t.ID) })
	}
	return nil
}

func (o *Orchestrator) closeThreadSessions(ctx context.Context, tx *core.Tx, threadID, reason string) error {
	sessions, err := core.ThreadLiveSessions(ctx, tx, threadID)
	if err != nil {
		return err
	}
	for _, s := range sessions {
		if err := o.closeSession(ctx, tx, s, reason); err != nil {
			return err
		}
	}
	return nil
}

// notifyCoordinator posts "[Thread #12 · title · review] <lead's latest>" to the coordinator.
func (o *Orchestrator) notifyCoordinator(ctx context.Context, tx *core.Tx, t core.Thread, next string) error {
	coord, conv, err := o.ensureCoordinatorTx(ctx, tx, t.UserID, t.ProjectID)
	if err != nil {
		return err
	}
	text := fmt.Sprintf("[Thread #%d · %s · %s] %s", t.No, t.Title, next, o.leadLine(ctx, tx, t.ID))
	rec := &core.NewMessage{ConversationID: conv, Role: "system", Author: "platform", Kind: "note", Content: text,
		Meta: map[string]any{"thread_no": t.No}}
	_, err = o.Deliver(ctx, tx, coord, Delivery{Text: text, Kind: "system", Priority: "later", Record: rec})
	return err
}

func (o *Orchestrator) leadLine(ctx context.Context, q core.DB, threadID string) string {
	var text string
	_ = q.QueryRow(ctx, `SELECT content FROM at_messages WHERE conversation_id = $1 AND role = 'leader' ORDER BY id DESC LIMIT 1`,
		threadID).Scan(&text)
	line := strings.SplitN(strings.TrimSpace(text), "\n", 2)[0]
	r := []rune(line)
	if len(r) > 200 {
		line = string(r[:200]) + "…"
	}
	return line
}

// EnsureCoordinator returns the project's coordinator and its conversation, creating both
// (no session yet: it opens with the first message).
func (o *Orchestrator) EnsureCoordinator(ctx context.Context, userID, projectID string) (core.Employee, string, error) {
	var emp core.Employee
	var conv string
	err := o.core.InTx(ctx, func(tx *core.Tx) error {
		var err error
		emp, conv, err = o.ensureCoordinatorTx(ctx, tx, userID, projectID)
		return err
	})
	return emp, conv, err
}

func (o *Orchestrator) ensureCoordinatorTx(ctx context.Context, tx *core.Tx, userID, projectID string) (core.Employee, string, error) {
	conv, err := core.EnsureCoordinatorConversation(ctx, tx, userID, projectID)
	if err != nil {
		return core.Employee{}, "", err
	}
	emp, err := core.Coordinator(ctx, tx, projectID)
	if !errors.Is(err, core.ErrNotFound) {
		return emp, conv, err
	}
	role, err := o.coordinatorRole(ctx, userID)
	if err != nil {
		return emp, conv, err
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM at_projects WHERE id = $1 FOR UPDATE`, projectID); err != nil {
		return emp, conv, err
	}
	if emp, err = core.Coordinator(ctx, tx, projectID); err == nil {
		return emp, conv, nil
	}
	emp, err = core.InsertEmployee(ctx, tx, core.NewEmployee{ProjectID: projectID, Name: "coordinator", Role: role,
		InstanceNo: 1, Look: role.Look})
	return emp, conv, err
}

// userRoles are the user's templates, or the platform defaults when there are none yet.
func (o *Orchestrator) userRoles(ctx context.Context, userID string) ([]store.Role, error) {
	all, err := o.store.Roles(ctx, userID)
	if err != nil || len(all) > 0 {
		return all, err
	}
	var looks office.Looks
	if o.office != nil {
		if l, err := o.office.Get("default"); err == nil && l != nil {
			looks = office.LooksOf(l.Doc)
		}
	}
	return roles.Defaults(looks), nil
}

func (o *Orchestrator) coordinatorRole(ctx context.Context, userID string) (store.Role, error) {
	list, err := o.userRoles(ctx, userID)
	if err != nil {
		return store.Role{}, err
	}
	for _, r := range list {
		if r.Tier == "coordinator" {
			return r, nil
		}
	}
	return defaultCoordinator(), nil
}

// handoffSummary is the first message of a fresh session that replaces a lost one (§6.2).
func (o *Orchestrator) handoffSummary(ctx context.Context, tx *core.Tx, e core.Employee) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "[交接] 你接替之前的会话继续担任 %s（%s）。之前的对话记录在另一台节点上，下面是平台整理的现状：\n", e.Name, e.Role)
	if e.ThreadID != nil {
		if err := o.threadDigest(ctx, tx, &b, e); err != nil {
			return "", err
		}
	}
	rows, err := tx.Query(ctx, `SELECT content FROM (SELECT id, content FROM at_messages
		WHERE conversation_id = $1 AND (meta->>'to' = $2 OR role IN ('user','leader')) ORDER BY id DESC LIMIT 20) x ORDER BY id`,
		convOf(e), e.Name)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	b.WriteString("\n最近的消息：\n")
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "- %s\n", clip(c, 300))
	}
	return b.String(), rows.Err()
}

func convOf(e core.Employee) string {
	if e.ThreadID != nil {
		return *e.ThreadID
	}
	return ""
}

func (o *Orchestrator) threadDigest(ctx context.Context, tx *core.Tx, b *strings.Builder, e core.Employee) error {
	plan, err := core.MainTasks(ctx, tx, *e.ThreadID)
	if err != nil {
		return err
	}
	b.WriteString("主任务：\n")
	for _, m := range plan {
		fmt.Fprintf(b, "- [%s] %s\n", m.Status, m.Title)
	}
	if task, err := core.CurrentTask(ctx, tx, *e.ThreadID, e.Name); err == nil {
		fmt.Fprintf(b, "你当前的任务 %s：%s\n", task.ID, task.Instruction)
	}
	if t, err := core.ThreadByID(ctx, tx, *e.ThreadID); err == nil && e.Tier == "member" {
		fmt.Fprintf(b, "你的分支：%s\n", MemberBranch(t.No, e.Name))
	}
	return nil
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// hairPalette is the default layout's hair colours (instance hair when the role has none).
func hairPalette(o *Orchestrator) []string {
	l, err := o.office.Get("default")
	if err != nil || l == nil {
		return nil
	}
	return office.LooksOf(l.Doc).Hair
}

func (o *Orchestrator) logErr(what string, err error) {
	if err != nil {
		o.log.Warn(what, "err", err)
	}
}
