package artifacts

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Prefix is where files are served: /files/<token>/<path>.
const Prefix = "/files/"

// cdns are the hosts published pages may load from (the design skill names the same ones).
const cdns = "https://cdn.jsdelivr.net https://unpkg.com https://cdnjs.cloudflare.com"

// Server serves stored files to whoever holds a signed URL.
type Server struct {
	// st is read per request: the shared folders may be switched on after the server is built.
	st     func() Store
	signer *Signer
	// origin is AGENT_TEAMS_ARTIFACT_ORIGIN (scheme://host) or "" to serve under the console's /files/.
	origin string
	// appOrigin may frame the files (frame-ancestors).
	appOrigin string
	log       *slog.Logger
}

// NewServer builds the file server. origin and appOrigin are scheme://host[:port] or "".
func NewServer(st func() Store, signer *Signer, origin, appOrigin string, log *slog.Logger) *Server {
	return &Server{st: st, signer: signer, origin: strings.TrimRight(origin, "/"), appOrigin: strings.TrimRight(appOrigin, "/"), log: log}
}

// Isolated reports whether files have their own origin (then the frame may keep allow-same-origin).
func (s *Server) Isolated() bool { return s.origin != "" }

// BaseURL is the signed directory URL of a version; append a file path to it.
func (s *Server) BaseURL(deliverableID string, version int) (string, int64) {
	token, exp := s.signer.Sign(deliverableID, version)
	return s.origin + Prefix + token + "/", exp.Unix()
}

// ServeHTTP answers GET/HEAD /files/<token>/<path>.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.origin != "" && !sameHost(s.origin, r.Host) {
		http.NotFound(w, r)
		return
	}
	id, version, path, ok := s.parse(r.URL.EscapedPath())
	if !ok {
		http.Error(w, "链接无效或已过期，请刷新页面", http.StatusForbidden)
		return
	}
	f, err := s.find(r.Context(), id, version, path)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.log.Warn("serve artifact", "deliverable_id", id, "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	s.headers(w.Header(), f)
	if r.Method == http.MethodGet {
		_, _ = w.Write(f.Data)
	}
}

// parse splits /files/<token>/<path> and verifies the token.
func (s *Server) parse(escaped string) (string, int, string, bool) {
	rest, ok := strings.CutPrefix(escaped, Prefix)
	if !ok {
		return "", 0, "", false
	}
	token, rawPath, _ := strings.Cut(rest, "/")
	id, version, ok := s.signer.Verify(token)
	if !ok {
		return "", 0, "", false
	}
	path, err := url.PathUnescape(rawPath)
	return id, version, path, err == nil
}

func (s *Server) find(ctx context.Context, id string, version int, path string) (ArtifactFile, error) {
	if path == "" {
		files, err := Files(ctx, s.st().DB, id, version)
		if err != nil {
			return ArtifactFile{}, err
		}
		paths := make([]string, len(files))
		for i, f := range files {
			paths[i] = f.Path
		}
		if path = Entry(paths); path == "" {
			return ArtifactFile{}, ErrNotFound
		}
	}
	if !ValidPath(path) {
		return ArtifactFile{}, ErrNotFound
	}
	st := s.st()
	return st.Load(ctx, st.DB, id, version, path)
}

func (s *Server) headers(h http.Header, f ArtifactFile) {
	h.Set("Content-Type", f.ContentType)
	h.Set("Content-Length", strconv.Itoa(len(f.Data)))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "private, max-age=3600")
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Content-Security-Policy", s.policy())
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
}

// policy: always a CSP sandbox; without a separate origin it must not allow-same-origin,
// otherwise a published page could read the console's cookies and call its API.
func (s *Server) policy() string {
	sandbox := "sandbox allow-scripts allow-forms allow-popups allow-modals allow-downloads"
	if s.Isolated() {
		sandbox += " allow-same-origin"
	}
	ancestors := "'self'"
	if s.appOrigin != "" {
		ancestors += " " + s.appOrigin
	}
	return strings.Join([]string{
		sandbox,
		"default-src 'self' data: blob: " + cdns,
		"script-src 'self' 'unsafe-inline' 'unsafe-eval' blob: " + cdns,
		"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com " + cdns,
		"font-src 'self' data: https://fonts.gstatic.com " + cdns,
		"img-src 'self' data: blob: https:",
		"connect-src 'self' " + cdns,
		"form-action 'none'",
		"frame-ancestors " + ancestors,
	}, "; ")
}

func sameHost(origin, host string) bool {
	u, err := url.Parse(origin)
	return err == nil && strings.EqualFold(u.Host, host)
}
