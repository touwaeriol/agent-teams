package github_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/github"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/testdb"
)

// TestGuardSeesTransactions: a GitHub request made inside a database transaction is reported,
// one made outside is not.
func TestGuardSeesTransactions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := &github.Client{BaseURL: srv.URL, WebURL: srv.URL, HTTP: srv.Client()}
	var hits []string
	prev := github.InTxCall
	github.InTxCall = func(method, path string, _ []byte) { hits = append(hits, method+" "+path) }
	defer func() { github.InTxCall = prev }()

	ctx := context.Background()
	pool, err := store.OpenPool(ctx, testdb.Migrated(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := c.Do(ctx, http.MethodGet, "/outside", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	err = store.InTx(ctx, pool, func(store.DB) error {
		_, err := c.Do(ctx, http.MethodGet, "/inside", "", nil, nil)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0] != "GET /inside" {
		t.Fatalf("reported %v", hits)
	}
}
