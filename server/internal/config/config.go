// Package config reads the control plane's settings from the environment.
package config

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strings"
)

// Config is everything atctl needs from its environment (architecture.md §12.2).
type Config struct {
	DatabaseURL  string
	ValkeyURL    string
	SecretKeys   []string // first one seals, all of them open
	PublicURL    string
	Bind         string
	Port         string
	OfficeDir    string
	WebDir       string
	NodeDistDir  string
	AdminEmails  map[string]bool
	LogLevel     slog.Level
	WebhookToken string // GITHUB_WEBHOOK_SECRET, optional override of the App's own secret
	GitHubAPI    string // GITHUB_API_URL: REST base; GitHub Enterprise, or a local emulator in tests
	GitHubWeb    string // GITHUB_WEB_URL: web base for OAuth and App pages
	// ArtifactOrigin (AGENT_TEAMS_ARTIFACT_ORIGIN) is a separate origin, e.g. https://files.example.net,
	// that serves published deliverable files; empty = /files/ on the console with a CSP sandbox.
	ArtifactOrigin string
}

// Getenv looks a variable up; os.Getenv in production, a map in tests.
type Getenv func(string) string

// Load reads the configuration. DATABASE_URL is required: v2 has no in-memory mode.
func Load(getenv Getenv) (Config, error) {
	cfg := Config{
		DatabaseURL:    getenv("DATABASE_URL"),
		ValkeyURL:      getenv("VALKEY_URL"),
		SecretKeys:     secretKeys(getenv),
		PublicURL:      strings.TrimRight(first(getenv("AGENT_TEAMS_PUBLIC_URL"), getenv("PUBLIC_URL")), "/"),
		Bind:           first(getenv("BIND"), "0.0.0.0"),
		Port:           first(getenv("PORT"), "3001"),
		OfficeDir:      first(getenv("OFFICE_DIR"), getenv("OFFICE_LAYOUT_DIR")),
		WebDir:         getenv("WEB_DIR"),
		NodeDistDir:    first(getenv("NODE_DIST_DIR"), "/srv/node-dist"),
		AdminEmails:    emails(getenv("AGENT_TEAMS_ADMIN_EMAILS")),
		LogLevel:       level(getenv("LOG_LEVEL")),
		WebhookToken:   getenv("GITHUB_WEBHOOK_SECRET"),
		GitHubAPI:      strings.TrimRight(getenv("GITHUB_API_URL"), "/"),
		GitHubWeb:      strings.TrimRight(getenv("GITHUB_WEB_URL"), "/"),
		ArtifactOrigin: strings.TrimRight(getenv("AGENT_TEAMS_ARTIFACT_ORIGIN"), "/"),
	}
	return cfg, cfg.check()
}

func (c Config) check() error {
	if c.DatabaseURL == "" {
		return errors.New("DATABASE_URL is required (v2 has no in-memory mode)")
	}
	if c.PublicURL == "" {
		return errors.New("set AGENT_TEAMS_PUBLIC_URL (or PUBLIC_URL), e.g. https://agent.example.com")
	}
	return c.checkArtifactOrigin()
}

// checkArtifactOrigin: a bare http(s) origin on another host than the web app, otherwise
// published pages would run on the app's origin and could use the session.
func (c Config) checkArtifactOrigin() error {
	if c.ArtifactOrigin == "" {
		return nil
	}
	a, err := url.Parse(c.ArtifactOrigin)
	if err != nil || (a.Scheme != "https" && a.Scheme != "http") || a.Host == "" || (a.Path != "" && a.Path != "/") {
		return errors.New("AGENT_TEAMS_ARTIFACT_ORIGIN must be an origin like https://files.example.net")
	}
	if p, err := url.Parse(c.PublicURL); err == nil && strings.EqualFold(p.Hostname(), a.Hostname()) {
		return errors.New("AGENT_TEAMS_ARTIFACT_ORIGIN must use a different host than the public URL")
	}
	return nil
}

// Addr is the listen address, BIND:PORT.
func (c Config) Addr() string { return net.JoinHostPort(c.Bind, c.Port) }

// LocalURL is how atctl healthcheck reaches its own server.
func (c Config) LocalURL() string { return fmt.Sprintf("http://127.0.0.1:%s", c.Port) }

// SecureCookies is true behind TLS.
func (c Config) SecureCookies() bool { return strings.HasPrefix(c.PublicURL, "https://") }

// NewLogger returns a JSON slog logger at the configured level.
func NewLogger(w io.Writer, lvl slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl}))
}

// Environ is the real process environment.
func Environ() Getenv { return os.Getenv }

func secretKeys(getenv Getenv) []string {
	var keys []string
	for _, k := range strings.Split(getenv("AGENT_TEAMS_SECRET_KEYS"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			keys = append(keys, k)
		}
	}
	if len(keys) > 0 {
		return keys
	}
	if k := getenv("AGENT_TEAMS_SECRET_KEY"); k != "" {
		return []string{k}
	}
	return nil
}

func emails(raw string) map[string]bool {
	out := map[string]bool{}
	for _, e := range strings.Split(raw, ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			out[e] = true
		}
	}
	return out
}

func level(raw string) slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(raw)); err != nil {
		return slog.LevelInfo
	}
	return l
}

func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
