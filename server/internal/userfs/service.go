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
	// Files opens the volume's tree for the console (no mount).
	Files(ctx context.Context, volume, metaURL, metaPassword string) (Files, error)
	// Close releases what Files holds for the volume (before it is destroyed).
	Close(volume string)
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

// Enable provisions the user's volume (or finishes a provisioning that failed). It returns the
// volume as it stands after this attempt: ready, or error with the reason.
func (s *Service) Enable(ctx context.Context, userID string) (Volume, error) {
	if !s.Enabled() {
		return Volume{}, ErrDisabled
	}
	vol, err := insert(ctx, s.db, s.vault, userID, s.cfg.CapacityGB)
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

func (s *Service) provision(ctx context.Context, vol Volume) error {
	sec, err := secrets(ctx, s.db, s.vault, vol)
	if err != nil {
		return fmt.Errorf("open secrets: %w", err)
	}
	n := NamesFor(vol.UserID)
	if err := s.meta.Ensure(ctx, n.Role, sec.MetaPassword); err != nil {
		return fmt.Errorf("元数据库：%w", err)
	}
	if err := s.iam.Ensure(ctx, n, sec.SecretKey, Policy(s.cfg.Bucket, n.Volume)); err != nil {
		return fmt.Errorf("对象存储账号：%w", err)
	}
	f := FormatSpec{MetaPassword: sec.MetaPassword, Volume: n.Volume, CapacityGB: vol.CapacityGB,
		Bucket: s.cfg.BucketURL(), AccessKey: n.AccessKey, SecretKey: sec.SecretKey}
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
	s.volumes.Close(vol.Volume)
	if err := s.teardown(ctx, vol); err != nil {
		_ = setState(context.WithoutCancel(ctx), s.db, userID, StateError, "删除失败："+err.Error())
		return err
	}
	return remove(ctx, s.db, userID)
}

// teardown deletes the volume: its data and metadata, the account and the role.
func (s *Service) teardown(ctx context.Context, vol Volume) error {
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
	if err := s.iam.Remove(ctx, n); err != nil {
		return fmt.Errorf("对象存储账号：%w", err)
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
	return protocol.FsMount{Volume: n.Volume, Generation: vol.Generation, MetaURL: MetaURL(n.Role, s.cfg.MetaHost, db, s.cfg.MetaSSLMode),
		MetaPassword: sec.MetaPassword, Client: protocol.FsMountClient{Version: s.cfg.ClientVersion, Assets: assets}}, nil
}

// Files is the tree of the user's ready volume (ErrNotFound unless ready), for the console's
// own reads and writes.
func (s *Service) Files(ctx context.Context, userID string) (Files, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	vol, err := Get(ctx, s.db, userID)
	if err != nil {
		return nil, err
	}
	if vol.State != StateReady {
		return nil, ErrNotFound
	}
	sec, err := secrets(ctx, s.db, s.vault, vol)
	if err != nil {
		return nil, err
	}
	db, err := s.cfg.database()
	if err != nil {
		return nil, err
	}
	return s.volumes.Files(ctx, vol.Volume, MetaURL(vol.MetaRole, s.cfg.adminHost(), db, "disable"), sec.MetaPassword)
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
