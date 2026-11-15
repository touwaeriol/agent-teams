package httpapi

import (
	"context"
	"net/http"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// principal is the signed-in caller of a request.
type principal struct {
	User    store.User
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
}

func withRequest(ctx context.Context, r *http.Request) context.Context {
	return context.WithValue(ctx, requestKey, requestInfo{IP: clientIP(r), UserAgent: r.UserAgent(), Host: r.Host})
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
	if p.User.Role != "admin" {
		return nil, fail(http.StatusForbidden, "admin_required", "需要管理员权限")
	}
	return p, nil
}
