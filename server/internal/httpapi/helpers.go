package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/auth"
	"github.com/touwaeriol/agent-teams/server/internal/github"
	"github.com/touwaeriol/agent-teams/server/internal/msg"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// rateLimit returns 429 with Retry-After once key exceeds limit per window.
func (s *Server) rateLimit(ctx context.Context, key string, limit int, window time.Duration) error {
	ok, wait := s.limiter.Allow(ctx, key, limit, window)
	if ok {
		return nil
	}
	m := msg.T("请求太频繁，请稍后再试")
	e := &APIError{Status: http.StatusTooManyRequests, Code: "rate_limited", Detail: m.String(), Message: &m,
		header: http.Header{"Retry-After": {strconv.Itoa(int(math.Ceil(wait.Seconds())))}}}
	return e
}

// audit records an action; failures are logged, never returned.
func (s *Server) audit(ctx context.Context, userID, action, targetType, targetID string, detail any) {
	raw, _ := json.Marshal(detail)
	if detail == nil {
		raw = nil
	}
	ip := requestOf(ctx).IP
	entry := store.AuditEntry{Actor: "user:" + userID, Action: action, TargetType: targetType, TargetID: targetID, Detail: raw}
	if userID != "" {
		entry.UserID = &userID
	} else {
		entry.Actor = "system"
	}
	if ip != "" {
		entry.IP = &ip
	}
	if err := s.store.Audit(ctx, entry); err != nil {
		s.log.Warn("audit failed", "action", action, "err", err)
	}
}

// storeErr maps store errors to API errors; notFound is the 404 message.
func storeErr(err error, notFound string) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return fail(http.StatusNotFound, "not_found", notFound)
	case errors.Is(err, store.ErrConflict):
		return fail(http.StatusConflict, "conflict", "已经存在")
	}
	return err
}

// storeErrMsg is storeErr with a translatable 404 message.
func storeErrMsg(err error, notFound msg.Text) error {
	if errors.Is(err, store.ErrNotFound) {
		return failMsg(http.StatusNotFound, "not_found", notFound)
	}
	return storeErr(err, notFound.Key)
}

// githubErr maps GitHub failures to 422 (v1) or 409 for configuration problems.
func githubErr(err error) error {
	var ge *github.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ge):
		return fail(http.StatusUnprocessableEntity, "github", ge.Message)
	case errors.Is(err, github.ErrNoApp), errors.Is(err, auth.ErrUnseal), errors.Is(err, auth.ErrNoSecret):
		return failErr(http.StatusConflict, "github_config", err)
	}
	return err
}

func ptr[T any](v T) *T { return &v }

func pageLimit(limit int) int {
	if limit <= 0 || limit > 200 {
		return 50
	}
	return limit
}

func cursorString(id int64) *string {
	if id == 0 {
		return nil
	}
	s := fmt.Sprint(id)
	return &s
}
