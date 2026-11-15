package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/auth"
	"github.com/touwaeriol/agent-teams/server/internal/kv"
)

func TestParseRepo(t *testing.T) {
	cases := map[string]string{
		"owner/repo":                               "owner/repo",
		"https://github.com/owner/repo.git":        "owner/repo",
		"git@github.com:owner/repo.git":            "owner/repo",
		"github.com/owner/repo/tree/main?x=1#frag": "owner/repo",
	}
	for in, want := range cases {
		if got, err := ParseRepo(in); err != nil || got != want {
			t.Errorf("ParseRepo(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "owner", "https://gitlab.com/a/b", "a b/c"} {
		if _, err := ParseRepo(bad); err == nil {
			t.Errorf("ParseRepo(%q) accepted", bad)
		}
	}
}

func testKey(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}))
}

func TestAppJWT(t *testing.T) {
	_, pemText := testKey(t)
	now := time.Unix(1_700_000_000, 0)
	jwt, err := AppJWT(42, pemText, now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt %q", jwt)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	_ = json.Unmarshal(raw, &claims)
	if claims["iss"] != "42" || claims["iat"].(float64) != 1_700_000_000-60 || claims["exp"].(float64) != 1_700_000_000+480 {
		t.Fatalf("claims %v", claims)
	}
	if _, err := AppJWT(1, "garbage", now); err == nil {
		t.Fatal("garbage key accepted")
	}
}

type memSettings map[string][]byte

func (m memSettings) Setting(_ context.Context, key string, dest any) (bool, error) {
	raw, ok := m[key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, dest)
}

func (m memSettings) SaveSetting(_ context.Context, key string, v any) error {
	raw, err := json.Marshal(v)
	m[key] = raw
	return err
}

func (m memSettings) DeleteSetting(_ context.Context, key string) error {
	delete(m, key)
	return nil
}

func fakeGitHub(t *testing.T, mints *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/access_tokens"):
			n := atomic.AddInt32(mints, 1)
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
				w.WriteHeader(401)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token": "ghs_" + string(rune('a'+n)), "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
		case r.URL.Path == "/user/repos":
			if r.URL.Query().Get("page") == "1" {
				w.Header().Set("Link", `<x?page=2>; rel="next"`)
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"full_name": "o/r" + r.URL.Query().Get("page"), "default_branch": "main"}})
		case r.URL.Path == "/repos/o/missing":
			w.WriteHeader(404)
		default:
			w.WriteHeader(500)
		}
	}))
}

func TestTokensCacheAndScope(t *testing.T) {
	var mints int32
	srv := fakeGitHub(t, &mints)
	defer srv.Close()
	_, pemText := testKey(t)
	box := auth.NewSecretBox("k")
	settings := memSettings{}
	apps := NewApps(settings, box)
	if _, err := apps.Save(context.Background(), map[string]any{
		"id": float64(7), "slug": "at", "client_id": "cid", "client_secret": "cs", "pem": pemText,
		"webhook_secret": "wh", "owner": map[string]any{"login": "me"}}, map[string]string{"checks": "read"}); err != nil {
		t.Fatal(err)
	}
	app, _ := apps.Load(context.Background())
	if app.WebhookSecret != "wh" || app.Owner != "me" || app.Permissions["checks"] != "read" {
		t.Fatalf("app %+v", app)
	}
	client := &Client{BaseURL: srv.URL, WebURL: srv.URL, HTTP: srv.Client()}
	store := kv.NewMemory()
	tokens := NewTokens(client, apps, store, box)
	ctx := context.Background()
	read := TokenRequest{Repositories: []string{"r"}, Permissions: map[string]string{"contents": "read"}}
	a, err := tokens.Installation(ctx, 9, read)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := tokens.Installation(ctx, 9, read)
	if a != b || mints != 1 {
		t.Fatalf("cache miss: %q %q mints=%d", a, b, mints)
	}
	if c, _ := tokens.Installation(ctx, 9, TokenRequest{}); c == a || mints != 2 {
		t.Fatal("different scope shared a token")
	}
	// A second process with only the shared cache reuses the sealed token.
	other := NewTokens(client, apps, store, box)
	if d, _ := other.Installation(ctx, 9, read); d != a || mints != 2 {
		t.Fatal("shared cache not used")
	}
	_ = tokens.Reset(ctx)
	if e, _ := tokens.Installation(ctx, 9, read); e == a || mints != 3 {
		t.Fatal("reset did not drop the cache")
	}
}

func TestClientPagingAndErrors(t *testing.T) {
	var n int32
	srv := fakeGitHub(t, &n)
	defer srv.Close()
	client := &Client{BaseURL: srv.URL, WebURL: srv.URL, HTTP: srv.Client()}
	repos, more, err := client.UserRepos(context.Background(), "t", 1)
	if err != nil || !more || repos[0].FullName != "o/r1" {
		t.Fatalf("page1 %v %v %v", repos, more, err)
	}
	if _, more, _ = client.UserRepos(context.Background(), "t", 2); more {
		t.Fatal("page2 claims more")
	}
	_, err = client.Repo(context.Background(), "t", "o/missing")
	if ge, ok := err.(*Error); !ok || ge.Status != 404 {
		t.Fatalf("want 404 error, got %v", err)
	}
}
