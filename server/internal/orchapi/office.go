package orchapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// maxReplay is the longest replay window per request (api.md §3.11).
const maxReplay = 6 * time.Hour

// BoardCounts are task counts of one thread by status.
type BoardCounts map[string]int

// Board is the task board part of the office.
type Board struct {
	Claimable int                    `json:"claimable"`
	ByThread  map[string]BoardCounts `json:"by_thread" doc:"By thread number"`
}

// OfficeSnapshot is GET /api/projects/{pid}/office.
type OfficeSnapshot struct {
	LayoutID      string                      `json:"layout_id"`
	LayoutVersion string                      `json:"layout_version"`
	Threads       []core.ThreadView           `json:"threads"`
	Employees     []core.EmployeeBrief        `json:"employees"`
	Board         Board                       `json:"board"`
	Nodes         []NodeBrief                 `json:"nodes"`
	MergeQueue    []core.MergeEntryView       `json:"merge_queue"`
	CI            map[string][]core.CheckView `json:"ci" doc:"By thread number"`
	LastEventID   int64                       `json:"last_event_id"`
}

type officeOutput struct{ Body OfficeSnapshot }

type replayInput struct {
	PID    string    `path:"pid" maxLength:"64"`
	From   time.Time `query:"from" required:"true"`
	To     time.Time `query:"to" required:"true"`
	Thread int       `query:"thread" minimum:"0"`
}

// ReplayEvent is one event of the replay window.
type ReplayEvent struct {
	ID   int64     `json:"id"`
	At   time.Time `json:"at"`
	Type string    `json:"type"`
	Data any       `json:"data"`
}

// Replay is a snapshot at from plus the events until to.
type Replay struct {
	Snapshot OfficeSnapshot `json:"snapshot"`
	Events   []ReplayEvent  `json:"events"`
}

type replayOutput struct{ Body Replay }

type employeeInput struct {
	ID string `path:"id" maxLength:"64"`
}

// SessionBrief is the employee's session in the card.
type SessionBrief struct {
	State string        `json:"state"`
	Node  *core.NodeRef `json:"node"`
}

// EmployeeCard is the office popup of one employee.
type EmployeeCard struct {
	core.EmployeeBrief
	CurrentTask    *core.TaskView  `json:"current_task"`
	RecentActivity []core.Activity `json:"recent_activity"`
	Session        *SessionBrief   `json:"session"`
}

type employeeOutput struct{ Body EmployeeCard }

func (a *API) registerOffice(api huma.API) {
	huma.Register(api, op("project-office", "GET", "/api/projects/{pid}/office", "office",
		summary("First paint of the office; continue with the event stream from last_event_id")), a.officeSnapshot)
	huma.Register(api, op("project-office-replay", "GET", "/api/projects/{pid}/office/replay", "office"), a.officeReplay)
	huma.Register(api, op("get-employee", "GET", "/api/employees/{id}", "office"), a.employeeCard)
}

func (a *API) officeSnapshot(ctx context.Context, in *pidInput) (*officeOutput, error) {
	_, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	var snap OfficeSnapshot
	err = pgx.BeginTxFunc(ctx, a.e.Core.Pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(tx pgx.Tx) error {
			var err error
			snap, err = a.snapshot(ctx, tx, p)
			return err
		})
	return &officeOutput{Body: snap}, err
}

// snapshot reads everything in one REPEATABLE READ transaction so it matches last_event_id.
func (a *API) snapshot(ctx context.Context, q core.DB, p store.Project) (OfficeSnapshot, error) {
	s := OfficeSnapshot{CI: map[string][]core.CheckView{}, MergeQueue: []core.MergeEntryView{}}
	var err error
	if s.LastEventID, err = core.ProjectSeq(ctx, q, p.ID); err != nil {
		return s, err
	}
	s.LayoutID, s.LayoutVersion = a.layoutOf(ctx, p)
	threads, err := core.Threads(ctx, q, p.ID, []string{"queued", "running", "waiting", "review", "paused"})
	if err != nil {
		return s, err
	}
	if s.Threads, err = a.threadsIn(ctx, q, threads); err != nil {
		return s, err
	}
	if s.Employees, err = a.e.Core.EmployeeBriefs(ctx, q, p.ID, nil); err != nil {
		return s, err
	}
	if s.Board, err = board(ctx, q, p.ID); err != nil {
		return s, err
	}
	if err := a.queuesAndChecks(ctx, q, threads, &s); err != nil {
		return s, err
	}
	s.Nodes, err = a.nodeBriefs(ctx, p.UserID)
	return s, err
}

func (a *API) threadsIn(ctx context.Context, q core.DB, threads []core.Thread) ([]core.ThreadView, error) {
	out := make([]core.ThreadView, 0, len(threads))
	for _, t := range threads {
		v, err := a.e.Core.ThreadViewOf(ctx, q, t)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (a *API) queuesAndChecks(ctx context.Context, q core.DB, threads []core.Thread, s *OfficeSnapshot) error {
	for _, t := range threads {
		entries, err := core.MergeQueue(ctx, q, t.ID, true)
		if err != nil {
			return err
		}
		for _, m := range entries {
			s.MergeQueue = append(s.MergeQueue, core.MergeViewOf(m, t.No))
		}
		_, checks, err := core.LatestChecks(ctx, q, t.ID)
		if err != nil {
			return err
		}
		if len(checks) > 0 {
			s.CI[strconv.Itoa(t.No)] = checks
		}
	}
	return nil
}

// layoutOf is the project's layout id (project setting → global default → default) and version.
func (a *API) layoutOf(ctx context.Context, p store.Project) (string, string) {
	id := p.OfficeLayout
	if id == "" {
		var def struct {
			Layout string `json:"layout"`
		}
		if found, err := a.e.Store.Setting(ctx, "office_default", &def); err == nil && found {
			id = def.Layout
		}
	}
	if id == "" {
		id = "default"
	}
	if a.office == nil {
		return id, ""
	}
	if l, err := a.office.Get(id); err == nil && l != nil {
		return id, l.Version
	}
	return id, ""
}

func board(ctx context.Context, q core.DB, projectID string) (Board, error) {
	b := Board{ByThread: map[string]BoardCounts{}}
	rows, err := q.Query(ctx, `SELECT th.no, t.status, count(*) FROM at_tasks t JOIN at_threads th ON th.id = t.thread_id
		WHERE th.project_id = $1 AND th.status IN ('queued','running','waiting','review','paused') GROUP BY th.no, t.status`, projectID)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	for rows.Next() {
		var no, n int
		var st string
		if err := rows.Scan(&no, &st, &n); err != nil {
			return b, err
		}
		key := strconv.Itoa(no)
		if b.ByThread[key] == nil {
			b.ByThread[key] = BoardCounts{}
		}
		b.ByThread[key][st] = n
		if st == "pending" {
			b.Claimable += n
		}
	}
	return b, rows.Err()
}

func (a *API) officeReplay(ctx context.Context, in *replayInput) (*replayOutput, error) {
	_, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	if !in.To.After(in.From) || in.To.Sub(in.From) > maxReplay {
		return nil, httpapi.Fail(http.StatusUnprocessableEntity, "validation", "回放时段必须大于 0 且不超过 6 小时")
	}
	snap, err := a.snapshot(ctx, a.e.Core.Pool, p)
	if err != nil {
		return nil, err
	}
	if err := a.statusesAt(ctx, p.ID, in.From, snap.Employees); err != nil {
		return nil, err
	}
	evs, err := core.EventsBetween(ctx, a.e.Core.Pool, p.ID, in.From, in.To, 20000)
	if err != nil {
		return nil, err
	}
	return &replayOutput{Body: Replay{Snapshot: snap, Events: filterThread(evs, in.Thread)}}, nil
}

// statusesAt replaces each employee's status with its last status before t.
func (a *API) statusesAt(ctx context.Context, projectID string, t time.Time, emps []core.EmployeeBrief) error {
	rows, err := a.e.Core.Pool.Query(ctx, `SELECT DISTINCT ON (employee_id) employee_id, state, substate, bubble, bubble_kind,
		task_id, task_kind, tier, role, created_at, seq FROM at_status_events WHERE project_id = $1 AND created_at < $2
		ORDER BY employee_id, created_at DESC`, projectID, t)
	if err != nil {
		return err
	}
	defer rows.Close()
	at := map[string]core.EmployeeStatusView{}
	for rows.Next() {
		var id string
		var s core.EmployeeStatusView
		if err := rows.Scan(&id, &s.State, &s.Substate, &s.Bubble, &s.BubbleKind, &s.TaskID, &s.TaskKind, &s.Tier, &s.Role, &s.Since, &s.Seq); err != nil {
			return err
		}
		at[id] = s
	}
	for i := range emps {
		emps[i].Status = nil
		if s, ok := at[emps[i].ID]; ok {
			emps[i].Status = &s
		}
	}
	return rows.Err()
}

func filterThread(evs []core.Event, no int) []ReplayEvent {
	out := []ReplayEvent{}
	for _, e := range evs {
		if no != 0 && (e.ThreadNo == nil || *e.ThreadNo != no) {
			continue
		}
		var data any
		_ = json.Unmarshal(e.Data, &data)
		out = append(out, ReplayEvent{ID: e.Seq, At: e.CreatedAt, Type: e.Type, Data: data})
	}
	return out
}

func (a *API) employeeCard(ctx context.Context, in *employeeInput) (*employeeOutput, error) {
	u, err := a.user(ctx)
	if err != nil {
		return nil, err
	}
	emp, err := core.EmployeeByID(ctx, a.e.Core.Pool, in.ID)
	if err == nil {
		_, err = a.e.Store.ProjectOf(ctx, u.ID, emp.ProjectID)
	}
	if err != nil {
		return nil, httpapi.Fail(http.StatusNotFound, "not_found", "没有这个员工")
	}
	card, err := a.card(ctx, emp)
	return &employeeOutput{Body: card}, err
}

func (a *API) card(ctx context.Context, emp core.Employee) (EmployeeCard, error) {
	brief, err := a.e.Core.EmployeeBriefOf(ctx, a.e.Core.Pool, emp.ID)
	if err != nil {
		return EmployeeCard{}, err
	}
	c := EmployeeCard{EmployeeBrief: brief, RecentActivity: []core.Activity{}}
	if emp.ThreadID != nil {
		if task, err := core.CurrentTask(ctx, a.e.Core.Pool, *emp.ThreadID, emp.Name); err == nil {
			no := 0
			if brief.ThreadNo != nil {
				no = *brief.ThreadNo
			}
			v := core.TaskViewOf(task, no)
			c.CurrentTask = &v
		}
	}
	if s, err := core.LiveSession(ctx, a.e.Core.Pool, emp.ID); err == nil {
		c.Session = &SessionBrief{State: s.State, Node: brief.Node}
	}
	c.RecentActivity, err = a.employeeActivity(ctx, emp)
	return c, err
}

func (a *API) employeeActivity(ctx context.Context, emp core.Employee) ([]core.Activity, error) {
	rows, err := a.e.Core.Pool.Query(ctx, `SELECT seq, actor, COALESCE(payload->>'text', ''), created_at FROM at_events
		WHERE project_id = $1 AND type = 'activity' AND actor = $2 AND COALESCE(thread_id, '') = COALESCE($3, '')
		ORDER BY seq DESC LIMIT 10`, emp.ProjectID, emp.Name, emp.ThreadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.Activity{}
	for rows.Next() {
		var x core.Activity
		if err := rows.Scan(&x.ID, &x.Employee, &x.Text, &x.At); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
