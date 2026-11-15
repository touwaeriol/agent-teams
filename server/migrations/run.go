package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"regexp"
	"strconv"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx" for goose
	"github.com/pressly/goose/v3"
)

var versionPattern = regexp.MustCompile(`^(\d+)_.*\.sql$`)

// Latest is the highest migration version shipped in this binary.
func Latest() int64 {
	entries, _ := fs.ReadDir(FS, ".")
	var latest int64
	for _, e := range entries {
		if m := versionPattern.FindStringSubmatch(e.Name()); m != nil {
			if v, _ := strconv.ParseInt(m[1], 10, 64); v > latest {
				latest = v
			}
		}
	}
	return latest
}

// Open returns a database/sql handle for goose.
func Open(url string) (*sql.DB, error) { return sql.Open("pgx", url) }

func provider(db *sql.DB) (*goose.Provider, error) {
	return goose.NewProvider(goose.DialectPostgres, db, FS)
}

// Up applies every pending migration and returns the versions applied.
func Up(ctx context.Context, db *sql.DB) ([]int64, error) {
	p, err := provider(db)
	if err != nil {
		return nil, err
	}
	results, err := p.Up(ctx)
	applied := make([]int64, 0, len(results))
	for _, r := range results {
		applied = append(applied, r.Source.Version)
	}
	return applied, err
}

// Status lists each migration and whether it is applied.
func Status(ctx context.Context, db *sql.DB) ([]string, error) {
	p, err := provider(db)
	if err != nil {
		return nil, err
	}
	statuses, err := p.Status(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(statuses))
	for i, s := range statuses {
		out[i] = fmt.Sprintf("%05d  %-8s  %s", s.Source.Version, s.State, s.Source.Path)
	}
	return out, nil
}

// Version is the database's current migration version.
func Version(ctx context.Context, db *sql.DB) (int64, error) {
	p, err := provider(db)
	if err != nil {
		return 0, err
	}
	return p.GetDBVersion(ctx)
}
