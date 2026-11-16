package httpapi

import (
	_ "embed"
	"fmt"
	"net/http"
	"strings"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
	"github.com/gin-gonic/gin"
)

// Route authorization is Casbin RBAC over (role, gin route template, method), with the
// model and policy embedded in the binary (authz/model.conf, authz/policy.csv). It decides
// which role may reach a route group; whether a row belongs to the caller is decided by the
// handlers, which load projects, threads, deliverables, schedules, nodes… scoped to the
// caller's user id and answer 404 for anyone else's.

//go:embed authz/model.conf
var authzModel string

//go:embed authz/policy.csv
var authzPolicy string

// Subjects of a request.
const (
	roleAnonymous = "anonymous"
	roleUser      = "user"
	roleAdmin     = "admin"
)

// authorizer enforces the embedded policy.
type authorizer struct {
	e *casbin.SyncedCachedEnforcer
}

func newAuthorizer() (*authorizer, error) {
	m, err := model.NewModelFromString(authzModel)
	if err != nil {
		return nil, fmt.Errorf("authz model: %w", err)
	}
	e, err := casbin.NewSyncedCachedEnforcer(m)
	if err != nil {
		return nil, fmt.Errorf("authz enforcer: %w", err)
	}
	if err := loadPolicy(e, authzPolicy); err != nil {
		return nil, err
	}
	return &authorizer{e: e}, nil
}

// mustAuthorizer panics on a broken embedded policy (caught by tests).
func mustAuthorizer() *authorizer {
	a, err := newAuthorizer()
	if err != nil {
		panic(err)
	}
	return a
}

// loadPolicy adds every "p, …" and "g, …" line; blank lines and # comments are skipped.
func loadPolicy(e *casbin.SyncedCachedEnforcer, text string) error {
	for n, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if err := addPolicyLine(e, line); err != nil {
			return fmt.Errorf("authz policy line %d: %w", n+1, err)
		}
	}
	return nil
}

func addPolicyLine(e *casbin.SyncedCachedEnforcer, line string) error {
	fields := strings.Split(line, ",")
	for i := range fields {
		fields[i] = strings.TrimSpace(fields[i])
	}
	switch {
	case fields[0] == "p" && len(fields) == 4:
		_, err := e.AddPolicy(fields[1], fields[2], fields[3])
		return err
	case fields[0] == "g" && len(fields) == 3:
		_, err := e.AddGroupingPolicy(fields[1], fields[2])
		return err
	}
	return fmt.Errorf("unrecognised rule %q", line)
}

// allowed reports whether role may call method on the route template.
func (a *authorizer) allowed(role, route, method string) (bool, error) {
	return a.e.Enforce(role, route, method)
}

// roleOf is the Casbin subject of a caller (nil = not signed in).
func roleOf(p *principal) string {
	switch {
	case p == nil:
		return roleAnonymous
	case p.User.Role == roleAdmin:
		return roleAdmin
	}
	return roleUser
}

// authorize is the gin middleware: 401 for anonymous callers of a protected route,
// 403 for signed-in callers without the role.
func (s *Server) authorize(c *gin.Context) {
	p := principalOf(c.Request.Context())
	ok, err := s.authz.allowed(roleOf(p), c.FullPath(), c.Request.Method)
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
