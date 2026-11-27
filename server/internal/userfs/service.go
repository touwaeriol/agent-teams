package userfs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/credvault"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Meta creates the volume's PostgreSQL role and schema and reads usage from its metadata.
type Meta interface {
	// Ensure creates the role (login, password) and its schema, or resets the password of an
	// existing one. Idempotent.
	Ensure(ctx context.Context, role, password string) error
	// Usage reads what a folder of the volume holds from its directory quota (zero before the
	// volume was formatted or the folder got its quota).
	Usage(ctx context.Context, schema, dir string) (Usage, error)
}

// Usage is what a folder holds: bytes in use (JuiceFS counts in 4 KiB blocks) and files plus folders.
type Usage struct {
	UsedBytes int64 `json:"used_bytes"`
	Inodes    int64 `json:"inodes"`
}

// IAM creates the volume's object-store account.
type IAM interface {
	// Ensure creates (or resets) the account and attaches a policy allowing only the volume's prefix.
	Ensure(ctx context.Context, n Names, secretKey string, policy []byte) error
}

// Volumes formats the JuiceFS volume, sets folder quotas and runs the gateway over it (the juicefs CLI).
type Volumes interface {
	// Format creates the volume, or updates the settings of an existing one with that name.
	Format(ctx context.Context, f FormatSpec) error
	// Quota sets the capacity of a folder of the volume (path from the root, "/u-<id>").
	Quota(ctx context.Context, metaURL, metaPassword, path string, capacityGB int) error
	// Serve runs the gateway (once per process) and returns the console's root access to it.
	Serve(ctx context.Context, spec GatewaySpec) (Gateway, error)
}

// Gateway is the console's root access to the gateway: the users' buckets (the volume's
// top-level folders) and IAM users.
type Gateway interface {
	// Files is a bucket's tree.
	Files(bucket string) Files
	// MakeBucket creates a bucket (an existing one is fine).
	MakeBucket(ctx context.Context, bucket string) error
	// RemoveBucket deletes a bucket with everything in it (a missing one is fine).
	RemoveBucket(ctx context.Context, bucket string) error
	// Buckets lists the buckets.
	Buckets(ctx context.Context) ([]string, error)
	// EnsureUser creates (or resets) the user's IAM user, allowed its bucket only.
	EnsureUser(ctx context.Context, n UserNames, secretKey string) error
	// RemoveUser deletes the user's IAM user and policy (missing ones are fine).
	RemoveUser(ctx context.Context, n UserNames) error
}

// FormatSpec is one juicefs format.
type FormatSpec struct {
	MetaURL      string
	MetaPassword string
	Volume       string
	Bucket       string
	AccessKey    string
	SecretKey    string
}

// Service provisions the shared volume and the users' folders in it, and hands nodes their mount
// settings.
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
	suspects map[string]bool // folders without a row at the last SweepOrphans

	volMu  sync.Mutex
	shared *shared // the volume, once provisioned by this process
}

// ErrBusy: the folder is being switched on; it can be deleted once that ends.
var ErrBusy = errors.New("共享文件夹正在开启，请稍后再删除")

// New builds the service; meta, iam and volumes nil = the real backends from cfg.
func New(cfg Config, db store.DB, vault *credvault.Vault, meta Meta, iam IAM, volumes Volumes, log *slog.Logger) *Service {
	return &Service{cfg: cfg, db: db, vault: vault, meta: meta, iam: iam, volumes: volumes, log: log}
}

// Enabled tells whether the console can provision volumes.
func (s *Service) Enabled() bool {
	return s != nil && s.cfg.Enabled() && s.vault.Ready() && s.meta != nil && s.iam != nil && s.volumes != nil
}

// Get returns the user's folder.
func (s *Service) Get(ctx context.Context, userID string) (Volume, error) {
	return Get(ctx, s.db, userID)
}

// Usage is what the user's ready folder holds (ErrNotFound when the user has none).
func (s *Service) Usage(ctx context.Context, userID string) (Usage, error) {
	vol, err := s.Get(ctx, userID)
	if err != nil {
		return Usage{}, err
	}
	if vol.State != StateReady {
		return Usage{}, nil
	}
	return s.meta.Usage(ctx, s.cfg.SharedNames().Role, vol.Bucket)
}

// Enable provisions the user's folder (or finishes a provisioning that failed). It returns the
// volume as it stands after this attempt: ready, or error with the reason; provisioning or
// deleting when another call is at it. Only the call that created the row or claimed the failed
// one provisions, and it finishes even when its client goes away.
func (s *Service) Enable(ctx context.Context, userID string) (Volume, error) {
	if !s.Enabled() {
		return Volume{}, ErrDisabled
	}
	ctx = context.WithoutCancel(ctx)
	vol, owned, err := insert(ctx, s.db, s.vault, s.cfg.SharedNames().Volume, userID, s.cfg.CapacityGB)
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
		s.log.Warn("shared folder provisioning failed", "user_id", userID, "bucket", vol.Bucket, "err", err)
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

// provision makes the user's bucket (provisioning the volume and its gateway first when this
// process has not yet), with its projects/ and shared/ folders and its quota, then the IAM user
// allowed that bucket.
func (s *Service) provision(ctx context.Context, vol Volume) error {
	v, err := s.volume(ctx)
	if err != nil {
		return err
	}
	if err := v.gw.MakeBucket(ctx, vol.Bucket); err != nil {
		return fmt.Errorf("创建 bucket：%w", err)
	}
	f := v.gw.Files(vol.Bucket)
	for _, dir := range []string{"projects", SharedDir} {
		if err := f.Mkdir(ctx, dir); err != nil {
			return fmt.Errorf("创建目录 %s：%w", dir, err)
		}
	}
	if err := s.volumes.Quota(ctx, v.metaURL, v.secrets.MetaPassword, "/"+vol.Bucket, vol.CapacityGB); err != nil {
		return fmt.Errorf("目录配额：%w", err)
	}
	sec, err := userSecrets(ctx, s.db, s.vault, vol)
	if err != nil {
		return err
	}
	if err := v.gw.EnsureUser(ctx, vol.Names(), sec.SecretKey); err != nil {
		return fmt.Errorf("网关账号：%w", err)
	}
	return nil
}

// Disable deletes the user's folder (the account is being deleted) with every file in it. It
// returns once the folder is marked deleting and the nodes were told to unmount; the deletion
// itself runs in the background (Wait), so it finishes even when the row goes with the user meanwhile.
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
	s.changed(ctx, userID) // nodes unmount before the data goes
	ctx = context.WithoutCancel(ctx)
	s.bg.Add(1)
	go func() { defer s.bg.Done(); s.finishDisable(ctx, vol) }()
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

func (s *Service) finishDisable(ctx context.Context, vol Volume) {
	if err := s.teardown(ctx, vol); err != nil {
		s.log.Warn("shared folder deletion failed", "user_id", vol.UserID, "bucket", vol.Bucket, "err", err)
		_ = setState(ctx, s.db, vol.UserID, StateError, "删除失败："+err.Error())
		return
	}
	if err := remove(ctx, s.db, vol.UserID); err != nil {
		s.log.Warn("shared folder row not removed", "user_id", vol.UserID, "err", err)
	}
}

// teardown revokes the user's key (every node loses the bucket at once), then deletes the bucket
// once its nodes unmounted.
func (s *Service) teardown(ctx context.Context, vol Volume) error {
	v, err := s.volume(ctx)
	if err != nil {
		return err
	}
	if err := v.gw.RemoveUser(ctx, vol.Names()); err != nil {
		return fmt.Errorf("删除网关账号：%w", err)
	}
	s.waitUnmounted(ctx, vol)
	return s.removeBucket(ctx, vol.Bucket)
}

// removeBucket deletes a user's bucket with everything in it (nothing there is fine).
func (s *Service) removeBucket(ctx context.Context, bucket string) error {
	v, err := s.volume(ctx)
	if err != nil {
		return err
	}
	if err := v.gw.RemoveBucket(ctx, bucket); err != nil {
		return fmt.Errorf("删除 bucket：%w", err)
	}
	return nil
}

// waitUnmounted waits (UnmountWait at most) until no node whose daemon mounts the folder still
// reports it mounted. Nodes mounting it outside the daemon (Docker volume plugin) cannot unmount
// on request: the folder is removed under them, which is logged.
func (s *Service) waitUnmounted(ctx context.Context, vol Volume) {
	deadline := time.Now().Add(orDefault(s.UnmountWait, time.Minute))
	for time.Now().Before(deadline) {
		ids, err := mountedNodes(ctx, s.db, vol.UserID, vol.Bucket, true)
		if err != nil || len(ids) == 0 {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(orDefault(s.UnmountPoll, time.Second)):
		}
	}
	if ids, _ := mountedNodes(ctx, s.db, vol.UserID, vol.Bucket, false); len(ids) > 0 {
		s.log.Warn("shared folder removed while nodes still report it mounted", "user_id", vol.UserID, "bucket", vol.Bucket, "nodes", ids)
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

// MountSpec is the fs.mount for the user's nodes: the user's bucket of the gateway and its key;
// ErrNotFound unless the folder is ready.
func (s *Service) MountSpec(ctx context.Context, userID string) (protocol.FsMount, error) {
	if !s.Enabled() {
		return protocol.FsMount{}, ErrDisabled
	}
	vol, err := s.ready(ctx, userID)
	if err != nil {
		return protocol.FsMount{}, err
	}
	sec, err := userSecrets(ctx, s.db, s.vault, vol)
	if err != nil {
		return protocol.FsMount{}, err
	}
	assets := []protocol.FsMountClientAssetsElem{}
	for _, a := range s.cfg.ClientAssets() {
		assets = append(assets, protocol.FsMountClientAssetsElem{Platform: a.Platform, URL: a.URL, SHA256: a.SHA256})
	}
	return protocol.FsMount{Bucket: vol.Bucket, Generation: vol.Generation, Endpoint: strings.TrimSuffix(s.cfg.GatewayURL, "/"),
		AccessKey: vol.AccessKey, SecretKey: sec.SecretKey,
		Client: protocol.FsMountClient{Tool: protocol.FsMountClientTool(s.cfg.clientTool()), Version: s.cfg.ClientVersion, Assets: assets}}, nil
}

// ready is the user's folder when it is ready (ErrNotFound otherwise).
func (s *Service) ready(ctx context.Context, userID string) (Volume, error) {
	vol, err := Get(ctx, s.db, userID)
	if err != nil {
		return vol, err
	}
	if vol.State != StateReady || vol.Bucket == "" {
		return vol, ErrNotFound
	}
	return vol, nil
}

// Files is the tree of the user's ready bucket (ErrNotFound unless ready), for the console's own
// reads and writes.
func (s *Service) Files(ctx context.Context, userID string) (Files, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	vol, err := s.ready(ctx, userID)
	if err != nil {
		return nil, err
	}
	v, err := s.volume(ctx)
	if err != nil {
		return nil, err
	}
	return v.gw.Files(vol.Bucket), nil
}

// Ready tells whether the user has a ready folder (session.open sends a shared folder only then).
func (s *Service) Ready(ctx context.Context, userID string) bool {
	if s == nil || !s.cfg.Enabled() {
		return false
	}
	vol, err := Get(ctx, s.db, userID)
	return err == nil && vol.State == StateReady
}

// IsNotFound reports ErrNotFound.
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }
