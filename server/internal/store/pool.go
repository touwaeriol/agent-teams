package store

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DB is a GORM handle source: the process pool or a transaction.
type DB interface {
	Gorm(ctx context.Context) *gorm.DB
}

// Detacher is a transaction that can hand out the pool it runs on, for writes that must survive
// its rollback (blobgc.Reserve).
type Detacher interface {
	Detached() DB
}

// Detached returns the handle outside q's transaction: the pool of a Detacher, else q itself.
func Detached(q DB) DB {
	if d, ok := q.(Detacher); ok {
		return d.Detached()
	}
	return q
}

// Pool is the process-wide GORM handle. Its connections come from a pgx pool
// (the GORM postgres driver runs on pgx), so pool_max_conns etc. in the URL apply.
type Pool struct {
	gdb  *gorm.DB
	sql  *sql.DB
	pgxp *pgxpool.Pool
}

// defaultMaxConns replaces pgxpool's default (max(4, CPUs)) unless the URL sets pool_max_conns.
// A few connections stay pinned by advisory-lock holders, and code inside a transaction may
// still take a second connection, so a pool of a handful can run dry and wait forever.
const defaultMaxConns = 32

// idleInTxTimeout makes PostgreSQL end a session left idle inside a transaction, so a stuck
// transaction releases its row locks instead of blocking every writer behind it.
const idleInTxTimeout = "120s"

// OpenPool connects GORM to PostgreSQL and pings.
func OpenPool(ctx context.Context, url string) (*Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	if !strings.Contains(url, "pool_max_conns") {
		cfg.MaxConns = defaultMaxConns
	}
	if _, ok := cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"]; !ok {
		cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = idleInTxTimeout
	}
	pp, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	sqlDB := stdlib.OpenDBFromPool(pp)
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), gormConfig())
	if err == nil {
		err = sqlDB.PingContext(ctx)
	}
	if err != nil {
		_ = sqlDB.Close()
		pp.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &Pool{gdb: gdb, sql: sqlDB, pgxp: pp}, nil
}

func gormConfig() *gorm.Config {
	return &gorm.Config{
		SkipDefaultTransaction: true,
		Logger: slowOnly{logger.New(log.New(os.Stderr, "gorm ", log.LstdFlags), logger.Config{
			SlowThreshold:        2 * time.Second,
			LogLevel:             logger.Warn,
			ParameterizedQueries: true,
		})},
		NowFunc: func() time.Time { return time.Now().UTC() },
	}
}

// slowOnly logs slow statements; failed ones are returned to (and reported by) the caller.
type slowOnly struct{ logger.Interface }

func (l slowOnly) LogMode(level logger.LogLevel) logger.Interface {
	return slowOnly{l.Interface.LogMode(level)}
}

func (l slowOnly) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if err == nil {
		l.Interface.Trace(ctx, begin, fc, nil)
	}
}

// Gorm returns the handle bound to ctx.
func (p *Pool) Gorm(ctx context.Context) *gorm.DB { return p.gdb.WithContext(ctx) }

// SQL is the database/sql handle (dedicated connections for advisory locks).
func (p *Pool) SQL() *sql.DB { return p.sql }

// MaxConns is the pool's connection limit.
func (p *Pool) MaxConns() int32 { return p.pgxp.Config().MaxConns }

// Close releases every connection.
func (p *Pool) Close() {
	_ = p.sql.Close()
	p.pgxp.Close()
}

// Ping checks the database is reachable.
func (p *Pool) Ping(ctx context.Context) error { return p.sql.PingContext(ctx) }

// bound is a DB tied to one open transaction.
type bound struct{ g *gorm.DB }

func (b bound) Gorm(ctx context.Context) *gorm.DB { return b.g.WithContext(ctx) }

// Bound wraps a transaction's *gorm.DB as a DB.
func Bound(g *gorm.DB) DB { return bound{g} }

// InTx runs fn in a transaction on q (a savepoint when q is already a transaction).
func InTx(ctx context.Context, q DB, fn func(tx DB) error, opts ...*sql.TxOptions) error {
	return q.Gorm(ctx).Transaction(func(g *gorm.DB) error { return fn(Bound(g)) }, opts...)
}
