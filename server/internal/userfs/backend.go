package userfs

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
)

// Backend kinds (at_user_fs.backend).
const (
	// BackendPlatform: the platform's bucket and metadata database (the default).
	BackendPlatform = "platform"
	// BackendS3: the user's own S3 bucket; the console formats it as JuiceFS and keeps the
	// metadata in its PostgreSQL like a platform volume.
	BackendS3 = "s3"
	// BackendJuiceFS: the user's own, already formatted JuiceFS; the console only hands nodes its
	// metadata URL and never touches its data.
	BackendJuiceFS = "juicefs"
)

// Backend is what the user picked when switching the folder on. Secret fields are sealed and
// never shown again.
type Backend struct {
	Kind string
	// BackendS3: endpoint (https://host[:port]), bucket, keys, optional region.
	S3Endpoint string
	Bucket     string
	AccessKey  string
	SecretKey  string
	// BackendJuiceFS: the metadata engine URL; its password may be inline or in MetaPassword.
	MetaURL      string
	MetaPassword string
}

// StorageSummary is what the settings page shows of a backend (no secret).
type StorageSummary struct {
	Endpoint string `json:"endpoint,omitempty"`
	Bucket   string `json:"bucket,omitempty"`
	Meta     string `json:"meta,omitempty"`
}

var (
	bucketName  = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-]{1,61}[a-z0-9]$`)
	metaSchemes = map[string]bool{"postgres": true, "postgresql": true, "mysql": true, "redis": true, "rediss": true, "tikv": true, "etcd": true}
	// ErrInvalidBackend wraps a user mistake in the storage settings.
	ErrInvalidBackend = errors.New("存储配置不正确")
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w：%s", ErrInvalidBackend, fmt.Sprintf(format, a...))
}

// Normalize checks a backend and fills in defaults (an empty kind is the platform's).
func (b Backend) Normalize() (Backend, error) {
	b.Kind = strings.TrimSpace(b.Kind)
	switch b.Kind {
	case "", BackendPlatform:
		return Backend{Kind: BackendPlatform}, nil
	case BackendS3:
		return b.normalizeS3()
	case BackendJuiceFS:
		return b.normalizeJuiceFS()
	}
	return b, invalid("未知的存储类型 %q", b.Kind)
}

func (b Backend) normalizeS3() (Backend, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(b.S3Endpoint), "/"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.Path != "" || u.User != nil || u.RawQuery != "" {
		return b, invalid("S3 地址要写成 https://主机[:端口]，不带路径")
	}
	b.S3Endpoint = u.Scheme + "://" + u.Host
	b.Bucket = strings.TrimSpace(b.Bucket)
	if !bucketName.MatchString(b.Bucket) {
		return b, invalid("bucket 名不合法")
	}
	b.AccessKey, b.SecretKey = strings.TrimSpace(b.AccessKey), strings.TrimSpace(b.SecretKey)
	if b.AccessKey == "" || b.SecretKey == "" {
		return b, invalid("请填写 S3 的 Access Key 和 Secret Key")
	}
	return Backend{Kind: BackendS3, S3Endpoint: b.S3Endpoint, Bucket: b.Bucket, AccessKey: b.AccessKey, SecretKey: b.SecretKey}, nil
}

// normalizeJuiceFS moves an inline password into MetaPassword when the URL parses (postgres,
// redis…); a URL that does not (mysql's user:pw@(host)/db) keeps it inline.
func (b Backend) normalizeJuiceFS() (Backend, error) {
	raw := strings.TrimSpace(b.MetaURL)
	scheme, _, ok := strings.Cut(raw, "://")
	if !ok || !metaSchemes[strings.ToLower(scheme)] {
		return b, invalid("元数据地址要以 postgres://、mysql://、redis://、rediss://、tikv:// 或 etcd:// 开头")
	}
	out := Backend{Kind: BackendJuiceFS, MetaURL: raw, MetaPassword: b.MetaPassword}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return out, nil
	}
	if pw, set := u.User.Password(); set {
		if out.MetaPassword == "" {
			out.MetaPassword = pw
		}
		name := u.User.Username()
		u.User = nil
		if name != "" {
			u.User = url.User(name)
		}
		out.MetaURL = u.String()
	}
	return out, nil
}

// Summary is the backend without secrets.
func (b Backend) Summary() StorageSummary {
	switch b.Kind {
	case BackendS3:
		return StorageSummary{Endpoint: b.S3Endpoint, Bucket: b.Bucket}
	case BackendJuiceFS:
		return StorageSummary{Meta: redactMeta(b.MetaURL)}
	}
	return StorageSummary{}
}

// redactMeta shows scheme://host/db of a metadata URL (no user, password or query).
func redactMeta(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		scheme, _, _ := strings.Cut(raw, "://")
		return scheme + "://…"
	}
	return u.Scheme + "://" + u.Host + u.Path
}

// PublicEndpoint refuses an S3 endpoint that resolves to a loopback, private, link-local or
// otherwise internal address: the console itself formats the bucket, so a user must not point it
// at the console's own network.
func PublicEndpoint(ctx context.Context, endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return invalid("S3 地址无法解析")
	}
	host := u.Hostname()
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		return invalid("解析不到 S3 主机 %s", host)
	}
	for _, a := range addrs {
		if internalAddr(a.Unmap()) {
			return invalid("S3 地址 %s 指向内网地址，不能使用", host)
		}
	}
	return nil
}

func internalAddr(a netip.Addr) bool {
	return !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsUnspecified() || a.IsMulticast() || a.IsInterfaceLocalMulticast() || netip.MustParsePrefix("100.64.0.0/10").Contains(a)
}
