package store

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
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

// Pool is the process-wide GORM handle. Its connections come from a pgx pool
// (the GORM postgres driver runs on pgx), so pool_max_conns etc. in the URL apply.
type Pool struct {
	gdb  *gorm.DB
	sql  *sql.DB
	pgxp *pgxpool.Pool
}

// OpenPool connects GORM to PostgreSQL and pings.
func OpenPool(ctx context.Context, url string) (*Pool, error) {
	pp, err := pgxpool.New(ctx, url)
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
