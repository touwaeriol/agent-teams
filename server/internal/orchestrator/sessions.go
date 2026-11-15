package orchestrator

import (
	"context"
	"errors"
	"slices"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/ghops"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Delivery is one message for an employee's session (architecture.md §6.4).
type Delivery struct {
	Text      string
	Kind      string // user | task | mail | answer | system
	Priority  string // now | next | later
	Interrupt bool
	// Record is the conversation message to store with the msg_id; nil stores a note
	// in the employee's conversation addressed to it (meta.to).
	Record *core.NewMessage
}

// Deliver records the message and sends it to the employee's session, opening one when needed.
func (o *Orchestrator) Deliver(ctx context.Context, tx *core.Tx, e core.Employee, d Delivery) (core.Message, error) {
	msgID := core.NewID("m")
	m, err := o.recordDelivery(ctx, tx, e, d, msgID)
	if err != nil {
		return m, err
	}
	sess, err := core.LiveSession(ctx, tx, e.ID)
	switch {
	case errors.Is(err, core.ErrNotFound):
		text, err := o.withHandoff(ctx, tx, e, d.Text)
		if err != nil {
			return m, err
		}
		_, err = o.openSession(ctx, tx, e, &protocol.FirstMessage{MsgID: protocol.MsgID(msgID), Text: text}, nil)
		return m, err
	case err != nil:
		return m, err
	case sess.State == "crashed":
		return m, o.reopen(ctx, tx, e, sess, &protocol.FirstMessage{MsgID: protocol.MsgID(msgID), Text: d.Text})
	}
	return m, o.send(ctx, tx, sess, msgID, d)
}

// withHandoff prefixes the handoff summary when the previous session was lost with its node.
func (o *Orchestrator) withHandoff(ctx context.Context, tx *core.Tx, e core.Employee, text string) (string, error) {
	last, err := core.LatestSession(ctx, tx, e.ID)
	if errors.Is(err, core.ErrNotFound) || (err == nil && last.CloseReason != "node_removed") {
		return text, nil
	}
	if err != nil {
		return "", err
	}
	summary, err := o.handoffSummary(ctx, tx, e)
	return summary + "\n\n---\n" + text, err
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
	_, err = tx.Exec(ctx, `UPDATE at_employees SET node_id = NULL WHERE node_id = $1`, nodeID)
	return err
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
	if e.ThreadID != nil {
		if no, err := core.ThreadNoOf(ctx, tx, e.ThreadID); err == nil && no != nil {
			rec.Meta["thread_no"] = *no
		}
	}
	m, err := core.InsertMessage(ctx, tx, *rec)
	if err != nil {
		return m, err
	}
	return m, tx.EmitMessage(ctx, e.ProjectID, e.ThreadID, m)
}

// conversationOf is the conversation an employee's messages belong to.
func (o *Orchestrator) conversationOf(ctx context.Context, tx *core.Tx, e core.Employee) (string, error) {
	if e.ThreadID != nil {
		return *e.ThreadID, nil
	}
	userID, err := ownerOf(ctx, tx, e.ProjectID)
	if err != nil {
		return "", err
	}
	return core.EnsureCoordinatorConversation(ctx, tx, userID, e.ProjectID)
}

func ownerOf(ctx context.Context, q core.DB, projectID string) (string, error) {
	var uid string
	err := q.QueryRow(ctx, `SELECT user_id FROM at_projects WHERE id = $1`, projectID).Scan(&uid)
	return uid, core.MapErr(err)
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
		Priority: protocol.SessionSendPriority(prio), Kind: &kind, InterruptFirst: d.Interrupt}
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
	node, err := o.pickNode(ctx, tx, e, env.project)
	if err != nil {
		return core.AgentSession{}, err
	}
	paths := PathsOf(e, env.thread)
	sess, err := core.InsertSession(ctx, tx, core.NewSession{EmployeeID: e.ID, NodeID: node.ID, Runtime: e.Runtime,
		Cwd: paths.Cwd, Branch: paths.Branch, PermissionMode: "default"})
	if err != nil {
		return sess, err
	}
	if err := o.placeEmployee(ctx, tx, e, node.ID, paths); err != nil {
		return sess, err
	}
	return sess, o.queueOpen(ctx, tx, env, node, sess, first, resume)
}

// sessionEnv is what opening a session needs to know besides the employee.
type sessionEnv struct {
	emp     core.Employee
	project store.Project
	thread  *core.Thread
	repo    *ghops.Repo
}

func (o *Orchestrator) loadEnv(ctx context.Context, tx *core.Tx, e core.Employee) (sessionEnv, error) {
	env := sessionEnv{emp: e}
	var err error
	if env.project, err = o.project(ctx, tx, e.ProjectID); err != nil {
		return env, err
	}
	if e.ThreadID != nil {
		t, err := core.ThreadByID(ctx, tx, *e.ThreadID)
		if err != nil {
			return env, err
		}
		env.thread = &t
	}
	if o.gh != nil {
		if repo, err := o.gh.RepoOf(ctx, env.project); err == nil {
			env.repo = &repo
		}
	}
	return env, nil
}

func (o *Orchestrator) placeEmployee(ctx context.Context, tx *core.Tx, e core.Employee, nodeID string, p Paths) error {
	if err := core.SetEmployeeNode(ctx, tx, e.ID, &nodeID); err != nil {
		return err
	}
	if e.ThreadID == nil {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO at_thread_nodes (thread_id, node_id, paths) VALUES ($1, $2, ARRAY[$3])
		ON CONFLICT (thread_id, node_id) DO UPDATE SET cleaned_at = NULL,
		paths = CASE WHEN $3 = ANY(at_thread_nodes.paths) THEN at_thread_nodes.paths ELSE at_thread_nodes.paths || $3 END`,
		*e.ThreadID, nodeID, p.Cwd)
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
	rules, err := o.Rules(ctx, tx, env.emp.ProjectID, env.emp.ThreadID, env.emp.Name)
	if err != nil {
		return err
	}
	open, err := buildOpen(sessionInput{emp: env.emp, thread: env.thread, project: env.project, node: node, sess: sess,
		prepare: prepare, first: first, resume: resume, system: system, rules: rules, hasRepo: env.repo != nil,
		protRefs: protectedRefs(env)})
	if err != nil {
		return err
	}
	_, err = o.gw.Enqueue(ctx, tx, node.ID, nodegw.Command{Type: protocol.TypeSessionOpen, Body: open,
		SessionID: sess.ID, OpenGen: sess.OpenGen})
	o.touch(tx, env.emp.ID, "session.open")
	return err
}

func protectedRefs(env sessionEnv) []string {
	refs := []string{}
	if env.thread != nil {
		refs = append(refs, env.thread.Branch)
	}
	if env.project.Branch != "" {
		refs = append(refs, env.project.Branch)
	}
	return refs
}

// queuePrepare queues git.prepare for an employee's worktree; "" when it needs none.
func (o *Orchestrator) queuePrepare(ctx context.Context, tx *core.Tx, env sessionEnv, nodeID string, fetchOnly bool) (string, error) {
	if env.emp.Tier == "coordinator" || env.thread == nil || env.repo == nil {
		return "", nil
	}
	p := PathsOf(env.emp, env.thread)
	base := p.Base
	if env.emp.Tier == "lead" {
		base = env.thread.BaseBranch
	}
	mode := protocol.GitPrepareMode(orDefault(env.project.WorkMode, "worktree"))
	job := core.NewID("g")
	body := protocol.GitPrepare{JobID: protocol.JobID(job), ProjectID: env.project.ID, Repo: protocol.Repo(env.repo.Slug),
		Mode: mode, Cwd: protocol.RelPath(p.Cwd), Branch: p.Branch, Base: &base, Fetch: true}
	if mode == "worktree" {
		shared := protocol.RelPath(p.Shared)
		body.Shared = &shared
	}
	_, err := o.gw.Enqueue(ctx, tx, nodeID, nodegw.Command{Type: protocol.TypeGitPrepare, Body: body,
		Meta: map[string]any{"purpose": "prepare", "employee_id": env.emp.ID, "fetch_only": fetchOnly}})
	return job, err
}

// RefreshWorktree queues a fetch for a read-only session's worktree before review messages (§10.2).
func (o *Orchestrator) RefreshWorktree(ctx context.Context, tx *core.Tx, e core.Employee) error {
	if !readOnly(e) || e.NodeID == nil {
		return nil
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
	cands := candidates(e, nodes)
	if len(cands) == 0 {
		return store.Node{}, ErrNoNode
	}
	return o.leastLoaded(ctx, tx, cands), nil
}

func candidates(e core.Employee, nodes []store.Node) []store.Node {
	var picked []store.Node
	for _, n := range nodes {
		if len(e.Spec.NodeIDs) > 0 && !slices.Contains(e.Spec.NodeIDs, n.ID) {
			continue
		}
		if e.Runtime == "codex" && !(n.Isolated || n.Isolation == "agent_user") {
			continue
		}
		picked = append(picked, n)
	}
	if len(picked) == 0 && len(e.Spec.NodeIDs) > 0 && e.Runtime != "codex" {
		return nodes // configured nodes were deleted: fall back to any of the user's nodes
	}
	return picked
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
		if free := o.gw.Capacity(n) - active; free > bestFree {
			best, bestFree = n, free
		}
	}
	return best
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

// handoff closes a session and opens a fresh one whose first message is the handoff summary (§6.2).
func (o *Orchestrator) handoff(ctx context.Context, tx *core.Tx, e core.Employee, s core.AgentSession, pending *protocol.FirstMessage) error {
	if err := o.closeSession(ctx, tx, s, "employee_removed"); err != nil {
		return err
	}
	summary, err := o.handoffSummary(ctx, tx, e)
	if err != nil {
		return err
	}
	text := summary
	msgID := core.NewID("m")
	if pending != nil {
		text += "\n\n---\n" + pending.Text
		msgID = string(pending.MsgID)
	}
	if err := core.SetEmployeeNode(ctx, tx, e.ID, nil); err != nil {
		return err
	}
	e.NodeID = nil
	_, err = o.openSession(ctx, tx, e, &protocol.FirstMessage{MsgID: protocol.MsgID(msgID), Text: text}, nil)
	return err
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
	if _, err := tx.Exec(ctx, `UPDATE at_agent_sessions SET permission_mode = $2 WHERE id = $1`, s.ID, mode); err != nil {
		return err
	}
	_, err := o.gw.Enqueue(ctx, tx, *s.NodeID, nodegw.Command{Type: protocol.TypeSessionSetMode,
		Body: protocol.SessionSetMode{SessionID: protocol.SessionID(s.ID), PermissionMode: &m}, SessionID: s.ID, OpenGen: s.OpenGen})
	return err
}
