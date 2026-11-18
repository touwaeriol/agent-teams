package core

import (
	"context"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// The project task queue (shared-members.md §4): pending tasks of a project's going threads
// whose dependencies are done, claimed by idle members of the role. Readiness is kept by events
// (ready_at, stamped when the last dependency is done) so a scheduling pass never walks the
// dependencies itself.

// liveThreads are a project's threads that are not over ($1 = project id): a claimed or running
// task there keeps its member busy (a paused thread's member waits for it to resume).
const liveThreads = `SELECT id FROM at_threads WHERE project_id = $1 AND status NOT IN ('done','failed','archived')`

// staffOwned holds for a claimed task (alias x) no legacy member of its thread has the name of:
// in such a thread the name means the legacy member, not the project member (shared-members.md §8).
const staffOwned = `NOT EXISTS (SELECT 1 FROM at_employees l WHERE l.thread_id = x.thread_id AND l.name = x.claimed_by)`

// busyTask holds for a claimed or running task (alias x) in a thread that is not over.
const busyTask = `x.status IN ('claimed','running') AND x.thread_id IN (` + liveThreads + `)`

// staffTask selects the claimed / running tasks (alias x) of project member $2 in project $1.
const staffTask = `x.claimed_by = $2 AND ` + busyTask + ` AND ` + staffOwned

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
	err := store.Raw(ctx, q, `SELECT DISTINCT x.claimed_by FROM at_tasks x WHERE `+busyTask+` AND `+staffOwned, projectID).
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
	// TurnThread is the thread of its live session while a turn runs there ("" when none): it
	// takes no task of another thread until that turn ends (shared-members.md §4.1).
	TurnThread string
}

// heldTask holds for a claimed or running task (alias x) of employee e: a legacy member's in its
// own thread, a project member's in any going thread of the project.
const heldTask = `x.claimed_by = e.name AND ` + busyTask + ` AND (x.thread_id = e.thread_id OR (e.thread_id IS NULL AND ` + staffOwned + `))`

// IdleMembers lists a project's active members with no claimed or running task: project
// members first, then the legacy members of running or waiting threads, by role and instance.
func IdleMembers(ctx context.Context, q DB, projectID string) ([]IdleMember, error) {
	return rawAll[IdleMember](ctx, q, `SELECT e.*, COALESCE(e.thread_id, e.current_thread_id, s.thread_id, '') AS thread,
			CASE WHEN s.in_turn AND s.state = 'active' THEN COALESCE(s.thread_id, '') ELSE '' END AS turn_thread
		FROM at_employees e LEFT JOIN LATERAL (SELECT a.thread_id, a.state, a.in_turn FROM at_agent_sessions a
			WHERE a.employee_id = e.id AND a.state <> 'closed' LIMIT 1) s ON true
		WHERE e.project_id = $1 AND e.active AND e.tier = 'member'
		AND (e.thread_id IS NULL OR e.thread_id IN (SELECT id FROM at_threads WHERE project_id = $1 AND status IN ('running','waiting')))
		AND NOT EXISTS (SELECT 1 FROM at_tasks x WHERE `+heldTask+`)
		ORDER BY e.thread_id NULLS FIRST, e.role, e.instance_no`, projectID)
}

// QueuedTask is a claimable task with what scoring it needs (shared-members.md §4.1). Task
// holds the scheduling columns only (queuedCols): the claim loads the chosen row whole.
type QueuedTask struct {
	Task
	OnlyRoles []string `gorm:"serializer:jsonb"`
	// DepWorkers are the members who did the task's dependencies (one entry per dependency).
	DepWorkers []string `gorm:"serializer:jsonb"`
	// Legacy names the thread's own members: a project member of such a name does not work there.
	Legacy []string `gorm:"serializer:jsonb"`
	// NamedActive: the task names an instance that is an active member (a task named to a
	// member that was retired goes to the role's pool).
	NamedActive bool
	// Ready is ReadyAt (always set on a claimable task).
	Ready time.Time
}

// QueueLimit bounds the claimable tasks of one role a scheduling pass looks at.
const QueueLimit = 200

// depsDone holds for a task (alias t) whose dependencies are all done.
const depsDone = `NOT EXISTS (SELECT 1 FROM jsonb_array_elements_text(t.depends_on) dep(id)
	LEFT JOIN at_tasks d ON d.conversation_id = t.conversation_id AND d.id = dep.id WHERE d.status IS DISTINCT FROM 'done')`

// TaskReady holds for a pending task (alias t) whose dependencies are all done: ready_at is kept
// by events (§4.1), so neither the claim nor the office board walks the dependencies.
const TaskReady = `t.ready_at IS NOT NULL`

// ClaimingThread holds for a thread (alias th) whose pending tasks members claim now.
const ClaimingThread = `th.status IN ('running','waiting')`

// NamedActive holds for a task (alias t, thread alias th) that names an active member of the
// project (a member of another thread's legacy team or a retired one does not count).
const NamedActive = `EXISTS (SELECT 1 FROM at_employees n WHERE n.project_id = th.project_id AND n.name = t.assignee AND n.active
	AND n.tier = 'member' AND (n.thread_id IS NULL OR n.thread_id = t.thread_id))`

// queuedCols are the task columns a scheduling pass reads (alias t).
const queuedCols = `t.conversation_id, t.id, t.thread_id, t.assignee, t.role, t.kind, t.status, t.depends_on, t.priority,
	t.created_by, t.created_at, t.reserved_for, t.reserved_until, t.ready_at, t.downstream`

// ClaimableTasks lists the project's claimable tasks of the given roles (those of the idle
// members): pending and ready (ready_at set: every dependency done), in a running or waiting
// thread; the most urgent first (priority, critical path, readiness, creation), at most
// QueueLimit per role (one busy role's backlog never hides another role's tasks).
func ClaimableTasks(ctx context.Context, q DB, projectID string, roles []string) ([]QueuedTask, error) {
	return rawAll[QueuedTask](ctx, q, `SELECT * FROM (SELECT `+queuedCols+`, th.only_roles,
			(SELECT COALESCE(jsonb_agg(d.claimed_by), '[]'::jsonb) FROM jsonb_array_elements_text(t.depends_on) dep(id)
				JOIN at_tasks d ON d.conversation_id = t.conversation_id AND d.id = dep.id) AS dep_workers,
			(SELECT COALESCE(jsonb_agg(l.name), '[]'::jsonb) FROM at_employees l
				WHERE l.project_id = th.project_id AND l.thread_id = t.thread_id) AS legacy,
			CASE WHEN t.assignee = t.role THEN false ELSE `+NamedActive+` END AS named_active,
			t.ready_at AS ready,
			row_number() OVER (PARTITION BY t.role ORDER BY t.priority, t.downstream DESC, t.ready_at, t.created_at) AS rank
		FROM at_tasks t JOIN at_threads th ON th.id = t.thread_id
		WHERE th.project_id = $1 AND `+ClaimingThread+` AND t.status = 'pending' AND `+TaskReady+`
			AND t.role = ANY($2::text[])) q
		WHERE rank <= $3 ORDER BY priority, downstream DESC, ready, created_at`, projectID, store.TextArray(roles), QueueLimit)
}

// MarkReady stamps ready_at on the pending tasks of a thread that waited on task id and now
// have every dependency done (id just became done): only its direct dependents are looked at.
func MarkReady(ctx context.Context, tx *Tx, conversationID, id string) error {
	_, err := store.Exec(ctx, tx, `UPDATE at_tasks t SET ready_at = now() WHERE t.conversation_id = $1 AND t.status = 'pending'
		AND t.ready_at IS NULL AND jsonb_exists(t.depends_on, $2) AND `+depsDone, conversationID, id)
	return err
}

// RepairReady is the safety net of the ready_at events: it stamps the pending tasks whose
// dependencies are all done but that stayed unstamped and untouched for olderThan (an event
// path that missed the stamp; normally it happens in the transaction of the last dependency's
// done) and returns them as "conversation/task".
func RepairReady(ctx context.Context, q DB, olderThan time.Duration) ([]string, error) {
	var ids []string
	err := store.Raw(ctx, q, `UPDATE at_tasks t SET ready_at = now() WHERE t.status = 'pending' AND t.ready_at IS NULL
		AND t.updated_at < now() - make_interval(secs => $1) AND `+depsDone+`
		RETURNING t.conversation_id || '/' || t.id`, olderThan.Seconds()).Scan(&ids).Error
	return ids, err
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

// projectsWithWork selects the projects that have claimable tasks (pending and ready) in running
// or waiting threads.
const projectsWithWork = `SELECT DISTINCT th.project_id FROM at_tasks t JOIN at_threads th ON th.id = t.thread_id
	WHERE t.status = 'pending' AND ` + TaskReady + ` AND ` + ClaimingThread

// ProjectsWithWork lists the projects that have claimable tasks in running or waiting threads.
func ProjectsWithWork(ctx context.Context, q DB) ([]string, error) {
	var ids []string
	err := store.Raw(ctx, q, projectsWithWork).Scan(&ids).Error
	return ids, err
}

// UserProjectsWithWork is ProjectsWithWork for one user's projects.
func UserProjectsWithWork(ctx context.Context, q DB, userID string) ([]string, error) {
	var ids []string
	err := store.Raw(ctx, q, projectsWithWork+` AND th.user_id = $1`, userID).Scan(&ids).Error
	return ids, err
}

// HasStaff reports whether the project has project members (active or not).
func HasStaff(ctx context.Context, q DB, projectID string) (bool, error) {
	n, err := count(employees(ctx, q).Where("project_id = ? AND thread_id IS NULL AND tier = 'member'", projectID))
	return n > 0, err
}
