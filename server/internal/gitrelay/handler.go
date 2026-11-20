package gitrelay

import (
	"fmt"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Route is where the relay is mounted; RouteRoot is its prefix (cgi Root).
const (
	Route     = "/git/*path"
	RouteRoot = "/git"
)

var relayPath = regexp.MustCompile(`^/git/(gr_[a-z0-9]+)\.git(/info/refs|/git-upload-pack|/git-receive-pack)$`)

// ServeHTTP is the smart HTTP endpoint nodes clone, fetch and push through (Basic auth: any
// user name, a relay token as the password). Only the smart protocol is served.
func (r *Relay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	m := relayPath.FindStringSubmatch(req.URL.Path)
	service := ""
	if m != nil {
		service = serviceOf(req, m[2])
	}
	if service == "" {
		http.NotFound(w, req)
		return
	}
	if !r.Enabled() {
		http.Error(w, ErrDisabled.Error(), http.StatusServiceUnavailable)
		return
	}
	claims, ok := r.authorize(w, req, m[1])
	if !ok {
		return
	}
	receive := service == "git-receive-pack"
	if receive && claims.Mode == ModeRead {
		http.Error(w, "this token may only fetch", http.StatusForbidden)
		return
	}
	repo, err := r.Store.GitRepo(req.Context(), claims.User, claims.Repo)
	if err != nil {
		http.NotFound(w, req)
		return
	}
	if receive {
		r.serveReceive(w, req, repo, claims, m[2] != "/info/refs")
		return
	}
	if _, err := r.Sync(req.Context(), repo, false); err != nil {
		r.upstreamFailed(w, repo, err)
		return
	}
	r.cgi(false, nil).ServeHTTP(w, req)
}

func serviceOf(req *http.Request, rest string) string {
	switch {
	case rest == "/info/refs" && req.Method == http.MethodGet:
		s := req.URL.Query().Get("service")
		if s == "git-upload-pack" || s == "git-receive-pack" {
			return s
		}
	case rest != "/info/refs" && req.Method == http.MethodPost:
		return rest[1:]
	}
	return ""
}

func (r *Relay) authorize(w http.ResponseWriter, req *http.Request, repoID string) (Claims, bool) {
	_, tok, ok := req.BasicAuth()
	if !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="agent-teams git"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return Claims{}, false
	}
	c, err := r.Verify(tok)
	if err != nil {
		// 401 again: git tells its credential helper to erase the token and the node asks anew.
		w.Header().Set("WWW-Authenticate", `Basic realm="agent-teams git"`)
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return Claims{}, false
	}
	if c.Repo != repoID {
		http.Error(w, "the token is for another repository", http.StatusForbidden)
		return Claims{}, false
	}
	return c, true
}

func (r *Relay) upstreamFailed(w http.ResponseWriter, repo store.GitRepo, err error) {
	r.log().Warn("git relay: upstream", "repo", repo.ID, "err", err)
	http.Error(w, "上游仓库暂时不可用："+err.Error(), http.StatusBadGateway)
}

// serveReceive holds the repository's lock for the whole push, so a sync cannot move the mirror
// under receive-pack; the pre-receive hook calls back to forward the push to the upstream.
func (r *Relay) serveReceive(w http.ResponseWriter, req *http.Request, repo store.GitRepo, c Claims, post bool) {
	defer r.lock(repo.ID)()
	dir, err := r.syncLocked(req.Context(), repo, !post)
	if err != nil {
		r.upstreamFailed(w, repo, err)
		return
	}
	if !post {
		r.cgi(true, nil).ServeHTTP(w, req)
		return
	}
	hooks, err := r.ensureHooks()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	hs, err := r.serveHook(req.Context(), repo, c, dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer hs.close()
	exe, _ := r.exe()
	r.cgi(true, []string{"core.hooksPath", hooks}, hookSockEnv+"="+hs.path, hookNonceEnv+"="+hs.nonce, hookExeEnv+"="+exe).ServeHTTP(w, req)
}

// cgi is git http-backend over the mirrors root, with receive-pack on or off.
func (r *Relay) cgi(receive bool, cfg []string, env ...string) *cgi.Handler {
	bin := r.Git.Bin
	if bin == "" {
		bin = "git"
	}
	if p, err := exec.LookPath(bin); err == nil {
		bin = p
	}
	kv := [][2]string{{"http.uploadpack", "true"}, {"http.receivepack", strconv.FormatBool(receive)},
		{"http.getanyfile", "false"}, {"uploadpack.allowAnySHA1InWant", "false"}, {"core.fsmonitor", "false"}}
	if len(cfg) == 2 {
		kv = append(kv, [2]string{cfg[0], cfg[1]})
	} else {
		kv = append(kv, [2]string{"core.hooksPath", "/dev/null"})
	}
	all := []string{"GIT_PROJECT_ROOT=" + r.Dir, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null", "HOME=" + os.TempDir(), "PATH=" + os.Getenv("PATH"), "LC_ALL=C",
		"GIT_CONFIG_COUNT=" + strconv.Itoa(len(kv))}
	for i, p := range kv {
		all = append(all, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, p[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, p[1]))
	}
	all = append(append(all, r.Git.Env...), env...)
	return &cgi.Handler{Path: bin, Args: []string{"http-backend"}, Root: RouteRoot, Dir: r.Dir, Env: all,
		Logger: nil, Stderr: logWriter{r}}
}

type logWriter struct{ r *Relay }

func (l logWriter) Write(p []byte) (int, error) {
	l.r.log().Warn("git http-backend", "stderr", string(p))
	return len(p), nil
}

func (r *Relay) exe() (string, error) {
	if r.Git.Exe != "" {
		return r.Git.Exe, nil
	}
	return os.Executable()
}

const hookScript = "#!/bin/sh\nexec \"$" + hookExeEnv + "\" git-hook pre-receive\n"

// ensureHooks writes the hooks directory receive-pack runs (core.hooksPath for relay pushes only).
func (r *Relay) ensureHooks() (string, error) {
	dir := filepath.Join(r.Dir, ".hooks")
	path := filepath.Join(dir, "pre-receive")
	if b, err := os.ReadFile(path); err == nil && string(b) == hookScript {
		return dir, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(hookScript), 0o700); err != nil {
		return "", err
	}
	return dir, os.Rename(tmp, path)
}
