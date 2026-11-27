package userfs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

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
	// Exists tells whether the role and its schema are both there (a volume whose metadata was
	// never created, or already dropped, has nothing for juicefs destroy to work on).
	Exists(ctx context.Context, role string) (bool, error)
	// Usage reads the volume's space and file counters from its metadata (zero before the volume
	// was formatted).
	Usage(ctx context.Context, role string) (Usage, error)
	// Roles lists the volume roles that have a schema (jfs_<16 hex>).
	Roles(ctx context.Context) ([]string, error)
}

// Usage is what a volume holds: bytes in use (JuiceFS counts in 4 KiB blocks) and files plus folders.
type Usage struct {
	UsedBytes int64 `json:"used_bytes"`
	Inodes    int64 `json:"inodes"`
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
	// UnmountWait bounds how long a deletion waits for the nodes to unmount (default 60 s), polling
	// every UnmountPoll (default 1 s).
	UnmountWait, UnmountPoll time.Duration

	bg sync.WaitGroup

	orphanMu sync.Mutex
	suspects map[string]bool // roles without a volume row at the last SweepOrphans
}

// ErrBusy: the volume is being switched on; it can be deleted once that ends.
var ErrBusy = errors.New("共享文件夹正在开启，请稍后再删除")

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

// Usage is what the user's ready volume holds (ErrNotReady-style errors from Files: not found, not ready).
func (s *Service) Usage(ctx context.Context, userID string) (Usage, error) {
	vol, err := s.Get(ctx, userID)
	if err != nil {
		return Usage{}, err
	}
	if vol.State != StateReady {
		return Usage{}, nil
	}
	return s.meta.Usage(ctx, vol.MetaRole)
}

// Enable provisions the user's volume (or finishes a provisioning that failed). It returns the
// volume as it stands after this attempt: ready, or error with the reason; provisioning or
// deleting when another call is at it. Only the call that created the row or claimed the failed
// one provisions, and it finishes even when its client goes away.
func (s *Service) Enable(ctx context.Context, userID string) (Volume, error) {
	if !s.Enabled() {
		return Volume{}, ErrDisabled
	}
	ctx = context.WithoutCancel(ctx)
	vol, owned, err := insert(ctx, s.db, s.vault, userID, s.cfg.CapacityGB)
	if err == nil && !owned {
		var claimed Volume
		if claimed, owned, err = claimProvisioning(ctx, s.db, userID); owned {
			vol = claimed
		}
	}
	if err != nil || !owned {
		return vol, err
	}
	if err := s.provision(ctx, vol); err != nil {
		s.log.Warn("shared folder provisioning failed", "user_id", userID, "volume", vol.Volume, "err", err)
		_ = setState(ctx, s.db, userID, StateError, err.Error())
		return Get(ctx, s.db, userID)
	}
	if err := setState(ctx, s.db, userID, StateReady, ""); err != nil {
		return vol, err
	}
	s.changed(ctx, userID)
	return Get(ctx, s.db, userID)
}

// ProvisionAll switches the shared folder on for every active user who lacks a ready one (the
// folder is on by default): new accounts, failed attempts after retryAfter, provisionings left
// standing. It stops between users when ctx ends.
// It returns how many volumes became ready.
func (s *Service) ProvisionAll(ctx context.Context) (int, error) {
	if !s.Enabled() {
		return 0, nil
	}
	ids, err := usersToProvision(ctx, s.db, 50)
	if err != nil {
		return 0, err
	}
	ready := 0
	for _, id := range ids {
		if ctx.Err() != nil {
			return ready, ctx.Err()
		}
		if vol, err := s.Enable(ctx, id); err == nil && vol.State == StateReady {
			ready++
		}
	}
	return ready, nil
}

func (s *Service) provision(ctx context.Context, vol Volume) error {
	metaURL, sec, err := s.consoleMeta(ctx, vol)
	if err != nil {
		return err
	}
	n := vol.Names()
	if err := s.meta.Ensure(ctx, n.Role, sec.MetaPassword); err != nil {
		return fmt.Errorf("元数据库：%w", err)
	}
	if err := s.iam.Ensure(ctx, n, sec.SecretKey, Policy(s.cfg.Bucket, n.Volume)); err != nil {
		return fmt.Errorf("对象存储账号：%w", err)
	}
	f := FormatSpec{MetaURL: metaURL, MetaPassword: sec.MetaPassword, Volume: n.Volume, CapacityGB: vol.CapacityGB,
		Bucket: s.cfg.BucketURL(), AccessKey: n.AccessKey, SecretKey: sec.SecretKey}
	if err := s.volumes.Format(ctx, f); err != nil {
		return fmt.Errorf("juicefs format：%w", err)
	}
	return nil
}

// consoleMeta is the volume's metadata URL as the console reaches it (AdminURL's host and TLS
// settings) and its opened secrets.
func (s *Service) consoleMeta(ctx context.Context, vol Volume) (string, Secrets, error) {
	sec, err := secrets(ctx, s.db, s.vault, vol)
	if err != nil {
		return "", sec, fmt.Errorf("open secrets: %w", err)
	}
	db, err := s.cfg.database()
	if err != nil {
		return "", sec, err
	}
	return metaURL(vol.MetaRole, s.cfg.adminHost(), db, s.cfg.adminTLS()), sec, nil
}

// Disable deletes the user's volume (the account is being deleted): every file in it, its
// metadata, role and account. It returns once the volume is marked deleting and the nodes were
// told to unmount; the deletion itself runs in the background (Wait) from the secrets read now,
// so it finishes even when the row goes with the user meanwhile.
func (s *Service) Disable(ctx context.Context, userID string) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	vol, ok, err := claimDeleting(ctx, s.db, userID)
	if err != nil {
		return err
	}
	if !ok {
		return s.notClaimed(ctx, userID)
	}
	metaURL, sec, err := s.consoleMeta(ctx, vol)
	if err != nil {
		_ = setState(ctx, s.db, userID, StateError, "删除失败："+err.Error())
		return err
	}
	s.changed(ctx, userID) // nodes unmount before the data goes
	ctx = context.WithoutCancel(ctx)
	s.bg.Add(1)
	go func() { defer s.bg.Done(); s.finishDisable(ctx, vol, metaURL, sec.MetaPassword) }()
	return nil
}

// notClaimed explains why a deletion did not start: no volume, one being deleted already (fine),
// or one being switched on.
func (s *Service) notClaimed(ctx context.Context, userID string) error {
	vol, err := Get(ctx, s.db, userID)
	switch {
	case err != nil:
		return err
	case vol.State == StateDeleting:
		return nil
	default:
		return ErrBusy
	}
}

// Wait blocks until the deletions in progress have ended.
func (s *Service) Wait() { s.bg.Wait() }

// Close stops what the volume backends keep running (the console's juicefs gateways), at shutdown.
func (s *Service) Close() {
	if s == nil {
		return
	}
	if c, ok := s.volumes.(interface{ Shutdown() }); ok {
		c.Shutdown()
	}
}

func (s *Service) finishDisable(ctx context.Context, vol Volume, metaURL, password string) {
	if err := s.teardown(ctx, vol, metaURL, password); err != nil {
		s.log.Warn("shared folder deletion failed", "user_id", vol.UserID, "volume", vol.Volume, "err", err)
		_ = setState(ctx, s.db, vol.UserID, StateError, "删除失败："+err.Error())
		return
	}
	if err := remove(ctx, s.db, vol.UserID); err != nil {
		s.log.Warn("shared folder row not removed", "user_id", vol.UserID, "err", err)
	}
}

// teardown deletes the volume once its nodes unmounted: its data and metadata, the account and
// the role.
func (s *Service) teardown(ctx context.Context, vol Volume, metaURL, password string) error {
	s.waitUnmounted(ctx, vol)
	s.volumes.Close(vol.Volume)
	if err := s.destroy(ctx, vol, metaURL, password); err != nil {
		return err
	}
	n := vol.Names()
	if err := s.iam.Remove(ctx, n); err != nil {
		return fmt.Errorf("对象存储账号：%w", err)
	}
	return s.meta.Drop(ctx, n.Role)
}

// destroy runs juicefs destroy unless the volume's metadata is not there (never created, or
// already dropped by an earlier attempt).
func (s *Service) destroy(ctx context.Context, vol Volume, metaURL, password string) error {
	exists, err := s.meta.Exists(ctx, vol.MetaRole)
	if err != nil {
		return fmt.Errorf("元数据库：%w", err)
	}
	if !exists {
		return nil
	}
	if err := s.volumes.Destroy(ctx, metaURL, password); err != nil {
		return fmt.Errorf("juicefs destroy：%w", err)
	}
	return nil
}

// waitUnmounted waits (UnmountWait at most) until no node whose daemon mounts the volume still
// reports it mounted. Nodes mounting it outside the daemon (Docker volume plugin) cannot unmount
// on request: the volume is destroyed under them, which is logged.
func (s *Service) waitUnmounted(ctx context.Context, vol Volume) {
	deadline := time.Now().Add(orDefault(s.UnmountWait, time.Minute))
	for time.Now().Before(deadline) {
		ids, err := mountedNodes(ctx, s.db, vol.UserID, vol.Volume, true)
		if err != nil || len(ids) == 0 {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(orDefault(s.UnmountPoll, time.Second)):
		}
	}
	if ids, _ := mountedNodes(ctx, s.db, vol.UserID, vol.Volume, false); len(ids) > 0 {
		s.log.Warn("shared folder destroyed while nodes still report it mounted", "user_id", vol.UserID, "volume", vol.Volume, "nodes", ids)
	}
}

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
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
	assets := []protocol.FsMountClientAssetsElem{}
	for _, a := range s.cfg.ClientAssets() {
		assets = append(assets, protocol.FsMountClientAssetsElem{Platform: a.Platform, URL: a.URL, SHA256: a.SHA256})
	}
	return protocol.FsMount{Volume: vol.Volume, Generation: vol.Generation, MetaURL: MetaURL(vol.MetaRole, s.cfg.MetaHost, db, s.cfg.MetaSSLMode),
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
	metaURL, sec, err := s.consoleMeta(ctx, vol)
	if err != nil {
		return nil, err
	}
	return s.volumes.Files(ctx, vol.Volume, metaURL, sec.MetaPassword)
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
