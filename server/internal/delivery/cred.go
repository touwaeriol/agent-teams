package delivery

import (
	"context"
	"errors"
	"strings"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/ghops"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// credTTL is the lifetime reported for minted tokens (installation tokens live 1 h; the
// daemon refreshes 5 minutes before this).
const credTTL = 50 * time.Minute

// Credential answers cred.request with a token narrowed to the caller (protocol.md §8,
// architecture.md §10.4). Ownership of the session or job was checked by the gateway.
func (e *Engine) Credential(ctx context.Context, node store.Node, r *protocol.CredRequest) error {
	resp := e.credential(ctx, node, r)
	resp.ReqID = r.ReqID
	return e.gw.SendNow(ctx, node.ID, protocol.TypeCredResponse, resp)
}

func (e *Engine) credential(ctx context.Context, node store.Node, r *protocol.CredRequest) protocol.CredResponse {
	projectID, scope, err := e.credScope(ctx, node, r)
	if err != nil {
		return refuse(err.Error())
	}
	_, repo, err := e.repoOf(ctx, projectID)
	if err != nil {
		return refuse("no_repo")
	}
	if !samePath(repo.Slug, r.Path) || r.Host != "github.com" {
		return refuse("repo_not_in_project")
	}
	tok, err := e.gh.Token(ctx, repo, scope)
	if errors.Is(err, ghops.ErrPATNarrow) && r.JobID != nil {
		tok, err = e.gh.Token(ctx, repo, ghops.ScopeFull) // platform jobs may use the PAT (§10.4)
	}
	if errors.Is(err, ghops.ErrPATNarrow) {
		return refuse("pat_not_narrowable")
	}
	if err != nil {
		e.log.Warn("mint credential", "node_id", node.ID, "err", err)
		return refuse("token_unavailable")
	}
	user, exp := "x-access-token", e.now().Add(credTTL).UTC()
	return protocol.CredResponse{Ok: true, Username: &user, Password: &tok, ExpiresAt: &exp}
}

func refuse(reason string) protocol.CredResponse {
	return protocol.CredResponse{Ok: false, Error: &reason}
}

func samePath(slug, path string) bool {
	p := strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	return strings.EqualFold(p, slug)
}

// credScope decides the project and permission set for the caller.
func (e *Engine) credScope(ctx context.Context, node store.Node, r *protocol.CredRequest) (string, ghops.Scope, error) {
	if r.SessionID != nil {
		return e.sessionScope(ctx, string(*r.SessionID), r.Purpose)
	}
	if r.JobID != nil {
		return e.jobScope(ctx, node, string(*r.JobID))
	}
	return "", 0, errors.New("no_caller")
}

func (e *Engine) sessionScope(ctx context.Context, sessionID string, purpose protocol.CredRequestPurpose) (string, ghops.Scope, error) {
	sc, err := core.SessionContextOf(ctx, e.core.Pool, sessionID)
	if err != nil {
		return "", 0, errors.New("unknown_session")
	}
	if sc.Employee.Tier == "coordinator" {
		return "", 0, errors.New("coordinator_has_no_credentials")
	}
	if purpose == "push" {
		return "", 0, errors.New("push_only_via_platform")
	}
	return sc.Employee.ProjectID, ghops.ScopeRead, nil
}

func (e *Engine) jobScope(ctx context.Context, node store.Node, jobID string) (string, ghops.Scope, error) {
	var typ, projectID string
	err := e.core.Pool.QueryRow(ctx, `SELECT type, COALESCE(body->>'project_id', '') FROM at_node_commands
		WHERE node_id = $1 AND type LIKE 'git.%' AND body->>'job_id' = $2`, node.ID, jobID).Scan(&typ, &projectID)
	if err != nil || projectID == "" {
		return "", 0, errors.New("unknown_job")
	}
	if typ != protocol.TypeGitPush {
		return projectID, ghops.ScopeRead, nil
	}
	d, err := core.DeliveryOf(ctx, e.core.Pool, projectID)
	if err == nil && d.AllowWorkflowEdits {
		return projectID, ghops.ScopePushFlows, nil
	}
	return projectID, ghops.ScopePush, nil
}
