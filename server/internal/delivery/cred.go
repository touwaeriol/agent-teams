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

// patTTL is the lifetime reported for a PAT, which does not expire on its own: the daemon
// asks again after it, so a project that stopped allowing the PAT stops handing it out.
// Installation tokens report their real expiry (remote-servers.md §10.3 #2).
const patTTL = 30 * time.Minute

// credTimeout bounds one cred.request (ownership checks plus, on a cache miss, a GitHub call).
const credTimeout = time.Minute

// credInflight bounds the cred.requests minted at the same time across all node links.
const credInflight = 32

// credSlots is the semaphore for credInflight (package-level: the engine is built once per process).
var credSlots = make(chan struct{}, credInflight)

// Credential answers cred.request with a token narrowed to the caller (protocol.md §8,
// architecture.md §10.4). Ownership of the session or job was checked by the gateway.
// The answer is worked out off the link's read loop (a slow GitHub call must not stall the
// node's other messages); cred.request is unreliable and paired by req_id, so order does not matter.
func (e *Engine) Credential(ctx context.Context, node store.Node, r *protocol.CredRequest) error {
	req := *r
	select {
	case credSlots <- struct{}{}:
	default:
		// Never wait on the read loop: the daemon asks again after 10 s with the same req_id.
		e.log.Warn("cred.request dropped: too many in flight", "node_id", node.ID, "req_id", req.ReqID, "limit", credInflight)
		return nil
	}
	go func() {
		defer func() { <-credSlots }()
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), credTimeout)
		defer cancel()
		if err := e.answerCredential(cctx, node, &req); err != nil {
			e.log.Warn("cred.response not sent", "node_id", node.ID, "req_id", req.ReqID, "err", err)
		}
	}()
	return nil
}

func (e *Engine) answerCredential(ctx context.Context, node store.Node, r *protocol.CredRequest) error {
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
	refresh := r.ForceRefresh != nil && *r.ForceRefresh // git rejected the last token (credential erase)
	tok, exp, err := e.gh.Credential(ctx, repo, scope, refresh)
	if errors.Is(err, ghops.ErrPATNarrow) && r.JobID != nil {
		tok, exp, err = e.gh.Credential(ctx, repo, ghops.ScopeFull, refresh) // platform jobs may use the PAT (§10.4)
	}
	if errors.Is(err, ghops.ErrPATNarrow) {
		return refuse("pat_not_narrowable")
	}
	if err != nil {
		e.log.Warn("mint credential", "node_id", node.ID, "err", err)
		return refuse("token_unavailable")
	}
	user := "x-access-token"
	exp = credExpiry(exp, e.now())
	return protocol.CredResponse{Ok: true, Username: &user, Password: &tok, ExpiresAt: &exp}
}

// credExpiry is the expiry reported to the daemon: the token's own, or patTTL from now for a
// credential without one (a PAT).
func credExpiry(exp, now time.Time) time.Time {
	if exp.IsZero() {
		return now.Add(patTTL).UTC()
	}
	return exp.UTC()
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

// sessionScope: leads and members get one token per repository that can fetch, push branches
// and open pull requests and issues (architecture.md §10.4; the user asked for gh and git push in
// every employee session). The purpose is ignored: git's credential helper cannot tell a fetch
// from a push. Protected branches are refused by the platform pre-push hook; merging stays with
// the merge queue and user acceptance. The coordinator gets nothing.
func (e *Engine) sessionScope(ctx context.Context, sessionID string, _ protocol.CredRequestPurpose) (string, ghops.Scope, error) {
	sc, err := core.SessionContextOf(ctx, e.core.Pool, sessionID)
	if err != nil {
		return "", 0, errors.New("unknown_session")
	}
	if sc.Employee.Tier == "coordinator" {
		return "", 0, errors.New("coordinator_has_no_credentials")
	}
	scope := ghops.ScopeEmployee
	if d, err := core.DeliveryOf(ctx, e.core.Pool, sc.Employee.ProjectID); err == nil && d.AllowWorkflowEdits {
		scope = ghops.ScopeEmployeeFlows
	}
	return sc.Employee.ProjectID, scope, nil
}

func (e *Engine) jobScope(ctx context.Context, node store.Node, jobID string) (callerScope, error) {
	var typ string
	cs := callerScope{}
	err := store.QueryRow(ctx, e.core.Pool, `SELECT type, COALESCE(body->>'project_id', ''), COALESCE(body->>'repo', '') FROM at_node_commands
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
