package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"regexp"
	"runtime/debug"
	"time"

	"github.com/gin-gonic/gin"
)

// RequestIDHeader carries a caller-chosen request id into the logs.
const RequestIDHeader = "X-Request-Id"

var safeRequestID = regexp.MustCompile(`^[A-Za-z0-9._:/+=-]{1,128}$`)

type requestIDKey struct{}

// requestID keeps a sane incoming X-Request-Id or makes one, for the request's logs.
func requestID(c *gin.Context) {
	id := c.GetHeader(RequestIDHeader)
	if !safeRequestID.MatchString(id) {
		id = newRequestID()
	}
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), requestIDKey{}, id))
	c.Next()
}

func newRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// RequestID returns the id requestID gave the request ("" outside a request).
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// realIP replaces RemoteAddr with the client address a reverse proxy (Caddy) reports, but only
// when the request comes from a loopback or private address, and only from the last
// X-Forwarded-For entry (the one the nearest proxy appended). X-Real-IP / True-Client-IP are
// ignored: Caddy passes them through from the client, so trusting them would let anyone pick
// an address and dodge the login rate limit.
func realIP(c *gin.Context) {
	if ip := forwardedFor(c.Request); ip != "" {
		c.Request.RemoteAddr = net.JoinHostPort(ip, "0")
	}
	c.Next()
}

// logRequests writes one line per request (not /healthz).
func (s *Server) logRequests(c *gin.Context) {
	start := time.Now()
	c.Next()
	r := c.Request
	if r.URL.Path == "/healthz" {
		return
	}
	s.log.Info("http", "request_id", RequestID(r.Context()), "method", r.Method, "path", r.URL.Path,
		"status", c.Writer.Status(), "ms", time.Since(start).Milliseconds(), "ip", clientIP(r))
}

// recoverer turns a handler panic into a logged 500 (unless the response already started).
func (s *Server) recoverer(c *gin.Context) {
	defer func() {
		v := recover()
		if v == nil || v == http.ErrAbortHandler {
			if v != nil {
				panic(v)
			}
			return
		}
		s.log.Error("panic", "request_id", RequestID(c.Request.Context()), "err", v, "stack", string(debug.Stack()))
		if !c.Writer.Written() {
			writeError(c.Writer, fail(http.StatusInternalServerError, "internal", "服务器出错了"))
		}
		c.Abort()
	}()
	c.Next()
}

// methodNotAllowed answers a known path called with another method (empty 405, as before).
func methodNotAllowed(c *gin.Context) {
	c.Status(http.StatusMethodNotAllowed)
	c.Writer.WriteHeaderNow()
}
