package orchapi

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/delivery"
	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Waiting is "等你处理" across threads.
type Waiting struct {
	Questions []core.QuestionView `json:"questions"`
	Reviews   []core.ThreadView   `json:"reviews"`
}

type waitingOutput struct{ Body Waiting }

type searchInput struct {
	PID string `path:"pid" maxLength:"64"`
	Q   string `query:"q" minLength:"1" maxLength:"200"`
}

// SearchResult groups matches.
type SearchResult struct {
	Threads      []core.ThreadView      `json:"threads"`
	Messages     []SearchMessage        `json:"messages"`
	Tasks        []core.TaskView        `json:"tasks"`
	Deliverables []core.DeliverableView `json:"deliverables"`
}

// SearchMessage is a matching message with its conversation.
type SearchMessage struct {
	ConversationID string           `json:"conversation_id"`
	ThreadNo       *int             `json:"thread_no"`
	Message        core.MessageView `json:"message"`
}

type searchOutput struct{ Body SearchResult }

// NodeBrief is a node in project views (office server room).
type NodeBrief struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Online         bool   `json:"online"`
	Isolation      string `json:"isolation"`
	ActiveSessions int    `json:"active_sessions"`
	ParkedSessions int    `json:"parked_sessions"`
	Queued         int    `json:"queued"`
	Slots          int    `json:"slots"`
}

// DayCost is one day of estimated cost.
type DayCost struct {
	Day     string  `json:"day"`
	CostUSD float64 `json:"cost_usd"`
}

// Usage7d is the last week's estimated cost.
type Usage7d struct {
	CostUSDEst float64   `json:"cost_usd_est"`
	ByDay      []DayCost `json:"by_day"`
}

// Overview is the project home in one request.
type Overview struct {
	Project            httpapi.ProjectView    `json:"project"`
	Threads            []core.ThreadView      `json:"threads"`
	Waiting            Waiting                `json:"waiting"`
	RecentDeliverables []core.DeliverableView `json:"recent_deliverables"`
	Memory             []store.MemoryBrief    `json:"memory" doc:"Memory files, MEMORY.md first (at most 6)"`
	Usage7d            Usage7d                `json:"usage_7d"`
	Nodes              []NodeBrief            `json:"nodes"`
}

type overviewOutput struct{ Body Overview }

type ciDetectOutput struct{ Body delivery.Detection }

// ciRepoInput names the repository of a CI request ("" = the project's first repository).
type ciRepoInput struct {
	PID  string `path:"pid" maxLength:"64"`
	Repo string `query:"repo" maxLength:"300" doc:"owner/name of one of the project's repositories; default the first"`
}

type setupPROutput struct {
	Body struct {
		PRURL string `json:"pr_url"`
	}
}

func (a *API) registerProjectViews(api huma.API) {
	t := "projects"
	huma.Register(api, op("project-waiting", "GET", "/api/projects/{pid}/waiting", t, summary("Open questions and cards, threads in review")), a.waiting)
	huma.Register(api, op("project-search", "GET", "/api/projects/{pid}/search", t), a.search)
	huma.Register(api, op("project-overview", "GET", "/api/projects/{pid}/overview", t), a.overview)
	huma.Register(api, op("ci-detect", "GET", "/api/projects/{pid}/ci/detect", t, summary("Workflows, branch coverage, checks permission, ruleset advice")), a.ciDetect)
	huma.Register(api, op("ci-setup-pr", "POST", "/api/projects/{pid}/ci/setup-pr", t, summary("Open a PR adding thread/** and mq/** push triggers")), a.ciSetupPR)
}

func (a *API) waiting(ctx context.Context, in *pidInput) (*waitingOutput, error) {
	_, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	w, err := a.waitingOf(ctx, p.ID)
	return &waitingOutput{Body: w}, err
}

func (a *API) waitingOf(ctx context.Context, projectID string) (Waiting, error) {
	qs, err := core.OpenUserQuestions(ctx, a.e.Core.Pool, projectID)
	if err != nil {
		return Waiting{}, err
	}
	views, err := a.questionViews(ctx, qs)
	if err != nil {
		return Waiting{}, err
	}
	reviews, err := core.Threads(ctx, a.e.Core.Pool, projectID, []string{"review"})
	if err != nil {
		return Waiting{}, err
	}
	rv, err := a.threadViews(ctx, reviews)
	return Waiting{Questions: views, Reviews: rv}, err
}

func (a *API) search(ctx context.Context, in *searchInput) (*searchOutput, error) {
	_, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	q := strings.TrimSpace(in.Q)
	res := SearchResult{}
	if res.Threads, err = a.searchThreads(ctx, p.ID, q); err != nil {
		return nil, err
	}
	if res.Messages, err = a.searchMessages(ctx, p.ID, q); err != nil {
		return nil, err
	}
	if res.Tasks, err = a.searchTasks(ctx, p.ID, q); err != nil {
		return nil, err
	}
	res.Deliverables, err = a.deliverables(ctx, p.ID, core.DeliverableFilter{Q: q, Limit: 20, NoImages: true})
	return &searchOutput{Body: res}, err
}

func (a *API) searchThreads(ctx context.Context, pid, q string) ([]core.ThreadView, error) {
	all, err := core.Threads(ctx, a.e.Core.Pool, pid, nil)
	if err != nil {
		return nil, err
	}
	var hits []core.Thread
	lq := strings.ToLower(q)
	for _, t := range all {
		if strings.Contains(strings.ToLower(t.Title), lq) || strings.Contains(strings.ToLower(t.Brief), lq) {
			hits = append(hits, t)
		}
	}
	return a.threadViews(ctx, hits)
}

func (a *API) searchMessages(ctx context.Context, pid, q string) ([]SearchMessage, error) {
	rows, err := store.Query(ctx, a.e.Core.Pool, `SELECT m.id, m.conversation_id, m.role, m.content, m.created_at, m.author, m.kind,
		m.delivered_at, m.meta, t.no FROM at_messages m JOIN at_conversations c ON c.id = m.conversation_id
		LEFT JOIN at_threads t ON t.id = c.id
		WHERE c.project_id = $1 AND c.kind IN ('coordinator','thread') AND m.content ILIKE '%' || $2 || '%'
		ORDER BY m.id DESC LIMIT 30`, pid, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SearchMessage{}
	for rows.Next() {
		var m core.Message
		var no *int
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content, &m.CreatedAt, &m.Author, &m.Kind, &m.DeliveredAt, &m.Meta, &no); err != nil {
			return nil, err
		}
		out = append(out, SearchMessage{ConversationID: m.ConversationID, ThreadNo: no, Message: core.MessageViewOf(m)})
	}
	return out, rows.Err()
}

func (a *API) searchTasks(ctx context.Context, pid, q string) ([]core.TaskView, error) {
	var rows []struct {
		core.Task `gorm:"embedded"`
		No        int
	}
	err := a.e.Core.Pool.Gorm(ctx).Table("at_tasks t").Select("t.*, th.no").Joins("JOIN at_threads th ON th.id = t.thread_id").
		Where("th.project_id = ? AND (t.instruction ILIKE '%' || ? || '%' OR t.summary ILIKE '%' || ? || '%')", pid, q, q).
		Order("t.created_at DESC").Limit(30).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]core.TaskView, 0, len(rows))
	for _, r := range rows {
		out = append(out, core.TaskViewOf(r.Task, r.No))
	}
	return out, nil
}

func (a *API) overview(ctx context.Context, in *pidInput) (*overviewOutput, error) {
	_, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	ov := Overview{Memory: []store.MemoryBrief{}}
	if ov.Project, err = a.projectView(ctx, p); err != nil {
		return nil, err
	}
	threads, err := core.Threads(ctx, a.e.Core.Pool, p.ID, nil)
	if err != nil {
		return nil, err
	}
	if ov.Threads, err = a.threadViews(ctx, threads); err != nil {
		return nil, err
	}
	if ov.Waiting, err = a.waitingOf(ctx, p.ID); err != nil {
		return nil, err
	}
	if ov.RecentDeliverables, err = a.deliverables(ctx, p.ID, core.DeliverableFilter{Limit: 6, NoImages: true}); err != nil {
		return nil, err
	}
	if ov.Memory, err = a.recentMemory(ctx, p.ID); err != nil {
		return nil, err
	}
	if ov.Usage7d, err = a.usage7d(ctx, p.ID); err != nil {
		return nil, err
	}
	ov.Nodes, err = a.nodeBriefs(ctx, p.UserID)
	return &overviewOutput{Body: ov}, err
}

// projectView renders ProjectView with the same delivery defaults as the REST layer.
func (a *API) projectView(ctx context.Context, p store.Project) (httpapi.ProjectView, error) {
	d, err := core.DeliveryOf(ctx, a.e.Core.Pool, p.ID)
	if err != nil {
		return httpapi.ProjectView{}, err
	}
	var hd httpapi.Delivery
	raw, _ := json.Marshal(d)
	_ = json.Unmarshal(raw, &hd)
	return httpapi.ProjectViewOf(p, hd), nil
}

func (a *API) recentMemory(ctx context.Context, pid string) ([]store.MemoryBrief, error) {
	files, err := store.ListMemoryFiles(ctx, a.e.Core.Pool, store.ProjectMemory(pid))
	if len(files) > 6 {
		files = files[:6]
	}
	return files, err
}

func (a *API) usage7d(ctx context.Context, pid string) (Usage7d, error) {
	rows, err := store.Query(ctx, a.e.Core.Pool, `SELECT to_char(date_trunc('day', created_at), 'YYYY-MM-DD'), COALESCE(sum(cost_usd), 0)::float8
		FROM at_usage WHERE project_id = $1 AND created_at >= $2 GROUP BY 1 ORDER BY 1`, pid, time.Now().Add(-7*24*time.Hour))
	if err != nil {
		return Usage7d{}, err
	}
	defer rows.Close()
	u := Usage7d{ByDay: []DayCost{}}
	for rows.Next() {
		var d DayCost
		if err := rows.Scan(&d.Day, &d.CostUSD); err != nil {
			return u, err
		}
		u.ByDay = append(u.ByDay, d)
		u.CostUSDEst += d.CostUSD
	}
	return u, rows.Err()
}

func (a *API) nodeBriefs(ctx context.Context, userID string) ([]NodeBrief, error) {
	nodes, err := a.e.Store.ListNodes(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]NodeBrief, 0, len(nodes))
	for _, n := range nodes {
		live := a.e.Gateway.Status(n.ID)
		iso := n.Isolation
		if live.Isolation != "" {
			iso = live.Isolation
		}
		out = append(out, NodeBrief{ID: n.ID, Name: n.Name, Online: live.Online, Isolation: iso, ActiveSessions: live.ActiveSessions,
			ParkedSessions: live.ParkedSessions, Queued: live.Queued, Slots: a.e.Gateway.Capacity(n)})
	}
	return out, nil
}

func (a *API) ciDetect(ctx context.Context, in *ciRepoInput) (*ciDetectOutput, error) {
	_, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	d, err := a.e.Delivery.DetectCI(ctx, p.ID, in.Repo)
	if err != nil {
		return nil, fail(err, "没有这个项目")
	}
	return &ciDetectOutput{Body: d}, nil
}

func (a *API) ciSetupPR(ctx context.Context, in *ciRepoInput) (*setupPROutput, error) {
	_, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	url, err := a.e.Delivery.SetupCIPR(ctx, p.ID, in.Repo)
	if err != nil {
		return nil, fail(err, "没有这个项目")
	}
	out := &setupPROutput{}
	out.Body.PRURL = url
	return out, nil
}
