module github.com/touwaeriol/agent-teams/server

go 1.26.0

replace github.com/touwaeriol/agent-teams/protocol => ../protocol/gen/go

require (
	github.com/casbin/casbin/v2 v2.135.0
	github.com/coder/websocket v1.8.15
	github.com/danielgtaylor/huma/v2 v2.40.0
	github.com/fernet/fernet-go v0.0.0-20240119011108-303da6aec611
	github.com/fsnotify/fsnotify v1.10.1
	github.com/gin-gonic/gin v1.12.0
	github.com/jackc/pgx/v5 v5.13.0
	github.com/oklog/ulid/v2 v2.1.2
	github.com/pressly/goose/v3 v3.30.1
	github.com/riverqueue/river v0.51.2
	github.com/riverqueue/river/riverdriver/riverdatabasesql v0.51.2
	github.com/robfig/cron/v3 v3.0.1
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2
	github.com/touwaeriol/agent-teams/protocol v0.0.0-00010101000000-000000000000
	github.com/valkey-io/valkey-go v1.0.79
	go.yaml.in/yaml/v3 v3.0.6
	golang.org/x/crypto v0.59.0
	golang.org/x/text v0.44.0
	gorm.io/driver/postgres v1.6.0
	gorm.io/gorm v1.31.1
)

require (
	github.com/bmatcuk/doublestar/v4 v4.6.1 // indirect
	github.com/bytedance/gopkg v0.1.4 // indirect
	github.com/bytedance/sonic v1.15.4 // indirect
	github.com/bytedance/sonic/loader v0.5.3 // indirect
	github.com/casbin/govaluate v1.3.0 // indirect
	github.com/cloudwego/base64x v0.1.7 // indirect
	github.com/gabriel-vasile/mimetype v1.4.16 // indirect
	github.com/gin-contrib/sse v1.1.2 // indirect
	github.com/go-playground/locales v0.14.2 // indirect
	github.com/go-playground/universal-translator v0.18.2 // indirect
	github.com/go-playground/validator/v10 v10.30.5 // indirect
	github.com/goccy/go-json v0.11.2 // indirect
	github.com/goccy/go-yaml v1.19.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/klauspost/cpuid/v2 v2.4.1 // indirect
	github.com/leodido/go-urn v1.5.0 // indirect
	github.com/lib/pq v1.12.3 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mfridman/interpolate v0.2.0 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/pelletier/go-toml/v2 v2.4.3 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	github.com/quic-go/quic-go v0.64.1 // indirect
	github.com/riverqueue/river/riverdriver v0.51.2 // indirect
	github.com/riverqueue/river/riverdriver/riverpgxv5 v0.51.2 // indirect
	github.com/riverqueue/river/rivershared v0.51.2 // indirect
	github.com/riverqueue/river/rivertype v0.51.2 // indirect
	github.com/sethvargo/go-retry v0.5.0 // indirect
	github.com/tidwall/gjson v1.20.0 // indirect
	github.com/tidwall/match v1.2.0 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	github.com/twitchyliquid64/golang-asm v0.15.1 // indirect
	github.com/ugorji/go/codec v1.3.2 // indirect
	go.mongodb.org/mongo-driver/v2 v2.9.2 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/arch v0.32.0 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sync v0.25.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
