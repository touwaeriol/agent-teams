package httpapi

import (
	_ "embed"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
	"github.com/gin-gonic/gin"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Route authorization is Casbin RBAC over (subject, gin route template, method). The model
// and the route policy (p lines, plus the role hierarchy) are embedded in the binary
// (authz/model.conf, authz/policy.csv): they are code, versioned with the routes. Which role
// a user holds is data: one grouping rule per user (g, <user id>, admin | user) in the
// casbin_rule table, loaded and written only through this enforcer (authzAdapter). It decides
// which role may reach a route group; whether a row belongs to the caller is decided by the
// handlers, which load projects, threads, deliverables, schedules, nodes… scoped to the
// caller's user id and answer 404 for anyone else's.
//
// Several console replicas: each keeps the rules in memory, applies its own changes at once
// and reloads casbin_rule every authzReloadEvery (Server.RunAuthz), so a role change made on
// another replica takes effect there within that interval.

//go:embed authz/model.conf
var authzModel string

//go:embed authz/policy.csv
var authzPolicy string

// Subjects of a request: a signed-in caller is its user id; these are the role names.
const (
	SubjectAnonymous = "anonymous"
	SubjectNode      = "node"
	SubjectUser      = "user"
	SubjectAdmin     = "admin"
)

// Short names inside the package.
const (
	roleAnonymous = SubjectAnonymous
	roleNode      = SubjectNode
	roleUser      = SubjectUser
	roleAdmin     = SubjectAdmin
)

// authzReloadEvery is how often RunAuthz re-reads casbin_rule (changes from other replicas).
const authzReloadEvery = 10 * time.Second

// authorizer enforces the embedded route policy with the users' roles from casbin_rule.
type authorizer struct {
	e *casbin.SyncedCachedEnforcer
}

// newAuthorizer loads the embedded policy and, with a store, every user's role.
func newAuthorizer(db *store.Store) (*authorizer, error) {
	m, err := model.NewModelFromString(authzModel)
	if err != nil {
		return nil, fmt.Errorf("authz model: %w", err)
	}
	e, err := casbin.NewSyncedCachedEnforcer(m, &authzAdapter{policy: authzPolicy, db: db})
	if err != nil {
		return nil, fmt.Errorf("authz enforcer: %w", err)
	}
	e.EnableAutoSave(true)
	return &authorizer{e: e}, nil
}

// mustAuthorizer panics on a broken embedded policy or an unreadable casbin_rule.
func mustAuthorizer(db *store.Store) *authorizer {
	a, err := newAuthorizer(db)
	if err != nil {
		panic(err)
	}
	return a
}

// allowed reports whether subject (a user id or a role) may call method on the route template.
func (a *authorizer) allowed(subject, route, method string) (bool, error) {
	return a.e.Enforce(subject, route, method)
}

// role is a user's role for display and requireAdmin: admin if they hold it, else user.
// A user with no grouping rule at all is shown as user but is denied every user route.
func (a *authorizer) role(userID string) string {
	if ok, _ := a.e.HasRoleForUser(userID, roleAdmin); ok {
		return roleAdmin
	}
	return roleUser
}

// setRole makes role the user's only role (registration, admin change, atctl), persisted in
// casbin_rule by the enforcer's auto-save.
func (a *authorizer) setRole(userID, role string) error {
	defer a.invalidate()
	current, err := a.e.GetRolesForUser(userID)
	if err != nil {
		return err
	}
	switch {
	case len(current) == 1 && current[0] == role:
		return nil
	case len(current) == 1:
		_, err = a.e.UpdateGroupingPolicy([]string{userID, current[0]}, []string{userID, role})
		return err
	case len(current) > 1:
		if _, err := a.e.RemoveFilteredGroupingPolicy(0, userID); err != nil {
			return err
		}
	}
	_, err = a.e.AddGroupingPolicy(userID, role)
	return err
}

// forget removes every grouping rule of a deleted user.
func (a *authorizer) forget(userID string) error {
	defer a.invalidate()
	_, err := a.e.RemoveFilteredGroupingPolicy(0, userID)
	return err
}

// reload re-reads the embedded policy and casbin_rule (other replicas' changes).
func (a *authorizer) reload() error {
	defer a.invalidate()
	return a.e.LoadPolicy()
}

// invalidate drops cached decisions: the cache is keyed by request, not by rule.
func (a *authorizer) invalidate() { _ = a.e.InvalidateCache() }

// subjectOf is the Casbin subject of a caller (nil = not signed in): the user id, else node
// for a request without a session that carries a Bearer token (its handler checks the node
// token), else anonymous.
func subjectOf(p *principal, r *http.Request) string {
	switch {
	case p == nil && bearerToken(r) != "":
		return roleNode
	case p == nil:
		return roleAnonymous
	}
	return p.User.ID
}

// authorize is the gin middleware: 401 for callers without a session on a protected route
// (anonymous, or a daemon off its routes), 403 for signed-in callers without the role.
func (s *Server) authorize(c *gin.Context) {
	p := principalOf(c.Request.Context())
	ok, err := s.authz.allowed(subjectOf(p, c.Request), c.FullPath(), c.Request.Method)
	if err != nil {
		s.log.Error("authorization failed", "err", err, "route", c.FullPath())
		abortWith(c, fail(http.StatusInternalServerError, "internal", "服务器出错了"))
		return
	}
	if !ok {
		abortWith(c, denied(p, c.FullPath()))
		return
	}
	c.Next()
}

func denied(p *principal, route string) error {
	switch {
	case p == nil:
		return fail(http.StatusUnauthorized, "unauthorized", "请先登录")
	case strings.HasPrefix(route, "/api/admin/"):
		return fail(http.StatusForbidden, "admin_required", "需要管理员权限")
	}
	return fail(http.StatusForbidden, "forbidden", "没有权限")
}

// abortWith writes an API error and stops the handler chain.
func abortWith(c *gin.Context, err error) {
	writeError(c.Writer, err)
	c.Abort()
}
