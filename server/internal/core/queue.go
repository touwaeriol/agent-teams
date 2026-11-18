package core

import (
	"context"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// The project task queue (shared-members.md §4): pending tasks of a project's going threads
// whose dependencies are done, claimed by idle members of the role.

// liveThreads are a project's threads that are not over ($1 = project id): a claimed or running
// task there keeps its member busy (a paused thread's member waits for it to resume).
const liveThreads = `SELECT id FROM at_threads WHERE project_id = $1 AND status NOT IN ('done','failed','archived')`

// staffTask selects the claimed / running tasks (alias x) of project member $2 in project $1; a
// task in a thread where a legacy member has that name belongs to the legacy member.
const staffTask = `x.claimed_by = $2 AND x.status IN ('claimed','running') AND x.thread_id IN (` + liveThreads + `)
	AND NOT EXISTS (SELECT 1 FROM at_employees l WHERE l.thread_id = x.thread_id AND l.name = x.claimed_by)`

// CurrentTaskOf is the employee's claimed or running task: for a project member anywhere in the
// project, for a lead or legacy member in its own thread (ErrNotFound when it has none).
func CurrentTaskOf(ctx context.Context, q DB, e Employee) (Task, error) {
	if e.ThreadID != nil {
		return CurrentTask(ctx, q, *e.ThreadID, e.Name)
	}
	if !e.ProjectMember() {
		return Task{}, ErrNotFound
	}
	return rawOne[Task](ctx, q, `SELECT x.* FROM at_tasks x WHERE `+staffTask+` ORDER BY x.claimed_at DESC LIMIT 1`,
		e.ProjectID, e.Name)
}

// WorkThreadOf is the thread an employee works in now: a lead's or legacy member's own thread;
// for a project member the thread of its current task, else its current thread (nil: none).
func WorkThreadOf(ctx context.Context, q DB, e Employee) (*string, error) {
	if !e.ProjectMember() {
		return e.WorkThreadID(), nil
	}
	task, err := CurrentTaskOf(ctx, q, e)
	if err == nil {
		return &task.ConversationID, nil
	}
	if err != ErrNotFound {
		return nil, err
	}
	return e.CurrentThreadID, nil
}

// BusyMembers names the project members holding a claimed or running task in the project (a
// task in a thread whose legacy member has that name is the legacy member's).
func BusyMembers(ctx context.Context, q DB, projectID string) (map[string]bool, error) {
	var names []string
	err := store.Raw(ctx, q, `SELECT DISTINCT x.claimed_by FROM at_tasks x WHERE x.status IN ('claimed','running')
		AND x.thread_id IN (`+liveThreads+`)
		AND NOT EXISTS (SELECT 1 FROM at_employees l WHERE l.thread_id = x.thread_id AND l.name = x.claimed_by)`, projectID).
		Scan(&names).Error
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out, err
}

// IdleMember is a member free to claim a task, with where it works now.
type IdleMember struct {
	Employee
	// Thread is its current thread, else the thread of its live session ("" when neither).
	Thread string
}

// IdleMembers lists a project's active members with no claimed or running task: project
// members (none in the whole project) first, then the legacy members of going threads (none in
// their thread), by role and instance.
func IdleMembers(ctx context.Context, q DB, projectID string) ([]IdleMember, error) {
	return rawAll[IdleMember](ctx, q, `SELECT e.*, COALESCE(e.thread_id, e.current_thread_id,
			(SELECT a.thread_id FROM at_agent_sessions a WHERE a.employee_id = e.id AND a.state <> 'closed' LIMIT 1), '') AS thread
		FROM at_employees e WHERE e.project_id = $1 AND e.active AND e.tier = 'member'
		AND (e.thread_id IS NULL OR e.thread_id IN (SELECT id FROM at_threads WHERE project_id = $1 AND status IN ('running','waiting')))
		AND NOT EXISTS (SELECT 1 FROM at_tasks x WHERE x.claimed_by = e.name AND x.status IN ('claimed','running') AND (
			x.thread_id = e.thread_id OR (e.thread_id IS NULL AND x.thread_id IN (`+liveThreads+`)
				AND NOT EXISTS (SELECT 1 FROM at_employees l WHERE l.thread_id = x.thread_id AND l.name = x.claimed_by))))
		ORDER BY e.thread_id NULLS FIRST, e.role, e.instance_no`, projectID)
}

// QueuedTask is a claimable task with what scoring it needs (shared-members.md §4.1).
type QueuedTask struct {
	Task
	OnlyRoles []string `gorm:"serializer:jsonb"`
	// DepWorkers are the members who did the task's dependencies (one entry per dependency).
	DepWorkers []string `gorm:"serializer:jsonb"`
	// Downstream counts the unfinished tasks that depend on it.
	Downstream int
	// Legacy names the thread's own members: a project member of such a name does not work there.
	Legacy []string `gorm:"serializer:jsonb"`
	// Ready is ReadyAt, or now for a task whose readiness was never stamped.
	Ready time.Time
}

// QueueLimit bounds the claimable tasks one scheduling pass looks at per project.
const QueueLimit = 200

// depsDone holds for a task (alias t) whose dependencies are all done.
const depsDone = `NOT EXISTS (SELECT 1 FROM jsonb_array_elements_text(t.depends_on) dep(id)
	LEFT JOIN at_tasks d ON d.conversation_id = t.conversation_id AND d.id = dep.id WHERE d.status IS DISTINCT FROM 'done')`

// ClaimableTasks lists the project's claimable tasks: pending, dependencies done, in a running
// or waiting thread; the most urgent first, at most QueueLimit.
func ClaimableTasks(ctx context.Context, q DB, projectID string) ([]QueuedTask, error) {
	return rawAll[QueuedTask](ctx, q, `SELECT t.*, th.only_roles,
			(SELECT COALESCE(jsonb_agg(d.claimed_by), '[]'::jsonb) FROM at_tasks d
				WHERE d.conversation_id = t.conversation_id AND jsonb_exists(t.depends_on, d.id)) AS dep_workers,
			(SELECT count(*) FROM at_tasks n WHERE n.conversation_id = t.conversation_id AND jsonb_exists(n.depends_on, t.id)
				AND n.status NOT IN ('done','failed','cancelled')) AS downstream,
			(SELECT COALESCE(jsonb_agg(l.name), '[]'::jsonb) FROM at_employees l WHERE l.thread_id = t.thread_id) AS legacy,
			COALESCE(t.ready_at, now()) AS ready
		FROM at_tasks t JOIN at_threads th ON th.id = t.thread_id
		WHERE th.project_id = $1 AND th.status IN ('running','waiting') AND t.status = 'pending' AND `+depsDone+`
		ORDER BY t.priority, ready, t.created_at LIMIT $2`, projectID, QueueLimit)
}

// MarkReady stamps ready_at on the pending tasks of a thread that waited on task id and now
// have every dependency done (id just finished).
func MarkReady(ctx context.Context, tx *Tx, conversationID, id string) error {
	_, err := store.Exec(ctx, tx, `UPDATE at_tasks t SET ready_at = now() WHERE t.conversation_id = $1 AND t.status = 'pending'
		AND t.ready_at IS NULL AND jsonb_exists(t.depends_on, $2) AND `+depsDone, conversationID, id)
	return err
}

// StampReady stamps ready_at on the project's pending tasks whose dependencies are done but
// whose readiness was never recorded (tasks from before the column, put back by a retry).
func StampReady(ctx context.Context, tx *Tx, projectID string) error {
	_, err := store.Exec(ctx, tx, `UPDATE at_tasks t SET ready_at = now() WHERE t.status = 'pending' AND t.ready_at IS NULL
		AND t.thread_id IN (SELECT id FROM at_threads WHERE project_id = $1 AND status IN ('running','waiting')) AND `+depsDone, projectID)
	return err
}

// ThreadRepoSets maps each thread of a project to the repositories it works in (at_thread_repos).
func ThreadRepoSets(ctx context.Context, q DB, projectID string) (map[string][]string, error) {
	var rows []struct{ ThreadID, Repo string }
	err := store.Raw(ctx, q, `SELECT r.thread_id, r.repo FROM at_thread_repos r JOIN at_threads t ON t.id = r.thread_id
		WHERE t.project_id = $1 ORDER BY r.thread_id, r.repo`, projectID).Scan(&rows).Error
	out := map[string][]string{}
	for _, r := range rows {
		out[r.ThreadID] = append(out[r.ThreadID], r.Repo)
	}
	return out, err
}

// ProjectsWithWork lists the projects that have pending tasks in running or waiting threads.
func ProjectsWithWork(ctx context.Context, q DB) ([]string, error) {
	var ids []string
	err := store.Raw(ctx, q, `SELECT DISTINCT th.project_id FROM at_tasks t JOIN at_threads th ON th.id = t.thread_id
		WHERE t.status = 'pending' AND th.status IN ('running','waiting')`).Scan(&ids).Error
	return ids, err
}

// HasStaff reports whether the project has project members (active or not).
func HasStaff(ctx context.Context, q DB, projectID string) (bool, error) {
	n, err := count(employees(ctx, q).Where("project_id = ? AND thread_id IS NULL AND tier = 'member'", projectID))
	return n > 0, err
}
