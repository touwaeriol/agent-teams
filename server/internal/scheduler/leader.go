package scheduler

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RunAsLeader runs fn while this process holds the PostgreSQL advisory lock
// named key (one runner for "global" loops across replicas, architecture.md §3.1).
// It retries every retry until ctx ends.
func RunAsLeader(ctx context.Context, pool *pgxpool.Pool, key string, retry time.Duration, fn func(ctx context.Context)) {
	for ctx.Err() == nil {
		held, release := acquire(ctx, pool, key)
		if held {
			fn(ctx)
			release()
			continue
		}
		wait(ctx, retry)
	}
}

func acquire(ctx context.Context, pool *pgxpool.Pool, key string) (bool, func()) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return false, nil
	}
	var ok bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtext($1))`, key).Scan(&ok); err != nil || !ok {
		conn.Release()
		return false, nil
	}
	return true, func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtext($1))`, key)
		conn.Release()
	}
}

func wait(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
