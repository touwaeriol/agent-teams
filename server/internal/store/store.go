// Package store is the PostgreSQL access layer (GORM over the pgx driver).
// The goose SQL migrations own the schema; the GORM models here mirror those
// tables and are never auto-migrated. Every query that touches user data is
// scoped by user_id so another user's rows read as "not found".
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/oklog/ulid/v2"
	"gorm.io/gorm"
)

// ErrNotFound means no row matched (including rows owned by someone else).
var ErrNotFound = errors.New("not found")

// ErrConflict means a unique value is already taken.
var ErrConflict = errors.New("conflict")

// Store is the account/project data layer, bound to the pool or to a transaction.
type Store struct {
	Pool *Pool
	db   DB
}

// Open connects and pings.
func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := OpenPool(ctx, url)
	if err != nil {
		return nil, err
	}
	return &Store{Pool: pool, db: pool}, nil
}

// Close releases the pool.
func (s *Store) Close() { s.Pool.Close() }

// Ping checks the database is reachable.
func (s *Store) Ping(ctx context.Context) error { return s.Pool.Ping(ctx) }

// Tx runs fn in a transaction with a Store bound to it.
func (s *Store) Tx(ctx context.Context, fn func(tx *Store) error) error {
	return s.db.Gorm(ctx).Transaction(func(g *gorm.DB) error {
		return fn(&Store{Pool: s.Pool, db: Bound(g)})
	})
}

// g is the handle for one call.
func (s *Store) g(ctx context.Context) *gorm.DB { return s.db.Gorm(ctx) }

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

// IsNoRows reports whether err means "no row" from GORM, database/sql or pgx.
func IsNoRows(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows)
}

// IsUnique reports whether err is a unique-constraint violation.
func IsUnique(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// MapErr turns "no row" into ErrNotFound and unique violations into ErrConflict.
func MapErr(err error) error {
	if err == nil {
		return nil
	}
	if IsNoRows(err) {
		return ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return fmt.Errorf("%w: %s", ErrConflict, pg.ConstraintName)
	}
	return err
}

func mapErr(err error) error { return MapErr(err) }

// Changed returns ErrNotFound when a write matched no row (GORM result).
func Changed(res *gorm.DB) error {
	if res.Error != nil {
		return MapErr(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// Change is one column assignment of a partial update.
type Change struct {
	Col string
	Val any
}

// changeMap turns changes into a GORM Updates map (arrays keep their PostgreSQL type).
func changeMap(changes []Change) map[string]any {
	m := make(map[string]any, len(changes))
	for _, c := range changes {
		m[c.Col] = Param(c.Val)
	}
	return m
}

// MigrationVersion is goose's current version (readyz compares it with the binary's).
func (s *Store) MigrationVersion(ctx context.Context) (int64, error) {
	var v int64
	err := s.g(ctx).Table("goose_db_version").Where("is_applied").
		Select("COALESCE(max(version_id), 0)").Row().Scan(&v)
	return v, mapErr(err)
}

// EnsurePartitions creates this month's and the next months' event partitions.
func (s *Store) EnsurePartitions(ctx context.Context, ahead int) error {
	return s.g(ctx).Exec(`SELECT at_ensure_event_partitions(?)`, ahead).Error
}
