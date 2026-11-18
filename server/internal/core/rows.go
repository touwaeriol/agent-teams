package core

import (
	"encoding/json"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Thread is a row of at_threads.
type Thread struct {
	ID          string
	ProjectID   string
	UserID      string
	No          int
	Title       string
	Brief       string
	Status      string
	PauseReason string
	Branch      string
	BaseBranch  string
	PRNumber    *int    `gorm:"column:pr_number"`
	PRURL       *string `gorm:"column:pr_url"`
	PRState     string  `gorm:"column:pr_state"`
	MergeError  *string `gorm:"column:merge_error"`
	Summary     string
	AutoApprove bool
	OpenedBy    string
	// SourceMessageID is the user's project-chat message the thread was opened for (nil: none).
	SourceMessageID *int64 `gorm:"column:source_message_id"`
	// ForwardedBrief: the brief is forwarded user messages (their copies are in the conversation).
	ForwardedBrief bool
	RolesSnapshot  []store.Role `gorm:"serializer:jsonb"`
	// OnlyRoles are the member roles the lead may assign to (empty: all; NULL in the table).
	OnlyRoles    []string `gorm:"serializer:jsonb"`
	WaitingCount int
	CreatedAt    time.Time
	UpdatedAt    time.Time
	StartedAt    *time.Time
	FinishedAt   *time.Time
	PinnedAt     *time.Time
	RemindAt     *time.Time
	RemindedAt   *time.Time
}

// TableName maps Thread to at_threads.
func (Thread) TableName() string { return "at_threads" }

const threadCols = `id, project_id, user_id, no, title, brief, status, pause_reason, branch, base_branch, pr_number,
	pr_url, pr_state, merge_error, summary, auto_approve, opened_by, source_message_id, forwarded_brief, roles_snapshot, only_roles, waiting_count, created_at, updated_at,
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
	// CurrentThreadID is where a project member works: its current task's thread, or the thread
	// of its latest session (shared-members.md §2). Nil for the coordinator, leads, legacy members.
	CurrentThreadID *string
}

// ProjectMember reports whether the employee is project staff (a member shared by every thread).
func (e Employee) ProjectMember() bool { return e.ThreadID == nil && e.Tier == "member" }

// WorkThreadID is the thread the employee works in: its own thread (lead, legacy member) or,
// for a project member, its current thread; nil for the coordinator and an unassigned member.
func (e Employee) WorkThreadID() *string {
	if e.ThreadID != nil {
		return e.ThreadID
	}
	return e.CurrentThreadID
}

// TableName maps Employee to at_employees.
func (Employee) TableName() string { return "at_employees" }

const employeeCols = `id, project_id, thread_id, name, tier, role, role_id, runtime, node_id, instance_no, look, spec,
	model, active, created_at, current_thread_id`

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
	// ThreadID is the thread whose worktree the session was opened in (nil: coordinator).
	ThreadID *string
	// InTurn: a turn runs in the session (between turn_started and turn_ended).
	InTurn bool
	// ContextUsed / ContextMax: the context the session used at its last turn end and the window
	// the runtime measured it against (tokens; nil until a context_usage daemon reports them).
	ContextUsed *int
	ContextMax  *int
	// TasksDone counts the task briefs that reached the session (clear_after_tasks, §5).
	TasksDone int
}

// TableName maps AgentSession to at_agent_sessions.
func (AgentSession) TableName() string { return "at_agent_sessions" }

const sessionCols = `id, employee_id, node_id, runtime, sdk_session_id, codex_thread_id, cwd, branch, state, open_gen,
	permission_mode, init, last_event_no, opened_at, last_active_at, closed_at, close_reason, thread_id, in_turn,
	context_used, context_max, tasks_done`

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
	// Sync is what the pre-task worktree sync did, per repository; SyncPending counts jobs still out.
	Sync        map[string]TaskSync `gorm:"serializer:jsonb"`
	SyncPending int
	SyncRound   string
	// Handoff is complete_task's note for whoever builds on the task (HandoffAsked: asked once).
	Handoff      string
	HandoffAsked bool
	// Merged is what the task's merge added to the thread branch, per repository.
	Merged map[string]TaskMerge `gorm:"serializer:jsonb"`
	// ReservedFor keeps rework for the member who did the task until ReservedUntil; ReadyAt is
	// when the task's dependencies were met (shared-members.md §4).
	ReservedFor   string
	ReservedUntil *time.Time
	ReadyAt       *time.Time
	// Downstream counts the unfinished tasks that depend on this one, directly or through others
	// (the critical-path tie-break of the queue, shared-members.md §4.1).
	Downstream int
	// CompactID: the claimed task waits for session.compacted of this id; WaitSince is when its
	// current wait (compaction, then the pre-task sync) began (shared-members.md §5).
	CompactID string
	WaitSince *time.Time
	// Progress is the member's latest report_progress (part of the task anchor).
	Progress string
	// Compactions during the task, the context at its start and end (tokens), and whether the
	// lead was told the task is long (§5.2).
	Compactions int
	CtxStart    *int
	CtxEnd      *int
	SplitHinted bool
}

// TaskSync is one repository's pre-task sync result (Error when the job failed).
type TaskSync struct {
	Action    string   `json:"action,omitempty"`
	HeadSHA   string   `json:"head_sha,omitempty"`
	Conflicts []string `json:"conflicts,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// TaskMerge is the range a task's merge added to the thread branch in one repository.
type TaskMerge struct {
	Base  string   `json:"base"`
	Head  string   `json:"head"`
	Files []string `json:"files"`
}

// TableName maps Task to at_tasks.
func (Task) TableName() string { return "at_tasks" }

const taskCols = `conversation_id, id, thread_id, main_task_id, assignee, role, claimed_by, kind, instruction,
	depends_on, status, parent, priority, attempt, summary, branch, head_sha, pushed, nudges, created_by, created_at,
	updated_at, claimed_at, finished_at, sync, sync_pending, sync_round, handoff, handoff_asked, merged,
	reserved_for, reserved_until, ready_at, compact_id, wait_since, progress, compactions, ctx_start, ctx_end, split_hinted, downstream`

// Finished reports whether the task reached a final state.
func (t Task) Finished() bool { return finalStatus(t.Status) }

// finalStatus reports whether a task status is final (done, failed, cancelled).
func finalStatus(status string) bool {
	return status == "done" || status == "failed" || status == "cancelled"
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
	BaseSHA     string `gorm:"column:base_sha"` // the thread branch before the merge
	Status      string
	Attempts    int
	Error       string
	Position    int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// TableName maps MergeEntry to at_merge_queue.
func (MergeEntry) TableName() string { return "at_merge_queue" }

const mergeCols = `id, thread_id, task_id, employee, repo, head_branch, head_sha, trial_branch, trial_sha, base_sha, status, attempts,
	error, position, created_at, updated_at`
