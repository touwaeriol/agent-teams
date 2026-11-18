package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/composer"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/ghops"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
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
}

// Deliver records the message and sends it to the employee's session, opening one when needed.
func (o *Orchestrator) Deliver(ctx context.Context, tx *core.Tx, e core.Employee, d Delivery) (core.Message, error) {
	msgID := core.NewID("m")
	e, err := atWork(ctx, tx, e)
	if err != nil {
		return core.Message{}, err
	}
	if e, err = o.engage(ctx, tx, e, d.Record); err != nil {
		return core.Message{}, err
	}
	m, err := o.recordDelivery(ctx, tx, e, d, msgID)
	if err != nil {
		return m, err
	}
	if ended, err := o.threadEnded(ctx, tx, e); err != nil || ended {
		return m, err // a finished or archived thread's employees are not woken: the message is only kept
	}
	if e.ProjectMember() && e.CurrentThreadID == nil {
		return m, nil // a project member without a thread has no worktree to wake in: its next task brings it
	}
	o.wake(tx, e.ID)
	sess, err := core.LiveSession(ctx, tx, e.ID)
	switch {
	case errors.Is(err, core.ErrNotFound):
		text, resume, err := o.continuation(ctx, tx, e, d.Text)
		if err != nil {
			return m, err
		}
		_, err = o.openSession(ctx, tx, e, &protocol.FirstMessage{MsgID: protocol.MsgID(msgID), Text: text, Images: imageRefs(d.Images)}, resume)
		return m, err
	case err != nil:
		return m, err
	case sess.State == "crashed":
		return m, o.reopen(ctx, tx, e, sess, &protocol.FirstMessage{MsgID: protocol.MsgID(msgID), Text: d.Text, Images: imageRefs(d.Images)})
	}
	return m, o.send(ctx, tx, sess, msgID, d)
}

// engage brings a project member that works in no thread into the thread a message to it is
// recorded in (the user or a lead talks to it there).
func (o *Orchestrator) engage(ctx context.Context, tx *core.Tx, e core.Employee, rec *core.NewMessage) (core.Employee, error) {
	if !e.ProjectMember() || e.CurrentThreadID != nil || rec == nil {
		return e, nil
	}
	t, err := core.ThreadByID(ctx, tx, rec.ConversationID)
	if errors.Is(err, core.ErrNotFound) || (err == nil && t.ProjectID != e.ProjectID) {
		return e, nil
	}
	if err != nil {
		return e, err
	}
	return o.MoveToThread(ctx, tx, e, t.ID)
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
	if movedThread(e, last) {
		// A project member's session belongs to one thread's worktree: a fresh one, with a digest.
		digest, err := o.switchDigest(ctx, tx, e, *e.CurrentThreadID)
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

// movedThread reports whether a project member's last session was in another thread than the
// one it works in now.
func movedThread(e core.Employee, last core.AgentSession) bool {
	return e.ProjectMember() && last.ThreadID != nil && e.CurrentThreadID != nil && !sameThread(last.ThreadID, e.CurrentThreadID)
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

func (o *Orchestrator) recordDelivery(ctx context.Context, tx *core.Tx, e core.Employee, d Delivery, msgID string) (core.Message, error) {
	rec := d.Record
	if rec == nil {
		conv, err := o.conversationOf(ctx, tx, e)
		if err != nil {
			return core.Message{}, err
		}
		rec = &core.NewMessage{ConversationID: conv, Role: "system", Author: "platform", Kind: "note", Content: d.Text,
			Meta: map[string]any{"to": e.Name}}
	}
	if rec.Meta == nil {
		rec.Meta = map[string]any{}
	}
	rec.Meta["msg_id"] = msgID
	if th := e.WorkThreadID(); th != nil {
		if no, err := core.ThreadNoOf(ctx, tx, th); err == nil && no != nil {
			rec.Meta["thread_no"] = *no
		}
	}
	m, err := core.InsertMessage(ctx, tx, *rec)
	if err != nil {
		return m, err
	}
	return m, tx.EmitMessage(ctx, e.ProjectID, e.WorkThreadID(), m)
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
		Priority: protocol.SessionSendPriority(prio), Kind: &kind, InterruptFirst: d.Interrupt, Images: imageRefs(d.Images)}
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
	node, err := o.pickNode(ctx, tx, e, env.project)
	if err != nil {
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
	env.repos = o.Repos(ctx, env.project)
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
	open, err := buildOpen(sessionInput{emp: env.emp, thread: env.thread, project: env.project, node: node, sess: sess,
		prepare: prepare, first: first, resume: resume, system: system, rules: rules, hasRepo: len(env.repos) > 0,
		repos: env.slugs(), protRefs: o.protectedRefs(ctx, tx, env), defModel: composer.DefaultModel(ctx, tx, env.project.UserID, env.emp.Runtime)})
	if err != nil {
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
	body := protocol.GitPrepare{JobID: protocol.JobID(job), ProjectID: env.project.ID, Repo: protocol.Repo(r.Slug),
		Mode: mode, Cwd: protocol.RelPath(cwd), Branch: p.Branch, Base: &base, Fetch: true, Sync: opts.sync}
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

// QueueSync queues one git.prepare with sync per repository of the employee's thread, so the
// task about to start sees the current thread branch (member) or the branch under review
// (review); meta comes back with each result. It returns how many jobs it queued: 0 when there
// is nothing to sync yet (no session in this thread so far: it will be opened and prepared
// anyway), the daemon cannot sync, or the project checks out full clones.
func (o *Orchestrator) QueueSync(ctx context.Context, tx *core.Tx, e core.Employee, sync protocol.GitPrepareSync, meta map[string]any) (int, error) {
	if ok, err := o.workedHere(ctx, tx, e); err != nil || !ok {
		return 0, err
	}
	if ok, err := core.NodeHasFeature(ctx, tx, *e.NodeID, FeatureGitSync); err != nil || !ok {
		return 0, err
	}
	env, err := o.loadEnv(ctx, tx, e)
	if err != nil || env.thread == nil || env.project.WorkMode == "clone" {
		return 0, err
	}
	meta = maps.Clone(meta)
	meta["purpose"] = "sync"
	for _, r := range env.repos {
		if _, err := o.prepareRepo(ctx, tx, env, *e.NodeID, r, prepareOpts{sync: &sync, meta: meta}); err != nil {
			return 0, err
		}
	}
	return len(env.repos), nil
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

// pickNode chooses the node for an employee: its current node if still there, else
// the online candidate with the most free slots, else the first candidate.
func (o *Orchestrator) pickNode(ctx context.Context, tx *core.Tx, e core.Employee, p store.Project) (store.Node, error) {
	nodes, err := o.store.ListNodes(ctx, p.UserID)
	if err != nil {
		return store.Node{}, err
	}
	if e.NodeID != nil {
		for _, n := range nodes {
			if n.ID == *e.NodeID {
				return n, nil
			}
		}
	}
	cands := core.NodeCandidates(e, nodes)
	if len(cands) == 0 {
		return store.Node{}, ErrNoNode
	}
	return o.leastLoaded(ctx, tx, cands), nil
}

func (o *Orchestrator) leastLoaded(ctx context.Context, tx *core.Tx, cands []store.Node) store.Node {
	best, bestFree := cands[0], -1<<30
	for _, n := range cands {
		if !o.gw.Online(n.ID) {
			continue
		}
		active, err := core.ActiveSessionCount(ctx, tx, n.ID)
		if err != nil {
			continue
		}
		if free := freeSlots(o.gw.Capacity(n), active); free > bestFree {
			best, bestFree = n, free
		}
	}
	return best
}

// freeSlots ranks a node: an unlimited node (capacity 0) counts as far roomier than any capped one.
func freeSlots(capacity, active int) int {
	if capacity <= 0 {
		capacity = 1 << 20
	}
	return capacity - active
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
	node, err := o.store.NodeOf(ctx, env.project.UserID, deref(s.NodeID))
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
	r := protocol.SessionCloseReason(reason)
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
