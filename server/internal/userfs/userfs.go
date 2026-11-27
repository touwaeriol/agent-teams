// Package userfs is the user's shared folder (juicefs-shared-storage design): one JuiceFS volume
// for the whole platform, served to nodes through the console's own `juicefs gateway
// --multi-buckets` (an S3 API over the volume, in which each top-level folder is a bucket). The
// console provisions the volume once:
//
//   - metadata: a schema (Config.MetaRole, default juicefs) in the console's PostgreSQL, owned by a
//     login role of the same name that has no privilege outside it; only the console reaches it;
//   - data: the prefix <volume>/ of one bucket, written with an object-storage account whose policy
//     allows that prefix only;
//   - the volume itself, formatted with `juicefs format` (trash), and the gateway over it
//     (Config.GatewayListen, published as Config.GatewayURL).
//
// Each user gets a bucket of the gateway (u-<16 hex>: a top-level folder of the volume) with a
// JuiceFS directory quota (Config.CapacityGB), and a gateway IAM user whose policy allows that
// bucket only. Nodes mount the bucket over S3 with the user's key; they never see the metadata or
// the object store. Deleting the IAM user revokes every node of the user at once. The volume's
// secrets are sealed in at_settings (SettingKey), each user's secret key in its at_user_fs row.
// Inside a bucket, projects/<pid>/ is a project's folder and shared/ the folder all the user's
// projects share.
package userfs

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Volume states (at_user_fs.state).
const (
	StateProvisioning = "provisioning"
	StateReady        = "ready"
	StateError        = "error"
	StateDeleting     = "deleting"
)

// SharedDir is the volume folder every project of the user can use.
const SharedDir = "shared"

// ProjectDir is a project's folder in the volume.
func ProjectDir(projectID string) string { return "projects/" + projectID }

var (
	// ErrDisabled: the console has no AGENT_TEAMS_FS_* settings.
	ErrDisabled = errors.New("共享文件夹没有配置（AGENT_TEAMS_FS_*）")
	// ErrNotFound: the user has no volume.
	ErrNotFound = errors.New("还没有开启共享文件夹")
)

// Asset is one platform's juicefs client archive (fs.mount client.assets).
type Asset struct {
	Platform string
	URL      string
	SHA256   string
}

// DefaultClientVersion is the juicefs release nodes run unless AGENT_TEAMS_FS_CLIENT_* overrides it.
const DefaultClientVersion = "1.4.2"

// DefaultAssets are the checksums of the juicefs v1.4.2 release (checksums.txt of the GitHub release).
var DefaultAssets = []Asset{
	{Platform: "linux-amd64", SHA256: "79648a0a67145bf42ca4e8336578c13a5bbc9a0bd34df340b3b93dac1e975247"},
	{Platform: "linux-arm64", SHA256: "19f72a30d3642a2f7971c8c4a31d2647de62f19520716e85c3a777de5f4c9036"},
	{Platform: "darwin-amd64", SHA256: "ebbd140dc1870653317129ccd08c401fa4c898f15dc962a66e94a77ea0a7d8c5"},
	{Platform: "darwin-arm64", SHA256: "36167e3bb12657bd886f9ab5509b2b0800897da5cf84069b0970031ce53456cc"},
}

// DefaultClientTool is the S3 mount client nodes run unless AGENT_TEAMS_FS_CLIENT_TOOL says otherwise.
const DefaultClientTool = "geesefs"

// DefaultClientBase is where the default assets are downloaded from: <base>/v<version>/juicefs-<version>-<platform>.tar.gz.
const DefaultClientBase = "https://github.com/juicedata/juicefs/releases/download"

// Config is AGENT_TEAMS_FS_*.
type Config struct {
	// AdminURL is a PostgreSQL URL allowed to create roles and schemas (default: DATABASE_URL).
	AdminURL string
	// GatewayListen is where the console's gateway listens (host:port); GatewayURL is that gateway
	// as nodes reach it (https://..., through the reverse proxy).
	GatewayListen string
	GatewayURL    string
	// S3Endpoint is the object store (default AGENT_TEAMS_S3_ENDPOINT), Bucket the bucket all volumes share.
	S3Endpoint string
	Bucket     string
	Region     string
	// AdminAccessKey / AdminSecretKey manage the store's users and policies (RustFS / MinIO admin API).
	AdminAccessKey string
	AdminSecretKey string
	// CapacityGB is the quota of a user's folder.
	CapacityGB int
	// Volume is the shared volume's name (default DefaultVolume); MetaRole its metadata role and
	// schema (default DefaultMetaRole).
	Volume   string
	MetaRole string
	// MetaUser is the user name the role connects as (default MetaRole). A pooler that routes by
	// user name wants more: Supabase's session pooler takes <role>.<project ref>.
	MetaUser string
	// JuiceFS is the juicefs binary the console runs (format, quota, file access).
	JuiceFS string
	// ClientTool is the S3 mount client nodes run (DefaultClientTool); ClientVersion and ClientBase
	// pick its download, Assets are their checksums.
	ClientTool    string
	ClientVersion string
	ClientBase    string
	Assets        []Asset
}

// DefaultVolume and DefaultMetaRole name the shared volume and its metadata schema;
// DefaultGatewayListen is where the gateway listens.
const (
	DefaultVolume        = "agent-teams"
	DefaultMetaRole      = "juicefs"
	DefaultGatewayListen = "0.0.0.0:9310"
)

func (c Config) volume() string   { return orString(c.Volume, DefaultVolume) }
func (c Config) metaRole() string { return orString(c.MetaRole, DefaultMetaRole) }
func (c Config) metaUser() string { return orString(c.MetaUser, c.metaRole()) }
func (c Config) gatewayListen() string {
	return orString(c.GatewayListen, DefaultGatewayListen)
}
func (c Config) clientTool() string { return orString(c.ClientTool, DefaultClientTool) }

func orString(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

// Enabled tells whether the feature is configured.
func (c Config) Enabled() bool {
	return c.GatewayURL != "" && c.Bucket != "" && c.S3Endpoint != "" && c.AdminAccessKey != "" && c.AdminSecretKey != "" && c.AdminURL != ""
}

// database is the database name of AdminURL (the volume's schema lives there).
func (c Config) database() (string, error) {
	u, err := url.Parse(c.AdminURL)
	if err != nil {
		return "", fmt.Errorf("AGENT_TEAMS_FS_META_ADMIN_URL: %w", err)
	}
	db := strings.TrimPrefix(u.Path, "/")
	if db == "" {
		return "", errors.New("AGENT_TEAMS_FS_META_ADMIN_URL has no database name")
	}
	return db, nil
}

// adminHost is host:port of AdminURL (the console formats the volume through it).
func (c Config) adminHost() string {
	u, err := url.Parse(c.AdminURL)
	if err != nil {
		return ""
	}
	return u.Host
}

// adminTLS are the TLS settings of AdminURL (sslmode, default disable, and the certificate files
// when given): the console's own metadata URLs connect like AdminURL does.
func (c Config) adminTLS() url.Values {
	q := url.Values{"sslmode": {"disable"}}
	u, err := url.Parse(c.AdminURL)
	if err != nil {
		return q
	}
	for _, k := range []string{"sslmode", "sslrootcert", "sslcert", "sslkey"} {
		if v := u.Query().Get(k); v != "" {
			q.Set(k, v)
		}
	}
	return q
}

// ClientAssets are the client downloads with URLs filled in.
func (c Config) ClientAssets() []Asset {
	out := make([]Asset, 0, len(c.Assets))
	for _, a := range c.Assets {
		if a.URL == "" {
			a.URL = fmt.Sprintf("%s/v%s/juicefs-%s-%s.tar.gz", strings.TrimSuffix(c.ClientBase, "/"), c.ClientVersion, c.ClientVersion, a.Platform)
		}
		out = append(out, a)
	}
	return out
}

// Names are the shared volume's identifiers: the volume (also its bucket prefix), the metadata role
// and schema, and the object-store account and policy (derived from the volume name, so the access
// key stays within the 20 characters some stores allow).
type Names struct {
	Volume    string
	Role      string
	AccessKey string // jfs<16 hex>
	Policy    string // jfs-<16 hex>
}

// SharedNames are the names of the volume c configures.
func (c Config) SharedNames() Names {
	sum := sha256.Sum256([]byte("agent-teams-fs|" + c.volume()))
	id := hex.EncodeToString(sum[:8])
	return Names{Volume: c.volume(), Role: c.metaRole(), AccessKey: "jfs" + id, Policy: "jfs-" + id}
}

// UserNames are a user's identifiers in the gateway, all from one id (16 hex of a hash of the
// user id, so no user id reaches the gateway, the metadata or the object store): the bucket (a
// top-level folder of the volume), the IAM user and its policy.
type UserNames struct {
	ID        string
	Bucket    string // u-<id>
	AccessKey string // fs<id>
	Policy    string // fs-<id>
}

// UserNamesFor are userID's names.
func UserNamesFor(userID string) UserNames {
	sum := sha256.Sum256([]byte("agent-teams-user-fs|" + userID))
	return namesOf(hex.EncodeToString(sum[:8]))
}

func namesOf(id string) UserNames {
	return UserNames{ID: id, Bucket: BucketPrefix + id, AccessKey: "fs" + id, Policy: "fs-" + id}
}

// BucketPrefix starts every user's bucket.
const BucketPrefix = "u-"

// userBucketID is the id of a user's bucket name ("" for any other name).
func userBucketID(bucket string) string {
	id, ok := strings.CutPrefix(bucket, BucketPrefix)
	if !ok || len(id) != 16 {
		return ""
	}
	if _, err := hex.DecodeString(id); err != nil {
		return ""
	}
	return id
}

// BucketPolicy is a gateway user's policy: its own bucket, nothing else (no bucket creation or
// deletion either).
func BucketPolicy(bucket string) []byte {
	return fmt.Appendf(nil, `{"Version":"2012-10-17","Statement":[`+
		`{"Effect":"Allow","Action":["s3:GetBucketLocation","s3:ListBucket","s3:ListBucketMultipartUploads"],"Resource":["arn:aws:s3:::%[1]s"]},`+
		`{"Effect":"Allow","Action":["s3:GetObject","s3:PutObject","s3:DeleteObject","s3:AbortMultipartUpload","s3:ListMultipartUploadParts"],"Resource":["arn:aws:s3:::%[1]s/*"]}]}`,
		bucket)
}

// Secrets are what the volume's setting seals: the metadata role's password, the object-store
// account's secret key and the gateway's root password (GatewayRoot is its user).
type Secrets struct {
	MetaPassword    string `json:"meta_password"`
	SecretKey       string `json:"secret_key"`
	GatewayPassword string `json:"gateway_password"`
}

// GatewayRoot is the gateway's root user (the console's own access: buckets, IAM, files).
const GatewayRoot = "agent-teams"

// UserSecrets are what a user's row seals: the gateway IAM user's secret key.
type UserSecrets struct {
	SecretKey string `json:"secret_key"`
}

func randomSecret(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// NewSecrets makes fresh volume secrets.
func NewSecrets() Secrets {
	return Secrets{MetaPassword: randomSecret(24), SecretKey: randomSecret(30), GatewayPassword: randomSecret(30)}
}

// metaURL is the metadata URL without password (META_PASSWORD carries it): user connects, schema
// is the search path (the role's own setting says the same, for poolers that drop it), tls holds
// the connection settings (sslmode and certificate files).
func metaURL(user, schema, host, database string, tls url.Values) string {
	q := url.Values{}
	for k, v := range tls {
		q[k] = v
	}
	q.Set("search_path", schema)
	return (&url.URL{Scheme: "postgres", User: url.User(user), Host: host, Path: "/" + database, RawQuery: q.Encode()}).String()
}

// BucketURL is the bucket address juicefs format takes (objects then go under <volume>/).
func (c Config) BucketURL() string {
	return strings.TrimSuffix(c.S3Endpoint, "/") + "/" + c.Bucket
}

// Policy is the object-store policy of a volume's account: list its prefix, read and write objects under it.
func Policy(bucket, volume string) []byte {
	return fmt.Appendf(nil, `{"Version":"2012-10-17","Statement":[`+
		`{"Effect":"Allow","Action":["s3:GetBucketLocation"],"Resource":["arn:aws:s3:::%[1]s"]},`+
		`{"Effect":"Allow","Action":["s3:ListBucket"],"Resource":["arn:aws:s3:::%[1]s"],"Condition":{"StringLike":{"s3:prefix":["%[2]s","%[2]s/*"]}}},`+
		`{"Effect":"Allow","Action":["s3:GetObject","s3:PutObject","s3:DeleteObject","s3:AbortMultipartUpload","s3:ListMultipartUploadParts"],"Resource":["arn:aws:s3:::%[1]s/%[2]s/*"]}]}`,
		bucket, volume)
}
