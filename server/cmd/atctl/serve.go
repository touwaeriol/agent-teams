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
	"github.com/touwaeriol/agent-teams/server/internal/jobs"
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
	gh := githubClient(cfg)
	box := auth.NewSecretBox(cfg.SecretKeys...)
	tokens := github.NewTokens(gh, github.NewApps(db, box), cache, box)
	deps, start, err := wire(httpapi.Deps{Config: cfg, Store: db, KV: cache, GitHub: gh, Tokens: tokens, Office: layouts,
		Log: log, Version: migrations.Latest})
	if err != nil {
		return err
	}
	srv := httpapi.New(deps)
	start(ctx)
	go srv.RunAuthz(ctx)
	return listen(ctx, cfg, srv.Handler(), log)
}

// githubClient is the GitHub REST client, pointed at GITHUB_API_URL / GITHUB_WEB_URL when set.
func githubClient(cfg config.Config) *github.Client {
	gh := github.NewClient()
	if cfg.GitHubAPI != "" {
		gh.BaseURL = cfg.GitHubAPI
	}
	if cfg.GitHubWeb != "" {
		gh.WebURL = cfg.GitHubWeb
	}
	return gh
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

// jobRunsKept is how long the recorded runs of the background jobs are kept.
const jobRunsKept = 30 * 24 * time.Hour

// maintenanceJob is the daily housekeeping: expired sessions, event partitions, SSH audit rows
// (365 days) and certificates (30 days after expiry), and old background job runs.
func maintenanceJob(db *store.Store, reg *jobs.Registry) jobs.Job {
	return jobs.Job{Name: "maintenance", Title: "每日维护", Every: 24 * time.Hour,
		Description: "清理过期登录会话、预建事件分区、清理一年前的 SSH 审计和过期证书、清理 30 天前的后台任务记录",
		Run: func(ctx context.Context, _ jobs.Trigger) (string, error) {
			sessions, serr := db.DeleteExpiredSessions(ctx)
			perr := db.EnsurePartitions(ctx, partitionsAhead)
			audit, certs, aerr := db.PruneSSH(ctx, time.Now())
			runs, rerr := reg.Prune(ctx, jobRunsKept)
			summary := ""
			if sessions+audit+certs+runs > 0 {
				summary = fmt.Sprintf("过期会话 %d，SSH 审计 %d，证书 %d，任务记录 %d", sessions, audit, certs, runs)
			}
			return summary, errors.Join(serr, perr, aerr, rerr)
		}}
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
