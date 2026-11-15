package orchapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/kv"
)

// idempotencyTTL is how long a creating POST remembers its result (api.md §0).
const idempotencyTTL = 24 * time.Hour

// idempotent runs fn once per (user, Idempotency-Key): a repeated request gets the first
// result decoded into out. Without a key fn always runs.
func idempotent[T any](ctx context.Context, a *API, userID, op, key string, fn func() (T, error)) (T, error) {
	if key == "" {
		return fn()
	}
	cacheKey := "idem:" + userID + ":" + op + ":" + key
	var out T
	if raw, ok, err := a.e.KV.Get(ctx, cacheKey); err == nil && ok {
		return out, json.Unmarshal([]byte(raw), &out)
	}
	out, err := fn()
	if err != nil {
		return out, err
	}
	if raw, err := json.Marshal(out); err == nil {
		_ = a.e.KV.Set(ctx, cacheKey, string(raw), idempotencyTTL)
	}
	return out, nil
}

// limitMessages applies "发消息 60 次/分钟/用户" (api.md §0).
func (a *API) limitMessages(ctx context.Context, userID string) error {
	ok, _ := kv.NewLimiter(a.e.KV).Allow(ctx, "rl:msg:"+userID, 60, time.Minute)
	if ok {
		return nil
	}
	return httpapi.Fail(http.StatusTooManyRequests, "rate_limited", "消息发得太快了，请稍后再试")
}
