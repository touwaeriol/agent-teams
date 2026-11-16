// Package testdb gives integration tests a fresh PostgreSQL database on the
// server named by TEST_DATABASE_URL (tests skip when it is unset).
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/touwaeriol/agent-teams/server/migrations"
)

// URL creates an empty database and returns its URL; it is dropped after the test.
func URL(t *testing.T) string {
	t.Helper()
	admin := os.Getenv("TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	conn, err := migrations.Open(admin)
	if err != nil {
		t.Fatalf("connect test postgres: %v", err)
	}
	name := "t_" + random()
	if _, err := conn.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_ = conn.Close()
	})
	u, _ := url.Parse(admin)
	u.Path = "/" + name
	return u.String()
}

// Exec runs a SQL script (several statements) on its own connection.
func Exec(t *testing.T, dbURL, script string) {
	t.Helper()
	ctx := context.Background()
	conn, err := migrations.Open(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, script); err != nil {
		t.Fatalf("exec script: %v", err)
	}
}

// Migrated returns a fresh database with every migration applied.
func Migrated(t *testing.T) string {
	t.Helper()
	dbURL := URL(t)
	db, err := migrations.Open(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := migrations.Up(context.Background(), db); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	return dbURL
}

func random() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
