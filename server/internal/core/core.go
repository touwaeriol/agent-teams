// Package core is the data layer shared by the orchestration engines
// (orchestrator, scheduler, toolsvc, approvals, status, delivery): rows and
// views of threads, employees, sessions, tasks, questions, messages and mail,
// plus the project event stream. Writes go through Tx, which allocates the
// project event sequence inside the transaction (architecture.md §11.1) and
// publishes "something new" hints only after the commit.
package core

import (
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/blobstore"

	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/oklog/ulid/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// ErrNotFound means no row matched.
var ErrNotFound = errors.New("not found")

// ErrConflict means the row's state does not allow the change.
var ErrConflict = errors.New("conflict")

// DB is a GORM handle source: the Store's pool or a *Tx.
type DB = store.DB

// Publisher sends realtime hints (Valkey pub/sub plus local subscribers).
type Publisher interface {
	Publish(ctx context.Context, channel, message string)
}

// Store is the orchestration data layer.
type Store struct {
	Pool *store.Pool
	Pub  Publisher
	// Online reports whether a node has a live link (views show it).
	Online func(nodeID string) bool
	// Blobs holds attachment and shared-file bytes (#58); blobstore.None when not configured.
	Blobs blobstore.Store
	// AttachmentTTL is how long an attachment is kept after its last use (0 = DefaultAttachmentTTL).
	AttachmentTTL time.Duration
}

// New returns a Store; pub may be nil (no realtime hints).
func New(pool *store.Pool, pub Publisher) *Store {
	return &Store{Pool: pool, Pub: pub, Online: func(string) bool { return false }, Blobs: blobstore.None{}}
}

// Tx is one transaction plus what to do after it commits.
type Tx struct {
	g     *gorm.DB
	store *Store
	after []func(context.Context)
	hints map[string]string
	// settled are the thread conversations whose tasks settle after the commit (settleAfter).
	settled map[string]bool
}

// Gorm returns the transaction's handle bound to ctx (Tx satisfies DB).
func (tx *Tx) Gorm(ctx context.Context) *gorm.DB { return tx.g.WithContext(ctx) }

// InTx runs fn in a transaction; after a successful commit it publishes the
// event hints and runs AfterCommit callbacks in order.
func (s *Store) InTx(ctx context.Context, fn func(tx *Tx) error) error {
	var done *Tx
	err := s.Pool.Gorm(ctx).Transaction(func(g *gorm.DB) error {
		tx := &Tx{g: g, store: s, hints: map[string]string{}}
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

// settleAfter runs Store.SettleTasks for a thread conversation once the transaction has
// committed (once per conversation). A failure is logged: the scheduler sweep repairs what it missed.
func (tx *Tx) settleAfter(conversationID string) {
	if tx.settled[conversationID] {
		return
	}
	if tx.settled == nil {
		tx.settled = map[string]bool{}
	}
	tx.settled[conversationID] = true
	s := tx.store
	tx.AfterCommit(func(ctx context.Context) {
		if err := s.SettleTasks(context.WithoutCancel(ctx), conversationID); err != nil {
			slog.WarnContext(ctx, "settle tasks", "thread", conversationID, "err", err)
		}
	})
}

// SQLTx is the database/sql transaction under the GORM handle, for libraries that write in the
// same transaction (River's InsertTx).
func (tx *Tx) SQLTx() (*sql.Tx, error) {
	if t, ok := tx.g.Statement.ConnPool.(*sql.Tx); ok {
		return t, nil
	}
	return nil, errors.New("not in a database/sql transaction")
}

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

// MapErr turns "no row" into ErrNotFound.
func MapErr(err error) error {
	if store.IsNoRows(err) {
		return ErrNotFound
	}
	return err
}

// Affected returns ErrNotFound when the statement changed no row.
func Affected(r store.Result, err error) error {
	if err != nil {
		return err
	}
	if r.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Scanner is one row.
type Scanner = store.Scanner

// collect scans every row with scan.
func collect[T any](rows *store.Rows, err error, scan func(Scanner) (T, error)) ([]T, error) {
	return store.Collect(rows, err, scan)
}

// nextLocalID allocates t<n>/q<n> inside a conversation (v1 id rule); concurrent
// allocations serialise on an advisory lock.
func nextLocalID(ctx context.Context, tx *Tx, table, prefix, conversationID string) (string, error) {
	// A transaction-scoped advisory lock serializes allocators of one conversation without
	// locking its row: transactions that emitted an event (project row) and then write a message
	// (conversation row) would otherwise deadlock with an allocator about to emit one.
	if _, err := store.Exec(ctx, tx, `SELECT pg_advisory_xact_lock(hashtext($1))`, table+":"+conversationID); err != nil {
		return "", err
	}
	var n int
	q := fmt.Sprintf(`SELECT COALESCE(max(substring(id from 2)::int), 0) + 1 FROM %s
		WHERE conversation_id = $1 AND id ~ '^%s[0-9]+$'`, table, prefix)
	if err := store.QueryRow(ctx, tx, q, conversationID).Scan(&n); err != nil {
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
	var sub *Tx
	err := tx.g.WithContext(ctx).Transaction(func(g *gorm.DB) error {
		sub = &Tx{g: g, store: tx.store, hints: map[string]string{}}
		return fn(sub)
	})
	if err != nil {
		return err
	}
	for k, v := range sub.hints {
		tx.hints[k] = v
	}
	tx.after = append(tx.after, sub.after...)
	return nil
}

// take loads the one row g selects into a model (ErrNotFound when none).
func take[T any](g *gorm.DB) (T, error) {
	var v T
	err := g.Take(&v).Error
	return v, MapErr(err)
}

// find loads every row g selects into models.
func find[T any](g *gorm.DB) ([]T, error) {
	out := []T{}
	err := g.Find(&out).Error
	return out, err
}

// rawOne runs hand-written SQL ($n placeholders) and maps its one row onto a model by column name.
func rawOne[T any](ctx context.Context, q DB, sql string, args ...any) (T, error) {
	return take[T](store.Raw(ctx, q, sql, args...))
}

// rawAll runs hand-written SQL and maps every row onto a model by column name.
func rawAll[T any](ctx context.Context, q DB, sql string, args ...any) ([]T, error) {
	return find[T](store.Raw(ctx, q, sql, args...))
}

// ForUpdate locks the selected rows (SELECT … FOR UPDATE).
var ForUpdate = clause.Locking{Strength: clause.LockingStrengthUpdate}

// SkipLocked locks the selected rows and skips rows another transaction holds.
var SkipLocked = clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked}

// count counts the rows g selects.
func count(g *gorm.DB) (int, error) {
	var n int64
	err := g.Count(&n).Error
	return int(n), err
}

// stamped converts a column→value set for GORM Updates and bumps updated_at.
func stamped(set map[string]any) map[string]any {
	out := make(map[string]any, len(set)+1)
	for col, val := range set {
		out[col] = store.Param(val)
	}
	out["updated_at"] = gorm.Expr("now()")
	return out
}

// Changed returns ErrNotFound when a GORM write matched no row.
func Changed(res *gorm.DB) error {
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
