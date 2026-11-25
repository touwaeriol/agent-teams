package nodegw

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// Defaults of the per-address limit on rejected node tokens. A legitimate daemon whose token
// was revoked retries every 10 minutes (node/src/link/client.ts), far below the limit, so
// only guessing (or a flood) from one address trips it.
const (
	DefaultAuthFailLimit  = 20
	DefaultAuthFailWindow = 5 * time.Minute
	authFailMaxAddrs      = 10000 // memory bound; when full, expired entries are swept first
)

// authLimiter counts rejected node tokens per client address in a fixed window that starts
// with the first rejection. It is per process (each replica counts its own share), which is
// enough to make token guessing pointless and keep floods off the database.
type authLimiter struct {
	limit  int
	window time.Duration
	now    func() time.Time

	mu    sync.Mutex
	addrs map[string]*authFails
}

type authFails struct {
	n     int
	reset time.Time
}

func newAuthLimiter(limit int, window time.Duration, now func() time.Time) *authLimiter {
	if now == nil {
		now = time.Now
	}
	return &authLimiter{limit: limit, window: window, now: now, addrs: map[string]*authFails{}}
}

// blocked reports whether addr used up its rejections and, if so, how long until the window ends.
func (l *authLimiter) blocked(addr string) (bool, time.Duration) {
	if l.limit <= 0 {
		return false, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.addrs[addr]
	now := l.now()
	if f == nil || !now.Before(f.reset) {
		return false, 0
	}
	if f.n < l.limit {
		return false, 0
	}
	return true, f.reset.Sub(now)
}

// fail counts one rejected token from addr.
func (l *authLimiter) fail(addr string) {
	if l.limit <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	f := l.addrs[addr]
	if f == nil || !now.Before(f.reset) {
		if f == nil && len(l.addrs) >= authFailMaxAddrs {
			l.sweep(now)
			if len(l.addrs) >= authFailMaxAddrs {
				return // full of live entries: stop tracking new addresses rather than grow
			}
		}
		f = &authFails{reset: now.Add(l.window)}
		l.addrs[addr] = f
	}
	f.n++
}

func (l *authLimiter) sweep(now time.Time) {
	for a, f := range l.addrs {
		if !now.Before(f.reset) {
			delete(l.addrs, a)
		}
	}
}

// remoteAddr is the client address the limit is keyed on: the host part of RemoteAddr, which
// the HTTP stack's realIP middleware has already replaced with the last X-Forwarded-For entry
// when (and only when) the peer is the local reverse proxy, so clients cannot pick it.
func remoteAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
