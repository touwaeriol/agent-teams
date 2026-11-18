package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

func employees(ctx context.Context, q DB) *gorm.DB { return q.Gorm(ctx).Model(&Employee{}) }

// EmployeeByID loads an employee.
func EmployeeByID(ctx context.Context, q DB, id string) (Employee, error) {
	return take[Employee](employees(ctx, q).Where("id = ?", id))
}

// EmployeeByName finds an employee of a thread (threadID nil = the project coordinator scope).
// Inside a thread the thread's own employees (lead, legacy members) come first, then the
// project member of that name (shared-members.md §3).
func EmployeeByName(ctx context.Context, q DB, projectID string, threadID *string, name string) (Employee, error) {
	e, err := take[Employee](employees(ctx, q).Where("project_id = ? AND COALESCE(thread_id, '') = COALESCE(?, '') AND name = ?",
		projectID, threadID, name))
	if threadID == nil || !errors.Is(err, ErrNotFound) {
		return e, err
	}
	return ProjectMemberByName(ctx, q, projectID, name)
}

// ProjectMemberByName finds a project member (thread_id NULL) by name.
func ProjectMemberByName(ctx context.Context, q DB, projectID, name string) (Employee, error) {
	return take[Employee](employees(ctx, q).Where("project_id = ? AND thread_id IS NULL AND tier = 'member' AND name = ?",
		projectID, name))
}

// Coordinator returns the project's coordinator employee.
func Coordinator(ctx context.Context, q DB, projectID string) (Employee, error) {
	return take[Employee](employees(ctx, q).Where("project_id = ? AND thread_id IS NULL AND tier = 'coordinator'", projectID))
}

// Lead returns a thread's lead.
func Lead(ctx context.Context, q DB, threadID string) (Employee, error) {
	return take[Employee](employees(ctx, q).Where("thread_id = ? AND tier = 'lead'", threadID))
}

// ThreadEmployees lists the employees whose rows belong to a thread: its lead and the members
// created with it before members became project staff.
func ThreadEmployees(ctx context.Context, q DB, threadID string) ([]Employee, error) {
	return find[Employee](employees(ctx, q).Where("thread_id = ? AND active", threadID).Order(participantOrder))
}

const participantOrder = "CASE tier WHEN 'lead' THEN 0 ELSE 1 END, role, instance_no"

// ThreadParticipants are the people of a thread: its own employees (lead, legacy members) and
// the project members that have or had a task there, work there now or opened a session there.
// A project member whose name a legacy member of the thread holds is that legacy member there.
func ThreadParticipants(ctx context.Context, q DB, threadID string) ([]Employee, error) {
	return find[Employee](employees(ctx, q).Where(`active AND (thread_id = @t OR (thread_id IS NULL AND tier = 'member'
		AND project_id = (SELECT project_id FROM at_threads WHERE id = @t)
		AND NOT EXISTS (SELECT 1 FROM at_employees l WHERE l.thread_id = @t AND l.name = at_employees.name)
		AND (current_thread_id = @t
			OR EXISTS (SELECT 1 FROM at_tasks x WHERE x.thread_id = @t AND x.claimed_by = at_employees.name)
			OR EXISTS (SELECT 1 FROM at_agent_sessions a WHERE a.employee_id = at_employees.id AND a.thread_id = @t))))`,
		sql.Named("t", threadID)).Order(participantOrder))
}

// ThreadTeam is who a thread's lead works with: the thread's own employees and the project's
// active members of the roles the thread uses (OnlyRoles); a legacy member of the thread hides
// the project member of the same name.
func ThreadTeam(ctx context.Context, q DB, t Thread) ([]Employee, error) {
	g := employees(ctx, q).Where(`active AND (thread_id = @t OR (thread_id IS NULL AND tier = 'member' AND project_id = @p
		AND NOT EXISTS (SELECT 1 FROM at_employees l WHERE l.thread_id = @t AND l.name = at_employees.name)))`,
		sql.Named("t", t.ID), sql.Named("p", t.ProjectID))
	if len(t.OnlyRoles) > 0 {
		g = g.Where("thread_id IS NOT NULL OR role IN ?", t.OnlyRoles)
	}
	return find[Employee](g.Order(participantOrder))
}

// ProjectMembers lists a project's active members (staff shared by every thread).
func ProjectMembers(ctx context.Context, q DB, projectID string) ([]Employee, error) {
	return find[Employee](employees(ctx, q).Where("project_id = ? AND thread_id IS NULL AND tier = 'member' AND active", projectID).
		Order("role, instance_no"))
}

// ProjectEmployees lists every active employee of a project.
func ProjectEmployees(ctx context.Context, q DB, projectID string) ([]Employee, error) {
	return find[Employee](employees(ctx, q).Where("project_id = ? AND active", projectID).Order("created_at"))
}

// NewEmployee is what InsertEmployee stores.
type NewEmployee struct {
	ProjectID  string
	ThreadID   *string
	Name       string
	Role       store.Role
	InstanceNo int
	Look       store.Look
}

// InsertEmployee creates an employee from a role template snapshot.
func InsertEmployee(ctx context.Context, tx *Tx, n NewEmployee) (Employee, error) {
	id := NewID("em")
	err := tx.Gorm(ctx).Table("at_employees").Create(map[string]any{"id": id, "project_id": n.ProjectID,
		"thread_id": n.ThreadID, "name": n.Name, "tier": n.Role.Tier, "role": n.Role.Role,
		"role_id": gorm.Expr("(SELECT id FROM at_roles WHERE id = ?)", nilIfEmpty(n.Role.ID)), "runtime": runtimeOf(n.Role),
		"instance_no": n.InstanceNo, "look": jsonOf(n.Look), "spec": jsonOf(n.Role)}).Error
	if err != nil {
		return Employee{}, err
	}
	return EmployeeByID(ctx, tx, id)
}

func jsonOf(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func runtimeOf(r store.Role) string {
	if r.Runtime == "" {
		return "claude"
	}
	return r.Runtime
}

// InstantiateThread creates the thread's lead (lead-<no>) from the roles snapshot; members are
// project staff (EnsureStaff), not created per thread.
func InstantiateThread(ctx context.Context, tx *Tx, t Thread, hair func(i int) *string) ([]Employee, error) {
	for _, r := range t.RolesSnapshot {
		if r.Tier != "lead" {
			continue
		}
		e, err := InsertEmployee(ctx, tx, NewEmployee{ProjectID: t.ProjectID, ThreadID: &t.ID, Name: fmt.Sprintf("lead-%d", t.No),
			Role: r, InstanceNo: 1, Look: withHair(r.Look, hair, 0)})
		if err != nil {
			return nil, err
		}
		return []Employee{e}, nil
	}
	return nil, nil
}

// withHair gives a look without its own hair colour the palette's i-th one.
func withHair(look store.Look, hair func(i int) *string, i int) store.Look {
	if look.Hair == nil && hair != nil {
		look.Hair = hair(i)
	}
	return look
}

// SetEmployeeNode records the node the employee's sessions run on.
func SetEmployeeNode(ctx context.Context, tx *Tx, employeeID string, nodeID *string) error {
	return Changed(tx.Gorm(ctx).Table("at_employees").Where("id = ?", employeeID).Update("node_id", nodeID))
}

// SetCurrentThread records the thread a project member works in now (nil: none).
func SetCurrentThread(ctx context.Context, tx *Tx, employeeID string, threadID *string) error {
	return tx.Gorm(ctx).Table("at_employees").Where("id = ?", employeeID).Update("current_thread_id", threadID).Error
}

// LeaveThread clears the current thread of the project members working in a thread that ended.
func LeaveThread(ctx context.Context, tx *Tx, threadID string) error {
	return tx.Gorm(ctx).Table("at_employees").Where("current_thread_id = ?", threadID).Update("current_thread_id", nil).Error
}

// EmployeeBriefs lists employees of a project (threadID nil = all, else that thread's
// participants) with status and node.
func (s *Store) EmployeeBriefs(ctx context.Context, q DB, projectID string, threadID *string) ([]EmployeeBrief, error) {
	ids, err := participantIDs(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	rows, err := store.Query(ctx, q, `SELECT e.id, e.name, e.tier, e.role, COALESCE(e.spec->>'title', ''), e.runtime, t.no,
		n.id, n.name, e.look, st.state, st.substate, st.bubble, st.bubble_kind, st.task_id, st.task_kind, st.since, st.seq
		FROM at_employees e LEFT JOIN at_threads t ON t.id = e.thread_id LEFT JOIN at_nodes n ON n.id = e.node_id
		LEFT JOIN at_employee_status st ON st.employee_id = e.id
		WHERE e.project_id = $1 AND e.active AND ($2 OR e.id = ANY($3))
		ORDER BY (e.thread_id IS NULL AND e.tier = 'member'), t.no NULLS FIRST, CASE e.tier WHEN 'coordinator' THEN 0 WHEN 'lead' THEN 1 ELSE 2 END, e.role, e.instance_no`,
		projectID, threadID == nil, ids)
	return collect(rows, err, s.scanBrief)
}

// participantIDs are the ids of a thread's participants (none for threadID nil).
func participantIDs(ctx context.Context, q DB, threadID *string) ([]string, error) {
	ids := []string{}
	if threadID == nil {
		return ids, nil
	}
	ps, err := ThreadParticipants(ctx, q, *threadID)
	for _, e := range ps {
		ids = append(ids, e.ID)
	}
	return ids, err
}

// EmployeeBriefOf renders one employee.
func (s *Store) EmployeeBriefOf(ctx context.Context, q DB, employeeID string) (EmployeeBrief, error) {
	row := store.QueryRow(ctx, q, `SELECT e.id, e.name, e.tier, e.role, COALESCE(e.spec->>'title', ''), e.runtime, t.no,
		n.id, n.name, e.look, st.state, st.substate, st.bubble, st.bubble_kind, st.task_id, st.task_kind, st.since, st.seq
		FROM at_employees e LEFT JOIN at_threads t ON t.id = e.thread_id LEFT JOIN at_nodes n ON n.id = e.node_id
		LEFT JOIN at_employee_status st ON st.employee_id = e.id WHERE e.id = $1`, employeeID)
	b, err := s.scanBrief(row)
	return b, MapErr(err)
}

func (s *Store) scanBrief(r Scanner) (EmployeeBrief, error) {
	var b EmployeeBrief
	var nodeID, nodeName, state, sub, bubble, bk, taskID, taskKind *string
	var since *time.Time
	var seq *int64
	err := r.Scan(&b.ID, &b.Name, &b.Tier, &b.Role, &b.Title, &b.Runtime, &b.ThreadNo, &nodeID, &nodeName, &b.Look,
		&state, &sub, &bubble, &bk, &taskID, &taskKind, &since, &seq)
	if err != nil {
		return b, err
	}
	if nodeID != nil {
		b.Node = &NodeRef{ID: *nodeID, Name: deref(nodeName), Online: s.Online(*nodeID)}
	}
	if state != nil {
		b.Status = &EmployeeStatusView{State: *state, Substate: deref(sub), Bubble: deref(bubble), BubbleKind: deref(bk),
			TaskID: deref(taskID), TaskKind: deref(taskKind), Tier: b.Tier, Role: b.Role, Since: *since, Seq: *seq}
	}
	return b, nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
