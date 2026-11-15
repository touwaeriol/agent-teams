// Package store is the PostgreSQL access layer (pgx v5, plain SQL). Every
// query that touches user data is scoped by user_id so another user's rows
// read as "not found".
package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

// ErrNotFound means no row matched (including rows owned by someone else).
var ErrNotFound = errors.New("not found")

// ErrConflict means a unique value is already taken.
var ErrConflict = errors.New("conflict")

// DB is what queries need; satisfied by *pgxpool.Pool and pgx.Tx.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Store wraps the connection pool.
type Store struct {
	Pool *pgxpool.Pool
	db   DB
}

// Open connects and pings.
func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &Store{Pool: pool, db: pool}, nil
}

// Close releases the pool.
func (s *Store) Close() { s.Pool.Close() }

// Ping checks the database is reachable.
func (s *Store) Ping(ctx context.Context) error { return s.Pool.Ping(ctx) }

// Tx runs fn in a transaction with a Store bound to it.
func (s *Store) Tx(ctx context.Context, fn func(tx *Store) error) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		return fn(&Store{Pool: s.Pool, db: tx})
	})
}

// NewID returns a type-prefixed ULID for v2 tables (ro_…, me_…).
func NewID(prefix string) string {
	return prefix + "_" + strings.ToLower(ulid.Make().String())
}

// NewHexID is v1's uuid4().hex style id (32 random hex chars), used for v1 tables.
func NewHexID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return fmt.Errorf("%w: %s", ErrConflict, pg.ConstraintName)
	}
	return err
}

func affected(tag pgconn.CommandTag, err error) error {
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Change is one column assignment of a partial update.
type Change struct {
	Col string
	Val any
}

// setClause renders "a = $3, b = $4" starting at placeholder start.
func setClause(changes []Change, start int) (string, []any) {
	parts := make([]string, len(changes))
	args := make([]any, len(changes))
	for i, c := range changes {
		parts[i] = fmt.Sprintf("%s = $%d", c.Col, start+i)
		args[i] = c.Val
	}
	return strings.Join(parts, ", "), args
}

// MigrationVersion is goose's current version (readyz compares it with the binary's).
func (s *Store) MigrationVersion(ctx context.Context) (int64, error) {
	var v int64
	err := s.db.QueryRow(ctx,
		`SELECT COALESCE(max(version_id), 0) FROM goose_db_version WHERE is_applied`).Scan(&v)
	return v, mapErr(err)
}

// EnsurePartitions creates this month's and the next months' event partitions.
func (s *Store) EnsurePartitions(ctx context.Context, ahead int) error {
	_, err := s.db.Exec(ctx, `SELECT at_ensure_event_partitions($1)`, ahead)
	return err
}
