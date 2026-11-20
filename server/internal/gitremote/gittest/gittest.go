// Package gittest provides throwaway upstream Git repositories for tests: bare repositories on
// disk served over smart HTTPS (git http-backend behind Basic auth, private CA).
package gittest

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/gitremote"
)

// Git runs git in dir with a fixed identity and fails the test on error.
func Git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Bare makes root/<name>.git with one commit (a.txt) on main and returns its path.
func Bare(t testing.TB, root, name string) string {
	t.Helper()
	bare := filepath.Join(root, name+".git")
	Git(t, root, "init", "-q", "--bare", "-b", "main", bare)
	Git(t, bare, "config", "http.receivepack", "true")
	work := t.TempDir()
	Git(t, work, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	Git(t, work, "add", ".")
	Git(t, work, "commit", "-qm", "first")
	Git(t, work, "push", "-q", bare, "main")
	return bare
}

// Server is root served over HTTPS.
type Server struct {
	URL    string // https://127.0.0.1:port
	Runner gitremote.Git
	TLS    *tls.Config
}

// HTTPS serves root's bare repositories at URL/<name>.git, requiring Basic user:token.
func HTTPS(t testing.TB, root, user, token string) Server {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	backend := &cgi.Handler{Path: gitPath, Args: []string{"http-backend"},
		Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=t"}}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != user || p != token {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "auth", http.StatusUnauthorized)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	conf := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return Server{URL: srv.URL, TLS: conf, Runner: gitremote.Git{TempDir: t.TempDir(), Env: []string{"GIT_SSL_CAINFO=" + ca}, TLS: conf}}
}
