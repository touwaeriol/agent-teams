package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/auth"
	"github.com/touwaeriol/agent-teams/server/internal/config"
	"github.com/touwaeriol/agent-teams/server/internal/github"
	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/kv"
	"github.com/touwaeriol/agent-teams/server/internal/office"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/migrations"
)

// partitionsAhead is how many future months of event partitions are kept ready.
const partitionsAhead = 2

func serve(ctx context.Context, _ []string) error {
	cfg, err := config.Load(config.Environ())
	if err != nil {
		return err
	}
	log := config.NewLogger(os.Stdout, cfg.LogLevel)
	slog.SetDefault(log)
	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := checkMigrated(ctx, db); err != nil {
		return err
	}
	cache, err := openKV(cfg, log)
	if err != nil {
		return err
	}
	defer cache.Close()
	layouts, err := openOffice(ctx, cfg, db, log)
	if err != nil {
		return err
	}
	gh := github.NewClient()
	box := auth.NewSecretBox(cfg.SecretKeys...)
	tokens := github.NewTokens(gh, github.NewApps(db, box), cache, box)
	srv := httpapi.New(wire(httpapi.Deps{Config: cfg, Store: db, KV: cache, GitHub: gh, Tokens: tokens, Office: layouts,
		Log: log, Version: migrations.Latest}))
	go maintain(ctx, db, log)
	return listen(ctx, cfg, srv.Handler(), log)
}

func checkMigrated(ctx context.Context, db *store.Store) error {
	v, err := db.MigrationVersion(ctx)
	if err != nil {
		return fmt.Errorf("read migration version (run atctl migrate up first): %w", err)
	}
	if v < migrations.Latest() {
		return fmt.Errorf("database is at migration %d, this binary needs %d: run atctl migrate up", v, migrations.Latest())
	}
	return nil
}

func openKV(cfg config.Config, log *slog.Logger) (kv.KV, error) {
	if cfg.ValkeyURL == "" {
		log.Warn("VALKEY_URL not set: rate limits and token cache stay in this process")
		return kv.NewMemory(), nil
	}
	return kv.Dial(cfg.ValkeyURL)
}

func openOffice(ctx context.Context, cfg config.Config, db *store.Store, log *slog.Logger) (*office.Registry, error) {
	validator, err := office.NewValidator()
	if err != nil {
		return nil, err
	}
	reg := office.NewRegistry(cfg.OfficeDir, validator, store.OfficeDB{S: db}, log)
	if err := reg.Load(ctx); err != nil {
		return nil, err
	}
	if err := reg.Watch(ctx); err != nil {
		log.Warn("office layout watcher not running", "dir", cfg.OfficeDir, "err", err)
	}
	return reg, nil
}

// maintain runs the daily housekeeping: expired sessions and event partitions.
func maintain(ctx context.Context, db *store.Store, log *slog.Logger) {
	tick := time.NewTicker(24 * time.Hour)
	defer tick.Stop()
	for {
		if n, err := db.DeleteExpiredSessions(ctx); err != nil {
			log.Warn("session cleanup failed", "err", err)
		} else if n > 0 {
			log.Info("expired sessions removed", "count", n)
		}
		if err := db.EnsurePartitions(ctx, partitionsAhead); err != nil {
			log.Warn("event partitions", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func listen(ctx context.Context, cfg config.Config, h http.Handler, log *slog.Logger) error {
	// No WriteTimeout: SSE and long node streams stay open.
	server := &http.Server{Addr: cfg.Addr(), Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	errs := make(chan error, 1)
	go func() { errs <- server.ListenAndServe() }()
	log.Info("listening", "addr", cfg.Addr(), "public_url", cfg.PublicURL)
	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
