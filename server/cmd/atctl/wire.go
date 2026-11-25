package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/auth"
	"github.com/touwaeriol/agent-teams/server/internal/blobstore"
	"github.com/touwaeriol/agent-teams/server/internal/config"
	"github.com/touwaeriol/agent-teams/server/internal/credvault"
	"github.com/touwaeriol/agent-teams/server/internal/engine"
	"github.com/touwaeriol/agent-teams/server/internal/gitrelay"
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
	if d.Relay == nil {
		d.Relay = &gitrelay.Relay{Store: d.Store, Vault: vault, Dir: d.Config.GitMirrorDir, Log: d.Log}
	}
	blobs, err := objectStore(d.Config.S3, d.Log)
	if err != nil {
		return d, nil, err
	}
	e, err := engine.New(engine.Deps{Store: d.Store, KV: d.KV, GitHub: d.GitHub, Tokens: d.Tokens,
		Box: auth.NewSecretBox(d.Config.SecretKeys...), Office: d.Office, Log: d.Log, Gateway: nodegw.Config{Validate: true},
		Vault: vault, Relay: d.Relay, Blobs: blobs, AttachmentTTL: d.Config.AttachmentTTL})
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

// objectStore builds the attachments and shared-files store from AGENT_TEAMS_S3_* (#58): None when
// nothing is set (new uploads answer 503). A bucket that cannot be reached is only logged: the
// rest of the console keeps working and uploads fail until it is back.
func objectStore(c config.S3, log *slog.Logger) (blobstore.Store, error) {
	if !c.Configured() {
		log.Warn("object store not configured (AGENT_TEAMS_S3_*): new attachments and shared files are refused")
		return blobstore.None{}, nil
	}
	s, err := blobstore.New(blobstore.Config{Endpoint: c.Endpoint, PublicEndpoint: c.PublicEndpoint, Bucket: c.Bucket,
		AccessKey: c.AccessKey, SecretKey: c.SecretKey, Region: c.Region, PathStyle: c.PathStyle})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Check(ctx); err != nil {
		log.Error("object store unreachable", "endpoint", c.Endpoint, "bucket", c.Bucket, "err", err)
	}
	return s, nil
}
