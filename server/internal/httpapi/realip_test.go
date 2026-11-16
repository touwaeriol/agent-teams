package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRealIP(t *testing.T) {
	cases := []struct {
		name, remote string
		headers      map[string]string
		want         string
	}{
		{"proxy sets xff", "172.18.0.5:4000", map[string]string{"X-Forwarded-For": "203.0.113.9"}, "203.0.113.9"},
		{"spoofed first hop", "127.0.0.1:4000", map[string]string{"X-Forwarded-For": "1.1.1.1, 203.0.113.9"}, "203.0.113.9"},
		{"true-client-ip ignored", "172.18.0.5:4000", map[string]string{"True-Client-IP": "9.9.9.9", "X-Real-IP": "8.8.8.8", "X-Forwarded-For": "203.0.113.9"}, "203.0.113.9"},
		{"public peer not trusted", "198.51.100.7:4000", map[string]string{"X-Forwarded-For": "1.2.3.4"}, "198.51.100.7"},
		{"garbage xff", "127.0.0.1:4000", map[string]string{"X-Forwarded-For": "nonsense"}, "127.0.0.1"},
		{"no header", "10.0.0.3:4000", nil, "10.0.0.3"},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = c.remote
		for k, v := range c.headers {
			r.Header.Set(k, v)
		}
		var got string
		e := newEngine()
		e.Use(realIP)
		e.GET("/", func(c *gin.Context) { got = clientIP(c.Request) })
		e.ServeHTTP(httptest.NewRecorder(), r)
		if got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}
