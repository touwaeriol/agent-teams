package httpapi

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gin-gonic/gin"

	"github.com/touwaeriol/agent-teams/server/internal/platform"
)

//go:embed install-node.sh
var installScript string

//go:embed node-docker.md
var nodeDockerGuide string

var (
	distFile    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
	distVersion = regexp.MustCompile(`^agent-teams-node-([0-9][0-9A-Za-z.+-]*)\.tgz$`)
	safeVersion = regexp.MustCompile(`^[0-9][0-9A-Za-z.+-]*$`)
	hexSHA      = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// reservedPrefixes never fall back to the web app.
var reservedPrefixes = []string{"api", "auth", "nodes", "node", "mcp", "office", "install-node.sh", "node-docker.md", "healthz", "readyz"}

// mountPublic adds the routes that need no session, and the web app for everything else.
func (s *Server) mountPublic(e *gin.Engine) {
	r := e.Group("", s.authorize)
	r.GET("/install-node.sh", gin.WrapF(s.installNode))
	r.GET("/node-docker.md", gin.WrapF(s.nodeDockerDoc))
	r.GET("/node/dist/:file", s.nodeDist)
	r.GET("/office/*path", s.serveOfficeFile)
	e.NoRoute(s.fallback)
}

// fallback serves the web app; gin presets 404 for unmatched paths, the files decide.
func (s *Server) fallback(c *gin.Context) {
	c.Status(http.StatusOK)
	s.static(c.Writer, c.Request)
}

// installNode renders install-node.sh with the console URL and the current daemon release.
func (s *Server) installNode(w http.ResponseWriter, r *http.Request) {
	script := renderInstallScript(s.nodeURL(r.Context()), s.currentRelease(r.Context()))
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = io.WriteString(w, script)
}

// nodeDockerDoc is the public guide to running a node in Docker / Compose, in Markdown so it can be
// read in a browser or handed to an AI assistant as is.
func (s *Server) nodeDockerDoc(w http.ResponseWriter, r *http.Request) {
	doc := strings.NewReplacer("__CONSOLE__", s.nodeURL(r.Context()), "__IMAGE__", s.cfg.NodeImage).Replace(nodeDockerGuide)
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = io.WriteString(w, doc)
}

// renderInstallScript fills in the console URL and the release (validated values only).
func renderInstallScript(console string, rel platform.NodeRelease) string {
	if !safeVersion.MatchString(rel.Version) || !hexSHA.MatchString(rel.SHA256) {
		rel = platform.NodeRelease{}
	}
	return strings.NewReplacer("__CONSOLE__", console, "__VERSION__", rel.Version, "__SHA256__", rel.SHA256).Replace(installScript)
}

// currentRelease is at_settings.node_release, else the newest tgz in NODE_DIST_DIR.
func (s *Server) currentRelease(ctx context.Context) platform.NodeRelease {
	var rel platform.NodeRelease
	if found, err := s.store.Setting(ctx, "node_release", &rel); err == nil && found &&
		safeVersion.MatchString(rel.Version) && hexSHA.MatchString(rel.SHA256) {
		return rel
	}
	return s.newestDist()
}

func (s *Server) newestDist() platform.NodeRelease {
	entries, err := os.ReadDir(s.cfg.NodeDistDir)
	if err != nil {
		return platform.NodeRelease{}
	}
	type cand struct {
		name, version string
		mod           time.Time
	}
	var all []cand
	for _, e := range entries {
		m := distVersion.FindStringSubmatch(e.Name())
		info, err := e.Info()
		if m == nil || err != nil || !safeVersion.MatchString(m[1]) {
			continue
		}
		all = append(all, cand{e.Name(), m[1], info.ModTime()})
	}
	if len(all) == 0 {
		return platform.NodeRelease{}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].mod.After(all[j].mod) })
	sum, err := fileSHA256(filepath.Join(s.cfg.NodeDistDir, all[0].name))
	if err != nil {
		return platform.NodeRelease{}
	}
	return platform.NodeRelease{Version: all[0].version, SHA256: sum}
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// nodeDist serves daemon release files (tgz, .sha256, .sig).
func (s *Server) nodeDist(c *gin.Context) {
	w, r, name := c.Writer, c.Request, c.Param("file")
	path := filepath.Join(s.cfg.NodeDistDir, name)
	info, err := os.Stat(path)
	if !distFile.MatchString(name) || err != nil || info.IsDir() {
		writeError(w, fail(http.StatusNotFound, "not_found", "没有这个发布包"))
		return
	}
	http.ServeFile(w, r, path)
}

// static serves the built web app with an index.html fallback for client routes.
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	if reserved(path) || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		writeError(w, fail(http.StatusNotFound, "not_found", "Not Found"))
		return
	}
	if s.cfg.WebDir == "" {
		writeError(w, fail(http.StatusNotFound, "not_found", "网页没有构建（WEB_DIR 未设置）"))
		return
	}
	if file := s.webFile(path); file != "" {
		if strings.HasPrefix(path, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.ServeFile(w, r, file)
		return
	}
	s.index(w, r)
}

func reserved(path string) bool {
	head := strings.SplitN(path, "/", 2)[0]
	for _, p := range reservedPrefixes {
		if head == p {
			return true
		}
	}
	return false
}

func (s *Server) webFile(path string) string {
	if path == "" {
		return ""
	}
	full := filepath.Join(s.cfg.WebDir, filepath.Clean("/"+path))
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		return ""
	}
	return full
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	raw, err := os.ReadFile(filepath.Join(s.cfg.WebDir, "index.html"))
	if err != nil {
		writeError(w, fail(http.StatusNotFound, "not_found", "网页没有构建"))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(raw)
}

type healthOutput struct {
	Body struct {
		OK      bool   `json:"ok"`
		Version string `json:"version"`
	}
}

type readyOutput struct {
	Status int
	Body   struct {
		OK     bool              `json:"ok"`
		Checks map[string]string `json:"checks"`
	}
}

func (s *Server) registerHealth(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "healthz", Method: "GET", Path: "/healthz", Tags: []string{"health"},
		Summary: "Process is alive"}, s.healthz)
	huma.Register(api, huma.Operation{OperationID: "readyz", Method: "GET", Path: "/readyz", Tags: []string{"health"},
		Summary: "PostgreSQL, Valkey and the migration version are OK"}, s.readyz)
}

func (s *Server) healthz(context.Context, *struct{}) (*healthOutput, error) {
	out := &healthOutput{}
	out.Body.OK, out.Body.Version = true, Version
	return out, nil
}

func (s *Server) readyz(ctx context.Context, _ *struct{}) (*readyOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	checks := map[string]string{"postgres": "ok", "valkey": "ok", "migrations": "ok"}
	if err := s.store.Ping(ctx); err != nil {
		checks["postgres"] = err.Error()
	}
	if err := s.kv.Ping(ctx); err != nil {
		checks["valkey"] = err.Error()
	}
	if v, err := s.store.MigrationVersion(ctx); err != nil || v < s.expect() {
		checks["migrations"] = "database is behind this binary; run atctl migrate up"
	}
	out := &readyOutput{Status: http.StatusOK}
	out.Body.OK, out.Body.Checks = true, checks
	for _, v := range checks {
		if v != "ok" {
			out.Status, out.Body.OK = http.StatusServiceUnavailable, false
		}
	}
	return out, nil
}
