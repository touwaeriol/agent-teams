package config

import (
	"time"

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

// TestS3: all four required settings or none; path style and region default on.
func TestS3(t *testing.T) {
	base := map[string]string{"DATABASE_URL": "postgres://x", "AGENT_TEAMS_PUBLIC_URL": "https://a.example"}
	get := func(extra map[string]string) Getenv {
		return func(k string) string {
			if v, ok := extra[k]; ok {
				return v
			}
			return base[k]
		}
	}
	cfg, err := Load(get(nil))
	if err != nil || cfg.S3.Configured() {
		t.Fatalf("empty S3: %+v %v", cfg.S3, err)
	}
	full := map[string]string{"AGENT_TEAMS_S3_ENDPOINT": "https://s3.example/", "AGENT_TEAMS_S3_BUCKET": "b",
		"AGENT_TEAMS_S3_ACCESS_KEY": "k", "AGENT_TEAMS_S3_SECRET_KEY": "s"}
	cfg, err = Load(get(full))
	if err != nil || !cfg.S3.Configured() || !cfg.S3.PathStyle || cfg.S3.Region != "us-east-1" || cfg.S3.Endpoint != "https://s3.example" {
		t.Fatalf("full S3: %+v %v", cfg.S3, err)
	}
	if _, err := Load(get(map[string]string{"AGENT_TEAMS_S3_ENDPOINT": "https://s3.example"})); err == nil {
		t.Fatal("partial S3 accepted")
	}
	if cfg.AttachmentTTL != 24*time.Hour {
		t.Fatalf("default attachment TTL %v", cfg.AttachmentTTL)
	}
	if cfg, err := Load(get(map[string]string{"AGENT_TEAMS_ATTACHMENT_TTL": "36h"})); err != nil || cfg.AttachmentTTL != 36*time.Hour {
		t.Fatalf("TTL 36h: %v %v", cfg.AttachmentTTL, err)
	}
	for _, bad := range []string{"1d", "10s", "-1h"} {
		if _, err := Load(get(map[string]string{"AGENT_TEAMS_ATTACHMENT_TTL": bad})); err == nil {
			t.Fatalf("TTL %q accepted", bad)
		}
	}
	full["AGENT_TEAMS_S3_ENDPOINT"] = "s3.example"
	if _, err := Load(get(full)); err == nil {
		t.Fatal("endpoint without scheme accepted")
	}
	full["AGENT_TEAMS_S3_ENDPOINT"], full["AGENT_TEAMS_S3_PATH_STYLE"] = "http://s3:9000", "false"
	if cfg, err := Load(get(full)); err != nil || cfg.S3.PathStyle {
		t.Fatalf("path style off: %+v %v", cfg.S3, err)
	}
}

func TestFS(t *testing.T) {
	base := map[string]string{"DATABASE_URL": "postgres://u:p@pg:5432/agent_teams", "AGENT_TEAMS_PUBLIC_URL": "https://a.example",
		"AGENT_TEAMS_S3_ENDPOINT": "https://s3.example", "AGENT_TEAMS_S3_BUCKET": "b", "AGENT_TEAMS_S3_ACCESS_KEY": "k", "AGENT_TEAMS_S3_SECRET_KEY": "s"}
	get := func(extra map[string]string) Getenv {
		return func(k string) string {
			if v, ok := extra[k]; ok {
				return v
			}
			return base[k]
		}
	}
	cfg, err := Load(get(nil))
	if err != nil || cfg.FS.Configured() {
		t.Fatalf("off by default: %+v %v", cfg.FS, err)
	}
	full := map[string]string{"AGENT_TEAMS_FS_META_HOST": "db.example:15432", "AGENT_TEAMS_FS_BUCKET": "fs",
		"AGENT_TEAMS_FS_S3_ADMIN_ACCESS_KEY": "root", "AGENT_TEAMS_FS_S3_ADMIN_SECRET_KEY": "rootpw"}
	cfg, err = Load(get(full))
	if err != nil || !cfg.FS.Configured() || cfg.FS.AdminURL != base["DATABASE_URL"] || cfg.FS.S3Endpoint != "https://s3.example" ||
		cfg.FS.MetaSSLMode != "verify-full" || cfg.FS.CapacityGB != 20 || cfg.FS.JuiceFS != "juicefs" {
		t.Fatalf("defaults: %+v %v", cfg.FS, err)
	}
	if _, err := Load(get(map[string]string{"AGENT_TEAMS_FS_BUCKET": "fs"})); err == nil {
		t.Fatal("partial FS accepted")
	}
	bad := map[string]string{}
	for k, v := range full {
		bad[k] = v
	}
	bad["AGENT_TEAMS_FS_META_HOST"] = "db.example"
	if _, err := Load(get(bad)); err == nil {
		t.Fatal("meta host without port accepted")
	}
	bad["AGENT_TEAMS_FS_META_HOST"], bad["AGENT_TEAMS_FS_CLIENT_VERSION"] = "db.example:15432", "1.5.0"
	if _, err := Load(get(bad)); err == nil {
		t.Fatal("client version without checksums accepted")
	}
	bad["AGENT_TEAMS_FS_CLIENT_SHA256"] = "linux-amd64=ABC, linux-arm64=def"
	if cfg, err := Load(get(bad)); err != nil || cfg.FS.ClientSHA256["linux-amd64"] != "abc" || cfg.FS.ClientSHA256["linux-arm64"] != "def" {
		t.Fatalf("checksums: %+v %v", cfg.FS.ClientSHA256, err)
	}
	bad["AGENT_TEAMS_FS_CAPACITY_GB"] = "0"
	if cfg, err := Load(get(bad)); err != nil || cfg.FS.CapacityGB != 0 {
		t.Fatalf("capacity 0 (no limit): %+v %v", cfg.FS.CapacityGB, err)
	}
	bad["AGENT_TEAMS_FS_CAPACITY_GB"] = "-1"
	if _, err := Load(get(bad)); err == nil {
		t.Fatal("negative capacity accepted")
	}
}
