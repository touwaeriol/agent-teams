package core

import (
	"context"
	"encoding/json"
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
func EmployeeByName(ctx context.Context, q DB, projectID string, threadID *string, name string) (Employee, error) {
	return take[Employee](employees(ctx, q).Where("project_id = ? AND COALESCE(thread_id, '') = COALESCE(?, '') AND name = ?",
		projectID, threadID, name))
}

// Coordinator returns the project's coordinator employee.
func Coordinator(ctx context.Context, q DB, projectID string) (Employee, error) {
	return take[Employee](employees(ctx, q).Where("project_id = ? AND thread_id IS NULL AND tier = 'coordinator'", projectID))
}

// Lead returns a thread's lead.
func Lead(ctx context.Context, q DB, threadID string) (Employee, error) {
	return take[Employee](employees(ctx, q).Where("thread_id = ? AND tier = 'lead'", threadID))
}

// ThreadEmployees lists a thread's lead and members.
func ThreadEmployees(ctx context.Context, q DB, threadID string) ([]Employee, error) {
	return find[Employee](employees(ctx, q).Where("thread_id = ? AND active", threadID).
		Order("CASE tier WHEN 'lead' THEN 0 ELSE 1 END, role, instance_no"))
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
	spec, _ := json.Marshal(n.Role)
	look, _ := json.Marshal(n.Look)
	var roleID *string
	if n.Role.ID != "" {
		roleID = &n.Role.ID
	}
	id := NewID("em")
	err := tx.Gorm(ctx).Table("at_employees").Create(map[string]any{"id": id, "project_id": n.ProjectID,
		"thread_id": n.ThreadID, "name": n.Name, "tier": n.Role.Tier, "role": n.Role.Role,
		"role_id": gorm.Expr("(SELECT id FROM at_roles WHERE id = ?)", roleID), "runtime": runtimeOf(n.Role),
		"instance_no": n.InstanceNo, "look": look, "spec": spec}).Error
	if err != nil {
		return Employee{}, err
	}
	return EmployeeByID(ctx, tx, id)
}

func runtimeOf(r store.Role) string {
	if r.Runtime == "" {
		return "claude"
	}
	return r.Runtime
}

// InstantiateThread creates lead-<no> and <role>-<n> members from the roles snapshot.
func InstantiateThread(ctx context.Context, tx *Tx, t Thread, hair func(i int) *string) ([]Employee, error) {
	var out []Employee
	for _, r := range t.RolesSnapshot {
		names := instanceNames(r, t.No)
		for i, name := range names {
			look := r.Look
			if look.Hair == nil && hair != nil {
				look.Hair = hair(i)
			}
			e, err := InsertEmployee(ctx, tx, NewEmployee{ProjectID: t.ProjectID, ThreadID: &t.ID, Name: name, Role: r,
				InstanceNo: i + 1, Look: look})
			if err != nil {
				return nil, err
			}
			out = append(out, e)
		}
	}
	return out, nil
}

func instanceNames(r store.Role, threadNo int) []string {
	switch r.Tier {
	case "coordinator":
		return nil
	case "lead":
		return []string{fmt.Sprintf("lead-%d", threadNo)}
	}
	n := r.Count
	if n < 1 {
		n = 1
	}
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("%s-%d", r.Role, i+1)
	}
	return names
}

// SetEmployeeNode records the node the employee's sessions run on.
func SetEmployeeNode(ctx context.Context, tx *Tx, employeeID string, nodeID *string) error {
	return Changed(tx.Gorm(ctx).Table("at_employees").Where("id = ?", employeeID).Update("node_id", nodeID))
}

// EmployeeBriefs lists employees of a project (threadID nil = all, else that thread) with status and node.
func (s *Store) EmployeeBriefs(ctx context.Context, q DB, projectID string, threadID *string) ([]EmployeeBrief, error) {
	rows, err := store.Query(ctx, q, `SELECT e.id, e.name, e.tier, e.role, COALESCE(e.spec->>'title', ''), e.runtime, t.no,
		n.id, n.name, e.look, st.state, st.substate, st.bubble, st.bubble_kind, st.task_id, st.task_kind, st.since, st.seq
		FROM at_employees e LEFT JOIN at_threads t ON t.id = e.thread_id LEFT JOIN at_nodes n ON n.id = e.node_id
		LEFT JOIN at_employee_status st ON st.employee_id = e.id
		WHERE e.project_id = $1 AND e.active AND ($2::text IS NULL OR e.thread_id = $2)
		ORDER BY t.no NULLS FIRST, CASE e.tier WHEN 'coordinator' THEN 0 WHEN 'lead' THEN 1 ELSE 2 END, e.role, e.instance_no`,
		projectID, threadID)
	return collect(rows, err, s.scanBrief)
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
