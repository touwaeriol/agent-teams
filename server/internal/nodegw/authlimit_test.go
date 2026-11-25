package nodegw

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestAuthLimiterWindow(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newAuthLimiter(3, time.Minute, func() time.Time { return now })
	for i := 0; i < 3; i++ {
		if b, _ := l.blocked("a"); b {
			t.Fatalf("blocked after %d failures", i)
		}
		l.fail("a")
	}
	b, wait := l.blocked("a")
	if !b || wait != time.Minute {
		t.Fatalf("blocked=%v wait=%v", b, wait)
	}
	if b, _ := l.blocked("b"); b {
		t.Fatal("other address blocked")
	}
	now = now.Add(30 * time.Second)
	if _, wait := l.blocked("a"); wait != 30*time.Second {
		t.Fatalf("wait %v", wait)
	}
	now = now.Add(30 * time.Second) // window over: a fresh window starts with the next failure
	if b, _ := l.blocked("a"); b {
		t.Fatal("still blocked after the window")
	}
	l.fail("a")
	if b, _ := l.blocked("a"); b {
		t.Fatal("blocked after one failure in a new window")
	}
}

func TestAuthLimiterDisabledAndBounded(t *testing.T) {
	off := newAuthLimiter(-1, time.Minute, nil)
	for i := 0; i < 100; i++ {
		off.fail("a")
	}
	if b, _ := off.blocked("a"); b || len(off.addrs) != 0 {
		t.Fatal("disabled limiter tracks addresses")
	}
	now := time.Unix(1000, 0)
	l := newAuthLimiter(1, time.Minute, func() time.Time { return now })
	for i := 0; i < authFailMaxAddrs+10; i++ {
		l.fail(strconv.Itoa(i))
	}
	if len(l.addrs) != authFailMaxAddrs {
		t.Fatalf("%d addresses tracked", len(l.addrs))
	}
	now = now.Add(time.Minute) // all expired: the next new address sweeps them
	l.fail("fresh")
	if len(l.addrs) != 1 {
		t.Fatalf("%d addresses after sweep", len(l.addrs))
	}
}

func TestWriteAuthError(t *testing.T) {
	for _, c := range []struct {
		err    error
		status int
	}{
		{&throttled{wait: 1500 * time.Millisecond}, http.StatusTooManyRequests},
		{ErrTokenRejected, http.StatusUnauthorized},
		{http.ErrHandlerTimeout, http.StatusServiceUnavailable},
	} {
		rec := httptest.NewRecorder()
		WriteAuthError(rec, c.err)
		if rec.Code != c.status {
			t.Fatalf("%v: status %d", c.err, rec.Code)
		}
		if c.status == http.StatusTooManyRequests && rec.Header().Get("Retry-After") != "2" {
			t.Fatalf("Retry-After %q", rec.Header().Get("Retry-After"))
		}
	}
}

func TestRemoteAddr(t *testing.T) {
	r := httptest.NewRequest("GET", "/nodes/connect", nil)
	r.RemoteAddr = "203.0.113.7:4242"
	if got := remoteAddr(r); got != "203.0.113.7" {
		t.Fatalf("addr %q", got)
	}
	r.RemoteAddr = "odd"
	if got := remoteAddr(r); got != "odd" {
		t.Fatalf("addr %q", got)
	}
}
