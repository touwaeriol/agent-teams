package orchapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/events"
	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
)

type projectEventsInput struct {
	PID         string `path:"pid" maxLength:"64"`
	Topics      string `query:"topics" doc:"Comma separated event names (default: all)"`
	Thread      int    `query:"thread" minimum:"0" doc:"Only this thread number"`
	LastEventID string `header:"Last-Event-ID" doc:"Resume after this seq"`
	Last        string `query:"last_event_id" doc:"Same as Last-Event-ID for clients that cannot set headers"`
}

type userEventsInput struct {
	LastEventID string `header:"Last-Event-ID"`
	Last        string `query:"last_event_id"`
}

type sessionStreamInput struct {
	SID string `path:"sid" maxLength:"64"`
}

func sseOp(id, path, desc string) huma.Operation {
	o := op(id, "GET", path, "events", summary(desc))
	o.Responses = map[string]*huma.Response{"200": {Description: "text/event-stream",
		Content: map[string]*huma.MediaType{"text/event-stream": {Schema: &huma.Schema{Type: "string"}}}}}
	return o
}

func (a *API) registerStreams(api huma.API) {
	huma.Register(api, sseOp("project-events", "/api/projects/{pid}/events",
		"Project event stream (SSE); id = project seq, Last-Event-ID replays from PostgreSQL"), a.projectEvents)
	huma.Register(api, sseOp("user-events", "/api/events", "User event stream (project, node, notice)"), a.userEvents)
	huma.Register(api, sseOp("session-stream", "/api/sessions/{sid}/stream", "Live output of one session (not stored)"), a.sessionStream)
}

func lastID(header, query string) int64 {
	v := strings.TrimSpace(header)
	if v == "" {
		v = strings.TrimSpace(query)
	}
	if v == "" {
		return -1
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return -1
	}
	return n
}

func (a *API) projectEvents(ctx context.Context, in *projectEventsInput) (*huma.StreamResponse, error) {
	_, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	var thread *int
	if in.Thread > 0 {
		thread = &in.Thread
	}
	var topics []string
	if in.Topics != "" {
		topics = strings.Split(in.Topics, ",")
	}
	src := a.e.Events.ProjectSource(p.ID, topics, thread)
	return a.stream(func(ctx context.Context, w events.Writer) error {
		return a.e.Events.Serve(ctx, w, src, lastID(in.LastEventID, in.Last))
	}), nil
}

func (a *API) userEvents(ctx context.Context, in *userEventsInput) (*huma.StreamResponse, error) {
	u, err := a.user(ctx)
	if err != nil {
		return nil, err
	}
	src := a.e.Events.UserSource(u.ID)
	return a.stream(func(ctx context.Context, w events.Writer) error {
		return a.e.Events.Serve(ctx, w, src, lastID(in.LastEventID, in.Last))
	}), nil
}

func (a *API) sessionStream(ctx context.Context, in *sessionStreamInput) (*huma.StreamResponse, error) {
	u, err := a.user(ctx)
	if err != nil {
		return nil, err
	}
	sc, err := core.SessionContextOf(ctx, a.e.Core.Pool, in.SID)
	if err != nil || sc.OwnerID != u.ID {
		return nil, httpapi.Fail(http.StatusNotFound, "not_found", "没有这个会话")
	}
	watch := func(on bool) { a.watch(sc.Session, on) }
	return a.stream(func(ctx context.Context, w events.Writer) error {
		return a.e.Events.ServeDeltas(ctx, w, in.SID, watch)
	}), nil
}

// watch turns stream.delta on or off for a session (session.watch).
func (a *API) watch(s core.AgentSession, on bool) {
	if s.NodeID == nil {
		return
	}
	ctx := context.Background()
	err := a.e.Core.InTx(ctx, func(tx *core.Tx) error {
		_, err := a.e.Gateway.Enqueue(ctx, tx, *s.NodeID, nodegw.Command{Type: protocol.TypeSessionWatch,
			Body: protocol.SessionWatch{SessionID: protocol.SessionID(s.ID), On: on}, SessionID: s.ID, OpenGen: s.OpenGen})
		return err
	})
	if err != nil {
		return
	}
}

// stream adapts an SSE writer function to huma.
func (a *API) stream(serve func(ctx context.Context, w events.Writer) error) *huma.StreamResponse {
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		hctx.SetHeader("Content-Type", "text/event-stream")
		hctx.SetHeader("Cache-Control", "no-cache")
		hctx.SetHeader("X-Accel-Buffering", "no")
		hctx.SetStatus(http.StatusOK)
		bw := hctx.BodyWriter()
		w := events.Writer{W: bw, Flush: flusher(bw)}
		w.Flush()
		_ = serve(hctx.Context(), w)
	}}
}

func flusher(w any) func() {
	if f, ok := w.(http.Flusher); ok {
		return f.Flush
	}
	if rw, ok := w.(http.ResponseWriter); ok {
		rc := http.NewResponseController(rw)
		return func() { _ = rc.Flush() }
	}
	return func() {}
}
