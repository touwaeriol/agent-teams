package httpapi

import (
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/config"
)

func TestOpenAPIDocument(t *testing.T) {
	raw, err := New(Deps{Config: config.Config{PublicURL: "http://localhost:3001"}}).OpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	for _, want := range []string{`"openapi": "3.1.0"`, `"/auth/login"`, `"/api/projects/{pid}"`, `"/api/roles"`,
		`"/api/office/layouts/{id}"`, `"/api/nodes/{id}/token"`, `"APIError"`} {
		if !strings.Contains(doc, want) {
			t.Errorf("OpenAPI lacks %s", want)
		}
	}
	if strings.Contains(doc, `"$schema"`) {
		t.Error("response bodies must not carry $schema links")
	}
}
