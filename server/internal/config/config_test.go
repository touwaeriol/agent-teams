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

func TestLoadGitHubBaseURLs(t *testing.T) {
	cfg, err := Load(env(map[string]string{"DATABASE_URL": "postgres://x", "PUBLIC_URL": "http://a",
		"GITHUB_API_URL": "http://localgh:8787/", "GITHUB_WEB_URL": "http://localgh:8787"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHubAPI != "http://localgh:8787" || cfg.GitHubWeb != "http://localgh:8787" {
		t.Fatalf("github urls %q %q", cfg.GitHubAPI, cfg.GitHubWeb)
	}
}

func TestArtifactOrigin(t *testing.T) {
	base := map[string]string{"DATABASE_URL": "postgres://x", "PUBLIC_URL": "https://agent.example.com"}
	with := func(v string) map[string]string {
		m := map[string]string{"AGENT_TEAMS_ARTIFACT_ORIGIN": v}
		for k, x := range base {
			m[k] = x
		}
		return m
	}
	cfg, err := Load(env(with("https://files.example.net/")))
	if err != nil || cfg.ArtifactOrigin != "https://files.example.net" {
		t.Fatalf("origin %q %v", cfg.ArtifactOrigin, err)
	}
	for _, bad := range []string{"files.example.net", "https://agent.example.com", "https://files.example.net/x", "ftp://f"} {
		if _, err := Load(env(with(bad))); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if cfg, err := Load(env(base)); err != nil || cfg.ArtifactOrigin != "" {
		t.Fatalf("unset %q %v", cfg.ArtifactOrigin, err)
	}
}
