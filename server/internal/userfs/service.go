package userfs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/credvault"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Meta creates and drops a volume's PostgreSQL role and schema.
type Meta interface {
	// Ensure creates the role (login, password) and its schema, or resets the password of an
	// existing one. Idempotent.
	Ensure(ctx context.Context, role, password string) error
	// Drop removes the schema (with every table) and the role. A missing one is not an error.
	Drop(ctx context.Context, role string) error
}

// IAM creates and removes a volume's object-store account.
type IAM interface {
	// Ensure creates (or resets) the account and attaches a policy allowing only the volume's prefix.
	Ensure(ctx context.Context, n Names, secretKey string, policy []byte) error
	// Remove deletes the account and its policy. Missing ones are not errors.
	Remove(ctx context.Context, n Names) error
}

// Volumes formats and destroys JuiceFS volumes (the juicefs CLI).
type Volumes interface {
	// Format creates the volume, or updates the settings of an existing one with that name.
	Format(ctx context.Context, f FormatSpec) error
	// Destroy deletes the volume's objects and metadata. A volume that was never formatted is not an error.
	Destroy(ctx context.Context, metaURL, metaPassword string) error
}

// FormatSpec is one juicefs format.
type FormatSpec struct {
	MetaURL      string
	MetaPassword string
	Volume       string
	Bucket       string
	AccessKey    string
	SecretKey    string
	CapacityGB   int
}

// Service provisions volumes and hands nodes their mount settings.
type Service struct {
	cfg     Config
	db      store.DB
	vault   *credvault.Vault
	meta    Meta
	iam     IAM
	volumes Volumes
	log     *slog.Logger
	// OnChange runs after a user's volume became ready or was removed (push fs.mount / fs.unmount
	// to the user's nodes).
	OnChange func(ctx context.Context, userID string)
	// CheckEndpoint replaces PublicEndpoint (tests).
	CheckEndpoint func(ctx context.Context, endpoint string) error
}

// New builds the service; meta, iam and volumes nil = the real backends from cfg.
func New(cfg Config, db store.DB, vault *credvault.Vault, meta Meta, iam IAM, volumes Volumes, log *slog.Logger) *Service {
	return &Service{cfg: cfg, db: db, vault: vault, meta: meta, iam: iam, volumes: volumes, log: log}
}

// Enabled tells whether the console can provision volumes.
func (s *Service) Enabled() bool {
	return s != nil && s.cfg.Enabled() && s.vault.Ready() && s.meta != nil && s.iam != nil && s.volumes != nil
}

// Get returns the user's volume.
func (s *Service) Get(ctx context.Context, userID string) (Volume, error) {
	return Get(ctx, s.db, userID)
}

// ErrSwitch: the user already has a folder on another storage; it has to be deleted first (no
// migration between storages).
var ErrSwitch = errors.New("已经有共享文件夹了：换存储要先删除当前的（文件不会迁移）")

// Enable provisions the user's volume on backend b (or finishes a provisioning that failed). It
// returns the volume as it stands after this attempt: ready, or error with the reason. A failed
// volume may be retried with other settings; a ready one must be deleted before switching.
func (s *Service) Enable(ctx context.Context, userID string, b Backend) (Volume, error) {
	if !s.Enabled() {
		return Volume{}, ErrDisabled
	}
	b, err := b.Normalize()
	if err != nil {
		return Volume{}, err
	}
	if b.Kind == BackendS3 {
		if err := s.checkEndpoint(ctx, b.S3Endpoint); err != nil {
			return Volume{}, err
		}
	}
	vol, err := s.current(ctx, userID, b)
	if err != nil {
		return vol, err
	}
	if vol.State == StateReady || vol.State == StateDeleting {
		return vol, nil
	}
	if err := s.provision(ctx, vol); err != nil {
		s.log.Warn("shared folder provisioning failed", "user_id", userID, "volume", vol.Volume, "err", err)
		_ = setState(context.WithoutCancel(ctx), s.db, userID, StateError, err.Error())
		return Get(ctx, s.db, userID)
	}
	if err := setState(ctx, s.db, userID, StateReady, ""); err != nil {
		return vol, err
	}
	s.changed(ctx, userID)
	return Get(ctx, s.db, userID)
}

// capacityFor is a new volume's quota in GiB: the platform's limit on its own storage, none (0)
// when the user brings the storage.
func (s *Service) capacityFor(b Backend) int {
	if b.Kind == BackendPlatform {
		return s.cfg.CapacityGB
	}
	return 0
}

// current is the user's row for backend b: created when missing, replaced when a failed one was
// on other settings (nothing of it was in use), ErrSwitch when a working one is on another backend.
func (s *Service) current(ctx context.Context, userID string, b Backend) (Volume, error) {
	vol, err := Get(ctx, s.db, userID)
	switch {
	case IsNotFound(err):
		return insert(ctx, s.db, s.vault, userID, s.capacityFor(b), b)
	case err != nil:
		return vol, err
	case vol.State == StateError && (vol.Backend != b.Kind || b.Kind != BackendPlatform):
		if err := s.teardown(ctx, vol); err != nil {
			s.log.Warn("shared folder: cleaning up a failed volume", "user_id", userID, "err", err)
		}
		if err := remove(ctx, s.db, userID); err != nil {
			return vol, err
		}
		return insert(ctx, s.db, s.vault, userID, s.capacityFor(b), b)
	case vol.Backend != b.Kind:
		return vol, ErrSwitch
	}
	return vol, nil
}

// checkEndpoint is PublicEndpoint unless a test replaced it.
func (s *Service) checkEndpoint(ctx context.Context, endpoint string) error {
	if s.CheckEndpoint != nil {
		return s.CheckEndpoint(ctx, endpoint)
	}
	return PublicEndpoint(ctx, endpoint)
}

func (s *Service) provision(ctx context.Context, vol Volume) error {
	sec, err := secrets(ctx, s.db, s.vault, vol)
	if err != nil {
		return fmt.Errorf("open secrets: %w", err)
	}
	if vol.Backend == BackendJuiceFS {
		return nil // the user's own volume: nodes check it when they mount
	}
	n := NamesFor(vol.UserID)
	if err := s.meta.Ensure(ctx, n.Role, sec.MetaPassword); err != nil {
		return fmt.Errorf("元数据库：%w", err)
	}
	f := FormatSpec{MetaPassword: sec.MetaPassword, Volume: n.Volume, CapacityGB: vol.CapacityGB,
		Bucket: sec.Bucket, AccessKey: sec.AccessKey, SecretKey: sec.SecretKey}
	if vol.Backend == BackendPlatform {
		if err := s.iam.Ensure(ctx, n, sec.SecretKey, Policy(s.cfg.Bucket, n.Volume)); err != nil {
			return fmt.Errorf("对象存储账号：%w", err)
		}
		f.Bucket, f.AccessKey = s.cfg.BucketURL(), n.AccessKey
	}
	db, err := s.cfg.database()
	if err != nil {
		return err
	}
	f.MetaURL = MetaURL(n.Role, s.cfg.adminHost(), db, "disable")
	if err := s.volumes.Format(ctx, f); err != nil {
		return fmt.Errorf("juicefs format：%w", err)
	}
	return nil
}

// Disable deletes the user's volume: every file in it, its metadata, role and account.
func (s *Service) Disable(ctx context.Context, userID string) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	vol, err := Get(ctx, s.db, userID)
	if err != nil {
		return err
	}
	if err := setState(ctx, s.db, userID, StateDeleting, ""); err != nil {
		return err
	}
	s.changed(ctx, userID) // nodes unmount before the data goes
	if err := s.teardown(ctx, vol); err != nil {
		_ = setState(context.WithoutCancel(ctx), s.db, userID, StateError, "删除失败："+err.Error())
		return err
	}
	return remove(ctx, s.db, userID)
}

// teardown deletes what the console made: a platform volume entirely, a volume in the user's
// bucket with its data (objects under at-<id>/) and metadata; the user's own JuiceFS is only forgotten.
func (s *Service) teardown(ctx context.Context, vol Volume) error {
	if vol.Backend == BackendJuiceFS {
		return nil
	}
	n := NamesFor(vol.UserID)
	sec, err := secrets(ctx, s.db, s.vault, vol)
	if err != nil {
		return fmt.Errorf("open secrets: %w", err)
	}
	db, err := s.cfg.database()
	if err != nil {
		return err
	}
	if err := s.volumes.Destroy(ctx, MetaURL(n.Role, s.cfg.adminHost(), db, "disable"), sec.MetaPassword); err != nil {
		return fmt.Errorf("juicefs destroy：%w", err)
	}
	if vol.Backend == BackendPlatform {
		if err := s.iam.Remove(ctx, n); err != nil {
			return fmt.Errorf("对象存储账号：%w", err)
		}
	}
	return s.meta.Drop(ctx, n.Role)
}

func (s *Service) changed(ctx context.Context, userID string) {
	if s.OnChange != nil {
		s.OnChange(context.WithoutCancel(ctx), userID)
	}
}

// MountSpec is the fs.mount for the user's nodes; ErrNotFound unless the volume is ready.
func (s *Service) MountSpec(ctx context.Context, userID string) (protocol.FsMount, error) {
	if !s.Enabled() {
		return protocol.FsMount{}, ErrDisabled
	}
	vol, err := Get(ctx, s.db, userID)
	if err != nil {
		return protocol.FsMount{}, err
	}
	if vol.State != StateReady {
		return protocol.FsMount{}, ErrNotFound
	}
	sec, err := secrets(ctx, s.db, s.vault, vol)
	if err != nil {
		return protocol.FsMount{}, err
	}
	db, err := s.cfg.database()
	if err != nil {
		return protocol.FsMount{}, err
	}
	n := NamesFor(userID)
	assets := []protocol.FsMountClientAssetsElem{}
	for _, a := range s.cfg.ClientAssets() {
		assets = append(assets, protocol.FsMountClientAssetsElem{Platform: a.Platform, URL: a.URL, SHA256: a.SHA256})
	}
	m := protocol.FsMount{Volume: n.Volume, Generation: vol.Generation, MetaURL: MetaURL(n.Role, s.cfg.MetaHost, db, s.cfg.MetaSSLMode),
		MetaPassword: sec.MetaPassword, AccessKey: n.AccessKey, SecretKey: sec.SecretKey,
		Client: protocol.FsMountClient{Version: s.cfg.ClientVersion, Assets: assets}}
	switch vol.Backend {
	case BackendS3:
		m.AccessKey = sec.AccessKey
	case BackendJuiceFS:
		m.MetaURL, m.AccessKey, m.SecretKey = sec.MetaURL, "", ""
	}
	return m, nil
}

// Ready tells whether the user has a ready volume (session.open sends a shared folder only then).
func (s *Service) Ready(ctx context.Context, userID string) bool {
	if s == nil || !s.cfg.Enabled() {
		return false
	}
	vol, err := Get(ctx, s.db, userID)
	return err == nil && vol.State == StateReady
}

// IsNotFound reports ErrNotFound.
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }
