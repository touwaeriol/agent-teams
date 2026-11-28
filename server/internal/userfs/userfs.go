// Package userfs is the user's shared folder (shared-folder design, on the object store
// directly): one bucket of the platform's object store (Backblaze B2, or a RustFS / MinIO) holds
// every user's folder under users/<16 hex of a hash of the user id>/, and each user has a key of
// that store limited to its own prefix (a B2 application key with a namePrefix, or an IAM user
// whose policy allows the prefix). Nodes mount the prefix with geesefs (an S3 FUSE client) and the
// user's key. Revoking the key cuts every node of the user off at once. The capacity is a soft
// limit: a periodic count switches a user over it to a key that cannot write (it can still read
// and delete) until the folder is back under it. The user's secret key is sealed in its
// at_user_fs row and only ever leaves the console in fs.mount, on a node's live link. Inside a
// folder, projects/<pid>/ is a project's folder and shared/ the folder all the user's projects
// share. The console itself reads and writes the folders with the platform's key.
package userfs

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/touwaeriol/agent-teams/server/internal/msg"
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
	ErrDisabled = msg.Err("共享文件夹没有配置（AGENT_TEAMS_FS_*）")
	// ErrNotFound: the user has no volume.
	ErrNotFound = msg.Err("还没有开启共享文件夹")
)

// Asset is one platform's mount client download (fs.mount client.assets).
type Asset struct {
	Platform string
	URL      string
	SHA256   string
}

// DefaultClientTool is the S3 mount client nodes run.
const DefaultClientTool = "geesefs"

// DefaultClientVersion is the geesefs release nodes run unless AGENT_TEAMS_FS_CLIENT_* overrides it.
const DefaultClientVersion = "0.43.9"

// DefaultAssets are the sha256 of the geesefs v0.43.9 release binaries (2026-09-03); nodes refuse a
// download that does not match.
var DefaultAssets = []Asset{
	{Platform: "linux-amd64", SHA256: "8253f0bad8070f504b3ca94a76919d9617252e7ffae04efca9ca54d5cfbaaca8"},
	{Platform: "linux-arm64", SHA256: "d2a47ea0833c2369e1f7ba92cada1c8caadd0e91144f4219fd0ce6a483e52461"},
	{Platform: "darwin-amd64", SHA256: "be628ec055c422f32e66f150d77ba5229af06cda2dab61866e236d794bab3318"},
	{Platform: "darwin-arm64", SHA256: "ed2cbcbb5d2f7be6e5d9c4fd212772f2af195cfb2a442440191f6b11ca36fb53"},
}

// DefaultClientBase is where the default assets are downloaded from: <base>/v<version>/geesefs-<os>-<arch>
// (the release names macOS "mac").
const DefaultClientBase = "https://github.com/yandex-cloud/geesefs/releases/download"

// Providers (AGENT_TEAMS_FS_PROVIDER).
const (
	ProviderB2     = "b2"
	ProviderRustFS = "rustfs"
)

// Config is AGENT_TEAMS_FS_*.
type Config struct {
	// Provider is ProviderB2 or ProviderRustFS (RustFS or MinIO).
	Provider string
	// S3Endpoint is the store's S3 address as the console reaches it; NodeEndpoint as nodes reach it
	// (default S3Endpoint). Region signs requests.
	S3Endpoint   string
	NodeEndpoint string
	Region       string
	// AccessKey / SecretKey are the platform's key: RustFS the admin key, B2 an application key
	// with writeKeys and the file capabilities on its bucket.
	AccessKey string
	SecretKey string
	// CapacityGB is the soft limit of a user's folder (0 = none).
	CapacityGB int
	// ClientTool is the S3 mount client nodes run (DefaultClientTool); ClientVersion and ClientBase
	// pick its download, Assets are their checksums.
	ClientTool    string
	ClientVersion string
	ClientBase    string
	Assets        []Asset
}

func (c Config) clientTool() string { return orString(c.ClientTool, DefaultClientTool) }
func (c Config) region() string     { return orString(c.Region, "us-east-1") }
func (c Config) nodeEndpoint() string {
	return strings.TrimSuffix(orString(c.NodeEndpoint, c.S3Endpoint), "/")
}

func orString(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

// Enabled tells whether the feature is configured.
func (c Config) Enabled() bool {
	return c.S3Endpoint != "" && c.AccessKey != "" && c.SecretKey != ""
}

// ClientAssets are the client downloads with URLs filled in.
func (c Config) ClientAssets() []Asset {
	out := make([]Asset, 0, len(c.Assets))
	for _, a := range c.Assets {
		if a.URL == "" {
			a.URL = fmt.Sprintf("%s/v%s/geesefs-%s", strings.TrimSuffix(c.ClientBase, "/"), c.ClientVersion, strings.Replace(a.Platform, "darwin-", "mac-", 1))
		}
		out = append(out, a)
	}
	return out
}

// UsersDir holds the users' folders in the bucket.
const UsersDir = "users/"

// UserNames are a user's identifiers in the object store, all from one id (16 hex of a hash of the
// user id, so no user id reaches the store).
type UserNames struct {
	ID     string
	Prefix string // users/<id>/: the user's folder in the bucket
	Name   string // atu-<id>: the key's name (B2), the policy (RustFS), the mount directory on nodes
	User   string // atu<id>: the IAM user (RustFS)
}

// NamesOf are userID's names.
func NamesOf(userID string) UserNames {
	sum := sha256.Sum256([]byte("agent-teams-user-fs|" + userID))
	return namesOf(hex.EncodeToString(sum[:8]))
}

func namesOf(id string) UserNames {
	return UserNames{ID: id, Prefix: UsersDir + id + "/", Name: "atu-" + id, User: "atu" + id}
}

// hexID is s when it is 16 lowercase hex ("" otherwise).
func hexID(s string) string {
	if len(s) != 16 || strings.ToLower(s) != s {
		return ""
	}
	if _, err := hex.DecodeString(s); err != nil {
		return ""
	}
	return s
}

// prefixID is the id of a user's prefix users/<id>/ ("" for anything else).
func prefixID(prefix string) string {
	id, ok := strings.CutPrefix(prefix, UsersDir)
	if !ok {
		return ""
	}
	id, ok = strings.CutSuffix(id, "/")
	if !ok {
		return ""
	}
	return hexID(id)
}

// folderID is the id of a user's folder: its prefix users/<id>/, or its bucket atu-<id> ("" otherwise).
func folderID(f Folder) string {
	if f.Prefix != "" {
		return prefixID(f.Prefix)
	}
	if !strings.HasPrefix(f.Bucket, "atu-") {
		return ""
	}
	return keyID(f.Bucket)
}

// keyID is the id in a key or IAM user name (atu-<id> or atu<id>; "" for anything else).
func keyID(name string) string {
	id, ok := strings.CutPrefix(name, "atu")
	if !ok {
		return ""
	}
	return hexID(strings.TrimPrefix(id, "-"))
}

// Key is a key of the object store: its id (S3 access key) and secret.
type Key struct {
	ID     string
	Secret string
}

// KeyInfo is a key the store lists: its id and name.
type KeyInfo struct {
	ID   string
	Name string
}

// FolderPolicy is a user's IAM policy (RustFS / MinIO): list, read and delete in its own folder (a
// whole bucket, or a prefix of one), write too unless write is false; nothing outside it, no bucket,
// quota or policy change.
func FolderPolicy(f Folder, write bool) []byte {
	actions := `"s3:GetObject","s3:DeleteObject","s3:AbortMultipartUpload","s3:ListMultipartUploadParts"`
	if write {
		actions = `"s3:PutObject",` + actions
	}
	list := fmt.Sprintf(`{"Effect":"Allow","Action":["s3:ListBucket","s3:ListBucketMultipartUploads"],"Resource":["arn:aws:s3:::%s"]}`, f.Bucket)
	if f.Prefix != "" {
		list = fmt.Sprintf(`{"Effect":"Allow","Action":["s3:ListBucket"],"Resource":["arn:aws:s3:::%[1]s"],"Condition":{"StringLike":{"s3:prefix":["%[2]s","%[2]s*"]}}}`, f.Bucket, f.Prefix)
	}
	return fmt.Appendf(nil, `{"Version":"2012-10-17","Statement":[`+
		`{"Effect":"Allow","Action":["s3:GetBucketLocation"],"Resource":["arn:aws:s3:::%[1]s"]},%[2]s,`+
		`{"Effect":"Allow","Action":[%[3]s],"Resource":["arn:aws:s3:::%[1]s/%[4]s*"]}]}`,
		f.Bucket, list, actions, f.Prefix)
}

// UserSecrets are what a user's row seals: its key's secret.
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
