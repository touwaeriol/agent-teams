package main

import (
	"github.com/touwaeriol/agent-teams/server/internal/config"
	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
)

// wire is the one place where the orchestration engines plug into the HTTP
// server: set d.Nodes (node gateway registry), d.Events (SSE publisher),
// d.Hooks, and append d.Extensions that mount /nodes/connect, thread APIs and
// SSE streams. serve calls it with live dependencies; openapi calls it with
// d.Store == nil, so wiring must only build objects and register routes,
// never touch the database or the network.
func wire(d httpapi.Deps) httpapi.Deps {
	return d
}

// openAPIDocument builds the server exactly as serve does, without I/O, and renders the document.
func openAPIDocument() ([]byte, error) {
	srv := httpapi.New(wire(httpapi.Deps{Config: config.Config{PublicURL: "http://localhost:3001"}}))
	return srv.OpenAPI()
}
