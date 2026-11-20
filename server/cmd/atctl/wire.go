package main

import (
	"context"

	"github.com/touwaeriol/agent-teams/server/internal/auth"
	"github.com/touwaeriol/agent-teams/server/internal/config"
	"github.com/touwaeriol/agent-teams/server/internal/credvault"
	"github.com/touwaeriol/agent-teams/server/internal/engine"
	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
	"github.com/touwaeriol/agent-teams/server/internal/orchapi"
)

// wire is the one place where the orchestration engines plug into the HTTP
// server: it sets d.Nodes (node gateway registry), d.Events (SSE publisher),
// d.Hooks, and appends the extension that mounts /nodes/connect, the thread
// APIs and SSE streams. serve calls it with live dependencies and runs the
// returned start function; openapi calls it with d.Store == nil, so wiring
// then only registers routes and never touches the database or the network.
func wire(d httpapi.Deps) (httpapi.Deps, func(context.Context), error) {
	if d.Store == nil {
		d.Extensions = append(d.Extensions, orchapi.New(nil, nil).Register)
		return d, func(context.Context) {}, nil
	}
	vault, err := credvault.New(d.Config.SecretKeys...)
	if err != nil {
		return d, nil, err
	}
	e, err := engine.New(engine.Deps{Store: d.Store, KV: d.KV, GitHub: d.GitHub, Tokens: d.Tokens,
		Box: auth.NewSecretBox(d.Config.SecretKeys...), Office: d.Office, Log: d.Log, Gateway: nodegw.Config{Validate: true},
		Vault: vault})
	if err != nil {
		return d, nil, err
	}
	d.Nodes, d.Events, d.Hooks, d.SSH = e.Gateway, e.Events, e.Hooks(), e.SSHHooks()
	d.Extensions = append(d.Extensions, orchapi.New(e, d.Office).WithOrigins(d.Config.ArtifactOrigin, d.Config.PublicURL).Register)
	return d, e.Start, nil
}

// openAPIDocument builds the server exactly as serve does, without I/O, and renders the document.
func openAPIDocument() ([]byte, error) {
	d, _, err := wire(httpapi.Deps{Config: config.Config{PublicURL: "http://localhost:3001"}})
	if err != nil {
		return nil, err
	}
	return httpapi.New(d).OpenAPI()
}
