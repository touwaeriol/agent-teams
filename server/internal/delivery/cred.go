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
	cs, err := e.credScope(ctx, node, r)
	if err != nil {
		return refuse(err.Error())
	}
	_, repos, err := e.reposOf(ctx, cs.projectID)
	if err != nil {
		return refuse("no_repo")
	}
	repo, ok := ghops.Find(repos, r.Path)
	if !ok || !samePath(repo.Slug, r.Path) || r.Host != "github.com" || (cs.repo != "" && !samePath(cs.repo, r.Path)) {
		return refuse("repo_not_in_project")
	}
	scope := cs.scope
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

// callerScope is the project, permission set and (for git jobs) the one repository a caller may get a token for.
type callerScope struct {
	projectID string
	scope     ghops.Scope
	repo      string // "" = any repository of the project
}

// credScope decides the project and permission set for the caller. A session may read every
// repository of its project; a git job only the repository it works on (with that repository's connection).
func (e *Engine) credScope(ctx context.Context, node store.Node, r *protocol.CredRequest) (callerScope, error) {
	if r.SessionID != nil {
		pid, scope, err := e.sessionScope(ctx, string(*r.SessionID), r.Purpose)
		return callerScope{projectID: pid, scope: scope}, err
	}
	if r.JobID != nil {
		return e.jobScope(ctx, node, string(*r.JobID))
	}
	return callerScope{}, errors.New("no_caller")
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

func (e *Engine) jobScope(ctx context.Context, node store.Node, jobID string) (callerScope, error) {
	var typ string
	cs := callerScope{}
	err := e.core.Pool.QueryRow(ctx, `SELECT type, COALESCE(body->>'project_id', ''), COALESCE(body->>'repo', '') FROM at_node_commands
		WHERE node_id = $1 AND type LIKE 'git.%' AND body->>'job_id' = $2`, node.ID, jobID).Scan(&typ, &cs.projectID, &cs.repo)
	if err != nil || cs.projectID == "" {
		return cs, errors.New("unknown_job")
	}
	cs.scope = ghops.ScopeRead
	if typ != protocol.TypeGitPush {
		return cs, nil
	}
	cs.scope = ghops.ScopePush
	if d, err := core.DeliveryOf(ctx, e.core.Pool, cs.projectID); err == nil && d.AllowWorkflowEdits {
		cs.scope = ghops.ScopePushFlows
	}
	return cs, nil
}
