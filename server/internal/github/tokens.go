package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/auth"
	"github.com/touwaeriol/agent-teams/server/internal/kv"
)

// RefreshMargin: a cached token with less time left than this is replaced (v1: 600 s).
const RefreshMargin = 10 * time.Minute

// ForceRefreshEvery: a forced re-mint (RefreshInstallationToken) of one cache key happens at most
// this often per process; within it the token minted last is handed out again.
const ForceRefreshEvery = time.Minute

// Tokens mints installation tokens and caches them in memory and, sealed, in
// Valkey (shared by replicas). Cache keys include the repositories and the
// permission set, so narrowed tokens never leak into wider uses.
type Tokens struct {
	client *Client
	apps   *Apps
	kv     kv.KV
	box    *auth.SecretBox
	now    func() time.Time

	mu     sync.Mutex
	mem    map[string]Token
	forced map[string]time.Time // cache key -> last forced re-mint
}

// NewTokens builds the token cache.
func NewTokens(client *Client, apps *Apps, store kv.KV, box *auth.SecretBox) *Tokens {
	return &Tokens{client: client, apps: apps, kv: store, box: box, now: time.Now, mem: map[string]Token{}, forced: map[string]time.Time{}}
}

// Installation returns a token for the installation, narrowed by req.
func (t *Tokens) Installation(ctx context.Context, installationID int64, req TokenRequest) (string, error) {
	tok, err := t.InstallationToken(ctx, installationID, req)
	return tok.Value, err
}

// InstallationToken is Installation with the token's real expiry: a cached token may have
// anywhere between RefreshMargin and one hour left (cred.response reports it, §10.3 #2).
func (t *Tokens) InstallationToken(ctx context.Context, installationID int64, req TokenRequest) (Token, error) {
	key, err := t.cacheKey(ctx, installationID, req)
	if err != nil {
		return Token{}, err
	}
	if tok, ok := t.cached(ctx, key); ok {
		return tok, nil
	}
	tok, err := t.mint(ctx, installationID, req)
	if err != nil {
		return Token{}, err
	}
	t.remember(ctx, key, tok)
	return tok, nil
}

// RefreshInstallationToken is InstallationToken without the cache: a caller saw the cached
// token rejected (git credential erase: revoked on GitHub, or the installation's permissions
// changed). The new token replaces the cached one. Forced re-mints of one cache key are limited
// to one per ForceRefreshEvery, so a caller looping on erase cannot hammer GitHub.
func (t *Tokens) RefreshInstallationToken(ctx context.Context, installationID int64, req TokenRequest) (Token, error) {
	key, err := t.cacheKey(ctx, installationID, req)
	if err != nil {
		return Token{}, err
	}
	if !t.allowForce(key) {
		if tok, ok := t.cached(ctx, key); ok {
			return tok, nil
		}
	}
	tok, err := t.mint(ctx, installationID, req)
	if err != nil {
		return Token{}, err
	}
	t.remember(ctx, key, tok)
	return tok, nil
}

// allowForce records a forced re-mint of key unless one happened within ForceRefreshEvery.
func (t *Tokens) allowForce(key string) bool {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if last, ok := t.forced[key]; ok && now.Sub(last) < ForceRefreshEvery {
		return false
	}
	for k, at := range t.forced {
		if now.Sub(at) >= ForceRefreshEvery {
			delete(t.forced, k)
		}
	}
	t.forced[key] = now
	return true
}

func (t *Tokens) mint(ctx context.Context, installationID int64, req TokenRequest) (Token, error) {
	app, err := t.apps.Load(ctx)
	if err != nil {
		return Token{}, err
	}
	if app == nil {
		return Token{}, ErrNoApp
	}
	jwt, err := AppJWT(app.AppID, app.PEM, t.now())
	if err != nil {
		return Token{}, err
	}
	tok, err := t.client.InstallationToken(ctx, jwt, installationID, req)
	if err == nil || len(req.Permissions) == 0 || !notGranted(err) {
		return tok, err
	}
	// The installation lacks some requested permission (an App created before the
	// platform asked for it, not re-approved yet): ask for what it does grant.
	inst, ierr := t.client.Installation(ctx, jwt, installationID)
	if ierr != nil {
		return Token{}, err
	}
	narrowed := grantedSubset(req.Permissions, inst.Permissions)
	slog.Warn("installation lacks requested permissions; narrowed the token",
		"installation", installationID, "requested", req.Permissions, "granted", inst.Permissions, "using", narrowed)
	if len(narrowed) == 0 {
		return Token{}, err
	}
	return t.client.InstallationToken(ctx, jwt, installationID, TokenRequest{Repositories: req.Repositories, Permissions: narrowed})
}

func notGranted(err error) bool {
	var ge *Error
	return errors.As(err, &ge) && ge.Status == 422 && strings.Contains(ge.Message, "not granted")
}

// grantedSubset keeps each requested permission at the level the installation grants
// (write → read when only read is granted) and drops the ones it lacks.
func grantedSubset(want, granted map[string]string) map[string]string {
	out := map[string]string{}
	for k, level := range want {
		g := granted[k]
		switch {
		case g == "":
			continue
		case level == "write" && g != "write" && g != "admin":
			out[k] = "read"
		default:
			out[k] = level
		}
	}
	return out
}

func (t *Tokens) fresh(tok Token) bool { return tok.ExpiresAt.Sub(t.now()) > RefreshMargin }

func (t *Tokens) cached(ctx context.Context, key string) (Token, bool) {
	t.mu.Lock()
	tok, ok := t.mem[key]
	t.mu.Unlock()
	if ok && t.fresh(tok) {
		return tok, true
	}
	tok, ok = t.shared(ctx, key)
	if ok && t.fresh(tok) {
		t.mu.Lock()
		t.mem[key] = tok
		t.mu.Unlock()
		return tok, true
	}
	return Token{}, false
}

func (t *Tokens) shared(ctx context.Context, key string) (Token, bool) {
	sealed, ok, err := t.kv.Get(ctx, key)
	if err != nil || !ok {
		return Token{}, false
	}
	plain, err := t.box.Open(sealed)
	if err != nil {
		return Token{}, false
	}
	var tok Token
	return tok, json.Unmarshal([]byte(plain), &tok) == nil
}

func (t *Tokens) remember(ctx context.Context, key string, tok Token) {
	t.mu.Lock()
	t.mem[key] = tok
	t.mu.Unlock()
	raw, _ := json.Marshal(tok)
	sealed, err := t.box.Seal(string(raw))
	if ttl := tok.ExpiresAt.Sub(t.now()); err == nil && ttl > 0 {
		_ = t.kv.Set(ctx, key, sealed, ttl)
	}
}

// Reset forgets every cached token (App deleted or replaced).
func (t *Tokens) Reset(ctx context.Context) error {
	t.mu.Lock()
	t.mem = map[string]Token{}
	t.mu.Unlock()
	_, err := t.kv.Incr(ctx, "ghtok:gen", 0)
	return err
}

func (t *Tokens) cacheKey(ctx context.Context, installationID int64, req TokenRequest) (string, error) {
	gen, _, err := t.kv.Get(ctx, "ghtok:gen")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("ghtok:%s:%d:%s", gen, installationID, scopeHash(req)), nil
}

func scopeHash(req TokenRequest) string {
	repos := append([]string(nil), req.Repositories...)
	sort.Strings(repos)
	perms := make([]string, 0, len(req.Permissions))
	for k, v := range req.Permissions {
		perms = append(perms, k+"="+v)
	}
	sort.Strings(perms)
	sum := sha256.Sum256([]byte(strings.Join(repos, ",") + "|" + strings.Join(perms, ",")))
	return hex.EncodeToString(sum[:8])
}
