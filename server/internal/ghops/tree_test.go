package ghops

import (
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// The file-list cache must not hand one user's listing to another user naming the same repository.
func TestTreeCacheKeyIsPerConnection(t *testing.T) {
	a := Repo{Slug: "acme/app", conn: store.GitConnection{ID: "conn-a"}}
	b := Repo{Slug: "ACME/app", conn: store.GitConnection{ID: "conn-b"}}
	if treeKey(a, "main") == treeKey(b, "main") {
		t.Fatal("two connections share a cache entry")
	}
	if treeKey(a, "main") != treeKey(Repo{Slug: "ACME/App", conn: a.conn}, "main") {
		t.Fatal("same connection and repository should share the entry")
	}
}
