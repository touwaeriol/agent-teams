package httpapi

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

// filesPrefix is where published deliverable files are served (artifacts.Prefix).
const filesPrefix = "/files/"

// artifactHostOnly keeps the separate file origin (AGENT_TEAMS_ARTIFACT_ORIGIN) to files:
// on that host everything but /files/ is 404, so a published page, which runs there with
// allow-same-origin, finds no web app, login form or API on its own origin.
func artifactHostOnly(origin string) gin.HandlerFunc {
	host := ""
	if u, err := url.Parse(origin); err == nil && origin != "" {
		host = u.Host
	}
	return func(c *gin.Context) {
		r := c.Request
		if host != "" && strings.EqualFold(r.Host, host) && !strings.HasPrefix(r.URL.Path, filesPrefix) {
			http.NotFound(c.Writer, r)
			c.Abort()
			return
		}
		c.Next()
	}
}
