package httpapi

import (
	"net/http"
	"net/url"
	"strings"
)

// filesPrefix is where published deliverable files are served (artifacts.Prefix).
const filesPrefix = "/files/"

// artifactHostOnly keeps the separate file origin (AGENT_TEAMS_ARTIFACT_ORIGIN) to files:
// on that host everything but /files/ is 404, so a published page, which runs there with
// allow-same-origin, finds no web app, login form or API on its own origin.
func artifactHostOnly(origin string) func(http.Handler) http.Handler {
	host := ""
	if u, err := url.Parse(origin); err == nil && origin != "" {
		host = u.Host
	}
	return func(next http.Handler) http.Handler {
		if host == "" {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.EqualFold(r.Host, host) && !strings.HasPrefix(r.URL.Path, filesPrefix) {
				http.NotFound(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
