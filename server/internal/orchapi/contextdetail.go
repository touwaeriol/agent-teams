package orchapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
	"github.com/touwaeriol/agent-teams/server/internal/platform"
)

// ContextCategoryView is one row of the context window (SDK getContextUsage categories).
type ContextCategoryView struct {
	ID     string `json:"id" doc:"Stable row id: system_prompt, system_tools, mcp_tools, mcp_instructions, mcp_tools_deferred, system_tools_deferred, custom_agents, memory_files, skills, messages, autocompact_buffer, compact_buffer, free_space; more may come, '' from an old CLI. Classify on id and kind, not on name"`
	Name   string `json:"name" doc:"The runtime's English label"`
	Tokens int    `json:"tokens"`
	Kind   string `json:"kind" enum:"used,free,buffer,deferred" doc:"used: in the window; free: the rest of it; buffer: the autocompact reserve; deferred: tool schemas outside the window"`
}

// ContextMcpToolView is one MCP tool and its tokens.
type ContextMcpToolView struct {
	Name   string `json:"name"`
	Server string `json:"server"`
	Tokens int    `json:"tokens"`
	Loaded bool   `json:"loaded" doc:"false: deferred (tool search), not in the window yet"`
}

// ContextMcpServerView is one MCP server's share of the MCP server instructions row (estimated).
type ContextMcpServerView struct {
	Server string `json:"server"`
	Tokens int    `json:"tokens"`
}

// ContextMemoryFileView is one memory file (CLAUDE.md …) in the context.
type ContextMemoryFileView struct {
	Path   string `json:"path"`
	Type   string `json:"type"`
	Tokens int    `json:"tokens"`
}

// ContextSkillView is one skill's frontmatter in the context.
type ContextSkillView struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Tokens int    `json:"tokens"`
}

// ContextDetailView is GET /api/conversations/{cid}/context/detail: what fills the session's
// context window, by category (Claude only; protocol.md §4.10 上下文细分).
type ContextDetailView struct {
	Supported  bool       `json:"supported" doc:"The session's runtime reports a breakdown (Claude only). false: hide the context block"`
	Runtime    string     `json:"runtime" doc:"Runtime of the latest session ('' before the first one)"`
	SessionID  string     `json:"session_id" doc:"The latest session ('' before the first one)"`
	State      string     `json:"state" doc:"That session's state (active, turn_running, parked, closed, …)"`
	CanRefresh bool       `json:"can_refresh" doc:"POST …/context/refresh can read it now: the session runs on an online node whose daemon reports breakdowns"`
	Detail     string     `json:"detail" enum:"summary,full," doc:"summary: estimated after a turn; full: counted on a refresh; '' = no breakdown yet"`
	UpdatedAt  *time.Time `json:"updated_at" doc:"When the breakdown arrived; null = none yet"`
	Model      string     `json:"model"`
	// The totals: the bar is total_tokens / max_tokens (the autocompact window, not the model's).
	TotalTokens          int                     `json:"total_tokens"`
	MaxTokens            int                     `json:"max_tokens" doc:"The window measured against: the autocompact window; 0 = unknown"`
	RawMaxTokens         int                     `json:"raw_max_tokens" doc:"The model's own window; 0 = unknown"`
	AutoCompact          bool                    `json:"auto_compact"`
	AutoCompactThreshold int                     `json:"auto_compact_threshold" doc:"Tokens at which the runtime compacts by itself; 0 = unknown"`
	Categories           []ContextCategoryView   `json:"categories" doc:"In the runtime's order; deferred rows are outside the window"`
	McpTools             []ContextMcpToolView    `json:"mcp_tools" doc:"Every MCP tool (the mcp_tools and mcp_tools_deferred rows expanded)"`
	McpServers           []ContextMcpServerView  `json:"mcp_servers"`
	MemoryFiles          []ContextMemoryFileView `json:"memory_files"`
	Skills               []ContextSkillView      `json:"skills"`
}

type contextDetailOutput struct{ Body ContextDetailView }

// ContextRefreshStarted is POST /api/conversations/{cid}/context/refresh: the node was asked; the
// result is the project event "context" with this refresh_id (then GET …/context/detail again).
type ContextRefreshStarted struct {
	RefreshID string `json:"refresh_id"`
	SessionID string `json:"session_id"`
}

type contextRefreshOutput struct{ Body ContextRefreshStarted }

func (a *API) registerContextDetail(api huma.API) {
	t := "conversations"
	huma.Register(api, op("conversation-context-detail", "GET", "/api/conversations/{cid}/context/detail", t,
		summary("What fills the session's context window, by category (sessions on Claude only)")), a.contextDetail)
	huma.Register(api, op("conversation-context-refresh", "POST", "/api/conversations/{cid}/context/refresh", t, status(http.StatusAccepted),
		summary("Count the session's context precisely now; the result arrives as the project event context")), a.contextRefresh)
}

// soloSession is the latest session of a session's (多仓库会话) employee; ok=false before the first one.
func (a *API) soloSession(ctx context.Context, cid string) (core.AgentSession, bool, error) {
	_, c, e, err := a.conversationEmployee(ctx, cid)
	if err != nil {
		return core.AgentSession{}, false, err
	}
	if c.Kind != "thread" {
		return core.AgentSession{}, false, httpapi.Fail(http.StatusNotFound, "not_found", "只有会话有上下文细分")
	}
	t, err := core.ThreadByID(ctx, a.e.Core.Pool, c.ID)
	if err != nil {
		return core.AgentSession{}, false, fail(err, "没有这个对话")
	}
	if !t.Solo() {
		return core.AgentSession{}, false, httpapi.Fail(http.StatusNotFound, "not_found", "只有会话有上下文细分")
	}
	s, err := core.LatestSession(ctx, a.e.Core.Pool, e.ID)
	if errors.Is(err, core.ErrNotFound) {
		return s, false, nil
	}
	return s, err == nil, err
}

// processRunning: the session has a live runtime process on its node (a parked one is not started for a refresh).
func processRunning(s core.AgentSession) bool {
	switch s.State {
	case "active", "turn_running":
		return s.NodeID != nil
	}
	return false
}

func (a *API) canRefreshContext(s core.AgentSession) bool {
	return s.Runtime == "claude" && processRunning(s) && a.e.Gateway.ContextDetailSupported(*s.NodeID)
}

func (a *API) contextDetail(ctx context.Context, in *cidInput) (*contextDetailOutput, error) {
	s, ok, err := a.soloSession(ctx, in.CID)
	if err != nil {
		return nil, err
	}
	out := ContextDetailView{Categories: []ContextCategoryView{}, McpTools: []ContextMcpToolView{}, McpServers: []ContextMcpServerView{},
		MemoryFiles: []ContextMemoryFileView{}, Skills: []ContextSkillView{}}
	if !ok {
		return &contextDetailOutput{Body: out}, nil
	}
	out.Runtime, out.SessionID, out.State = s.Runtime, s.ID, s.State
	out.Supported = s.Runtime == "claude"
	out.CanRefresh = a.canRefreshContext(s)
	if !out.Supported {
		return &contextDetailOutput{Body: out}, nil
	}
	d, err := core.ContextDetailOf(ctx, a.e.Core.Pool, s.ID)
	if err != nil {
		return nil, err
	}
	if len(d.Detail) > 0 {
		var cd protocol.ContextDetail
		if err := json.Unmarshal(d.Detail, &cd); err != nil {
			return nil, err
		}
		fillContextDetail(&out, cd)
		out.UpdatedAt = d.At
	}
	return &contextDetailOutput{Body: out}, nil
}

func fillContextDetail(out *ContextDetailView, cd protocol.ContextDetail) {
	out.Detail, out.TotalTokens, out.MaxTokens = string(cd.Detail), cd.TotalTokens, cd.MaxTokens
	out.Model = deref(cd.Model)
	if cd.RawMaxTokens != nil {
		out.RawMaxTokens = *cd.RawMaxTokens
	}
	if cd.AutoCompact != nil {
		out.AutoCompact = *cd.AutoCompact
	}
	if cd.AutoCompactThreshold != nil {
		out.AutoCompactThreshold = *cd.AutoCompactThreshold
	}
	for _, c := range cd.Categories {
		out.Categories = append(out.Categories, ContextCategoryView{ID: deref(c.ID), Name: c.Name, Tokens: c.Tokens, Kind: string(c.Kind)})
	}
	for _, t := range cd.McpTools {
		out.McpTools = append(out.McpTools, ContextMcpToolView{Name: t.Name, Server: t.Server, Tokens: t.Tokens, Loaded: t.Loaded == nil || *t.Loaded})
	}
	for _, s := range cd.McpServers {
		out.McpServers = append(out.McpServers, ContextMcpServerView{Server: s.Server, Tokens: s.Tokens})
	}
	for _, f := range cd.MemoryFiles {
		out.MemoryFiles = append(out.MemoryFiles, ContextMemoryFileView{Path: f.Path, Type: deref(f.Type), Tokens: f.Tokens})
	}
	for _, s := range cd.Skills {
		out.Skills = append(out.Skills, ContextSkillView{Name: s.Name, Source: deref(s.Source), Tokens: s.Tokens})
	}
}

func (a *API) contextRefresh(ctx context.Context, in *cidInput) (*contextRefreshOutput, error) {
	s, ok, err := a.soloSession(ctx, in.CID)
	if err != nil {
		return nil, err
	}
	if !ok || s.Runtime != "claude" {
		return nil, httpapi.Fail(http.StatusConflict, "unsupported", "只有 Claude 会话有上下文细分")
	}
	if !processRunning(s) {
		return nil, httpapi.Fail(http.StatusConflict, "session_not_running", "会话现在没有在运行（已停放或已关闭），发一条消息后再刷新")
	}
	id := core.NewID("cr")
	err = a.e.Gateway.RefreshContext(ctx, *s.NodeID, protocol.SessionContextRefresh{SessionID: protocol.SessionID(s.ID), OpenGen: protocol.OpenGen(s.OpenGen), RefreshID: id})
	switch {
	case errors.Is(err, platform.ErrNodeOffline):
		return nil, httpapi.FailErr(http.StatusConflict, "node_offline", err)
	case errors.Is(err, nodegw.ErrContextDetailUnsupported):
		return nil, httpapi.FailErr(http.StatusConflict, "node_unsupported", err)
	case err != nil:
		return nil, err
	}
	return &contextRefreshOutput{Body: ContextRefreshStarted{RefreshID: id, SessionID: s.ID}}, nil
}
