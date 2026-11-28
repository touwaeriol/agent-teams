package httpapi

import (
	"context"

	"github.com/touwaeriol/agent-teams/server/internal/msg"
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

// Fail builds an API error {"detail", "code", "message"} with the given status; detail is also
// the message key (web i18n).
func Fail(status int, code, detail string) error { return fail(status, code, detail) }

// Failf is Fail with placeholders: Failf(409, "bad_state", "讨论串现在是 {status}", "status", s).
func Failf(status int, code, key string, kv ...any) error { return failf(status, code, key, kv...) }

// FailErr reports an engine error, keeping its translatable message when it has one (msg.Of).
func FailErr(status int, code string, err error) error { return failErr(status, code, err) }

// StoreError maps store.ErrNotFound/ErrConflict to 404/409 with the given 404 message.
func StoreError(err error, notFound string) error { return storeErr(err, notFound) }

// Invalid is a 422 validation error on one field.
func Invalid(field, message string) error { return invalidField(field, msg.T(message)) }

// Invalidf is Invalid with placeholders.
func Invalidf(field, key string, kv ...any) error { return invalidField(field, msg.T(key, kv...)) }

// InvalidMsg is a 422 on one field with a message from a lower layer (msg.Of(err), msg.T).
func InvalidMsg(field string, m msg.Text) error { return invalidField(field, m) }
