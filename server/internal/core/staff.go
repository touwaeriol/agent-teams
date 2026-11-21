package core

import (
	"bytes"
	"context"
	"fmt"
	"slices"

	"gorm.io/gorm"

	"github.com/touwaeriol/agent-teams/server/internal/runtimes"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Staffing is what EnsureStaff works from: the owner's role templates and nodes, and the hair
// palette new members draw from (by instance number).
type Staffing struct {
	Roles []store.Role
	Nodes []store.Node
	Hair  func(i int) *string
}

// EnsureStaff makes the project's members (thread_id NULL, shared by every thread) match the
// member templates' counts (shared-members.md §2): <role>-<n> rows are created or re-activated
// with the template's current snapshot, members beyond the counts are deactivated unless they
// hold a claimed or running task (a later call stops them), and members without a node get a
// home node. Idempotent; it locks the project row so concurrent calls run one after another. It
// returns the pending tasks that named a member it retired, now tasks of the member's role.
func EnsureStaff(ctx context.Context, tx *Tx, projectID string, s Staffing) ([]Task, error) {
	if err := LockProject(ctx, tx, projectID); err != nil {
		return nil, err
	}
	staff, err := staffByName(ctx, tx, projectID)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, r := range s.Roles {
		for n := 1; r.Tier == "member" && n <= staffCount(r); n++ {
			name := fmt.Sprintf("%s-%d", r.Role, n)
			want[name] = true
			if err := keepMember(ctx, tx, projectID, staff[name], r, n, s.Hair); err != nil {
				return nil, err
			}
		}
	}
	released, err := retireSurplus(ctx, tx, projectID, staff, want)
	if err != nil {
		return nil, err
	}
	return released, placeHomeless(ctx, tx, projectID, s.Nodes)
}

// LockProject takes the project row lock. Every event emit updates that row (the event seq), so
// a transaction that changes members or tasks takes it first: rows are then always locked
// project → member → task, and a claim pass, re-staffing and deliveries cannot deadlock.
func LockProject(ctx context.Context, tx *Tx, projectID string) error {
	return tx.Gorm(ctx).Exec(`SELECT 1 FROM at_projects WHERE id = ? FOR UPDATE`, projectID).Error
}

// StaffedProjects lists the user's projects that have project members.
func StaffedProjects(ctx context.Context, q DB, userID string) ([]string, error) {
	var ids []string
	err := q.Gorm(ctx).Raw(`SELECT DISTINCT e.project_id FROM at_employees e JOIN at_projects p ON p.id = e.project_id
		WHERE p.user_id = ? AND e.thread_id IS NULL AND e.tier = 'member' ORDER BY 1`, userID).Scan(&ids).Error
	return ids, err
}

func staffCount(r store.Role) int {
	return max(r.Count, 1)
}

// Surplus reports whether a project member is beyond what the role templates count (its role
// is gone or its instance number exceeds the role's count): EnsureStaff would retire it.
func Surplus(e Employee, roles []store.Role) bool {
	for _, r := range roles {
		if r.Tier == "member" && r.Role == e.Role {
			return e.InstanceNo > staffCount(r)
		}
	}
	return true
}

// staffByName loads every project member, active or not.
func staffByName(ctx context.Context, q DB, projectID string) (map[string]Employee, error) {
	list, err := find[Employee](employees(ctx, q).Where("project_id = ? AND thread_id IS NULL AND tier = 'member'", projectID))
	out := make(map[string]Employee, len(list))
	for _, e := range list {
		out[e.Name] = e
	}
	return out, err
}

// keepMember creates <role>-<n>, or refreshes an existing one from the template and activates
// it (only when something changed: no row lock for an unchanged member); a member whose runtime
// is no longer one of the role's goes to the role's first runtime and loses its node (so it gets
// a fitting one) and its /model pick (an id of the old runtime).
func keepMember(ctx context.Context, tx *Tx, projectID string, e Employee, r store.Role, n int, hair func(int) *string) error {
	if e.ID == "" {
		_, err := InsertEmployee(ctx, tx, NewEmployee{ProjectID: projectID, Name: fmt.Sprintf("%s-%d", r.Role, n), Role: r,
			InstanceNo: n, Look: withHair(r.Look, hair, n-1)})
		return err
	}
	if upToDate(e, r) {
		return nil
	}
	set := map[string]any{"active": true, "spec": jsonOf(r),
		"role_id": gorm.Expr("(SELECT id FROM at_roles WHERE id = ?)", nilIfEmpty(r.ID))}
	if !slices.Contains(r.RuntimeList(), e.Runtime) {
		set["runtime"], set["node_id"], set["model"] = runtimeOf(r), nil, ""
	}
	return tx.Gorm(ctx).Table("at_employees").Where("id = ?", e.ID).Updates(set).Error
}

// upToDate reports whether a member already is active with the template's snapshot.
func upToDate(e Employee, r store.Role) bool {
	return e.Active && slices.Contains(r.RuntimeList(), e.Runtime) && deref(e.RoleID) == r.ID && bytes.Equal(jsonOf(e.Spec), jsonOf(r))
}

// retireSurplus deactivates active members the templates no longer count, except busy ones
// (their rows are locked first, so no claim slips in between), and turns their pending named
// tasks into tasks of their role.
func retireSurplus(ctx context.Context, tx *Tx, projectID string, staff map[string]Employee, want map[string]bool) ([]Task, error) {
	var surplus []string
	for name, e := range staff {
		if e.Active && !want[name] {
			surplus = append(surplus, e.ID)
		}
	}
	if len(surplus) == 0 {
		return nil, nil
	}
	if err := tx.Gorm(ctx).Exec(`SELECT 1 FROM at_employees WHERE id IN ? FOR UPDATE`, surplus).Error; err != nil {
		return nil, err
	}
	busy, err := BusyMembers(ctx, tx, projectID)
	if err != nil {
		return nil, err
	}
	var ids, names []string
	for _, id := range surplus {
		if e := staffByID(staff, id); !busy[e.Name] {
			ids, names = append(ids, id), append(names, e.Name)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if err := tx.Gorm(ctx).Table("at_employees").Where("id IN ?", ids).Update("active", false).Error; err != nil {
		return nil, err
	}
	return releaseNamed(ctx, tx, projectID, names)
}

func staffByID(staff map[string]Employee, id string) Employee {
	for _, e := range staff {
		if e.ID == id {
			return e
		}
	}
	return Employee{}
}

// releaseNamed turns the pending tasks named to the given (retired) project members into tasks
// of their role, so another member of the role claims them.
func releaseNamed(ctx context.Context, tx *Tx, projectID string, names []string) ([]Task, error) {
	return rawAll[Task](ctx, tx, `UPDATE at_tasks x SET assignee = x.role FROM at_threads th
		WHERE th.id = x.thread_id AND th.project_id = $1 AND th.status NOT IN ('done','failed','archived')
		AND x.status = 'pending' AND x.assignee = ANY($2::text[]) AND x.assignee <> x.role
		AND NOT EXISTS (SELECT 1 FROM at_employees l WHERE l.thread_id = x.thread_id AND l.name = x.assignee)
		RETURNING x.*`, projectID, store.TextArray(names))
}

// placeHomeless gives every active member without a node a home node, spreading members over
// the nodes and members of one role over different nodes.
func placeHomeless(ctx context.Context, tx *Tx, projectID string, nodes []store.Node) error {
	members, err := ProjectMembers(ctx, tx, projectID)
	if err != nil {
		return err
	}
	load := newNodeLoad(members)
	for _, e := range members {
		if e.NodeID != nil {
			continue
		}
		home := load.pick(e, NodeCandidates(e, nodes))
		if home == nil {
			continue
		}
		if err := tx.Gorm(ctx).Table("at_employees").Where("id = ?", e.ID).Update("node_id", home.ID).Error; err != nil {
			return err
		}
		load.add(home.ID, e.Role)
	}
	return nil
}

// nodeLoad counts the members homed on each node, in total and per role.
type nodeLoad struct {
	total  map[string]int
	byRole map[string]int // node + "/" + role
}

func newNodeLoad(members []Employee) nodeLoad {
	l := nodeLoad{total: map[string]int{}, byRole: map[string]int{}}
	for _, e := range members {
		if e.NodeID != nil {
			l.add(*e.NodeID, e.Role)
		}
	}
	return l
}

func (l nodeLoad) add(nodeID, role string) {
	l.total[nodeID]++
	l.byRole[nodeID+"/"+role]++
}

// pick is the candidate with the fewest members of e's role, then the fewest members, then the
// earliest in the list (nil without candidates).
func (l nodeLoad) pick(e Employee, cands []store.Node) *store.Node {
	if len(cands) == 0 {
		return nil
	}
	best := slices.MinFunc(cands, func(a, b store.Node) int {
		if d := l.byRole[a.ID+"/"+e.Role] - l.byRole[b.ID+"/"+e.Role]; d != 0 {
			return d
		}
		return l.total[a.ID] - l.total[b.ID]
	})
	return &best
}

// NodeCandidates are the nodes an employee may run on: the role's configured nodes (any node
// when they were all deleted), and for Codex only isolated nodes.
func NodeCandidates(e Employee, nodes []store.Node) []store.Node {
	var picked []store.Node
	for _, n := range nodes {
		if len(e.Spec.NodeIDs) > 0 && !slices.Contains(e.Spec.NodeIDs, n.ID) {
			continue
		}
		if !runtimes.RunsOn(e.Runtime, n.Isolated, n.Isolation) {
			continue
		}
		picked = append(picked, n)
	}
	if len(picked) == 0 && len(e.Spec.NodeIDs) > 0 && !runtimes.Of(e.Runtime).NeedsIsolation {
		return nodes // configured nodes were deleted: fall back to any of the user's nodes
	}
	return picked
}

// StaleRoleModels are the user's coordinators and the leads of unfinished threads whose role copy
// (spec.model) differs from model, the model of their tier's template (tier: coordinator or
// lead), rows locked. Unlike members (keepMember), their copy is otherwise not refreshed after
// creation; only the model follows the template (orchestrator.RolesSaved).
func StaleRoleModels(ctx context.Context, tx *Tx, userID, tier, model string) ([]Employee, error) {
	var out []Employee
	err := employees(ctx, tx).Clauses(ForUpdate).
		Where("tier = ? AND COALESCE(spec->>'model', '') <> ?", tier, model).
		Where("project_id IN (SELECT id FROM at_projects WHERE user_id = ?)", userID).
		Where("thread_id IS NULL OR thread_id IN (SELECT id FROM at_threads WHERE status NOT IN ('done', 'failed', 'archived'))").
		Order("id").Find(&out).Error
	return out, err
}

// SetSpecModel writes model into the employee's role copy (spec.model).
func SetSpecModel(ctx context.Context, tx *Tx, employeeID, model string) error {
	return tx.Gorm(ctx).Exec(`UPDATE at_employees SET spec = jsonb_set(spec, '{model}', to_jsonb(?::text)) WHERE id = ?`,
		model, employeeID).Error
}
