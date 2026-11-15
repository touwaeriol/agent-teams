package httpapi

import (
	"context"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// The helpers below are for Extensions (orchestration routes registered on the
// same huma API): they share authentication, CSRF and the error format.

// CurrentUser returns the signed-in user of a request handled under the session middleware.
func CurrentUser(ctx context.Context) (store.User, bool) {
	p := principalOf(ctx)
	if p == nil {
		return store.User{}, false
	}
	return p.User, true
}

// RequireUser returns the signed-in user or the standard 401 error.
func RequireUser(ctx context.Context) (store.User, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return store.User{}, err
	}
	return p.User, nil
}

// RequireAdmin returns the signed-in administrator or 401/403.
func RequireAdmin(ctx context.Context) (store.User, error) {
	p, err := requireAdmin(ctx)
	if err != nil {
		return store.User{}, err
	}
	return p.User, nil
}

// Fail builds an API error {"detail", "code"} with the given status.
func Fail(status int, code, detail string) error { return fail(status, code, detail) }

// StoreError maps store.ErrNotFound/ErrConflict to 404/409 with the given 404 message.
func StoreError(err error, notFound string) error { return storeErr(err, notFound) }
