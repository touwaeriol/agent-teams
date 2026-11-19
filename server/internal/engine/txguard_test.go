package engine_test

import (
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/github"
)

// TestMain fails the package when any GitHub request is issued inside a database transaction.
func TestMain(m *testing.M) {
	var mu sync.Mutex
	seen := map[string]int{}
	github.InTxCall = func(method, path string, stack []byte) {
		mu.Lock()
		defer mu.Unlock()
		seen[method+" "+path+"\n    "+github.OwnFrames(stack)]++
	}
	code := m.Run()
	if len(seen) > 0 {
		keys := make([]string, 0, len(seen))
		for k := range seen {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(os.Stderr, "\nGitHub calls inside a database transaction:\n")
		for _, k := range keys {
			fmt.Fprintf(os.Stderr, "  [%d] %s\n", seen[k], k)
		}
		code = 1
	}
	os.Exit(code)
}
