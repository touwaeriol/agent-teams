package main

import (
	"context"
	"log/slog"
	"strings"
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
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/userfs"
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
	fs := sharedFolders(d.Config.FS, d.Config.PublicURL, d.Store, vault, d.Log)
	e, err := engine.New(engine.Deps{Store: d.Store, KV: d.KV, GitHub: d.GitHub, Tokens: d.Tokens,
		Box: auth.NewSecretBox(d.Config.SecretKeys...), Office: d.Office, Log: d.Log, Gateway: nodegw.Config{Validate: true},
		Vault: vault, Relay: d.Relay, Blobs: blobs, AttachmentTTL: d.Config.AttachmentTTL, FS: fs})
	if err != nil {
		return d, nil, err
	}
	d.Nodes, d.Events, d.Hooks, d.SSH, d.Memory = e.Gateway, e.Events, e.Hooks(), e.SSHHooks(), e.Core.Memory
	d.Extensions = append(d.Extensions, orchapi.New(e, d.Office).WithOrigins(d.Config.ArtifactOrigin, d.Config.PublicURL).Register)
	e.Jobs.Add(maintenanceJob(d.Store, e.Jobs))
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

// sharedFolders builds the user shared-folder service from AGENT_TEAMS_FS_* (nil when not set).
func sharedFolders(c config.FS, publicURL string, st *store.Store, vault *credvault.Vault, log *slog.Logger) *userfs.Service {
	if !c.Configured() {
		return nil
	}
	version, base, assets := userfs.DefaultClientVersion, userfs.DefaultClientBase, userfs.DefaultAssets
	if c.ClientVersion != "" {
		version, assets = c.ClientVersion, nil
		for platform, sum := range c.ClientSHA256 {
			assets = append(assets, userfs.Asset{Platform: platform, SHA256: sum})
		}
	}
	if c.ClientBase != "" {
		base = c.ClientBase
	}
	cfg := userfs.Config{Provider: c.Provider, S3Endpoint: c.S3Endpoint, NodeEndpoint: c.NodeEndpoint, Region: c.Region, AccessKey: c.AccessKey,
		SecretKey: c.SecretKey, CapacityGB: c.CapacityGB, ClientVersion: version, ClientBase: base, Assets: assets}
	if !vault.Ready() {
		log.Warn("shared folder needs AGENT_TEAMS_SECRET_KEYS to seal the users' keys: switched off")
	}
	var store userfs.Store = userfs.NewRustFS(c.S3Endpoint, c.Region, c.AccessKey, c.SecretKey, corsOrigins(publicURL))
	if c.Provider == userfs.ProviderB2 {
		store = userfs.NewB2(c.S3Endpoint, c.Region, c.AccessKey, c.SecretKey, c.Bucket)
	}
	return userfs.New(cfg, st.Pool, vault, store, log)
}

// corsOrigins are the browser origins allowed on the users' buckets: the console's public URL.
func corsOrigins(publicURL string) []string {
	if publicURL == "" {
		return nil
	}
	return []string{strings.TrimRight(publicURL, "/")}
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
