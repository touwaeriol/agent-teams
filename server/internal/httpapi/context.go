package httpapi

import (
	"context"
	"net/http"
	"regexp"
	"strings"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// principal is the signed-in caller of a request.
type principal struct {
	User    store.User
	Role    string // admin | user, from the Casbin enforcer when the session is resolved
	Session store.Session
	Bearer  bool // authenticated with Authorization: Bearer (exempt from CSRF)
}

type ctxKey int

const (
	principalKey ctxKey = iota
	requestKey
)

func withPrincipal(ctx context.Context, p *principal) context.Context {
	return context.WithValue(ctx, principalKey, p)
}

func principalOf(ctx context.Context) *principal {
	p, _ := ctx.Value(principalKey).(*principal)
	return p
}

// requestInfo is what handlers may need from the raw request.
type requestInfo struct {
	IP        string
	UserAgent string
	Host      string
	Origin    string // scheme://host of the request when the host is a plain host[:port], else ""
}

func withRequest(ctx context.Context, r *http.Request) context.Context {
	return context.WithValue(ctx, requestKey, requestInfo{IP: clientIP(r), UserAgent: r.UserAgent(), Host: r.Host, Origin: requestOrigin(r)})
}

func requestOf(ctx context.Context) requestInfo {
	info, _ := ctx.Value(requestKey).(requestInfo)
	return info
}

// requireUser returns the caller or 401 {"detail":"请先登录"}.
func requireUser(ctx context.Context) (*principal, error) {
	p := principalOf(ctx)
	if p == nil {
		return nil, fail(http.StatusUnauthorized, "unauthorized", "请先登录")
	}
	return p, nil
}

// requireAdmin returns the caller if it is an administrator.
func requireAdmin(ctx context.Context) (*principal, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if p.Role != roleAdmin {
		return nil, fail(http.StatusForbidden, "admin_required", "需要管理员权限")
	}
	return p, nil
}

var plainHost = regexp.MustCompile(`^(?:[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)*|\[[0-9A-Fa-f:.]+\])(?::[0-9]{1,5})?$`)

// requestOrigin is the origin the caller used: https when the request came over TLS or the proxy
// says so (Caddy sets X-Forwarded-Proto), else http. Hosts with anything beyond letters, digits,
// dots, dashes and a port give "" so a forged Host header cannot inject into install-node.sh.
func requestOrigin(r *http.Request) string {
	if !plainHost.MatchString(r.Host) {
		return ""
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]), "https") {
		scheme = "https"
	}
	return scheme + "://" + strings.ToLower(r.Host)
}
