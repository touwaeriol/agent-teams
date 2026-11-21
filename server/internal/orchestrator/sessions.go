package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/ghops"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
	"github.com/touwaeriol/agent-teams/server/internal/sshgw"
	"github.com/touwaeriol/agent-teams/server/internal/status"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Delivery is one message for an employee's session (architecture.md §6.4).
type Delivery struct {
	Text      string
	Kind      string // user | task | mail | answer | system
	Priority  string // now | next | later
	Interrupt bool
	// Images the user attached (delivered as image content blocks).
	Images []core.AttachmentRef
	// Record is the conversation message to store with the msg_id; nil stores a note
	// in the employee's conversation addressed to it (meta.to).
	Record *core.NewMessage
	// Anchor is the task anchor the daemon keeps and re-injects after the runtime compacts
	// (task briefs; shared-members.md §5.2); "" clears it. nil: decided by the member's task
	// (sendAnchor, withOpenAnchor).
	Anchor *string
}

// firstMessage is a delivery that opens (or reopens) a session.
func firstMessage(msgID, text string, d Delivery) *protocol.FirstMessage {
	return &protocol.FirstMessage{MsgID: protocol.MsgID(msgID), Text: text, Images: imageRefs(d.Images), Anchor: d.Anchor}
}

// Deliver records the message and sends it to the employee's session, opening one when needed.
// It takes the project row lock first (core.LockProject): moving a member and opening its
// session update member rows after the message's event, in the order every claim pass uses.
func (o *Orchestrator) Deliver(ctx context.Context, tx *core.Tx, e core.Employee, d Delivery) (core.Message, error) {
	if err := core.LockProject(ctx, tx, e.ProjectID); err != nil {
		return core.Message{}, err
	}
	msgID := core.NewID("m")
	tg, err := o.deliveryTarget(ctx, tx, e, d)
	if err != nil {
		return core.Message{}, err
	}
	e, d = tg.emp, tg.d
	var m core.Message
	if tg.conv != "" {
		if m, err = o.recordDelivery(ctx, tx, e, d, tg.conv, msgID); err != nil {
			return m, err
		}
	}
	if !tg.wake {
		return m, nil // an ended thread's employee or a member without a thread: the message is only kept
	}
	o.wake(tx, e.ID)
	sess, err := core.LiveSession(ctx, tx, e.ID)
	switch {
	case errors.Is(err, core.ErrNotFound):
		text, resume, err := o.continuation(ctx, tx, e, d.Text)
		if err != nil {
			return m, err
		}
		_, err = o.openSession(ctx, tx, e, firstMessage(msgID, text, d), resume)
		return m, err
	case err != nil:
		return m, err
	case sess.State == "crashed":
		return m, o.reopen(ctx, tx, e, sess, firstMessage(msgID, d.Text, d))
	}
	if d.Anchor, err = sendAnchor(ctx, tx, e, d); err != nil {
		return m, err
	}
	return m, o.send(ctx, tx, sess, msgID, d)
}

// target is where a delivery goes.
type target struct {
	emp  core.Employee // the employee as it works now (a project member pinned to its work thread)
	d    Delivery      // the delivery, its text headed when the member works in another thread
	conv string        // the conversation its row goes to ("" = none)
	wake bool          // the employee's session gets it
}

// deliveryTarget decides where a delivery is recorded and whether the session gets it
// (shared-members.md §6). With a record, the record's conversation (recordTarget). Without one,
// the employee's own conversation; a project member without a thread gets nothing: the mail,
// answer or note is kept where it was made (never in the coordinator conversation), and its
// next task brings it.
func (o *Orchestrator) deliveryTarget(ctx context.Context, tx *core.Tx, e core.Employee, d Delivery) (target, error) {
	e, err := atWork(ctx, tx, e)
	if err != nil {
		return target{}, err
	}
	if d.Record != nil {
		return o.recordTarget(ctx, tx, e, d)
	}
	tg := target{emp: e, d: d}
	if e.ProjectMember() && e.CurrentThreadID == nil {
		return tg, nil
	}
	if tg.conv, err = o.conversationOf(ctx, tx, e); err != nil {
		return tg, err
	}
	tg.wake, err = o.awake(ctx, tx, e)
	return tg, err
}

// recordTarget: the message is recorded in its record's conversation. A project member that does
// not work in that thread now moves there when it has no task; while its task is in another
// thread it gets the message in its current session, headed "[#N · 来自 X 的留言]", without
// switching.
func (o *Orchestrator) recordTarget(ctx context.Context, tx *core.Tx, e core.Employee, d Delivery) (target, error) {
	tg := target{emp: e, d: d, conv: d.Record.ConversationID}
	t, other, err := otherThread(ctx, tx, e, tg.conv)
	if err != nil {
		return tg, err
	}
	if other {
		if tg, err = o.reachElsewhere(ctx, tx, tg, t); err != nil {
			return tg, err
		}
	}
	tg.wake, err = o.awake(ctx, tx, tg.emp)
	return tg, err
}

// reachElsewhere brings a message of thread t to a project member working elsewhere (or nowhere).
func (o *Orchestrator) reachElsewhere(ctx context.Context, tx *core.Tx, tg target, t core.Thread) (target, error) {
	stay, err := staysPut(ctx, tx, tg.emp)
	if err != nil {
		return tg, err
	}
	if !stay {
		tg.emp, err = o.MoveToThread(ctx, tx, tg.emp, t.ID)
		return tg, err
	}
	tg.d.Text = elsewhereHeader(t.No, tg.d) + "\n" + tg.d.Text
	return tg, nil
}

// staysPut: a project member with a task, or in the middle of a turn, is not moved to another
// thread (moving closes its session).
func staysPut(ctx context.Context, q core.DB, e core.Employee) (bool, error) {
	_, err := core.CurrentTaskOf(ctx, q, e)
	if !errors.Is(err, core.ErrNotFound) {
		return err == nil, err
	}
	s, err := core.LiveSession(ctx, q, e.ID)
	if errors.Is(err, core.ErrNotFound) {
		return false, nil
	}
	return err == nil && s.InTurn && s.State == "active", err
}

// otherThread returns the thread conv is when it is a thread of a project member's project that
// the member does not work in now (other=false otherwise).
func otherThread(ctx context.Context, q core.DB, e core.Employee, conv string) (core.Thread, bool, error) {
	if !e.ProjectMember() || sameThread(e.CurrentThreadID, &conv) {
		return core.Thread{}, false, nil
	}
	t, err := core.ThreadByID(ctx, q, conv)
	if errors.Is(err, core.ErrNotFound) {
		return t, false, nil
	}
	return t, err == nil && t.ProjectID == e.ProjectID, err
}

// elsewhereKind names what a delivery is in the header of a message from another thread.
var elsewhereKind = map[string]string{"answer": "回答", "task": "任务", "system": "通知"}

// elsewhereHeader heads a message of thread no for a member working in another thread.
func elsewhereHeader(no int, d Delivery) string {
	kind, ok := elsewhereKind[d.Kind]
	if !ok {
		kind = "留言"
	}
	return fmt.Sprintf("[#%d · 来自 %s 的%s]", no, authorName(d.Record.Author), kind)
}

func authorName(author string) string {
	switch author {
	case "user":
		return "用户"
	case "platform", "":
		return "平台"
	}
	return author
}

// awake reports whether the employee's session gets a delivery: not when it works in a thread
// that ended, nor when it is a project member without a thread (no worktree to wake in).
func (o *Orchestrator) awake(ctx context.Context, tx *core.Tx, e core.Employee) (bool, error) {
	if e.ProjectMember() && e.CurrentThreadID == nil {
		return false, nil
	}
	ended, err := o.threadEnded(ctx, tx, e)
	return !ended, err
}

// threadEnded reports whether the employee works in a thread that is done, failed or archived.
func (o *Orchestrator) threadEnded(ctx context.Context, tx *core.Tx, e core.Employee) (bool, error) {
	if e.WorkThreadID() == nil {
		return false, nil
	}
	t, err := core.ThreadByID(ctx, tx, *e.WorkThreadID())
	if err != nil {
		return false, ignoreMissing(err)
	}
	return threadOver(t.Status), nil
}

func threadOver(status string) bool {
	return status == "done" || status == "failed" || status == "archived"
}

// wake shows the employee as busy (waking) from the moment a message is on its way: a parked
// or closed session takes a while to come back before its turn starts.
func (o *Orchestrator) wake(tx *core.Tx, employeeID string) {
	tx.AfterCommit(func(ctx context.Context) {
		o.status.Observe(ctx, employeeID, status.Observation{Kind: status.Woken, Cause: "message"})
	})
}

// resumableClose are close reasons after which the employee's next session picks up the old
// conversation by its SDK session id (thread finished and reopened, user cut off and back, a
// project member back in the thread it left before its next session opened elsewhere).
// A policy violation starts clean; a removed node cannot resume and gets the recap instead.
var resumableClose = []string{"thread_done", "employee_removed", "node_closed", "switch_thread"}

// reopenedPrefix starts the first message of a session that resumes after its thread ended
// (the user reopened it, or unarchived and resumed it): the employee goes on from where it was.
const reopenedPrefix = "[平台] 讨论串已重新打开，接着之前的对话继续。\n\n"

// continuation decides how a new session for e starts: resuming the previous session's
// conversation, with the recap prefixed when that session was lost with its node, or fresh.
func (o *Orchestrator) continuation(ctx context.Context, tx *core.Tx, e core.Employee, text string) (string, *protocol.ResumeRef, error) {
	last, err := core.LatestSession(ctx, tx, e.ID)
	if errors.Is(err, core.ErrNotFound) {
		return text, nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	if last.CloseReason == "node_removed" {
		summary, err := o.handoffSummary(ctx, tx, e)
		if err != nil {
			return "", nil, err
		}
		return summary + "\n\n---\n" + text, nil, o.resumedNote(ctx, tx, e)
	}
	if fresh := freshReason(e, last); fresh != "" {
		// A project member's session belongs to one thread's worktree (moved), or was cleared
		// after its tasks (context_clear): a fresh one, with a digest of the thread.
		digest, err := o.switchDigest(ctx, tx, e, *e.WorkThreadID(), fresh)
		if err != nil {
			return "", nil, err
		}
		return digest + "\n---\n" + text, nil, nil
	}
	if slices.Contains(resumableClose, last.CloseReason) {
		if last.CloseReason == "thread_done" && e.WorkThreadID() != nil {
			text = reopenedPrefix + text
		}
		return text, resumeRef(last), nil
	}
	return text, nil, nil
}

// CloseContextClear closes a member's session after clear_after_tasks tasks or a context too full
// to compact (shared-members.md §5): the next task starts a fresh session with a thread digest.
const CloseContextClear = "context_clear"

// freshReason is why the employee's next session starts fresh with a thread digest:
// switch_thread (a project member moved threads) or context_clear; "" otherwise.
func freshReason(e core.Employee, last core.AgentSession) string {
	if movedThread(e, last) {
		return "switch_thread"
	}
	if last.CloseReason == CloseContextClear && e.WorkThreadID() != nil {
		return CloseContextClear
	}
	return ""
}

// movedThread reports whether a project member's last session was in another thread than the
// one it works in now; a session whose thread was deleted (thread_id NULL) counts as another
// thread: its conversation belongs to a worktree that is gone, never resumed across cwd.
func movedThread(e core.Employee, last core.AgentSession) bool {
	return e.ProjectMember() && e.CurrentThreadID != nil && !sameThread(last.ThreadID, e.CurrentThreadID)
}

// MoveToThread makes a project member work in a thread (it claimed a task there): its current
// thread is recorded and a live or parked session it has in another thread's worktree is closed
// (switch_thread), so the next message opens a fresh session in this thread's worktree whose
// first message carries a digest of the thread (continuation). Others are returned unchanged.
func (o *Orchestrator) MoveToThread(ctx context.Context, tx *core.Tx, e core.Employee, threadID string) (core.Employee, error) {
	if !e.ProjectMember() {
		return e, nil
	}
	if err := core.SetCurrentThread(ctx, tx, e.ID, &threadID); err != nil {
		return e, err
	}
	e.CurrentThreadID = &threadID
	s, err := core.LiveSession(ctx, tx, e.ID)
	if errors.Is(err, core.ErrNotFound) || (err == nil && sameThread(s.ThreadID, &threadID)) {
		return e, nil
	}
	if err != nil {
		return e, err
	}
	return e, o.closeAll(ctx, tx, []core.AgentSession{s}, "switch_thread")
}

// atWork pins a project member to the thread it works in now (its current task's, else its
// current thread; shared-members.md §3), so paths, conversation and session follow that thread.
func atWork(ctx context.Context, q core.DB, e core.Employee) (core.Employee, error) {
	if !e.ProjectMember() {
		return e, nil
	}
	th, err := core.WorkThreadOf(ctx, q, e)
	e.CurrentThreadID = th
	return e, err
}

// NodeRemoved closes the sessions of a node that is being deleted; the next message to
// each employee opens a fresh session elsewhere with a handoff summary (§6.2).
func (o *Orchestrator) NodeRemoved(ctx context.Context, tx *core.Tx, nodeID string) error {
	live, err := core.NodeLiveSessions(ctx, tx, nodeID)
	if err != nil {
		return err
	}
	for _, s := range live {
		if err := core.CloseSession(ctx, tx, s.ID, "node_removed"); err != nil {
			return err
		}
	}
	return tx.Gorm(ctx).Table("at_employees").Where("node_id = ?", nodeID).Update("node_id", nil).Error
}

// recordDelivery stores the delivery in conv (its record, else a note addressed to the employee)
// with the msg_id, and emits it on conv's thread.
func (o *Orchestrator) recordDelivery(ctx context.Context, tx *core.Tx, e core.Employee, d Delivery, conv, msgID string) (core.Message, error) {
	rec := d.Record
	if rec == nil {
		rec = &core.NewMessage{ConversationID: conv, Role: "system", Author: "platform", Kind: "note", Content: d.Text,
			Meta: map[string]any{"to": e.Name}}
	}
	if rec.Meta == nil {
		rec.Meta = map[string]any{}
	}
	rec.Meta["msg_id"] = msgID
	var thread *string
	if no, err := core.ThreadNoOf(ctx, tx, &rec.ConversationID); err == nil && no != nil {
		rec.Meta["thread_no"], thread = *no, &rec.ConversationID
	}
	m, err := core.InsertMessage(ctx, tx, *rec)
	if err != nil {
		return m, err
	}
	return m, tx.EmitMessage(ctx, e.ProjectID, thread, m)
}

// conversationOf is the conversation an employee's messages belong to.
func (o *Orchestrator) conversationOf(ctx context.Context, tx *core.Tx, e core.Employee) (string, error) {
	if th := e.WorkThreadID(); th != nil {
		return *th, nil
	}
	userID, err := ownerOf(ctx, tx, e.ProjectID)
	if err != nil {
		return "", err
	}
	return core.EnsureCoordinatorConversation(ctx, tx, userID, e.ProjectID)
}

func ownerOf(ctx context.Context, q core.DB, projectID string) (string, error) {
	return core.ProjectOwner(ctx, q, projectID)
}

func (o *Orchestrator) send(ctx context.Context, tx *core.Tx, s core.AgentSession, msgID string, d Delivery) error {
	if s.NodeID == nil {
		return ErrNoNode
	}
	prio := d.Priority
	if prio == "" {
		prio = "next"
	}
	kind := protocol.SessionSendKind(orDefault(d.Kind, "system"))
	body := protocol.SessionSend{SessionID: protocol.SessionID(s.ID), MsgID: protocol.MsgID(msgID), Text: d.Text,
		Priority: protocol.SessionSendPriority(prio), Kind: &kind, InterruptFirst: d.Interrupt, Images: imageRefs(d.Images),
		Anchor: d.Anchor}
	_, err := o.gw.Enqueue(ctx, tx, *s.NodeID, nodegw.Command{Type: protocol.TypeSessionSend, Body: body,
		SessionID: s.ID, OpenGen: s.OpenGen})
	return err
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

// openSession creates a logical session on a node and queues git.prepare + session.open.
func (o *Orchestrator) openSession(ctx context.Context, tx *core.Tx, e core.Employee, first *protocol.FirstMessage,
	resume *protocol.ResumeRef) (core.AgentSession, error) {
	env, err := o.loadEnv(ctx, tx, e)
	if err != nil {
		return core.AgentSession{}, err
	}
	e = env.emp
	if err := o.pinThread(ctx, tx, e); err != nil {
		return core.AgentSession{}, err
	}
	worked, err := o.workedHere(ctx, tx, e)
	if err != nil {
		return core.AgentSession{}, err
	}
	node, err := o.pickNode(ctx, tx, e, env.project, resume != nil || worked)
	if err != nil {
		return core.AgentSession{}, err
	}
	if first, resume, err = o.moveResume(ctx, tx, e, node.ID, first, resume); err != nil {
		return core.AgentSession{}, err
	}
	paths := PathsOf(e, env.thread)
	sess, err := core.InsertSession(ctx, tx, core.NewSession{EmployeeID: e.ID, NodeID: node.ID, Runtime: e.Runtime,
		Cwd: paths.Cwd, Branch: paths.Branch, PermissionMode: "default", ThreadID: env.threadID()})
	if err != nil {
		return sess, err
	}
	if err := o.placeEmployee(ctx, tx, e, node.ID, o.checkoutPaths(ctx, tx, paths, env)); err != nil {
		return sess, err
	}
	return sess, o.queueOpen(ctx, tx, env, node, sess, first, resume)
}

// pinThread records the thread a project member opens a session in as its current thread.
func (o *Orchestrator) pinThread(ctx context.Context, tx *core.Tx, e core.Employee) error {
	if !e.ProjectMember() || e.CurrentThreadID == nil {
		return nil
	}
	return core.SetCurrentThread(ctx, tx, e.ID, e.CurrentThreadID)
}

// sessionEnv is what opening a session needs to know besides the employee.
type sessionEnv struct {
	emp     core.Employee
	project store.Project
	thread  *core.Thread
	repos   []ghops.Repo
}

// threadID is the thread the session works in (nil: coordinator).
func (env sessionEnv) threadID() *string {
	if env.thread == nil {
		return nil
	}
	return &env.thread.ID
}

// slugs lists the repositories checked out for the employee (none for the coordinator).
func (env sessionEnv) slugs() []string {
	if env.emp.Tier == "coordinator" || env.thread == nil {
		return nil
	}
	out := make([]string, len(env.repos))
	for i, r := range env.repos {
		out[i] = r.Slug
	}
	return out
}

// gitHosts are the standalone repositories' hosts (deduplicated, with their transport) the
// session's git may get credentials for (git-repos.md §5).
func (env sessionEnv) gitHosts() []protocol.SessionOpenGitHostsElem {
	var out []protocol.SessionOpenGitHostsElem
	seen := map[string]bool{}
	for _, r := range env.repos {
		g := r.GitRepo()
		if g == nil || g.CredentialID == nil || seen[g.Transport+"|"+g.Host] {
			continue
		}
		seen[g.Transport+"|"+g.Host] = true
		out = append(out, protocol.SessionOpenGitHostsElem{Transport: protocol.SessionOpenGitHostsElemTransport(g.Transport), Host: g.Host})
	}
	return out
}

// checkoutPaths are the per-repository checkouts of an employee (cleanup removes them).
func (o *Orchestrator) checkoutPaths(ctx context.Context, q core.DB, p Paths, env sessionEnv) []string {
	slugs := env.slugs()
	if len(slugs) == 0 {
		return []string{p.Cwd}
	}
	out := make([]string, len(slugs))
	for i, slug := range slugs {
		out[i] = o.CheckoutCwd(ctx, q, env.emp, *env.thread, slug, len(slugs))
	}
	return out
}

func (o *Orchestrator) loadEnv(ctx context.Context, tx *core.Tx, e core.Employee) (sessionEnv, error) {
	e, err := atWork(ctx, tx, e)
	if err != nil {
		return sessionEnv{emp: e}, err
	}
	env := sessionEnv{emp: e}
	if env.project, err = o.project(ctx, tx, e.ProjectID); err != nil {
		return env, err
	}
	if th := e.WorkThreadID(); th != nil {
		t, err := core.ThreadByID(ctx, tx, *th)
		if err != nil {
			return env, err
		}
		env.thread = &t
	}
	env.repos = o.ReposIn(ctx, tx, env.project)
	return env, nil
}

func (o *Orchestrator) placeEmployee(ctx context.Context, tx *core.Tx, e core.Employee, nodeID string, paths []string) error {
	if err := core.SetEmployeeNode(ctx, tx, e.ID, &nodeID); err != nil {
		return err
	}
	th := e.WorkThreadID()
	if th == nil {
		return nil
	}
	_, err := store.Exec(ctx, tx, `INSERT INTO at_thread_nodes (thread_id, node_id, paths) VALUES ($1, $2, $3::text[])
		ON CONFLICT (thread_id, node_id) DO UPDATE SET cleaned_at = NULL,
		paths = ARRAY(SELECT DISTINCT unnest(at_thread_nodes.paths || EXCLUDED.paths) ORDER BY 1)`,
		*th, nodeID, paths)
	return err
}

// queueOpen enqueues git.prepare (when the employee works in the repository) and session.open.
func (o *Orchestrator) queueOpen(ctx context.Context, tx *core.Tx, env sessionEnv, node store.Node, sess core.AgentSession,
	first *protocol.FirstMessage, resume *protocol.ResumeRef) error {
	prepare, err := o.queuePrepare(ctx, tx, env, node.ID, false)
	if err != nil {
		return err
	}
	system, err := o.systemAppend(ctx, tx, env.emp, env.thread, env.project)
	if err != nil {
		return err
	}
	rules, err := o.Rules(ctx, tx, env.emp.ProjectID, env.emp.WorkThreadID(), env.emp.Name)
	if err != nil {
		return err
	}
	window, err := core.LastContextWindow(ctx, tx, env.emp.ID, env.emp.Runtime)
	if err != nil {
		return err
	}
	open, err := buildOpen(sessionInput{emp: env.emp, thread: env.thread, project: env.project, node: node, sess: sess,
		prepare: prepare, first: first, resume: resume, system: system, rules: rules, hasRepo: len(env.repos) > 0,
		repos: env.slugs(), protRefs: o.protectedRefs(ctx, tx, env), gitHosts: env.gitHosts(),
		window: window})
	if err != nil {
		return err
	}
	if open.Anchor, err = openAnchor(ctx, tx, env.emp, first); err != nil {
		return err
	}
	// The servers the role is granted in the project (remote-servers.md §7.1); none for the
	// coordinator, the lead, read-only roles and daemons without ssh_gateway.
	if open.SshServers, err = sshgw.SessionServers(ctx, o.store, tx, node.ID, sess.ID); err != nil {
		return err
	}
	if err := o.addSkills(ctx, tx, env.project.UserID, env.emp, &open); err != nil {
		return err
	}
	if err := o.addPlugins(ctx, tx, env.project.UserID, env.emp, &open); err != nil {
		return err
	}
	if err := recordMode(ctx, tx, sess.ID, open); err != nil {
		return err
	}
	_, err = o.gw.Enqueue(ctx, tx, node.ID, nodegw.Command{Type: protocol.TypeSessionOpen, Body: open,
		SessionID: sess.ID, OpenGen: sess.OpenGen})
	o.touch(tx, env.emp.ID, "session.open")
	return err
}

// recordMode keeps the Claude mode the session opens in, so SetMode knows what it runs in.
func recordMode(ctx context.Context, tx *core.Tx, sessionID string, open protocol.SessionOpen) error {
	if open.Claude == nil {
		return nil
	}
	return setPermissionMode(ctx, tx, sessionID, string(open.Claude.PermissionMode))
}

// setPermissionMode records the permission mode a session runs in.
func setPermissionMode(ctx context.Context, tx *core.Tx, sessionID, mode string) error {
	return tx.Gorm(ctx).Table("at_agent_sessions").Where("id = ?", sessionID).Update("permission_mode", mode).Error
}

// protectedRefs are the thread branch and the thread's base and default branch in every repository.
func (o *Orchestrator) protectedRefs(ctx context.Context, q core.DB, env sessionEnv) []string {
	refs := []string{}
	if env.thread == nil {
		return refs
	}
	refs = append(refs, env.thread.Branch)
	for _, r := range env.repos {
		for _, b := range []string{o.BaseFor(ctx, q, *env.thread, r), o.DefaultBranch(ctx, r)} {
			if b != "" && !slices.Contains(refs, b) {
				refs = append(refs, b)
			}
		}
	}
	return refs
}

// queuePrepare queues one git.prepare per repository for an employee's checkouts; nil when it needs none.
func (o *Orchestrator) queuePrepare(ctx context.Context, tx *core.Tx, env sessionEnv, nodeID string, fetchOnly bool) ([]string, error) {
	if env.emp.Tier == "coordinator" || env.thread == nil {
		return nil, nil
	}
	jobs := make([]string, 0, len(env.repos))
	for _, r := range env.repos {
		job, err := o.prepareRepo(ctx, tx, env, nodeID, r, prepareOpts{fetchOnly: fetchOnly})
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

// prepareOpts: fetchOnly marks a refresh of an open session; sync (with its meta) brings an
// existing worktree up to date before a task (shared-members.md §4.2).
type prepareOpts struct {
	fetchOnly bool
	sync      *protocol.GitPrepareSync
	meta      map[string]any
}

// prepareRepo queues git.prepare of one repository: the employee's branch, started from the
// thread branch when it exists in that repository, else from the thread's base there.
func (o *Orchestrator) prepareRepo(ctx context.Context, tx *core.Tx, env sessionEnv, nodeID string, r ghops.Repo, opts prepareOpts) (string, error) {
	p := PathsOf(env.emp, env.thread)
	base := o.startPoint(ctx, tx, *env.thread, r)
	mode := protocol.GitPrepareMode(orDefault(env.project.WorkMode, "worktree"))
	job := core.NewID("g")
	cwd := o.CheckoutCwd(ctx, tx, env.emp, *env.thread, r.Slug, len(env.repos))
	body := protocol.GitPrepare{JobID: protocol.JobID(job), ProjectID: env.project.ID, Repo: protocol.Repo(r.NodeRepo()),
		RemoteURL: o.gh.NodeRemote(r), Mode: mode, Cwd: protocol.RelPath(cwd), Branch: p.Branch, Base: &base, Fetch: true, Sync: opts.sync}
	if mode == "worktree" {
		shared := protocol.RelPath(sharedFor(env.project.ID, r.Slug, cwd == p.Cwd))
		body.Shared = &shared
	}
	meta := map[string]any{"purpose": "prepare", "employee_id": env.emp.ID, "fetch_only": opts.fetchOnly, "repo": r.Slug}
	maps.Copy(meta, opts.meta)
	_, err := o.gw.Enqueue(ctx, tx, nodeID, nodegw.Command{Type: protocol.TypeGitPrepare, Body: body, Meta: meta})
	return job, err
}

// FeatureGitSync is the hello feature of daemons that sync an existing worktree (git.prepare sync).
const FeatureGitSync = "git_sync"

// FeatureReviewSHA is the hello feature of daemons that check out and verify a review sync's
// expect_sha (fetching again while the commit is not there yet). Older daemons reject the field.
const FeatureReviewSHA = "git_review_sha"

// QueueSync queues one git.prepare with sync per repository of the employee's thread, so the
// task about to start sees the current thread branch (member) or the branch under review
// (review, at the commits in expect: repository → sha, see ExpectedSHA); meta comes back with
// each result. It returns how many jobs it queued: 0 when the daemon cannot sync, the project
// checks out full clones, or (member) there is nothing to sync yet (no session in this thread
// so far: it will be opened and prepared anyway). A review syncs even then: the job creates
// the worktree, on the node the session will open on, and checks out the branch under review
// (a reviewer's first review in a thread must not see the thread branch instead).
func (o *Orchestrator) QueueSync(ctx context.Context, tx *core.Tx, e core.Employee, sync protocol.GitPrepareSync,
	expect map[string]string, meta map[string]any) (int, error) {
	review := sync.Mode == protocol.GitPrepareSyncModeReview
	worked, err := o.workedHere(ctx, tx, e)
	if err != nil || (!worked && !review) {
		return 0, err
	}
	env, err := o.loadEnv(ctx, tx, e)
	if err != nil || env.thread == nil || env.project.WorkMode == "clone" || len(env.repos) == 0 {
		return 0, err
	}
	nodeID, err := o.syncNode(ctx, tx, env, worked)
	if err != nil || nodeID == "" {
		return 0, err
	}
	if ok, err := core.NodeHasFeature(ctx, tx, nodeID, FeatureGitSync); err != nil || !ok {
		return 0, err
	}
	withSHA, err := core.NodeHasFeature(ctx, tx, nodeID, FeatureReviewSHA)
	if err != nil {
		return 0, err
	}
	if !worked {
		p := PathsOf(env.emp, env.thread)
		if err := o.placeEmployee(ctx, tx, env.emp, nodeID, o.checkoutPaths(ctx, tx, p, env)); err != nil {
			return 0, err
		}
	}
	meta = maps.Clone(meta)
	meta["purpose"] = "sync"
	for _, r := range env.repos {
		s := sync
		if want := ExpectedSHA(expect, r.Slug, len(env.repos)); review && withSHA && fullSHA(want) {
			s.ExpectSHA = &want
		}
		if _, err := o.prepareRepo(ctx, tx, env, nodeID, r, prepareOpts{sync: &s, meta: meta}); err != nil {
			return 0, err
		}
	}
	return len(env.repos), nil
}

// syncNode is the node a pre-task sync runs on: the employee's, or for a review in a thread it
// has no session in yet the node its session will open on ("" when there is none).
func (o *Orchestrator) syncNode(ctx context.Context, tx *core.Tx, env sessionEnv, worked bool) (string, error) {
	if worked {
		return *env.emp.NodeID, nil
	}
	node, err := o.pickNode(ctx, tx, env.emp, env.project, false)
	if errors.Is(err, ErrNoNode) {
		return "", nil
	}
	return node.ID, err
}

// ExpectedSHA is the commit expected in repository repo of a review: expect[repo], or for a
// single-repository project the head recorded without a repository (key "").
func ExpectedSHA(expect map[string]string, repo string, repos int) string {
	if sha, ok := expect[repo]; ok {
		return sha
	}
	if repos == 1 {
		return expect[""]
	}
	return ""
}

func fullSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// workedHere reports whether the employee has a node and opened a session in the thread it
// works in before (so its worktree there exists).
func (o *Orchestrator) workedHere(ctx context.Context, tx *core.Tx, e core.Employee) (bool, error) {
	if e.NodeID == nil {
		return false, nil
	}
	last, err := core.LatestSession(ctx, tx, e.ID)
	if errors.Is(err, core.ErrNotFound) {
		return false, nil
	}
	return err == nil && sameThread(last.ThreadID, e.WorkThreadID()), err
}

func sameThread(a, b *string) bool {
	return a != nil && b != nil && *a == *b
}

// sharedFor is the shared clone a worktree hangs off: the legacy single clone for legacy checkouts.
func sharedFor(projectID, slug string, legacy bool) string {
	if legacy {
		return LegacySharedDir(projectID)
	}
	return SharedDir(projectID, slug)
}

// RefreshWorktree queues a fetch for a read-only session's worktree before review messages (§10.2).
func (o *Orchestrator) RefreshWorktree(ctx context.Context, tx *core.Tx, e core.Employee) error {
	if !readOnly(e) {
		return nil
	}
	if ok, err := o.workedHere(ctx, tx, e); err != nil || !ok {
		return err
	}
	env, err := o.loadEnv(ctx, tx, e)
	if err != nil {
		return err
	}
	_, err = o.queuePrepare(ctx, tx, env, *e.NodeID, true)
	return err
}

// moveResume turns a resume into a recap when the session opens on another node than the one
// that holds the old conversation's transcript.
func (o *Orchestrator) moveResume(ctx context.Context, tx *core.Tx, e core.Employee, nodeID string,
	first *protocol.FirstMessage, resume *protocol.ResumeRef) (*protocol.FirstMessage, *protocol.ResumeRef, error) {
	if resume == nil || e.NodeID == nil || *e.NodeID == nodeID {
		return first, resume, nil
	}
	summary, err := o.handoffSummary(ctx, tx, e)
	if err != nil {
		return first, resume, err
	}
	moved := protocol.FirstMessage{MsgID: protocol.MsgID(core.NewID("m")), Text: summary}
	if first != nil {
		moved = *first
		moved.Text = summary + "\n\n---\n" + strings.TrimPrefix(first.Text, reopenedPrefix)
	}
	return &moved, nil, o.resumedNote(ctx, tx, e)
}

// reopen starts a new open generation of the same logical session with resume.
func (o *Orchestrator) reopen(ctx context.Context, tx *core.Tx, e core.Employee, s core.AgentSession, first *protocol.FirstMessage) error {
	gen, err := core.BumpOpenGen(ctx, tx, s.ID)
	if err != nil {
		return err
	}
	if err := core.SupersedeOldGen(ctx, tx, s.ID, gen); err != nil {
		return err
	}
	s.OpenGen, s.State = gen, "opening"
	env, err := o.loadEnv(ctx, tx, e)
	if err != nil {
		return err
	}
	node, err := o.store.On(tx).NodeOf(ctx, env.project.UserID, deref(s.NodeID))
	if err != nil {
		return o.handoff(ctx, tx, e, s, first)
	}
	return o.queueOpen(ctx, tx, env, node, s, first, resumeRef(s))
}

func resumeRef(s core.AgentSession) *protocol.ResumeRef {
	if s.SDKSessionID == nil && s.CodexThreadID == nil {
		return nil
	}
	return &protocol.ResumeRef{SDKSessionID: s.SDKSessionID, CodexThreadID: s.CodexThreadID}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// handoff closes a session and opens a fresh one whose first message is the recap (§6.2):
// the node lost the transcript (resume_failed) or the session had to move. Messages that were
// on their way to the old session (its first message and undelivered sends) follow the recap
// in the same first message, and the chat gets a quiet "会话已恢复" note.
func (o *Orchestrator) handoff(ctx context.Context, tx *core.Tx, e core.Employee, s core.AgentSession, pending *protocol.FirstMessage) error {
	if err := o.closeSession(ctx, tx, s, "employee_removed"); err != nil {
		return err
	}
	summary, err := o.handoffSummary(ctx, tx, e)
	if err != nil {
		return err
	}
	queued, err := o.inFlight(ctx, tx, s, pending)
	if err != nil {
		return err
	}
	first := protocol.FirstMessage{MsgID: protocol.MsgID(core.NewID("m")), Text: summary}
	if len(queued) > 0 {
		first.MsgID = queued[0].MsgID
		var texts []string
		for _, q := range queued {
			texts = append(texts, q.Text)
			first.Images = append(first.Images, q.Images...)
		}
		first.Text += "\n\n---\n" + strings.Join(texts, "\n\n---\n")
		if len(first.Images) > 8 {
			first.Images = first.Images[:8]
		}
		if err := markForwarded(ctx, tx, queued[1:]); err != nil {
			return err
		}
	}
	if err := core.SetEmployeeNode(ctx, tx, e.ID, nil); err != nil {
		return err
	}
	e.NodeID = nil
	if _, err := o.openSession(ctx, tx, e, &first, nil); err != nil {
		return err
	}
	return o.resumedNote(ctx, tx, e)
}

// inFlight lists the messages the old session never got: the pending first message (or the
// first message of its last session.open) and its undelivered session.send commands, in order.
func (o *Orchestrator) inFlight(ctx context.Context, tx *core.Tx, s core.AgentSession, pending *protocol.FirstMessage) ([]protocol.FirstMessage, error) {
	rows, err := store.Query(ctx, tx, `SELECT CASE WHEN c.type = 'session.open' THEN c.body->'first_message' ELSE c.body END
		FROM at_node_commands c
		WHERE c.session_id = $1 AND (c.type = 'session.send' OR (c.type = 'session.open' AND jsonb_exists(c.body, 'first_message')
			AND c.body->'first_message' <> 'null'::jsonb))
		AND EXISTS (SELECT 1 FROM at_messages m WHERE m.meta->>'msg_id' =
			COALESCE(c.body->'first_message'->>'msg_id', c.body->>'msg_id') AND m.delivered_at IS NULL)
		ORDER BY c.created_at, c.seq`, s.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.FirstMessage
	seen := map[protocol.MsgID]bool{}
	if pending != nil {
		out, seen[pending.MsgID] = append(out, *pending), true
	}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var m protocol.FirstMessage
		if json.Unmarshal(raw, &m) != nil || m.MsgID == "" || seen[m.MsgID] {
			continue
		}
		seen[m.MsgID] = true
		out = append(out, m)
	}
	return out, rows.Err()
}

// markForwarded stamps messages folded into a recap as delivered (the recap carries them).
func markForwarded(ctx context.Context, tx *core.Tx, msgs []protocol.FirstMessage) error {
	for _, m := range msgs {
		if _, _, err := core.MarkDelivered(ctx, tx, string(m.MsgID)); err != nil {
			return err
		}
	}
	return nil
}

// resumedNote tells the user, quietly, that the coordinator's or lead's session was rebuilt.
func (o *Orchestrator) resumedNote(ctx context.Context, tx *core.Tx, e core.Employee) error {
	if e.Tier == "member" {
		return nil
	}
	conv, err := o.conversationOf(ctx, tx, e)
	if err != nil {
		return err
	}
	return o.recordCard(ctx, tx, e, conv, CardResumed, "会话已恢复", map[string]any{"employee": e.Name})
}

// imageRefs turns message attachments into session.send images.
func imageRefs(list []core.AttachmentRef) []protocol.ImageRef {
	if len(list) == 0 {
		return nil
	}
	out := make([]protocol.ImageRef, len(list))
	for i, a := range list {
		name, size := a.Name, a.Size
		out[i] = protocol.ImageRef{ID: a.ID, MediaType: protocol.ImageRefMediaType(a.ContentType), Name: &name, Size: &size}
	}
	return out
}

// closeSession marks the session closed and tells its node.
func (o *Orchestrator) closeSession(ctx context.Context, tx *core.Tx, s core.AgentSession, reason string) error {
	if err := core.CloseSession(ctx, tx, s.ID, reason); err != nil {
		return err
	}
	if s.NodeID == nil {
		return nil
	}
	r := protocol.SessionCloseReason(wireCloseReason(ctx, tx, *s.NodeID, reason))
	gen := protocol.OpenGen(s.OpenGen)
	_, err := o.gw.Enqueue(ctx, tx, *s.NodeID, nodegw.Command{Type: protocol.TypeSessionClose,
		Body: protocol.SessionClose{SessionID: protocol.SessionID(s.ID), OpenGen: &gen, Reason: &r}, SessionID: s.ID, OpenGen: s.OpenGen})
	tx.AfterCommit(func(context.Context) { o.status.Forget(s.EmployeeID) })
	return err
}

// CloseSession exposes closeSession.
func (o *Orchestrator) CloseSession(ctx context.Context, tx *core.Tx, s core.AgentSession, reason string) error {
	return o.closeSession(ctx, tx, s, reason)
}

// Interrupt stops the employee's current turn.
func (o *Orchestrator) Interrupt(ctx context.Context, tx *core.Tx, e core.Employee, reason string) error {
	s, err := core.LiveSession(ctx, tx, e.ID)
	if errors.Is(err, core.ErrNotFound) || (err == nil && s.NodeID == nil) {
		return nil // no session: nothing is running
	}
	if err != nil {
		return err
	}
	r := protocol.SessionInterruptReason(reason)
	_, err = o.gw.Enqueue(ctx, tx, *s.NodeID, nodegw.Command{Type: protocol.TypeSessionInterrupt,
		Body: protocol.SessionInterrupt{SessionID: protocol.SessionID(s.ID), Reason: &r}, SessionID: s.ID, OpenGen: s.OpenGen})
	return err
}

// SetMode changes a live Claude session's permission mode (thread auto-approve).
func (o *Orchestrator) SetMode(ctx context.Context, tx *core.Tx, s core.AgentSession, mode string) error {
	if s.NodeID == nil || s.Runtime != "claude" || s.PermissionMode == mode {
		return nil
	}
	m := protocol.SessionSetModePermissionMode(mode)
	if err := setPermissionMode(ctx, tx, s.ID, mode); err != nil {
		return err
	}
	_, err := o.gw.Enqueue(ctx, tx, *s.NodeID, nodegw.Command{Type: protocol.TypeSessionSetMode,
		Body: protocol.SessionSetMode{SessionID: protocol.SessionID(s.ID), PermissionMode: &m}, SessionID: s.ID, OpenGen: s.OpenGen})
	return err
}
