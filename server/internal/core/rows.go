package core

import (
	"encoding/json"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Thread is a row of at_threads.
type Thread struct {
	ID            string
	ProjectID     string
	UserID        string
	No            int
	Title         string
	Brief         string
	Status        string
	PauseReason   string
	Branch        string
	BaseBranch    string
	PRNumber      *int
	PRURL         *string
	PRState       string
	Summary       string
	AutoApprove   bool
	OpenedBy      string
	RolesSnapshot []store.Role
	WaitingCount  int
	CreatedAt     time.Time
	UpdatedAt     time.Time
	StartedAt     *time.Time
	FinishedAt    *time.Time
}

const threadCols = `id, project_id, user_id, no, title, brief, status, pause_reason, branch, base_branch, pr_number,
	pr_url, pr_state, summary, auto_approve, opened_by, roles_snapshot, waiting_count, created_at, updated_at,
	started_at, finished_at`

func scanThread(r Scanner) (Thread, error) {
	var t Thread
	err := r.Scan(&t.ID, &t.ProjectID, &t.UserID, &t.No, &t.Title, &t.Brief, &t.Status, &t.PauseReason, &t.Branch,
		&t.BaseBranch, &t.PRNumber, &t.PRURL, &t.PRState, &t.Summary, &t.AutoApprove, &t.OpenedBy, &t.RolesSnapshot,
		&t.WaitingCount, &t.CreatedAt, &t.UpdatedAt, &t.StartedAt, &t.FinishedAt)
	return t, MapErr(err)
}

// Active reports whether the thread's sessions should be live.
func (t Thread) Active() bool {
	return t.Status == "running" || t.Status == "waiting" || t.Status == "review"
}

// Employee is a row of at_employees.
type Employee struct {
	ID         string
	ProjectID  string
	ThreadID   *string
	Name       string
	Tier       string
	Role       string
	RoleID     *string
	Runtime    string
	NodeID     *string
	InstanceNo int
	Look       store.Look
	Spec       store.Role
	Active     bool
	CreatedAt  time.Time
}

const employeeCols = `id, project_id, thread_id, name, tier, role, role_id, runtime, node_id, instance_no, look, spec,
	active, created_at`

func scanEmployee(r Scanner) (Employee, error) {
	var e Employee
	err := r.Scan(&e.ID, &e.ProjectID, &e.ThreadID, &e.Name, &e.Tier, &e.Role, &e.RoleID, &e.Runtime, &e.NodeID,
		&e.InstanceNo, &e.Look, &e.Spec, &e.Active, &e.CreatedAt)
	return e, MapErr(err)
}

// AgentSession is a row of at_agent_sessions (one logical session).
type AgentSession struct {
	ID             string
	EmployeeID     string
	NodeID         *string
	Runtime        string
	SDKSessionID   *string
	CodexThreadID  *string
	Cwd            string
	Branch         string
	State          string
	OpenGen        int
	PermissionMode string
	Init           json.RawMessage
	LastEventNo    int64
	OpenedAt       time.Time
	LastActiveAt   *time.Time
	ClosedAt       *time.Time
	CloseReason    string
}

const sessionCols = `id, employee_id, node_id, runtime, sdk_session_id, codex_thread_id, cwd, branch, state, open_gen,
	permission_mode, init, last_event_no, opened_at, last_active_at, closed_at, close_reason`

func scanSession(r Scanner) (AgentSession, error) {
	var s AgentSession
	err := r.Scan(&s.ID, &s.EmployeeID, &s.NodeID, &s.Runtime, &s.SDKSessionID, &s.CodexThreadID, &s.Cwd, &s.Branch,
		&s.State, &s.OpenGen, &s.PermissionMode, &s.Init, &s.LastEventNo, &s.OpenedAt, &s.LastActiveAt, &s.ClosedAt,
		&s.CloseReason)
	return s, MapErr(err)
}

// Task is a row of at_tasks (v1 table, v2 columns).
type Task struct {
	ConversationID string
	ID             string
	ThreadID       *string
	MainTaskID     *string
	Assignee       string
	Role           string
	ClaimedBy      string
	Kind           string
	Instruction    string
	DependsOn      []string
	Status         string
	Output         string
	Parent         string
	Priority       int
	Attempt        int
	Summary        string
	Branch         string
	HeadSHA        string
	Pushed         map[string]string // repo → head sha of the member branch pushed for this task
	Nudges         int
	CreatedBy      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ClaimedAt      *time.Time
	FinishedAt     *time.Time
}

const taskCols = `conversation_id, id, thread_id, main_task_id, assignee, role, claimed_by, kind, instruction,
	depends_on, status, output, parent, priority, attempt, summary, branch, head_sha, pushed, nudges, created_by, created_at,
	updated_at, claimed_at, finished_at`

func scanTask(r Scanner) (Task, error) {
	var t Task
	err := r.Scan(&t.ConversationID, &t.ID, &t.ThreadID, &t.MainTaskID, &t.Assignee, &t.Role, &t.ClaimedBy, &t.Kind,
		&t.Instruction, &t.DependsOn, &t.Status, &t.Output, &t.Parent, &t.Priority, &t.Attempt, &t.Summary,
		&t.Branch, &t.HeadSHA, &t.Pushed, &t.Nudges, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt, &t.ClaimedAt, &t.FinishedAt)
	if t.DependsOn == nil {
		t.DependsOn = []string{}
	}
	if t.Pushed == nil {
		t.Pushed = map[string]string{}
	}
	return t, MapErr(err)
}

// Finished reports whether the task reached a final state.
func (t Task) Finished() bool {
	return t.Status == "done" || t.Status == "failed" || t.Status == "cancelled"
}

// MainTask is a row of at_main_tasks.
type MainTask struct {
	ID        string
	ThreadID  string
	Pos       int
	Title     string
	Status    string
	AutoDone  bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

const mainTaskCols = `id, thread_id, pos, title, status, auto_done, created_at, updated_at`

func scanMainTask(r Scanner) (MainTask, error) {
	var m MainTask
	err := r.Scan(&m.ID, &m.ThreadID, &m.Pos, &m.Title, &m.Status, &m.AutoDone, &m.CreatedAt, &m.UpdatedAt)
	return m, MapErr(err)
}

// Question is a row of at_questions (questions and approval cards).
type Question struct {
	ConversationID string
	ID             string
	Asker          string
	TaskID         *string
	Audience       string
	Kind           string
	Text           string
	Options        []string
	Detail         map[string]any
	Answer         *string
	Status         string
	CreatedAt      time.Time
	AnsweredAt     *time.Time
	ThreadID       *string
	ProjectID      *string
	SessionID      *string
	ApprovalID     *string
	CallID         *string
	Blocking       bool
	DecidedBy      string
}

const questionCols = `conversation_id, id, asker, task_id, audience, kind, text, options, detail, answer, status,
	created_at, answered_at, thread_id, project_id, session_id, approval_id, call_id, blocking, decided_by`

func scanQuestion(r Scanner) (Question, error) {
	var q Question
	err := r.Scan(&q.ConversationID, &q.ID, &q.Asker, &q.TaskID, &q.Audience, &q.Kind, &q.Text, &q.Options, &q.Detail,
		&q.Answer, &q.Status, &q.CreatedAt, &q.AnsweredAt, &q.ThreadID, &q.ProjectID, &q.SessionID, &q.ApprovalID,
		&q.CallID, &q.Blocking, &q.DecidedBy)
	if q.Options == nil {
		q.Options = []string{}
	}
	if q.Detail == nil {
		q.Detail = map[string]any{}
	}
	return q, MapErr(err)
}

// Message is a row of at_messages.
type Message struct {
	ID             int64
	ConversationID string
	Role           string
	Content        string
	CreatedAt      time.Time
	Author         string
	Kind           string
	DeliveredAt    *time.Time
	Meta           map[string]any
}

const messageCols = `id, conversation_id, role, content, created_at, author, kind, delivered_at, meta`

func scanMessage(r Scanner) (Message, error) {
	var m Message
	err := r.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content, &m.CreatedAt, &m.Author, &m.Kind, &m.DeliveredAt, &m.Meta)
	if m.Meta == nil {
		m.Meta = map[string]any{}
	}
	return m, MapErr(err)
}

// Mail is a row of at_mail (employee-to-employee messages).
type Mail struct {
	ConversationID string
	ID             int64
	Sender         string
	Recipient      string
	Body           string
	CreatedAt      time.Time
	ReadBy         []string
}

const mailCols = `conversation_id, id, sender, recipient, body, created_at, read_by`

func scanMail(r Scanner) (Mail, error) {
	var m Mail
	err := r.Scan(&m.ConversationID, &m.ID, &m.Sender, &m.Recipient, &m.Body, &m.CreatedAt, &m.ReadBy)
	return m, MapErr(err)
}

// Deliverable is a row of at_deliverables.
type Deliverable struct {
	ID          string
	ProjectID   string
	ThreadID    *string
	Kind        string
	Title       string
	URL         string
	Detail      map[string]any
	PublishedBy string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

const deliverableCols = `d.id, d.project_id, d.thread_id, d.kind, d.title, d.url, d.detail, d.published_by, d.created_at,
	d.updated_at`

func scanDeliverable(r Scanner) (Deliverable, error) {
	var d Deliverable
	err := r.Scan(&d.ID, &d.ProjectID, &d.ThreadID, &d.Kind, &d.Title, &d.URL, &d.Detail, &d.PublishedBy, &d.CreatedAt,
		&d.UpdatedAt)
	if d.Detail == nil {
		d.Detail = map[string]any{}
	}
	return d, MapErr(err)
}

// MergeEntry is a row of at_merge_queue.
type MergeEntry struct {
	ID          string
	ThreadID    string
	TaskID      *string
	Employee    string
	Repo        string
	HeadBranch  string
	HeadSHA     string
	TrialBranch string
	TrialSHA    string
	Status      string
	Attempts    int
	Error       string
	Position    int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

const mergeCols = `id, thread_id, task_id, employee, repo, head_branch, head_sha, trial_branch, trial_sha, status, attempts,
	error, position, created_at, updated_at`

func scanMerge(r Scanner) (MergeEntry, error) {
	var m MergeEntry
	err := r.Scan(&m.ID, &m.ThreadID, &m.TaskID, &m.Employee, &m.Repo, &m.HeadBranch, &m.HeadSHA, &m.TrialBranch, &m.TrialSHA,
		&m.Status, &m.Attempts, &m.Error, &m.Position, &m.CreatedAt, &m.UpdatedAt)
	return m, MapErr(err)
}
