package orchapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/delivery"
	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
)

type tidInput struct {
	TID string `path:"tid" maxLength:"64"`
}

type newThreadInput struct {
	PID            string `path:"pid" maxLength:"64"`
	IdempotencyKey string `header:"Idempotency-Key" maxLength:"128"`
	Body           struct {
		Title string                   `json:"title" minLength:"1" maxLength:"200"`
		Brief string                   `json:"brief,omitempty" maxLength:"50000"`
		Roles []orchestrator.RoleCount `json:"roles,omitempty" doc:"Override member counts for this thread"`
	}
}

type threadOutput struct{ Body core.ThreadView }

type threadsInput struct {
	PID    string `path:"pid" maxLength:"64"`
	Status string `query:"status" doc:"Comma separated statuses"`
	Group  string `query:"group" enum:"waiting,active,done,"`
}

type threadsOutput struct{ Body []core.ThreadView }

// ThreadDetail is ThreadView plus what ?include= asked for (Mobile, api.md §3.12).
type ThreadDetail struct {
	core.ThreadView
	Messages  []core.MessageView  `json:"messages,omitempty"`
	Questions []core.QuestionView `json:"questions,omitempty"`
}

type getThreadInput struct {
	TID     string `path:"tid" maxLength:"64"`
	Include string `query:"include" doc:"messages,questions"`
}

type threadDetailOutput struct{ Body ThreadDetail }

type patchThreadInput struct {
	TID  string `path:"tid" maxLength:"64"`
	Body struct {
		Title       *string `json:"title,omitempty" minLength:"1" maxLength:"200"`
		Status      *string `json:"status,omitempty" enum:"paused,running,archived"`
		AutoApprove *bool   `json:"auto_approve,omitempty" doc:"Allow everything in this thread (needs sandbox isolation)"`
	}
}

type mainTasksOutput struct{ Body []core.MainTaskView }

type patchMainInput struct {
	TID  string `path:"tid" maxLength:"64"`
	MID  string `path:"mid" maxLength:"64"`
	Body struct {
		Status string `json:"status" enum:"todo,doing,done"`
	}
}

type membersOutput struct{ Body []core.EmployeeBrief }

type memberInput struct {
	TID  string `path:"tid" maxLength:"64"`
	Name string `path:"name" maxLength:"64"`
}

type memberMessageInput struct {
	TID  string `path:"tid" maxLength:"64"`
	Name string `path:"name" maxLength:"64"`
	Body struct {
		Content string `json:"content" minLength:"1" maxLength:"100000"`
	}
}

type pageInput struct {
	TID    string `path:"tid" maxLength:"64"`
	Before string `query:"before"`
	Limit  int    `query:"limit" minimum:"0" maximum:"200"`
}

// MailPage is a page of thread mail (newest first).
type MailPage struct {
	Items      []core.MailView `json:"items"`
	NextCursor *string         `json:"next_cursor"`
}

type mailOutput struct{ Body MailPage }

// ActivityPage is a page of thread activity (newest first).
type ActivityPage struct {
	Items      []core.Activity `json:"items"`
	NextCursor *string         `json:"next_cursor"`
}

type activityOutput struct{ Body ActivityPage }

type mergeOutput struct{ Body []core.MergeEntryView }

type checksOutput struct{ Body []core.CheckView }

type openPRInput struct {
	TID  string `path:"tid" maxLength:"64"`
	Body struct {
		Title string `json:"title,omitempty" maxLength:"256"`
		Body  string `json:"body,omitempty" maxLength:"60000"`
		Force bool   `json:"force,omitempty" doc:"Open even if some plan steps are not done"`
	}
}

type rejectInput struct {
	TID  string `path:"tid" maxLength:"64"`
	Body struct {
		Reason string `json:"reason" minLength:"1" maxLength:"20000"`
	}
}

// Usage is GET /api/threads/{tid}/usage.
type Usage struct {
	CostUSDEst *float64        `json:"cost_usd_est"`
	Tokens     UsageTokens     `json:"tokens"`
	ByEmployee []EmployeeUsage `json:"by_employee"`
}

// UsageTokens sums token usage.
type UsageTokens struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
}

// EmployeeUsage is one employee's share.
type EmployeeUsage struct {
	Employee   string      `json:"employee"`
	CostUSDEst *float64    `json:"cost_usd_est"`
	Tokens     UsageTokens `json:"tokens"`
}

type usageOutput struct{ Body Usage }

func (a *API) registerThreads(api huma.API) {
	t := "threads"
	huma.Register(api, op("create-thread", "POST", "/api/projects/{pid}/threads", t, status(201),
		summary("Open a thread directly (skipping the coordinator)")), a.createThread)
	huma.Register(api, op("list-threads", "GET", "/api/projects/{pid}/threads", t), a.listThreads)
	huma.Register(api, op("get-thread", "GET", "/api/threads/{tid}", t), a.getThread)
	huma.Register(api, op("update-thread", "PATCH", "/api/threads/{tid}", t), a.updateThread)
	huma.Register(api, op("archive-thread", "POST", "/api/threads/{tid}/archive", t,
		summary("Archive a thread: it ends (sessions closed, checkouts cleaned) and leaves the main list")), a.archiveThread)
	huma.Register(api, op("unarchive-thread", "POST", "/api/threads/{tid}/unarchive", t,
		summary("Put an archived thread back: finished threads stay finished, others come back paused")), a.unarchiveThread)
	huma.Register(api, op("delete-thread", "DELETE", "/api/threads/{tid}", t, status(204),
		summary("Delete a thread: sessions closed, checkouts purged on the nodes, its records removed; deliverables stay in the library")), a.deleteThread)
	huma.Register(api, op("list-main-tasks", "GET", "/api/threads/{tid}/main-tasks", t), a.listMain)
	huma.Register(api, op("update-main-task", "PATCH", "/api/threads/{tid}/main-tasks/{mid}", t), a.updateMain)
	huma.Register(api, op("list-members", "GET", "/api/threads/{tid}/members", t), a.listMembers)
	huma.Register(api, op("interrupt-member", "POST", "/api/threads/{tid}/members/{name}/interrupt", t, status(202)), a.interruptMember)
	huma.Register(api, op("message-member", "POST", "/api/threads/{tid}/members/{name}/message", t, status(202)), a.messageMember)
	huma.Register(api, op("list-mail", "GET", "/api/threads/{tid}/mail", t), a.listMail)
	huma.Register(api, op("list-activity", "GET", "/api/threads/{tid}/activity", t), a.listActivity)
	huma.Register(api, op("list-merge-queue", "GET", "/api/threads/{tid}/merge-queue", t), a.listMerge)
	huma.Register(api, op("list-checks", "GET", "/api/threads/{tid}/checks", t), a.listChecks)
	huma.Register(api, op("open-pr", "POST", "/api/threads/{tid}/open-pr", t), a.openPR)
	huma.Register(api, op("accept-thread", "POST", "/api/threads/{tid}/accept", t), a.accept)
	huma.Register(api, op("reject-thread", "POST", "/api/threads/{tid}/reject", t), a.reject)
	huma.Register(api, op("thread-usage", "GET", "/api/threads/{tid}/usage", t), a.usage)
}

func (a *API) createThread(ctx context.Context, in *newThreadInput) (*threadOutput, error) {
	u, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	v, err := idempotent(ctx, a, u.ID, "thread:"+p.ID, in.IdempotencyKey, func() (core.ThreadView, error) {
		t, err := a.e.Orch.OpenThread(ctx, orchestrator.ThreadRequest{UserID: u.ID, ProjectID: p.ID, Title: strings.TrimSpace(in.Body.Title),
			Brief: in.Body.Brief, Roles: in.Body.Roles, OpenedBy: "user"})
		if err != nil {
			return core.ThreadView{}, err
		}
		return a.threadView(ctx, t)
	})
	if err != nil {
		return nil, fail(err, "没有这个项目")
	}
	return &threadOutput{Body: v}, nil
}

var groups = map[string][]string{
	"waiting": {"waiting", "review"},
	"active":  {"queued", "running", "waiting", "review", "paused"},
	"done":    {"done", "failed", "archived"},
}

func (a *API) listThreads(ctx context.Context, in *threadsInput) (*threadsOutput, error) {
	_, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	statuses := groups[in.Group]
	if in.Status != "" {
		statuses = strings.Split(in.Status, ",")
	}
	threads, err := core.Threads(ctx, a.e.Core.Pool, p.ID, statuses)
	if err != nil {
		return nil, err
	}
	views, err := a.threadViews(ctx, threads)
	return &threadsOutput{Body: views}, err
}

func (a *API) getThread(ctx context.Context, in *getThreadInput) (*threadDetailOutput, error) {
	_, t, err := a.thread(ctx, in.TID)
	if err != nil {
		return nil, err
	}
	v, err := a.threadView(ctx, t)
	if err != nil {
		return nil, err
	}
	d := ThreadDetail{ThreadView: v}
	if strings.Contains(in.Include, "messages") {
		if d.Messages, err = a.recentMessages(ctx, t.ID); err != nil {
			return nil, err
		}
	}
	if strings.Contains(in.Include, "questions") {
		qs, err := core.ConversationQuestions(ctx, a.e.Core.Pool, t.ID, "open")
		if err != nil {
			return nil, err
		}
		if d.Questions, err = a.questionViews(ctx, qs); err != nil {
			return nil, err
		}
	}
	return &threadDetailOutput{Body: d}, nil
}

func (a *API) recentMessages(ctx context.Context, convID string) ([]core.MessageView, error) {
	msgs, err := core.Messages(ctx, a.e.Core.Pool, convID, 0, 30)
	out := make([]core.MessageView, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, core.MessageViewOf(m))
	}
	return out, err
}

func (a *API) updateThread(ctx context.Context, in *patchThreadInput) (*threadOutput, error) {
	u, t, err := a.thread(ctx, in.TID)
	if err != nil {
		return nil, err
	}
	if in.Body.AutoApprove != nil && *in.Body.AutoApprove {
		if err := a.checkIsolation(ctx, t); err != nil {
			return nil, err
		}
	}
	err = a.e.Core.InTx(ctx, func(tx *core.Tx) error { return a.patchThread(ctx, tx, u.ID, t, in) })
	if err != nil {
		return nil, fail(err, "没有这个讨论串")
	}
	t, err = core.ThreadByID(ctx, a.e.Core.Pool, t.ID)
	if err != nil {
		return nil, err
	}
	v, err := a.threadView(ctx, t)
	return &threadOutput{Body: v}, err
}

func (a *API) patchThread(ctx context.Context, tx *core.Tx, userID string, t core.Thread, in *patchThreadInput) error {
	if in.Body.Title != nil {
		if err := core.UpdateThread(ctx, tx, t.ID, map[string]any{"title": strings.TrimSpace(*in.Body.Title)}); err != nil {
			return err
		}
		if err := tx.EmitThread(ctx, t.ID); err != nil {
			return err
		}
	}
	if in.Body.AutoApprove != nil && *in.Body.AutoApprove != t.AutoApprove {
		if err := a.e.Approvals.SetThreadAutoApprove(ctx, tx, t, *in.Body.AutoApprove); err != nil {
			return err
		}
		auditTx(ctx, tx, userID, "thread_auto_approve", t.ID, map[string]any{"on": *in.Body.AutoApprove})
	}
	if in.Body.Status == nil {
		return nil
	}
	cur, err := core.LockThread(ctx, tx, t.ID)
	if err != nil {
		return err
	}
	return a.changeStatus(ctx, tx, cur, *in.Body.Status)
}

func (a *API) changeStatus(ctx context.Context, tx *core.Tx, t core.Thread, next string) error {
	switch {
	case next == t.Status:
		return nil
	case next == "paused" && (t.Status == "running" || t.Status == "waiting" || t.Status == "queued"):
		return a.e.Orch.Pause(ctx, tx, t, "user")
	case next == "running" && t.Status == "paused":
		return a.e.Orch.Resume(ctx, tx, t)
	case next == "archived":
		return a.e.Orch.Archive(ctx, tx, t)
	}
	return httpapi.Fail(http.StatusConflict, "bad_state", "讨论串现在是 "+t.Status+"，不能改成 "+next)
}

func (a *API) archiveThread(ctx context.Context, in *tidInput) (*threadOutput, error) {
	return a.lifecycle(ctx, in.TID, "thread_archive", a.e.Orch.Archive)
}

func (a *API) unarchiveThread(ctx context.Context, in *tidInput) (*threadOutput, error) {
	return a.lifecycle(ctx, in.TID, "thread_unarchive", a.e.Orch.Unarchive)
}

// lifecycle runs one status change under the thread lock, audits it and returns the thread.
func (a *API) lifecycle(ctx context.Context, tid, action string, change func(context.Context, *core.Tx, core.Thread) error) (*threadOutput, error) {
	u, t, err := a.thread(ctx, tid)
	if err != nil {
		return nil, err
	}
	err = a.e.Core.InTx(ctx, func(tx *core.Tx) error {
		cur, err := core.LockThread(ctx, tx, t.ID)
		if err != nil {
			return err
		}
		auditTx(ctx, tx, u.ID, action, t.ID, map[string]any{"from": cur.Status})
		return change(ctx, tx, cur)
	})
	if err != nil {
		return nil, fail(err, "没有这个讨论串")
	}
	if t, err = core.ThreadByID(ctx, a.e.Core.Pool, t.ID); err != nil {
		return nil, err
	}
	v, err := a.threadView(ctx, t)
	return &threadOutput{Body: v}, err
}

func (a *API) deleteThread(ctx context.Context, in *tidInput) (*struct{}, error) {
	u, t, err := a.thread(ctx, in.TID)
	if err != nil {
		return nil, err
	}
	err = a.e.Core.InTx(ctx, func(tx *core.Tx) error {
		cur, err := core.LockThread(ctx, tx, t.ID)
		if err != nil {
			return err
		}
		auditTx(ctx, tx, u.ID, "thread_delete", t.ID, map[string]any{"no": cur.No, "title": cur.Title, "status": cur.Status})
		return a.e.Orch.DeleteThread(ctx, tx, cur, func() error { return a.e.Delivery.PurgeThread(ctx, tx, cur) })
	})
	return nil, fail(err, "没有这个讨论串")
}

func auditTx(ctx context.Context, tx *core.Tx, userID, action, target string, detail map[string]any) {
	_, _ = tx.Exec(ctx, `INSERT INTO at_audit_log (user_id, actor, action, target_type, target_id, detail)
		VALUES ($1, $2, $3, 'thread', $4, $5)`, userID, "user:"+userID, action, target, detail)
}

// checkIsolation enforces §10.3: "allow everything" needs every employee's node at sandbox level or better.
func (a *API) checkIsolation(ctx context.Context, t core.Thread) error {
	var weak []string
	rows, err := a.e.Core.Pool.Query(ctx, `SELECT DISTINCT n.name FROM at_employees e JOIN at_nodes n ON n.id = e.node_id
		WHERE e.thread_id = $1 AND NOT n.isolated AND n.isolation NOT IN ('sandbox','agent_user')`, t.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if rows.Scan(&name) == nil {
			weak = append(weak, name)
		}
	}
	if len(weak) > 0 {
		return httpapi.Fail(http.StatusConflict, "isolation_required",
			"节点 "+strings.Join(weak, "、")+" 的隔离级别不够（需要 sandbox 或 agent_user），不能打开“本讨论串内全部放行”")
	}
	return rows.Err()
}

func (a *API) listMain(ctx context.Context, in *tidInput) (*mainTasksOutput, error) {
	_, t, err := a.thread(ctx, in.TID)
	if err != nil {
		return nil, err
	}
	views, err := core.MainTaskViews(ctx, a.e.Core.Pool, t.ID)
	return &mainTasksOutput{Body: views}, err
}

func (a *API) updateMain(ctx context.Context, in *patchMainInput) (*mainTasksOutput, error) {
	_, t, err := a.thread(ctx, in.TID)
	if err != nil {
		return nil, err
	}
	err = a.e.Core.InTx(ctx, func(tx *core.Tx) error {
		if err := core.SetMainTaskStatus(ctx, tx, t.ID, in.MID, in.Body.Status, false); err != nil {
			return err
		}
		if err := tx.EmitMainTasks(ctx, t); err != nil {
			return err
		}
		return tx.EmitThread(ctx, t.ID)
	})
	if err != nil {
		return nil, fail(err, "没有这个主任务")
	}
	views, err := core.MainTaskViews(ctx, a.e.Core.Pool, t.ID)
	return &mainTasksOutput{Body: views}, err
}

func (a *API) listMembers(ctx context.Context, in *tidInput) (*membersOutput, error) {
	_, t, err := a.thread(ctx, in.TID)
	if err != nil {
		return nil, err
	}
	briefs, err := a.e.Core.EmployeeBriefs(ctx, a.e.Core.Pool, t.ProjectID, &t.ID)
	return &membersOutput{Body: briefs}, err
}

func (a *API) interruptMember(ctx context.Context, in *memberInput) (*struct{}, error) {
	_, t, err := a.thread(ctx, in.TID)
	if err != nil {
		return nil, err
	}
	return nil, fail(a.e.Orch.InterruptMember(ctx, t, in.Name), "没有这个员工")
}

func (a *API) messageMember(ctx context.Context, in *memberMessageInput) (*messageOutput, error) {
	_, t, err := a.thread(ctx, in.TID)
	if err != nil {
		return nil, err
	}
	m, err := a.e.Orch.MessageMember(ctx, t, in.Name, strings.TrimSpace(in.Body.Content))
	if err != nil {
		return nil, fail(err, "没有这个员工")
	}
	return &messageOutput{Body: core.MessageViewOf(m)}, nil
}

func (a *API) listMail(ctx context.Context, in *pageInput) (*mailOutput, error) {
	_, t, err := a.thread(ctx, in.TID)
	if err != nil {
		return nil, err
	}
	limit := pageLimit(in.Limit, 50)
	mail, err := core.MailPage(ctx, a.e.Core.Pool, t.ID, parseCursor(in.Before), limit)
	if err != nil {
		return nil, err
	}
	page := MailPage{Items: make([]core.MailView, 0, len(mail))}
	for _, m := range mail {
		page.Items = append(page.Items, core.MailViewOf(m))
	}
	if len(mail) > 0 {
		page.NextCursor = cursorOf(mail[len(mail)-1].ID, len(mail) == limit)
	}
	return &mailOutput{Body: page}, nil
}

func (a *API) listActivity(ctx context.Context, in *pageInput) (*activityOutput, error) {
	_, t, err := a.thread(ctx, in.TID)
	if err != nil {
		return nil, err
	}
	limit := pageLimit(in.Limit, 50)
	acts, err := core.ThreadActivity(ctx, a.e.Core.Pool, t.ID, parseCursor(in.Before), limit)
	if err != nil {
		return nil, err
	}
	page := ActivityPage{Items: acts}
	if len(acts) > 0 {
		page.NextCursor = cursorOf(acts[len(acts)-1].ID, len(acts) == limit)
	}
	return &activityOutput{Body: page}, nil
}

func (a *API) listMerge(ctx context.Context, in *tidInput) (*mergeOutput, error) {
	_, t, err := a.thread(ctx, in.TID)
	if err != nil {
		return nil, err
	}
	entries, err := core.MergeQueue(ctx, a.e.Core.Pool, t.ID, false)
	out := make([]core.MergeEntryView, 0, len(entries))
	for _, m := range entries {
		out = append(out, core.MergeViewOf(m, t.No))
	}
	return &mergeOutput{Body: out}, err
}

func (a *API) listChecks(ctx context.Context, in *tidInput) (*checksOutput, error) {
	_, t, err := a.thread(ctx, in.TID)
	if err != nil {
		return nil, err
	}
	_, checks, err := core.LatestChecks(ctx, a.e.Core.Pool, t.ID)
	return &checksOutput{Body: checks}, err
}

func (a *API) openPR(ctx context.Context, in *openPRInput) (*threadOutput, error) {
	_, t, err := a.thread(ctx, in.TID)
	if err != nil {
		return nil, err
	}
	t, err = a.e.Delivery.OpenPR(ctx, t.ID, delivery.PRRequest{Title: in.Body.Title, Body: in.Body.Body, Force: in.Body.Force, By: "user"})
	if err != nil {
		return nil, fail(err, "没有这个讨论串")
	}
	v, err := a.threadView(ctx, t)
	return &threadOutput{Body: v}, err
}

func (a *API) accept(ctx context.Context, in *tidInput) (*threadOutput, error) {
	u, t, err := a.thread(ctx, in.TID)
	if err != nil {
		return nil, err
	}
	if _, err := a.e.Delivery.Accept(ctx, u.ID, t.ID); err != nil {
		return nil, fail(err, "没有这个讨论串")
	}
	t, err = core.ThreadByID(ctx, a.e.Core.Pool, t.ID)
	if err != nil {
		return nil, err
	}
	v, err := a.threadView(ctx, t)
	return &threadOutput{Body: v}, err
}

func (a *API) reject(ctx context.Context, in *rejectInput) (*threadOutput, error) {
	u, t, err := a.thread(ctx, in.TID)
	if err != nil {
		return nil, err
	}
	t, err = a.e.Delivery.Reject(ctx, u.ID, t.ID, in.Body.Reason)
	if err != nil {
		return nil, fail(err, "没有这个讨论串")
	}
	v, err := a.threadView(ctx, t)
	return &threadOutput{Body: v}, err
}

func (a *API) usage(ctx context.Context, in *tidInput) (*usageOutput, error) {
	_, t, err := a.thread(ctx, in.TID)
	if err != nil {
		return nil, err
	}
	rows, err := a.e.Core.Pool.Query(ctx, `SELECT e.name, sum(u.cost_usd)::float8, COALESCE(sum(u.input_tokens),0)::bigint,
		COALESCE(sum(u.output_tokens),0)::bigint, COALESCE(sum(u.cache_read_tokens),0)::bigint, COALESCE(sum(u.cache_write_tokens),0)::bigint
		FROM at_usage u JOIN at_agent_sessions s ON s.id = u.session_id JOIN at_employees e ON e.id = s.employee_id
		WHERE u.thread_id = $1 GROUP BY e.name ORDER BY e.name`, t.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := Usage{ByEmployee: []EmployeeUsage{}}
	for rows.Next() {
		var eu EmployeeUsage
		if err := rows.Scan(&eu.Employee, &eu.CostUSDEst, &eu.Tokens.Input, &eu.Tokens.Output, &eu.Tokens.CacheRead, &eu.Tokens.CacheWrite); err != nil {
			return nil, err
		}
		out.add(eu)
	}
	return &usageOutput{Body: out}, rows.Err()
}

func (u *Usage) add(eu EmployeeUsage) {
	u.ByEmployee = append(u.ByEmployee, eu)
	u.Tokens.Input += eu.Tokens.Input
	u.Tokens.Output += eu.Tokens.Output
	u.Tokens.CacheRead += eu.Tokens.CacheRead
	u.Tokens.CacheWrite += eu.Tokens.CacheWrite
	if eu.CostUSDEst != nil {
		total := *eu.CostUSDEst
		if u.CostUSDEst != nil {
			total += *u.CostUSDEst
		}
		u.CostUSDEst = &total
	}
}
