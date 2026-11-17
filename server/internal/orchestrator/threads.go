package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/office"
	"github.com/touwaeriol/agent-teams/server/internal/roles"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// DefaultMaxRunningThreads applies when at_settings.platform has no value; 0 means no limit.
const DefaultMaxRunningThreads = 0

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
	Repos     []RepoFocus // open_thread(repos): repositories to work in, with optional explicit bases
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
		Brief: req.Brief, AutoApprove: p.AutoApprove, OpenedBy: req.OpenedBy, Roles: roles})
	if err != nil {
		return t, err
	}
	if t, err = o.focusThread(ctx, tx, p, t, req.Repos); err != nil {
		return t, err
	}
	if _, err := core.InstantiateThread(ctx, tx, t, o.hairFor); err != nil {
		return t, err
	}
	return t, tx.EmitThread(ctx, t.ID)
}

// focusThread records open_thread's repositories and explicit bases and names them in the brief.
func (o *Orchestrator) focusThread(ctx context.Context, tx *core.Tx, p store.Project, t core.Thread, focus []RepoFocus) (core.Thread, error) {
	line, err := applyFocus(ctx, tx, p, t.ID, focus)
	if err != nil || line == "" {
		return t, err
	}
	t.Brief = strings.TrimSpace(t.Brief + "\n\n" + line)
	return t, core.UpdateThread(ctx, tx, t.ID, map[string]any{"brief": t.Brief})
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

// maxRunning is the user's running-thread limit (0 = no limit).
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

// threadSlotFree reports whether the user may run one more thread.
func (o *Orchestrator) threadSlotFree(ctx context.Context, q core.DB, userID string) (bool, error) {
	limit := o.maxRunning(ctx, userID)
	if limit <= 0 {
		return true, nil
	}
	running, err := core.RunningThreadCount(ctx, q, userID)
	return running < limit, err
}

// StartAllQueued starts every user's queued threads within their limits (server start: threads
// that queued under an older, tighter limit run without waiting for the next thread event).
func (o *Orchestrator) StartAllQueued(ctx context.Context) {
	users, err := core.QueuedThreadUsers(ctx, o.core.Pool)
	o.logErr("list users with queued threads", err)
	for _, u := range users {
		o.StartQueued(ctx, u)
	}
}

// StartQueued starts the user's queued threads in creation order while slots are free.
func (o *Orchestrator) StartQueued(ctx context.Context, userID string) {
	for {
		free, err := o.threadSlotFree(ctx, o.core.Pool, userID)
		if err != nil || !free {
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

// start marks the thread running and briefs the lead. Thread branches are created later, per
// repository, when the thread first delivers something there.
func (o *Orchestrator) start(ctx context.Context, t core.Thread) error {
	p, err := o.project(ctx, o.core.Pool, t.ProjectID)
	if err != nil {
		return err
	}
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
		files, err := briefFiles(ctx, tx, t.ID)
		if err != nil {
			return err
		}
		_, err = o.Deliver(ctx, tx, lead, Delivery{Text: briefText(cur, p), Kind: "user", Priority: "next", Images: files})
		return err
	})
}

func briefText(t core.Thread, p store.Project) string {
	text := fmt.Sprintf("[Thread #%d · %s]\n%s\n\n讨论串分支：%s", t.No, t.Title, t.Brief, t.Branch)
	if p.HasRepos() {
		text += fmt.Sprintf("（只在有改动的仓库里创建，默认从各仓库的默认分支切出）\n项目仓库：%s", strings.Join(repoNames(p), "、"))
	}
	return text
}

// notifyStatuses are the transitions reported to the coordinator (threads.md).
var notifyStatuses = map[string]bool{"waiting": true, "review": true, "done": true, "failed": true}

// shouldNotify reports whether a transition goes to the coordinator. A thread that waits only
// on approval cards (it flips running ↔ waiting on every tool approval) is not reported: the
// user sees the cards directly, the coordinator cannot decide them, and each report would cost
// the coordinator a turn.
func (o *Orchestrator) shouldNotify(ctx context.Context, tx *core.Tx, t core.Thread, next string) bool {
	if !notifyStatuses[next] {
		return false
	}
	if next != "waiting" {
		return true
	}
	c, err := core.ThreadCountsOf(ctx, tx, t)
	return err != nil || c.QuestionsOpen > 0
}

// setStatus changes a thread's status, emits it, reports upward and frees slots.
func (o *Orchestrator) setStatus(ctx context.Context, tx *core.Tx, t core.Thread, next, reason string) error {
	return o.setStatusLine(ctx, tx, t, next, reason, "")
}

// setStatusLine is setStatus with the line the coordinator is told ("" = the lead's latest message).
func (o *Orchestrator) setStatusLine(ctx context.Context, tx *core.Tx, t core.Thread, next, reason, line string) error {
	if t.Status == next {
		return nil
	}
	if _, err := core.SetThreadStatus(ctx, tx, t.ID, next, reason); err != nil {
		return err
	}
	if err := tx.EmitThread(ctx, t.ID); err != nil {
		return err
	}
	if t.Status == "archived" {
		// Unarchive only puts the thread back in the list: no session starts, no cleanup
		// runs again and the coordinator is not told about a status it already heard of.
		return nil
	}
	if err := o.afterStatus(ctx, tx, t, next); err != nil {
		return err
	}
	if !o.shouldNotify(ctx, tx, t, next) {
		return nil
	}
	// Reporting upward must never block the status change itself (e.g. no node for the coordinator).
	if err := tx.Savepoint(ctx, func(sub *core.Tx) error { return o.notifyCoordinator(ctx, sub, t, next, line) }); err != nil {
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
		if err := o.EndThreadWork(ctx, tx, t.ID); err != nil {
			return err
		}
		tx.AfterCommit(func(ctx context.Context) { o.ThreadEnded(ctx, t.ID) })
	case "running":
		// A resumed (or answered) thread starts a new stall episode: re-arm the stalled note.
		if err := core.UpdateThread(ctx, tx, t.ID, map[string]any{"stalled_note_at": nil}); err != nil {
			return err
		}
		tx.AfterCommit(func(ctx context.Context) { o.Kick(ctx, t.ID) })
	}
	return nil
}

// EndThreadWork stops everything a finished (done, failed, archived) or deleted thread still
// has going: every session of its employees is closed on its node (parked ones too, which
// frees their slots), the tool calls they were in are cancelled, the thread's open cards and
// questions are cancelled and its usage-limit waits are dropped.
func (o *Orchestrator) EndThreadWork(ctx context.Context, tx *core.Tx, threadID string) error {
	sessions, err := core.ThreadLiveSessions(ctx, tx, threadID)
	if err != nil {
		return err
	}
	if err := o.closeAll(ctx, tx, sessions, "thread_done"); err != nil {
		return err
	}
	if err := o.cancelOpenCards(ctx, tx, tx.Gorm(ctx).Where("thread_id = ?", threadID), "system:thread_ended"); err != nil {
		return err
	}
	return tx.Gorm(ctx).Exec(`DELETE FROM at_rate_limit_waits WHERE thread_id = ?`, threadID).Error
}

// closeAll closes sessions and cancels the tool calls they were waiting on.
func (o *Orchestrator) closeAll(ctx context.Context, tx *core.Tx, sessions []core.AgentSession, reason string) error {
	ids := make([]string, 0, len(sessions))
	for _, s := range sessions {
		if err := o.closeSession(ctx, tx, s, reason); err != nil {
			return err
		}
		ids = append(ids, s.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	return tx.Gorm(ctx).Table("at_tool_calls").Where("session_id IN ? AND status IN ('running','waiting')", ids).
		Updates(map[string]any{"status": "cancelled", "finished_at": gorm.Expr("now()")}).Error
}

// cancelOpenCards cancels the open questions and approval cards that scope selects.
func (o *Orchestrator) cancelOpenCards(ctx context.Context, tx *core.Tx, scope *gorm.DB, decidedBy string) error {
	var open []core.Question
	if err := scope.Model(&core.Question{}).Where("status = 'open'").Find(&open).Error; err != nil {
		return err
	}
	for _, q := range open {
		closed, err := core.CloseQuestion(ctx, tx, q, "cancelled", nil, decidedBy)
		if errors.Is(err, core.ErrConflict) {
			continue
		}
		if err != nil {
			return err
		}
		if err := tx.EmitQuestion(ctx, closed); err != nil {
			return err
		}
	}
	return nil
}

// ProjectArchived stops a project's work: its going threads are paused, every session of
// the project (coordinator included) is closed, their cards cancelled and usage-limit waits
// dropped. Nothing starts again on unarchive; the next message wakes whoever it is for.
func (o *Orchestrator) ProjectArchived(ctx context.Context, tx *core.Tx, projectID string) error {
	var going []core.Thread
	if err := tx.Gorm(ctx).Clauses(core.ForUpdate).Where("project_id = ? AND status IN ('running','waiting','queued')", projectID).
		Find(&going).Error; err != nil {
		return err
	}
	for _, t := range going {
		if err := o.Pause(ctx, tx, t, "project_archived"); err != nil {
			return err
		}
	}
	return o.closeProjectSessions(ctx, tx, projectID, "employee_removed")
}

// closeProjectSessions closes every session of a project and cancels what they waited on.
func (o *Orchestrator) closeProjectSessions(ctx context.Context, tx *core.Tx, projectID, reason string) error {
	live, err := core.ProjectLiveSessions(ctx, tx, projectID)
	if err != nil {
		return err
	}
	if err := o.closeAll(ctx, tx, live, reason); err != nil {
		return err
	}
	ids := make([]string, len(live))
	for i, s := range live {
		ids[i] = s.ID
	}
	if len(ids) > 0 {
		if err := o.cancelOpenCards(ctx, tx, tx.Gorm(ctx).Where("session_id IN ?", ids), "system:project_archived"); err != nil {
			return err
		}
	}
	return tx.Gorm(ctx).Exec(`DELETE FROM at_rate_limit_waits WHERE project_id = ?`, projectID).Error
}

// ProjectDeleting closes a project's sessions before its rows go.
func (o *Orchestrator) ProjectDeleting(ctx context.Context, tx *core.Tx, projectID string) error {
	return o.closeProjectSessions(ctx, tx, projectID, "employee_removed")
}

// notifyCoordinator posts "[Thread #12 · title · review] <lead's latest>" to the coordinator.
func (o *Orchestrator) notifyCoordinator(ctx context.Context, tx *core.Tx, t core.Thread, next, line string) error {
	coord, conv, err := o.ensureCoordinatorTx(ctx, tx, t.UserID, t.ProjectID)
	if err != nil {
		return err
	}
	if line == "" {
		line = o.leadLine(ctx, tx, t.ID)
	}
	text := fmt.Sprintf("[Thread #%d · %s · %s] %s", t.No, t.Title, next, line)
	rec := &core.NewMessage{ConversationID: conv, Role: "system", Author: "platform", Kind: "note", Content: text,
		Meta: map[string]any{"thread_no": t.No}}
	_, err = o.Deliver(ctx, tx, coord, Delivery{Text: text, Kind: "system", Priority: "later", Record: rec})
	return err
}

func (o *Orchestrator) leadLine(ctx context.Context, q core.DB, threadID string) string {
	var m core.Message
	_ = q.Gorm(ctx).Select("content").Where("conversation_id = ? AND role = 'leader'", threadID).Order("id DESC").
		Take(&m).Error
	text := m.Content
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

// PostToCoordinator delivers a message to the project coordinator as if the user sent it
// (scheduled tasks); meta marks where it came from.
func (o *Orchestrator) PostToCoordinator(ctx context.Context, userID, projectID, content string, meta map[string]any) (core.Message, error) {
	var m core.Message
	err := o.core.InTx(ctx, func(tx *core.Tx) error {
		emp, conv, err := o.ensureCoordinatorTx(ctx, tx, userID, projectID)
		if err != nil {
			return err
		}
		rec := &core.NewMessage{ConversationID: conv, Role: "user", Author: "user", Kind: "chat", Content: content, Meta: meta}
		m, err = o.Deliver(ctx, tx, emp, Delivery{Text: content, Kind: "user", Priority: "next", Record: rec})
		return err
	})
	return m, err
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
	var locked []store.Project
	if err := tx.Gorm(ctx).Select("id").Clauses(core.ForUpdate).Where("id = ?", projectID).Find(&locked).Error; err != nil {
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
	fmt.Fprintf(&b, "[会话恢复] 你之前的会话记录无法恢复（换了节点，或节点上的会话记录丢失），平台开了新会话让你继续担任 %s（%s）。"+
		"下面是平台整理的现状和最近的对话，请接着之前的工作继续，不要从头再来，也不要向用户重复已经说过的话：\n", e.Name, e.Role)
	if e.ThreadID != nil {
		if err := o.threadDigest(ctx, tx, &b, e); err != nil {
			return "", err
		}
	}
	if err := openQuestionsDigest(ctx, tx, &b, e); err != nil {
		return "", err
	}
	conv, err := o.conversationOf(ctx, tx, e)
	if err != nil {
		return "", err
	}
	if err := recentDigest(ctx, tx, &b, e, conv); err != nil {
		return "", err
	}
	return b.String(), nil
}

// recapMessages is how many recent messages the recap quotes.
const recapMessages = 20

// recentDigest quotes the employee's recent conversation (delivered messages and its replies).
func recentDigest(ctx context.Context, tx *core.Tx, b *strings.Builder, e core.Employee, conv string) error {
	rows, err := store.Query(ctx, tx, `SELECT role, author, content FROM (SELECT id, role, author, content FROM at_messages
		WHERE conversation_id = $1 AND kind IN ('chat','note','interrupt')
		AND ((role = 'leader' AND author = $2) OR (delivered_at IS NOT NULL AND (role = 'user' OR meta->>'to' = $2)))
		ORDER BY id DESC LIMIT $3) x ORDER BY id`, conv, e.Name, recapMessages)
	if err != nil {
		return err
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var role, author, content string
		if err := rows.Scan(&role, &author, &content); err != nil {
			return err
		}
		lines = append(lines, fmt.Sprintf("- %s：%s", speaker(role, author, e.Name), clip(content, 600)))
	}
	if len(lines) > 0 {
		b.WriteString("\n最近的对话（旧→新）：\n" + strings.Join(lines, "\n") + "\n")
	}
	return rows.Err()
}

func speaker(role, author, self string) string {
	switch {
	case role == "user":
		return "用户"
	case role == "leader" && author == self:
		return "你"
	}
	return "平台"
}

// openQuestionsDigest lists the employee's questions still waiting for an answer.
func openQuestionsDigest(ctx context.Context, tx *core.Tx, b *strings.Builder, e core.Employee) error {
	asked, err := openRequests(ctx, tx, e, `kind <> 'approval' AND status = 'open'`)
	if err != nil || len(asked) == 0 {
		return err
	}
	b.WriteString("你提出、还没有回答的问题（回答会以消息送达，不要重复提问）：" + strings.Join(asked, "；") + "\n")
	return nil
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
	if e.Tier == "lead" {
		if err := tasksDigest(ctx, tx, b, *e.ThreadID); err != nil {
			return err
		}
	}
	if task, err := core.CurrentTask(ctx, tx, *e.ThreadID, e.Name); err == nil {
		fmt.Fprintf(b, "你当前的任务 %s：%s\n", task.ID, task.Instruction)
	}
	if t, err := core.ThreadByID(ctx, tx, *e.ThreadID); err == nil && e.Tier == "member" {
		fmt.Fprintf(b, "你的分支：%s\n", MemberBranch(t.No, e.Name))
	}
	return nil
}

// tasksDigest lists the thread's tasks for the lead's recap.
func tasksDigest(ctx context.Context, tx *core.Tx, b *strings.Builder, threadID string) error {
	var tasks []struct{ ID, Who, Status, Instruction string }
	err := tx.Gorm(ctx).Table("at_tasks").
		Select("id, COALESCE(NULLIF(claimed_by, ''), assignee) AS who, status, instruction").
		Where("thread_id = ?", threadID).Order("created_at, id").Find(&tasks).Error
	if err != nil {
		return err
	}
	lines := make([]string, 0, len(tasks))
	for _, t := range tasks {
		lines = append(lines, fmt.Sprintf("- %s %s [%s] %s", t.ID, t.Who, t.Status, clip(t.Instruction, 200)))
	}
	if len(lines) > 0 {
		b.WriteString("任务：\n" + strings.Join(lines, "\n") + "\n")
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
