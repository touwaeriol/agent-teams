package core

import (
	"context"
	"fmt"
	"slices"

	"gorm.io/gorm"

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
// home node. Idempotent; it locks the project row so concurrent calls run one after another.
func EnsureStaff(ctx context.Context, tx *Tx, projectID string, s Staffing) error {
	if err := tx.Gorm(ctx).Exec(`SELECT 1 FROM at_projects WHERE id = ? FOR UPDATE`, projectID).Error; err != nil {
		return err
	}
	staff, err := staffByName(ctx, tx, projectID)
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, r := range s.Roles {
		for n := 1; r.Tier == "member" && n <= staffCount(r); n++ {
			name := fmt.Sprintf("%s-%d", r.Role, n)
			want[name] = true
			if err := keepMember(ctx, tx, projectID, staff[name], r, n, s.Hair); err != nil {
				return err
			}
		}
	}
	if err := retireSurplus(ctx, tx, projectID, staff, want); err != nil {
		return err
	}
	return placeHomeless(ctx, tx, projectID, s.Nodes)
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
// it; a member whose runtime changes loses its node so it gets a fitting one.
func keepMember(ctx context.Context, tx *Tx, projectID string, e Employee, r store.Role, n int, hair func(int) *string) error {
	if e.ID == "" {
		_, err := InsertEmployee(ctx, tx, NewEmployee{ProjectID: projectID, Name: fmt.Sprintf("%s-%d", r.Role, n), Role: r,
			InstanceNo: n, Look: withHair(r.Look, hair, n-1)})
		return err
	}
	set := map[string]any{"active": true, "spec": jsonOf(r), "runtime": runtimeOf(r),
		"role_id": gorm.Expr("(SELECT id FROM at_roles WHERE id = ?)", nilIfEmpty(r.ID))}
	if runtimeOf(r) != e.Runtime {
		set["node_id"] = nil
	}
	return tx.Gorm(ctx).Table("at_employees").Where("id = ?", e.ID).Updates(set).Error
}

// retireSurplus deactivates active members the templates no longer count, except busy ones.
func retireSurplus(ctx context.Context, tx *Tx, projectID string, staff map[string]Employee, want map[string]bool) error {
	busy, err := BusyMembers(ctx, tx, projectID)
	if err != nil {
		return err
	}
	var retire []string
	for name, e := range staff {
		if e.Active && !want[name] && !busy[name] {
			retire = append(retire, e.ID)
		}
	}
	if len(retire) == 0 {
		return nil
	}
	return tx.Gorm(ctx).Table("at_employees").Where("id IN ?", retire).Update("active", false).Error
}

// BusyMembers names the project members holding a claimed or running task anywhere in the
// project (a task in a thread whose legacy member has that name is the legacy member's).
func BusyMembers(ctx context.Context, q DB, projectID string) (map[string]bool, error) {
	var names []string
	err := q.Gorm(ctx).Raw(`SELECT DISTINCT x.claimed_by FROM at_tasks x JOIN at_threads t ON t.id = x.thread_id
		WHERE t.project_id = ? AND x.status IN ('claimed','running')
		AND NOT EXISTS (SELECT 1 FROM at_employees l WHERE l.thread_id = x.thread_id AND l.name = x.claimed_by)`, projectID).
		Scan(&names).Error
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out, err
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
