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
	PRNumber      *int    `gorm:"column:pr_number"`
	PRURL         *string `gorm:"column:pr_url"`
	PRState       string  `gorm:"column:pr_state"`
	Summary       string
	AutoApprove   bool
	OpenedBy      string
	RolesSnapshot []store.Role `gorm:"serializer:jsonb"`
	WaitingCount  int
	CreatedAt     time.Time
	UpdatedAt     time.Time
	StartedAt     *time.Time
	FinishedAt    *time.Time
	PinnedAt      *time.Time
	RemindAt      *time.Time
	RemindedAt    *time.Time
}

// TableName maps Thread to at_threads.
func (Thread) TableName() string { return "at_threads" }

const threadCols = `id, project_id, user_id, no, title, brief, status, pause_reason, branch, base_branch, pr_number,
	pr_url, pr_state, summary, auto_approve, opened_by, roles_snapshot, waiting_count, created_at, updated_at,
	started_at, finished_at, pinned_at, remind_at, reminded_at`

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
	Look       store.Look `gorm:"serializer:jsonb"`
	Spec       store.Role `gorm:"serializer:jsonb"`
	Model      string     // picked with /model; ahead of the project and role models
	Active     bool
	CreatedAt  time.Time
}

// TableName maps Employee to at_employees.
func (Employee) TableName() string { return "at_employees" }

const employeeCols = `id, project_id, thread_id, name, tier, role, role_id, runtime, node_id, instance_no, look, spec,
	model, active, created_at`

// AgentSession is a row of at_agent_sessions (one logical session).
type AgentSession struct {
	ID             string
	EmployeeID     string
	NodeID         *string
	Runtime        string
	SDKSessionID   *string `gorm:"column:sdk_session_id"`
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

// TableName maps AgentSession to at_agent_sessions.
func (AgentSession) TableName() string { return "at_agent_sessions" }

const sessionCols = `id, employee_id, node_id, runtime, sdk_session_id, codex_thread_id, cwd, branch, state, open_gen,
	permission_mode, init, last_event_no, opened_at, last_active_at, closed_at, close_reason`

// Task is a row of at_tasks.
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
	DependsOn      []string `gorm:"serializer:jsonb"`
	Status         string
	Parent         string
	Priority       int
	Attempt        int
	Summary        string
	Branch         string
	HeadSHA        string            `gorm:"column:head_sha"`
	Pushed         map[string]string `gorm:"serializer:jsonb"` // repo → head sha of the member branch pushed for this task
	Nudges         int
	CreatedBy      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ClaimedAt      *time.Time
	FinishedAt     *time.Time
}

// TableName maps Task to at_tasks.
func (Task) TableName() string { return "at_tasks" }

const taskCols = `conversation_id, id, thread_id, main_task_id, assignee, role, claimed_by, kind, instruction,
	depends_on, status, parent, priority, attempt, summary, branch, head_sha, pushed, nudges, created_by, created_at,
	updated_at, claimed_at, finished_at`

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

// TableName maps MainTask to at_main_tasks.
func (MainTask) TableName() string { return "at_main_tasks" }

const mainTaskCols = `id, thread_id, pos, title, status, auto_done, created_at, updated_at`

// Question is a row of at_questions (questions and approval cards).
type Question struct {
	ConversationID string
	ID             string
	Asker          string
	TaskID         *string
	Audience       string
	Kind           string
	Text           string
	Options        []string       `gorm:"serializer:jsonb"`
	Detail         map[string]any `gorm:"serializer:jsonb"`
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

// TableName maps Question to at_questions.
func (Question) TableName() string { return "at_questions" }

const questionCols = `conversation_id, id, asker, task_id, audience, kind, text, options, detail, answer, status,
	created_at, answered_at, thread_id, project_id, session_id, approval_id, call_id, blocking, decided_by`

// Message is a row of at_messages.
type Message struct {
	ID             int64
	ConversationID string
	Role           string
	Content        string
	CreatedAt      time.Time `gorm:"autoCreateTime:false;default:now()"`
	Author         string
	Kind           string
	DeliveredAt    *time.Time
	Meta           map[string]any `gorm:"serializer:jsonb"`
}

// TableName maps Message to at_messages.
func (Message) TableName() string { return "at_messages" }

const messageCols = `id, conversation_id, role, content, created_at, author, kind, delivered_at, meta`

// Mail is a row of at_mail (employee-to-employee messages).
type Mail struct {
	ConversationID string
	ID             int64
	Sender         string
	Recipient      string
	Body           string
	CreatedAt      time.Time
	ReadBy         []string `gorm:"serializer:jsonb"`
}

// TableName maps Mail to at_mail.
func (Mail) TableName() string { return "at_mail" }

const mailCols = `conversation_id, id, sender, recipient, body, created_at, read_by`

// Deliverable is a row of at_deliverables.
type Deliverable struct {
	ID          string
	ProjectID   string
	ThreadID    *string
	Kind        string
	Title       string
	URL         string         `gorm:"column:url"`
	Detail      map[string]any `gorm:"serializer:jsonb"`
	PublishedBy string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// TableName maps Deliverable to at_deliverables.
func (Deliverable) TableName() string { return "at_deliverables" }

const deliverableCols = `d.id, d.project_id, d.thread_id, d.kind, d.title, d.url, d.detail, d.published_by, d.created_at,
	d.updated_at`

// MergeEntry is a row of at_merge_queue.
type MergeEntry struct {
	ID          string
	ThreadID    string
	TaskID      *string
	Employee    string
	Repo        string
	HeadBranch  string
	HeadSHA     string `gorm:"column:head_sha"`
	TrialBranch string
	TrialSHA    string `gorm:"column:trial_sha"`
	Status      string
	Attempts    int
	Error       string
	Position    int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// TableName maps MergeEntry to at_merge_queue.
func (MergeEntry) TableName() string { return "at_merge_queue" }

const mergeCols = `id, thread_id, task_id, employee, repo, head_branch, head_sha, trial_branch, trial_sha, status, attempts,
	error, position, created_at, updated_at`
