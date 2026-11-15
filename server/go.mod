module github.com/touwaeriol/agent-teams/server

go 1.26.0

replace github.com/touwaeriol/agent-teams/protocol => ../protocol/gen/go

require (
	github.com/coder/websocket v1.8.15
	github.com/danielgtaylor/huma/v2 v2.40.0
	github.com/fernet/fernet-go v0.0.0-20240119011108-303da6aec611
	github.com/fsnotify/fsnotify v1.10.1
	github.com/go-chi/chi/v5 v5.5.0
	github.com/jackc/pgx/v5 v5.13.0
	github.com/oklog/ulid/v2 v2.1.2
	github.com/pressly/goose/v3 v3.30.1
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2
	github.com/touwaeriol/agent-teams/protocol v0.0.0-00010101000000-000000000000
	github.com/valkey-io/valkey-go v1.0.79
	go.yaml.in/yaml/v3 v3.0.6
	golang.org/x/crypto v0.59.0
	golang.org/x/text v0.44.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	github.com/mfridman/interpolate v0.2.0 // indirect
	github.com/sethvargo/go-retry v0.5.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/sync v0.25.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
)
