// Package core is the data layer shared by the orchestration engines
// (orchestrator, scheduler, toolsvc, approvals, status, delivery): rows and
// views of threads, employees, sessions, tasks, questions, messages and mail,
// plus the project event stream. Writes go through Tx, which allocates the
// project event sequence inside the transaction (architecture.md §11.1) and
// publishes "something new" hints only after the commit.
package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

// ErrNotFound means no row matched.
var ErrNotFound = errors.New("not found")

// ErrConflict means the row's state does not allow the change.
var ErrConflict = errors.New("conflict")

// DB is satisfied by *pgxpool.Pool and pgx.Tx.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Publisher sends realtime hints (Valkey pub/sub plus local subscribers).
type Publisher interface {
	Publish(ctx context.Context, channel, message string)
}

// Store is the orchestration data layer.
type Store struct {
	Pool *pgxpool.Pool
	Pub  Publisher
	// Online reports whether a node has a live link (views show it).
	Online func(nodeID string) bool
}

// New returns a Store; pub may be nil (no realtime hints).
func New(pool *pgxpool.Pool, pub Publisher) *Store {
	return &Store{Pool: pool, Pub: pub, Online: func(string) bool { return false }}
}

// Tx is one transaction plus what to do after it commits.
type Tx struct {
	pgx.Tx
	store *Store
	after []func(context.Context)
	hints map[string]string
}

// InTx runs fn in a transaction; after a successful commit it publishes the
// event hints and runs AfterCommit callbacks in order.
func (s *Store) InTx(ctx context.Context, fn func(tx *Tx) error) error {
	var done *Tx
	err := pgx.BeginFunc(ctx, s.Pool, func(t pgx.Tx) error {
		tx := &Tx{Tx: t, store: s, hints: map[string]string{}}
		if err := fn(tx); err != nil {
			return err
		}
		done = tx
		return nil
	})
	if err != nil {
		return err
	}
	done.flush(ctx)
	return nil
}

func (tx *Tx) flush(ctx context.Context) {
	if tx.store.Pub != nil {
		for channel, msg := range tx.hints {
			tx.store.Pub.Publish(ctx, channel, msg)
		}
	}
	for _, fn := range tx.after {
		fn(ctx)
	}
}

// AfterCommit schedules fn to run once the transaction has committed.
func (tx *Tx) AfterCommit(fn func(context.Context)) { tx.after = append(tx.after, fn) }

// Store returns the Store the transaction belongs to.
func (tx *Tx) Store() *Store { return tx.store }

// hint records a channel to notify after commit (last message wins).
func (tx *Tx) hint(channel, msg string) { tx.hints[channel] = msg }

// NewID returns a type-prefixed lower-case ULID (th_…, em_…, s_…).
func NewID(prefix string) string {
	return prefix + "_" + strings.ToLower(ulid.Make().String())
}

// NewHex returns n random bytes as hex (v1-style ids).
func NewHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// MapErr turns pgx.ErrNoRows into ErrNotFound.
func MapErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// Affected returns ErrNotFound when the statement changed no row.
func Affected(tag pgconn.CommandTag, err error) error {
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Scanner is one row (pgx.Row or pgx.Rows).
type Scanner interface{ Scan(dest ...any) error }

// collect scans every row with scan.
func collect[T any](rows pgx.Rows, err error, scan func(Scanner) (T, error)) ([]T, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// nextLocalID allocates t<n>/q<n> inside a conversation (v1 id rule). The
// conversation row is locked so concurrent allocations serialise.
func nextLocalID(ctx context.Context, tx *Tx, table, prefix, conversationID string) (string, error) {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM at_conversations WHERE id = $1 FOR UPDATE`, conversationID); err != nil {
		return "", err
	}
	var n int
	q := fmt.Sprintf(`SELECT COALESCE(max(substring(id from 2)::int), 0) + 1 FROM %s
		WHERE conversation_id = $1 AND id ~ '^%s[0-9]+$'`, table, prefix)
	if err := tx.QueryRow(ctx, q, conversationID).Scan(&n); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%d", prefix, n), nil
}

// prefixed qualifies each column of a column list with alias.
func prefixed(alias, cols string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = alias + "." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

func itoa(n int) string { return strconv.Itoa(n) }

// Savepoint runs fn in a nested transaction: on error its writes, hints and
// after-commit callbacks are discarded and the outer transaction continues.
func (tx *Tx) Savepoint(ctx context.Context, fn func(tx *Tx) error) error {
	sp, err := tx.Tx.Begin(ctx)
	if err != nil {
		return err
	}
	sub := &Tx{Tx: sp, store: tx.store, hints: map[string]string{}}
	if err := fn(sub); err != nil {
		_ = sp.Rollback(ctx)
		return err
	}
	if err := sp.Commit(ctx); err != nil {
		return err
	}
	for k, v := range sub.hints {
		tx.hints[k] = v
	}
	tx.after = append(tx.after, sub.after...)
	return nil
}
