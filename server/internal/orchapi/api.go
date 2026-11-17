// Package orchapi is the REST + SSE surface of the orchestration engines
// (api.md §1, §3.2–§3.5, §3.11, §3.13): coordinator conversations, threads,
// tasks, questions and approval cards, deliverables, CI setup, events, office
// live state and the read-only v1 conversations. It registers on the shared
// huma API through httpapi's extension point, so authentication, CSRF and the
// {detail, code} error shape are the foundation's.
package orchapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gin-gonic/gin"

	"github.com/touwaeriol/agent-teams/server/internal/approvals"
	"github.com/touwaeriol/agent-teams/server/internal/artifacts"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/delivery"
	"github.com/touwaeriol/agent-teams/server/internal/engine"
	"github.com/touwaeriol/agent-teams/server/internal/ghops"
	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/office"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
	"github.com/touwaeriol/agent-teams/server/internal/scheduler"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/toolsvc"
)

// API holds the handlers. Engine may be nil while generating the OpenAPI document.
type API struct {
	e      *engine.Engine
	office *office.Registry
	// published files (files.go)
	files          *artifacts.Server
	artifactOrigin string
	publicURL      string
}

// New returns the API for an engine.
func New(e *engine.Engine, reg *office.Registry) *API {
	a := &API{e: e, office: reg}
	if e != nil {
		e.Orch.RefContext = a.refContext
	}
	return a
}

// Register mounts every route (httpapi Deps.Extensions).
func (a *API) Register(api huma.API, r gin.IRoutes) {
	a.registerNodeLinks(r)
	a.registerConversations(api)
	a.registerThreads(api)
	a.registerTasks(api)
	a.registerProjectViews(api)
	a.registerDeliverables(api)
	a.registerFiles(api, r)
	a.registerStreams(api)
	a.registerOffice(api)
	a.registerSchedules(api)
	a.registerLibrary(api)
	a.registerAttachments(api, r)
	a.registerComposer(api)
	a.registerModel(api)
}

// registerNodeLinks mounts the daemon WebSocket (and its v2 alias). Without an engine
// (OpenAPI generation) the routes still exist, so the route table is the same in every mode.
func (a *API) registerNodeLinks(r gin.IRoutes) {
	h := offline
	if a.e != nil {
		h = gin.WrapF(a.e.Gateway.ServeWS)
	}
	r.GET("/nodes/connect", h)
	r.GET("/nodes/v2/connect", h)
}

// offline answers raw routes registered without an engine.
func offline(c *gin.Context) {
	c.String(http.StatusServiceUnavailable, "engine not running")
}

func op(id, method, path, tag string, extra ...func(*huma.Operation)) huma.Operation {
	o := huma.Operation{OperationID: id, Method: method, Path: path, Tags: []string{tag}}
	for _, f := range extra {
		f(&o)
	}
	return o
}

func status(code int) func(*huma.Operation) {
	return func(o *huma.Operation) { o.DefaultStatus = code }
}

func summary(s string) func(*huma.Operation) { return func(o *huma.Operation) { o.Summary = s } }

// errorMap translates engine errors into API errors.
var errorMap = []struct {
	err    error
	status int
	code   string
}{
	{core.ErrNotFound, 404, "not_found"},
	{store.ErrNotFound, 404, "not_found"},
	{orchestrator.ErrNotActive, 409, "thread_not_active"},
	{orchestrator.ErrArchived, 409, "thread_archived"},
	{orchestrator.ErrNotArchived, 409, "not_archived"},
	{orchestrator.ErrTooManyImages, 422, "validation"},
	{core.ErrNotImage, 422, "validation"},
	{core.ErrTooLarge, 422, "validation"},
	{orchestrator.ErrNoNode, 409, "no_node"},
	{orchestrator.ErrNoEmployee, 404, "employee_not_found"},
	{orchestrator.ErrRoleNotFound, 422, "unknown_role"},
	{orchestrator.ErrBadState, 409, "bad_state"},
	{orchestrator.ErrPRAwaiting, 409, "pr_awaiting"},
	{orchestrator.ErrMerging, 409, "merge_in_progress"},
	{orchestrator.ErrForwardEmpty, 422, "validation"},
	{orchestrator.ErrForwardNotUser, 422, "validation"},
	{orchestrator.ErrForwardTooMany, 422, "validation"},
	{orchestrator.ErrForwardFiles, 422, "validation"},
	{delivery.ErrPRExists, 409, "pr_exists"},
	{delivery.ErrNothingToPR, 409, "nothing_to_pr"},
	{delivery.ErrBaseNeedsRepo, 422, "validation"},
	{orchestrator.ErrUnknownRepo, 422, "unknown_repo"},
	{core.ErrBaseFixed, 409, "base_fixed"},
	{delivery.ErrMergeInProgress, 409, "merge_in_progress"},
	{delivery.ErrPlanOpen, 409, "plan_open"},
	{delivery.ErrNoPR, 409, "no_pr"},
	{delivery.ErrNotInReview, 409, "not_in_review"},
	{delivery.ErrNoWorkflow, 409, "no_workflow"},
	{delivery.ErrBadURL, 422, "validation"},
	{delivery.ErrPRKind, 400, "pr_not_deliverable"},
	{ghops.ErrNoRepo, 409, "no_repo"},
	{approvals.ErrAnswered, 409, "already_answered"},
	{approvals.ErrBadAnswer, 422, "validation"},
	{toolsvc.ErrAnswered, 409, "already_answered"},
	{toolsvc.ErrNotForUser, 409, "not_for_user"},
	{toolsvc.ErrBadAnswer, 422, "validation"},
	{scheduler.ErrTaskState, 409, "task_state"},
}

// fail maps an engine error to the API error format.
func fail(err error, notFound string) error {
	if err == nil {
		return nil
	}
	var apiErr *httpapi.APIError
	if errors.As(err, &apiErr) {
		return err
	}
	for _, m := range errorMap {
		if errors.Is(err, m.err) {
			detail := err.Error()
			if m.status == 404 {
				detail = notFound
			}
			return httpapi.Fail(m.status, m.code, detail)
		}
	}
	if ghops.Status(err) > 0 {
		return httpapi.Fail(http.StatusBadGateway, "github", err.Error())
	}
	return err
}

func (a *API) ready() error {
	if a.e == nil {
		return httpapi.Fail(http.StatusServiceUnavailable, "unavailable", "编排引擎没有启动")
	}
	return nil
}

// user returns the caller (401 otherwise).
func (a *API) user(ctx context.Context) (store.User, error) {
	if err := a.ready(); err != nil {
		return store.User{}, err
	}
	return httpapi.RequireUser(ctx)
}

// project loads one of the caller's projects.
func (a *API) project(ctx context.Context, pid string) (store.User, store.Project, error) {
	u, err := a.user(ctx)
	if err != nil {
		return u, store.Project{}, err
	}
	p, err := a.e.Store.ProjectOf(ctx, u.ID, pid)
	return u, p, fail(err, "没有这个项目")
}

// thread loads one of the caller's threads.
func (a *API) thread(ctx context.Context, tid string) (store.User, core.Thread, error) {
	u, err := a.user(ctx)
	if err != nil {
		return u, core.Thread{}, err
	}
	t, err := core.ThreadOfUser(ctx, a.e.Core.Pool, u.ID, tid)
	return u, t, fail(err, "没有这个讨论串")
}

// conversation loads one of the caller's conversations.
func (a *API) conversation(ctx context.Context, cid string) (store.User, core.Conversation, error) {
	u, err := a.user(ctx)
	if err != nil {
		return u, core.Conversation{}, err
	}
	c, err := core.ConversationOfUser(ctx, a.e.Core.Pool, u.ID, cid)
	return u, c, fail(err, "没有这个对话")
}

func (a *API) threadView(ctx context.Context, t core.Thread) (core.ThreadView, error) {
	return a.e.Core.ThreadViewOf(ctx, a.e.Core.Pool, t)
}

func (a *API) threadViews(ctx context.Context, threads []core.Thread) ([]core.ThreadView, error) {
	out := make([]core.ThreadView, 0, len(threads))
	for _, t := range threads {
		v, err := a.threadView(ctx, t)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func pageLimit(n, def int) int {
	if n <= 0 || n > 200 {
		return def
	}
	return n
}

func parseCursor(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func cursorOf(id int64, full bool) *string {
	if !full || id == 0 {
		return nil
	}
	s := strconv.FormatInt(id, 10)
	return &s
}
