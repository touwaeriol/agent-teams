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
	"github.com/touwaeriol/agent-teams/server/internal/msg"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Usage is what a user's folder holds.
type Usage struct {
	UsedBytes int64 `json:"used_bytes"`
	Objects   int64 `json:"objects"`
}

// Store is the object store provider: where each user's folder lives (RustFS: a bucket per user
// with a hard quota; B2: a prefix of the platform bucket) and per-user keys limited to it (IAM
// users, B2 application keys).
type Store interface {
	// Folder is where the user's folder lives.
	Folder(n UserNames) Folder
	// HardQuota tells whether the store enforces the capacity itself (otherwise EnforceQuotas does).
	HardQuota() bool
	// Files is a folder's tree, read and written with the platform key.
	Files(f Folder) Files
	// Prepare makes the folder's bucket ready (created, with its quota where the store has one).
	Prepare(ctx context.Context, f Folder, capacityGB int) error
	// Remove deletes the folder with everything in it (a missing one is fine).
	Remove(ctx context.Context, f Folder) error
	// Folders lists the users' folders the store holds.
	Folders(ctx context.Context) ([]Folder, error)
	// Usage counts what a folder holds.
	Usage(ctx context.Context, f Folder) (Usage, error)
	// IssueKey makes a key for the user's folder: list, read and delete, and write when write is set.
	IssueKey(ctx context.Context, n UserNames, f Folder, write bool) (Key, error)
	// RevokeKey deletes a key (a missing one is fine).
	RevokeKey(ctx context.Context, n UserNames, keyID string) error
	// Keys lists the store's keys.
	Keys(ctx context.Context) ([]KeyInfo, error)
}

// Service provisions the users' folders and hands nodes their mount settings.
type Service struct {
	cfg   Config
	db    store.DB
	vault *credvault.Vault
	store Store
	log   *slog.Logger
	// OnChange runs after a user's volume became ready or was removed (push fs.mount / fs.unmount
	// to the user's nodes).
	OnChange func(ctx context.Context, userID string)
	// UnmountWait bounds how long a deletion waits for the nodes to unmount (default 60 s), polling
	// every UnmountPoll (default 1 s).
	UnmountWait, UnmountPoll time.Duration

	bg sync.WaitGroup

	orphanMu sync.Mutex
	suspects map[string]bool // folders and keys without a row at the last SweepOrphans
}

// ErrBusy: the folder is being switched on; it can be deleted once that ends.
var ErrBusy = msg.Err("共享文件夹正在开启，请稍后再删除")

// New builds the service on st (NewB2 or NewRustFS from cfg in production).
func New(cfg Config, db store.DB, vault *credvault.Vault, st Store, log *slog.Logger) *Service {
	return &Service{cfg: cfg, db: db, vault: vault, store: st, log: log}
}

// Enabled tells whether the console can provision folders.
func (s *Service) Enabled() bool {
	return s != nil && s.cfg.Enabled() && s.vault.Ready() && s.store != nil
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
	return s.store.Usage(ctx, vol.Folder())
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
	vol, owned, err := insert(ctx, s.db, s.store.Folder(NamesOf(userID)), userID, s.cfg.CapacityGB)
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
		s.log.Warn("shared folder provisioning failed", "user_id", userID, "prefix", vol.Prefix, "err", err)
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

// provision makes the user's folder with its projects/ and shared/ folders, then a key for it
// (replacing the key of an earlier attempt).
func (s *Service) provision(ctx context.Context, vol Volume) error {
	if err := s.store.Prepare(ctx, vol.Folder(), vol.CapacityGB); err != nil {
		return fmt.Errorf("对象存储 bucket：%w", err)
	}
	f := s.store.Files(vol.Folder())
	for _, dir := range []string{"projects", SharedDir} {
		if err := f.Mkdir(ctx, dir); err != nil {
			return fmt.Errorf("创建目录 %s：%w", dir, err)
		}
	}
	if _, err := s.rotate(ctx, vol, false); err != nil {
		return fmt.Errorf("对象存储密钥：%w", err)
	}
	return nil
}

// rotate issues the volume a new key (one that cannot write when readOnly), records it under a
// new generation and revokes the previous one; it returns the volume as stored. The caller pushes
// the new fs.mount (OnChange).
func (s *Service) rotate(ctx context.Context, vol Volume, readOnly bool) (Volume, error) {
	n := vol.Names()
	key, err := s.store.IssueKey(ctx, n, vol.Folder(), !readOnly)
	if err != nil {
		return vol, err
	}
	stored, err := storeKey(ctx, s.db, s.vault, vol, key, readOnly)
	if err != nil {
		_ = s.store.RevokeKey(ctx, n, key.ID)
		return vol, err
	}
	if vol.AccessKey != "" && vol.AccessKey != key.ID {
		if err := s.store.RevokeKey(ctx, n, vol.AccessKey); err != nil {
			s.log.Warn("shared folder: previous key not revoked", "user_id", vol.UserID, "err", err)
		}
	}
	return stored, nil
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

func (s *Service) finishDisable(ctx context.Context, vol Volume) {
	if err := s.teardown(ctx, vol); err != nil {
		s.log.Warn("shared folder deletion failed", "user_id", vol.UserID, "prefix", vol.Prefix, "err", err)
		_ = setState(ctx, s.db, vol.UserID, StateError, "删除失败："+err.Error())
		return
	}
	if err := remove(ctx, s.db, vol.UserID); err != nil {
		s.log.Warn("shared folder row not removed", "user_id", vol.UserID, "err", err)
	}
}

// teardown revokes the user's key (every node loses the folder at once), then deletes the folder
// once its nodes unmounted.
func (s *Service) teardown(ctx context.Context, vol Volume) error {
	if err := s.store.RevokeKey(ctx, vol.Names(), vol.AccessKey); err != nil {
		return fmt.Errorf("删除对象存储密钥：%w", err)
	}
	s.waitUnmounted(ctx, vol)
	if err := s.store.Remove(ctx, vol.Folder()); err != nil {
		return fmt.Errorf("删除文件夹：%w", err)
	}
	return nil
}

// waitUnmounted waits (UnmountWait at most) until no node whose daemon mounts the folder still
// reports it mounted. Nodes mounting it outside the daemon (AGENT_TEAMS_FS_DIR) cannot unmount
// on request: the folder is removed under them, which is logged.
func (s *Service) waitUnmounted(ctx context.Context, vol Volume) {
	deadline := time.Now().Add(orDefault(s.UnmountWait, time.Minute))
	for time.Now().Before(deadline) {
		ids, err := mountedNodes(ctx, s.db, vol.UserID, vol.Names().Name, true)
		if err != nil || len(ids) == 0 {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(orDefault(s.UnmountPoll, time.Second)):
		}
	}
	if ids, _ := mountedNodes(ctx, s.db, vol.UserID, vol.Names().Name, false); len(ids) > 0 {
		s.log.Warn("shared folder removed while nodes still report it mounted", "user_id", vol.UserID, "prefix", vol.Prefix, "nodes", ids)
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

// MountSpec is the fs.mount for the user's nodes: the user's folder and its key; ErrNotFound
// unless the folder is ready.
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
	region := s.cfg.region()
	assets := []protocol.FsMountClientAssetsElem{}
	for _, a := range s.cfg.ClientAssets() {
		assets = append(assets, protocol.FsMountClientAssetsElem{Platform: a.Platform, URL: a.URL, SHA256: a.SHA256})
	}
	return protocol.FsMount{Name: vol.Names().Name, Bucket: vol.Bucket, Prefix: vol.Prefix, Generation: vol.Generation, Endpoint: s.cfg.nodeEndpoint(), Region: &region,
		AccessKey: vol.AccessKey, SecretKey: sec.SecretKey,
		Client: protocol.FsMountClient{Tool: protocol.FsMountClientTool(s.cfg.clientTool()), Version: s.cfg.ClientVersion, Assets: assets}}, nil
}

// ready is the user's folder when it is ready (ErrNotFound otherwise).
func (s *Service) ready(ctx context.Context, userID string) (Volume, error) {
	vol, err := Get(ctx, s.db, userID)
	if err != nil {
		return vol, err
	}
	if vol.State != StateReady || vol.AccessKey == "" {
		return vol, ErrNotFound
	}
	return vol, nil
}

// Files is the tree of the user's ready folder (ErrNotFound unless ready), for the console's own
// reads and writes.
func (s *Service) Files(ctx context.Context, userID string) (Files, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	vol, err := s.ready(ctx, userID)
	if err != nil {
		return nil, err
	}
	return s.store.Files(vol.Folder()), nil
}

// Ready tells whether the user has a ready folder (session.open sends a shared folder only then).
func (s *Service) Ready(ctx context.Context, userID string) bool {
	if s == nil || !s.cfg.Enabled() {
		return false
	}
	vol, err := Get(ctx, s.db, userID)
	return err == nil && vol.State == StateReady
}

// SoftQuota tells whether the capacity is enforced by EnforceQuotas (the store has no quota).
func (s *Service) SoftQuota() bool { return s.Enabled() && !s.store.HardQuota() }

// IsNotFound reports ErrNotFound.
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }
