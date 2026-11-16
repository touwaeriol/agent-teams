package core

import (
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// The views below are the JSON objects of api.md §2, shared by REST and SSE.

// NodeRef is the node part of EmployeeBrief.
type NodeRef struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Online bool   `json:"online"`
}

// EmployeeStatusView is an employee's current state (office).
type EmployeeStatusView struct {
	State      string    `json:"state"`
	Substate   string    `json:"substate"`
	Bubble     string    `json:"bubble"`
	BubbleKind string    `json:"bubble_kind"`
	TaskID     string    `json:"task_id"`
	TaskKind   string    `json:"task_kind"`
	Tier       string    `json:"tier"`
	Role       string    `json:"role"`
	Since      time.Time `json:"since"`
	Seq        int64     `json:"seq"`
}

// EmployeeBrief is an employee in lists.
type EmployeeBrief struct {
	ID       string              `json:"id"`
	Name     string              `json:"name"`
	Tier     string              `json:"tier"`
	Role     string              `json:"role"`
	Title    string              `json:"title"`
	Runtime  string              `json:"runtime"`
	ThreadNo *int                `json:"thread_no"`
	Node     *NodeRef            `json:"node"`
	Look     store.Look          `json:"look"`
	Status   *EmployeeStatusView `json:"status"`
}

// MainTaskView is a step of the thread plan.
type MainTaskView struct {
	ID         string `json:"id"`
	Pos        int    `json:"pos"`
	Title      string `json:"title"`
	Status     string `json:"status" enum:"todo,doing,done"`
	AutoDone   bool   `json:"auto_done"`
	TasksTotal int    `json:"tasks_total"`
	TasksDone  int    `json:"tasks_done"`
}

// ThreadCounts are the thread's badges.
type ThreadCounts struct {
	Waiting       int `json:"waiting"`
	QuestionsOpen int `json:"questions_open"`
	ApprovalsOpen int `json:"approvals_open"`
	TasksTotal    int `json:"tasks_total"`
	TasksDone     int `json:"tasks_done"`
	MainTotal     int `json:"main_total"`
	MainDone      int `json:"main_done"`
}

// LeadLast is the lead's latest reply.
type LeadLast struct {
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// ThreadView is a thread with its plan, members and counters.
type ThreadView struct {
	ID            string           `json:"id"`
	ProjectID     string           `json:"project_id"`
	No            int              `json:"no"`
	Title         string           `json:"title"`
	Brief         string           `json:"brief"`
	Status        string           `json:"status" enum:"queued,running,waiting,review,done,failed,paused,archived"`
	QueuePosition *int             `json:"queue_position"`
	PauseReason   string           `json:"pause_reason"`
	Branch        string           `json:"branch"`
	BaseBranch    string           `json:"base_branch" doc:"Deprecated: bases are per repository (repos)"`
	Repos         []ThreadRepoView `json:"repos" doc:"Repositories the thread set a base for, branched or opened a PR in"`
	PRNumber      *int             `json:"pr_number"`
	PRURL         *string          `json:"pr_url"`
	PRState       string           `json:"pr_state"`
	Summary       string           `json:"summary"`
	AutoApprove   bool             `json:"auto_approve"`
	OpenedBy      string           `json:"opened_by"`
	Members       []EmployeeBrief  `json:"members"`
	MainTasks     []MainTaskView   `json:"main_tasks"`
	Counts        ThreadCounts     `json:"counts"`
	LeadLast      *LeadLast        `json:"lead_last"`
	CostUSDEst    *float64         `json:"cost_usd_est"`
	CreatedAt     time.Time        `json:"created_at"`
	UpdatedAt     time.Time        `json:"updated_at"`
	StartedAt     *time.Time       `json:"started_at" doc:"When the thread first ran (null while queued)"`
	FinishedAt    *time.Time       `json:"finished_at" doc:"When it reached done, failed or archived"`
	// LastActivityAt is the latest of a state change, a message in the thread and a deliverable it published.
	LastActivityAt    time.Time        `json:"last_activity_at" doc:"Latest state change, message or deliverable of the thread"`
	LatestDeliverable *DeliverableView `json:"latest_deliverable" doc:"The newest deliverable the thread published (null: none)"`
}

// TaskView is a small task.
type TaskView struct {
	ID          string     `json:"id"`
	ThreadNo    int        `json:"thread_no"`
	MainTaskID  *string    `json:"main_task_id"`
	Assignee    string     `json:"assignee"`
	Role        string     `json:"role"`
	ClaimedBy   string     `json:"claimed_by"`
	Kind        string     `json:"kind"`
	Instruction string     `json:"instruction"`
	Status      string     `json:"status"`
	DependsOn   []string   `json:"depends_on"`
	Parent      string     `json:"parent"`
	Priority    int        `json:"priority"`
	Attempt     int        `json:"attempt"`
	Summary     string     `json:"summary"`
	Branch      string     `json:"branch"`
	HeadSHA     string     `json:"head_sha"`
	CreatedAt   time.Time  `json:"created_at"`
	ClaimedAt   *time.Time `json:"claimed_at"`
	FinishedAt  *time.Time `json:"finished_at"`
}

// TaskViewOf renders a task.
func TaskViewOf(t Task, threadNo int) TaskView {
	return TaskView{ID: t.ID, ThreadNo: threadNo, MainTaskID: t.MainTaskID, Assignee: t.Assignee, Role: t.Role,
		ClaimedBy: t.ClaimedBy, Kind: t.Kind, Instruction: t.Instruction, Status: t.Status, DependsOn: t.DependsOn,
		Parent: t.Parent, Priority: t.Priority, Attempt: t.Attempt, Summary: t.Summary, Branch: t.Branch,
		HeadSHA: t.HeadSHA, CreatedAt: t.CreatedAt, ClaimedAt: t.ClaimedAt, FinishedAt: t.FinishedAt}
}

// QuestionView is a question or approval card.
type QuestionView struct {
	ID             string         `json:"id"`
	ConversationID string         `json:"conversation_id"`
	ThreadNo       *int           `json:"thread_no"`
	Asker          string         `json:"asker"`
	TaskID         *string        `json:"task_id"`
	Audience       string         `json:"audience" enum:"lead,user"`
	Kind           string         `json:"kind" enum:"question,approval"`
	Text           string         `json:"text"`
	Options        []string       `json:"options"`
	Detail         map[string]any `json:"detail"`
	Blocking       bool           `json:"blocking"`
	Answer         *string        `json:"answer"`
	Status         string         `json:"status"`
	DecidedBy      string         `json:"decided_by"`
	CreatedAt      time.Time      `json:"created_at"`
	AnsweredAt     *time.Time     `json:"answered_at"`
}

// QuestionViewOf renders a question.
func QuestionViewOf(q Question, threadNo *int) QuestionView {
	return QuestionView{ID: q.ID, ConversationID: q.ConversationID, ThreadNo: threadNo, Asker: q.Asker, TaskID: q.TaskID,
		Audience: q.Audience, Kind: q.Kind, Text: q.Text, Options: q.Options, Detail: q.Detail, Blocking: q.Blocking,
		Answer: q.Answer, Status: q.Status, DecidedBy: q.DecidedBy, CreatedAt: q.CreatedAt, AnsweredAt: q.AnsweredAt}
}

// MessageView is a conversation message.
type MessageView struct {
	ID        int64          `json:"id"`
	Role      string         `json:"role"`
	Author    string         `json:"author"`
	Kind      string         `json:"kind"`
	Content   string         `json:"content"`
	Meta      map[string]any `json:"meta"`
	CreatedAt time.Time      `json:"created_at"`
}

// MessageViewOf renders a message.
func MessageViewOf(m Message) MessageView {
	return MessageView{ID: m.ID, Role: m.Role, Author: m.Author, Kind: m.Kind, Content: m.Content, Meta: m.Meta,
		CreatedAt: m.CreatedAt}
}

// MailView is an employee-to-employee message.
type MailView struct {
	ID        int64     `json:"id"`
	Sender    string    `json:"sender"`
	Recipient string    `json:"recipient"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// MailViewOf renders mail.
func MailViewOf(m Mail) MailView {
	return MailView{ID: m.ID, Sender: m.Sender, Recipient: m.Recipient, Body: m.Body, CreatedAt: m.CreatedAt}
}

// DeliverableView is a deliverable card.
type DeliverableView struct {
	ID          string         `json:"id"`
	ProjectID   string         `json:"project_id"`
	ThreadNo    *int           `json:"thread_no"`
	Kind        string         `json:"kind"`
	Title       string         `json:"title"`
	URL         string         `json:"url"`
	Detail      map[string]any `json:"detail"`
	PublishedBy string         `json:"published_by"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// MergeEntryView is a merge queue entry.
type MergeEntryView struct {
	ID         string    `json:"id"`
	ThreadNo   int       `json:"thread_no"`
	TaskID     *string   `json:"task_id"`
	Employee   string    `json:"employee"`
	Repo       string    `json:"repo"`
	HeadBranch string    `json:"head_branch"`
	Status     string    `json:"status"`
	Attempts   int       `json:"attempts"`
	Error      string    `json:"error"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// MergeViewOf renders a merge entry.
func MergeViewOf(m MergeEntry, threadNo int) MergeEntryView {
	return MergeEntryView{ID: m.ID, ThreadNo: threadNo, TaskID: m.TaskID, Employee: m.Employee, Repo: m.Repo, HeadBranch: m.HeadBranch,
		Status: m.Status, Attempts: m.Attempts, Error: m.Error, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
}

// CheckView is one CI check.
type CheckView struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
	Summary    string `json:"summary"`
}
