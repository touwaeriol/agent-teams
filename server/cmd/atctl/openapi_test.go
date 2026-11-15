package main

import (
	"bytes"
	"os"
	"testing"
)

const committedOpenAPI = "../../../web/src/api/openapi.json"

// TestOpenAPIUpToDate keeps web/src/api/openapi.json in sync with everything wired into the server.
// Regenerate with: atctl openapi > web/src/api/openapi.json (or UPDATE_OPENAPI=1 go test ./cmd/atctl).
func TestOpenAPIUpToDate(t *testing.T) {
	doc, err := openAPIDocument()
	if err != nil {
		t.Fatal(err)
	}
	doc = append(doc, '\n')
	if os.Getenv("UPDATE_OPENAPI") == "1" {
		if err := os.WriteFile(committedOpenAPI, doc, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	committed, err := os.ReadFile(committedOpenAPI)
	if err != nil {
		t.Skipf("no committed OpenAPI file: %v", err)
	}
	if !bytes.Equal(committed, doc) {
		t.Fatal("web/src/api/openapi.json is stale: run atctl openapi > web/src/api/openapi.json")
	}
}

func TestCommandsRegistered(t *testing.T) {
	for _, name := range []string{"serve", "migrate", "healthcheck", "openapi", "admin"} {
		if commands[name] == nil {
			t.Errorf("missing command %s", name)
		}
	}
}
