package userfs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/touwaeriol/agent-teams/server/internal/credvault"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// SettingKey is the at_settings key of the shared volume's credentials.
const SettingKey = "user_fs_volume"

// shared is the provisioned volume: its names, credentials and metadata URL as the console reaches it.
type shared struct {
	names   Names
	secrets Secrets
	metaURL string
}

// sharedSetting is at_settings[SettingKey].
type sharedSetting struct {
	Volume  string           `json:"volume"`
	Secrets credvault.Sealed `json:"secrets"`
}

func settingAAD(volume string) []byte { return credvault.SettingAAD(SettingKey + "|" + volume) }

// sharedSecrets opens the volume's credentials, creating them the first time (the first writer
// wins; every replica then reads the same ones).
func (s *Service) sharedSecrets(ctx context.Context) (Secrets, error) {
	volume := s.cfg.SharedNames().Volume
	if sec, ok, err := s.readSecrets(ctx, volume); err != nil || ok {
		return sec, err
	}
	raw, _ := json.Marshal(NewSecrets())
	sealed, err := s.vault.Seal(settingAAD(volume), raw)
	if err != nil {
		return Secrets{}, err
	}
	val, _ := json.Marshal(sharedSetting{Volume: volume, Secrets: sealed})
	if _, err := store.Exec(ctx, s.db, `INSERT INTO at_settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO NOTHING`, SettingKey, val); err != nil {
		return Secrets{}, err
	}
	sec, ok, err := s.readSecrets(ctx, volume)
	if err == nil && !ok {
		err = fmt.Errorf("%s names another volume than %s", SettingKey, volume)
	}
	return sec, err
}

// readSecrets opens the stored credentials; ok is false when there are none. Credentials of another
// volume name are an error (renaming the volume would hand nodes an unformatted one).
func (s *Service) readSecrets(ctx context.Context, volume string) (Secrets, bool, error) {
	var raw []byte
	err := store.QueryRow(ctx, s.db, `SELECT value FROM at_settings WHERE key = $1`, SettingKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Secrets{}, false, nil
	}
	if err != nil {
		return Secrets{}, false, err
	}
	var st sharedSetting
	if err := json.Unmarshal(raw, &st); err != nil {
		return Secrets{}, false, err
	}
	if st.Volume != volume {
		return Secrets{}, false, fmt.Errorf("%s is for volume %q, AGENT_TEAMS_FS_VOLUME says %q", SettingKey, st.Volume, volume)
	}
	plain, err := s.vault.Open(settingAAD(volume), st.Secrets)
	if err != nil {
		return Secrets{}, false, err
	}
	var sec Secrets
	return sec, true, json.Unmarshal(plain, &sec)
}

// volume provisions the shared volume once per process (again after a failure): the metadata role
// and schema, the object-store account limited to the volume's prefix, juicefs format.
func (s *Service) volume(ctx context.Context) (shared, error) {
	s.volMu.Lock()
	defer s.volMu.Unlock()
	if s.shared != nil {
		return *s.shared, nil
	}
	sec, err := s.sharedSecrets(ctx)
	if err != nil {
		return shared{}, fmt.Errorf("共享卷凭据：%w", err)
	}
	n := s.cfg.SharedNames()
	if err := s.meta.Ensure(ctx, n.Role, sec.MetaPassword); err != nil {
		return shared{}, fmt.Errorf("元数据库：%w", err)
	}
	if err := s.iam.Ensure(ctx, n, sec.SecretKey, Policy(s.cfg.Bucket, n.Volume)); err != nil {
		return shared{}, fmt.Errorf("对象存储账号：%w", err)
	}
	db, err := s.cfg.database()
	if err != nil {
		return shared{}, err
	}
	v := shared{names: n, secrets: sec, metaURL: metaURL(s.cfg.metaUser(), n.Role, s.cfg.adminHost(), db, s.cfg.adminTLS())}
	f := FormatSpec{MetaURL: v.metaURL, MetaPassword: sec.MetaPassword, Volume: n.Volume, Bucket: s.cfg.BucketURL(),
		AccessKey: n.AccessKey, SecretKey: sec.SecretKey}
	if err := s.volumes.Format(ctx, f); err != nil {
		return shared{}, fmt.Errorf("juicefs format：%w", err)
	}
	s.shared = &v
	return v, nil
}

// sharedFiles is the whole volume's tree (the console's own use: folders of every user).
func (s *Service) sharedFiles(ctx context.Context) (Files, error) {
	v, err := s.volume(ctx)
	if err != nil {
		return nil, err
	}
	return s.volumes.Files(ctx, v.names.Volume, v.metaURL, v.secrets.MetaPassword)
}

// subFiles is a folder of the volume as a tree of its own: paths are cleaned and joined under dir,
// so none leaves it.
type subFiles struct {
	f   Files
	dir string
}

func (s subFiles) path(p string) (string, error) { return Join(s.dir, p) }

func (s subFiles) List(ctx context.Context, dir string) ([]Entry, error) {
	p, err := s.path(dir)
	if err != nil {
		return nil, err
	}
	return s.f.List(ctx, p)
}

func (s subFiles) Open(ctx context.Context, name string) (io.ReadCloser, Entry, error) {
	p, err := s.path(name)
	if err != nil {
		return nil, Entry{}, err
	}
	return s.f.Open(ctx, p)
}

func (s subFiles) Put(ctx context.Context, name string, r io.Reader, size int64) error {
	p, err := s.path(name)
	if err != nil {
		return err
	}
	return s.f.Put(ctx, p, r, size)
}

func (s subFiles) Mkdir(ctx context.Context, dir string) error {
	p, err := s.path(dir)
	if err != nil {
		return err
	}
	return s.f.Mkdir(ctx, p)
}

// Remove refuses the folder's root: deleting a user's whole folder is Disable's.
func (s subFiles) Remove(ctx context.Context, name string) error {
	p, err := s.path(name)
	if err != nil {
		return err
	}
	if p == s.dir {
		return ErrBadPath
	}
	return s.f.Remove(ctx, p)
}
