// Package config reads the control plane's settings from the environment.
package config

import (
	"time"

	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strconv"
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
	// GitMirrorDir (GIT_MIRROR_DIR) holds the relay's bare mirrors of standalone Git repositories
	// (git-repos.md §5); empty disables the relay.
	GitMirrorDir string
	// NodeImage (AGENT_TEAMS_NODE_IMAGE) is the node container image the "add node" dialog puts in its
	// docker run / compose snippets.
	NodeImage string
	// S3 is the object store of attachments and shared files (AGENT_TEAMS_S3_*, #58); all empty =
	// not configured: new uploads answer 503, bytes still in the database stay readable.
	S3 S3
	// AttachmentTTL (AGENT_TEAMS_ATTACHMENT_TTL, a Go duration such as 24h) is how long an
	// attachment is kept after its last use, and how long daemons keep the downloaded copy.
	AttachmentTTL time.Duration
	// FS is the user shared folder (AGENT_TEAMS_FS_*, juicefs-shared-storage design); MetaHost and
	// Bucket empty = off.
	FS FS
}

// FS is the AGENT_TEAMS_FS_* settings of the JuiceFS shared folder.
type FS struct {
	MetaHost       string // AGENT_TEAMS_FS_META_HOST: host:port of PostgreSQL as nodes reach it (TLS)
	MetaSSLMode    string // AGENT_TEAMS_FS_META_SSLMODE, default verify-full
	AdminURL       string // AGENT_TEAMS_FS_META_ADMIN_URL: creates roles and schemas; default DATABASE_URL
	S3Endpoint     string // AGENT_TEAMS_FS_S3_ENDPOINT, default AGENT_TEAMS_S3_ENDPOINT
	Bucket         string // AGENT_TEAMS_FS_BUCKET: bucket all volumes share (one prefix each)
	AdminAccessKey string // AGENT_TEAMS_FS_S3_ADMIN_ACCESS_KEY: object store admin (users and policies)
	AdminSecretKey string // AGENT_TEAMS_FS_S3_ADMIN_SECRET_KEY
	CapacityGB     int    // AGENT_TEAMS_FS_CAPACITY_GB, default 20
	JuiceFS        string // AGENT_TEAMS_FS_JUICEFS: the juicefs binary, default juicefs
	ClientVersion  string // AGENT_TEAMS_FS_CLIENT_VERSION: client nodes run, default the built-in one
	ClientBase     string // AGENT_TEAMS_FS_CLIENT_BASE: download base, default the GitHub releases
	// ClientSHA256 (AGENT_TEAMS_FS_CLIENT_SHA256: platform=sha,platform=sha) replaces the built-in
	// checksums; required with another ClientVersion.
	ClientSHA256 map[string]string
}

// Configured tells whether the shared folder is switched on.
func (f FS) Configured() bool { return f.MetaHost != "" || f.Bucket != "" }

func (f FS) check() error {
	if !f.Configured() {
		return nil
	}
	if f.MetaHost == "" || f.Bucket == "" || f.S3Endpoint == "" || f.AdminAccessKey == "" || f.AdminSecretKey == "" {
		return errors.New("shared folder: set AGENT_TEAMS_FS_META_HOST, AGENT_TEAMS_FS_BUCKET, AGENT_TEAMS_FS_S3_ADMIN_ACCESS_KEY and AGENT_TEAMS_FS_S3_ADMIN_SECRET_KEY (and an S3 endpoint), or none")
	}
	if _, _, err := net.SplitHostPort(f.MetaHost); err != nil {
		return errors.New("AGENT_TEAMS_FS_META_HOST must be host:port")
	}
	if f.ClientVersion != "" && len(f.ClientSHA256) == 0 {
		return errors.New("AGENT_TEAMS_FS_CLIENT_VERSION needs AGENT_TEAMS_FS_CLIENT_SHA256 (platform=sha256,...)")
	}
	return nil
}

func fsConfig(getenv Getenv, databaseURL, s3Endpoint string) (FS, error) {
	f := FS{
		MetaHost:       strings.TrimSpace(getenv("AGENT_TEAMS_FS_META_HOST")),
		MetaSSLMode:    first(getenv("AGENT_TEAMS_FS_META_SSLMODE"), "verify-full"),
		AdminURL:       first(getenv("AGENT_TEAMS_FS_META_ADMIN_URL"), databaseURL),
		S3Endpoint:     strings.TrimRight(first(getenv("AGENT_TEAMS_FS_S3_ENDPOINT"), s3Endpoint), "/"),
		Bucket:         getenv("AGENT_TEAMS_FS_BUCKET"),
		AdminAccessKey: getenv("AGENT_TEAMS_FS_S3_ADMIN_ACCESS_KEY"),
		AdminSecretKey: getenv("AGENT_TEAMS_FS_S3_ADMIN_SECRET_KEY"),
		CapacityGB:     20,
		JuiceFS:        first(getenv("AGENT_TEAMS_FS_JUICEFS"), "juicefs"),
		ClientVersion:  getenv("AGENT_TEAMS_FS_CLIENT_VERSION"),
		ClientBase:     strings.TrimRight(getenv("AGENT_TEAMS_FS_CLIENT_BASE"), "/"),
	}
	if v := strings.TrimSpace(getenv("AGENT_TEAMS_FS_CAPACITY_GB")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return f, errors.New("AGENT_TEAMS_FS_CAPACITY_GB must be a positive number of GiB")
		}
		f.CapacityGB = n
	}
	for _, kv := range strings.Split(getenv("AGENT_TEAMS_FS_CLIENT_SHA256"), ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if !ok {
			continue
		}
		if f.ClientSHA256 == nil {
			f.ClientSHA256 = map[string]string{}
		}
		f.ClientSHA256[strings.TrimSpace(k)] = strings.ToLower(strings.TrimSpace(v))
	}
	return f, nil
}

// S3 is the AGENT_TEAMS_S3_* settings.
type S3 struct {
	Endpoint       string // AGENT_TEAMS_S3_ENDPOINT, e.g. https://rustfs.prophey.ai
	PublicEndpoint string // AGENT_TEAMS_S3_PUBLIC_ENDPOINT: what presigned URLs point at; default Endpoint
	Bucket         string // AGENT_TEAMS_S3_BUCKET
	AccessKey      string // AGENT_TEAMS_S3_ACCESS_KEY
	SecretKey      string // AGENT_TEAMS_S3_SECRET_KEY
	Region         string // AGENT_TEAMS_S3_REGION, default us-east-1
	PathStyle      bool   // AGENT_TEAMS_S3_PATH_STYLE, default true
}

// Configured is true when the four required settings are there.
func (s S3) Configured() bool {
	return s.Endpoint != "" && s.Bucket != "" && s.AccessKey != "" && s.SecretKey != ""
}

func (s S3) check() error {
	set := 0
	for _, v := range []string{s.Endpoint, s.Bucket, s.AccessKey, s.SecretKey} {
		if v != "" {
			set++
		}
	}
	if set != 0 && set != 4 {
		return errors.New("set all of AGENT_TEAMS_S3_ENDPOINT, AGENT_TEAMS_S3_BUCKET, AGENT_TEAMS_S3_ACCESS_KEY and AGENT_TEAMS_S3_SECRET_KEY, or none")
	}
	for _, e := range []string{s.Endpoint, s.PublicEndpoint} {
		if e == "" {
			continue
		}
		if u, err := url.Parse(e); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return errors.New("AGENT_TEAMS_S3_ENDPOINT / AGENT_TEAMS_S3_PUBLIC_ENDPOINT must be http(s)://host")
		}
	}
	return nil
}

// DefaultAttachmentTTL is AGENT_TEAMS_ATTACHMENT_TTL's default (user decision #58).
const DefaultAttachmentTTL = 24 * time.Hour

func attachmentTTL(v string) (time.Duration, error) {
	if strings.TrimSpace(v) == "" {
		return DefaultAttachmentTTL, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil || d < time.Minute {
		return 0, errors.New("AGENT_TEAMS_ATTACHMENT_TTL must be a duration of at least 1m, e.g. 24h")
	}
	return d, nil
}

// LoadS3 reads and checks only the AGENT_TEAMS_S3_* settings (atctl storage).
func LoadS3(getenv Getenv) (S3, error) {
	s := s3Config(getenv)
	return s, s.check()
}

func s3Config(getenv Getenv) S3 {
	return S3{
		Endpoint:       strings.TrimRight(getenv("AGENT_TEAMS_S3_ENDPOINT"), "/"),
		PublicEndpoint: strings.TrimRight(getenv("AGENT_TEAMS_S3_PUBLIC_ENDPOINT"), "/"),
		Bucket:         getenv("AGENT_TEAMS_S3_BUCKET"),
		AccessKey:      getenv("AGENT_TEAMS_S3_ACCESS_KEY"),
		SecretKey:      getenv("AGENT_TEAMS_S3_SECRET_KEY"),
		Region:         first(getenv("AGENT_TEAMS_S3_REGION"), "us-east-1"),
		PathStyle:      !strings.EqualFold(strings.TrimSpace(getenv("AGENT_TEAMS_S3_PATH_STYLE")), "false"),
	}
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
		GitMirrorDir:   getenv("GIT_MIRROR_DIR"),
		NodeImage:      first(getenv("AGENT_TEAMS_NODE_IMAGE"), "ghcr.io/touwaeriol/agent-teams-node:latest"),
		S3:             s3Config(getenv),
	}
	ttl, err := attachmentTTL(getenv("AGENT_TEAMS_ATTACHMENT_TTL"))
	if err != nil {
		return cfg, err
	}
	cfg.AttachmentTTL = ttl
	if cfg.FS, err = fsConfig(getenv, cfg.DatabaseURL, cfg.S3.Endpoint); err != nil {
		return cfg, err
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
	if err := c.S3.check(); err != nil {
		return err
	}
	if err := c.FS.check(); err != nil {
		return err
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
