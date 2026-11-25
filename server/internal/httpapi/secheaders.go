package httpapi

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// hstsValue is sent on HTTPS deployments only: a browser that saw it over plain HTTP ignores it,
// and a local http:// install must not pin itself to HTTPS.
const hstsValue = "max-age=31536000"

// securityHeaders sets baseline response headers on every route (audit #55 L9). They are set
// before the handler runs, so a handler with stricter needs (published files set their own
// Referrer-Policy and CSP) overrides them.
//
//   - Strict-Transport-Security only when the console is served over HTTPS, decided the same way
//     as the Secure cookie flag (config.SecureCookies: the public URL is https://), or when this
//     request itself arrived over TLS.
//   - X-Frame-Options SAMEORIGIN: the console may frame itself (office, previews) but no other
//     site may frame it. Published files under /files/ are skipped: they are framed by the console
//     from a separate file origin and carry their own CSP frame-ancestors.
//   - No script-src CSP here: the SPA would need a nonce/hash audit first.
func securityHeaders(secure bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		if !strings.HasPrefix(c.Request.URL.Path, filesPrefix) {
			h.Set("X-Frame-Options", "SAMEORIGIN")
		}
		if secure || c.Request.TLS != nil {
			h.Set("Strict-Transport-Security", hstsValue)
		}
		c.Next()
	}
}
