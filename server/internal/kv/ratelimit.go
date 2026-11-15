package kv

import (
	"context"
	"time"
)

// Limiter is a fixed-window rate limiter on top of KV counters.
type Limiter struct {
	KV  KV
	now func() time.Time
}

// NewLimiter returns a limiter over kv.
func NewLimiter(store KV) *Limiter { return &Limiter{KV: store, now: time.Now} }

// Allow counts one hit for key; it returns false and the wait once more than
// limit hits land in the current window. Errors from Valkey fail open.
func (l *Limiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, time.Duration) {
	now := l.now()
	bucket := now.Truncate(window)
	n, err := l.KV.Incr(ctx, "rl:"+key+":"+formatInt(bucket.Unix()), window)
	if err != nil || n <= int64(limit) {
		return true, 0
	}
	return false, bucket.Add(window).Sub(now)
}
