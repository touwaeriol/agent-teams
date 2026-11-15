package config

import (
	"log/slog"
	"testing"
)

func env(m map[string]string) Getenv { return func(k string) string { return m[k] } }

func TestLoadRequiresDatabaseAndPublicURL(t *testing.T) {
	if _, err := Load(env(nil)); err == nil {
		t.Fatal("want error without DATABASE_URL")
	}
	if _, err := Load(env(map[string]string{"DATABASE_URL": "postgres://x"})); err == nil {
		t.Fatal("want error without PUBLIC_URL")
	}
}

func TestLoadDefaultsAndAliases(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"DATABASE_URL":             "postgres://x",
		"AGENT_TEAMS_PUBLIC_URL":   "https://a.example/",
		"AGENT_TEAMS_SECRET_KEY":   "s1",
		"OFFICE_LAYOUT_DIR":        "/srv/office",
		"AGENT_TEAMS_ADMIN_EMAILS": " A@x.io, b@y.io ",
		"LOG_LEVEL":                "debug",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicURL != "https://a.example" || !cfg.SecureCookies() {
		t.Fatalf("public url %q", cfg.PublicURL)
	}
	if cfg.Addr() != "0.0.0.0:3001" || cfg.OfficeDir != "/srv/office" {
		t.Fatalf("addr %q office %q", cfg.Addr(), cfg.OfficeDir)
	}
	if len(cfg.SecretKeys) != 1 || !cfg.AdminEmails["a@x.io"] || cfg.LogLevel != slog.LevelDebug {
		t.Fatalf("unexpected %+v", cfg)
	}
}

func TestSecretKeysListWins(t *testing.T) {
	keys := secretKeys(env(map[string]string{"AGENT_TEAMS_SECRET_KEYS": "new, old", "AGENT_TEAMS_SECRET_KEY": "x"}))
	if len(keys) != 2 || keys[0] != "new" || keys[1] != "old" {
		t.Fatalf("keys %v", keys)
	}
}
