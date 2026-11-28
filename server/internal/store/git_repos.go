package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/touwaeriol/agent-teams/server/internal/credvault"
	"github.com/touwaeriol/agent-teams/server/internal/msg"
)

// Standalone Git repositories (git-repos.md §3, data-model.md §2.15): credentials shared by the
// repositories of one host (at_git_credentials) and the repositories (at_git_repos). Nothing here
// returns a credential except GitCredentialSecret / SealedGitCredentials, and only sealed.

var (
	// ErrInUse: the credential or repository is still used (by a repository / a project).
	ErrInUse = errors.New("still in use")
	// ErrHostMismatch: a repository may only use a credential of its own host.
	ErrHostMismatch = msg.Err("凭据和仓库不是同一个主机")
)

// GitCredential is a row of at_git_credentials without its sealed value.
type GitCredential struct {
	ID             string     `json:"id"`
	UserID         string     `json:"-"`
	Name           string     `json:"name"`
	Host           string     `json:"host"`
	Kind           string     `json:"kind"`
	Username       string     `json:"username"`
	HasPassphrase  bool       `json:"has_passphrase"`
	KeyFingerprint string     `json:"key_fingerprint"`
	PublicKey      string     `json:"public_key"`
	Generated      bool       `json:"generated"`
	ExpiresAt      *time.Time `json:"expires_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	CredUpdatedAt  time.Time  `json:"cred_updated_at"`
}

// TableName maps GitCredential to at_git_credentials.
func (GitCredential) TableName() string { return "at_git_credentials" }

const gitCredCols = `id, user_id, name, host, kind, username, has_passphrase, key_fingerprint, public_key, generated,
	expires_at, created_at, updated_at, cred_updated_at`

// GitSealed is a sealed Git credential with what may be shown about it.
type GitSealed struct {
	Sealed         credvault.Sealed
	HasPassphrase  bool
	KeyFingerprint string
	PublicKey      string
	Generated      bool
}

func gitSealedColumns(c GitSealed) map[string]any {
	return map[string]any{
		"cred_ciphertext": c.Sealed.Ciphertext, "cred_nonce": c.Sealed.Nonce, "wrapped_dek": c.Sealed.WrappedDEK,
		"dek_nonce": c.Sealed.DEKNonce, "kek_id": c.Sealed.KEKID, "has_passphrase": c.HasPassphrase,
		"key_fingerprint": c.KeyFingerprint, "public_key": c.PublicKey, "generated": c.Generated,
		"cred_updated_at": gorm.Expr("now()"),
	}
}

func (s *Store) gitCreds(ctx context.Context) *gorm.DB {
	return s.g(ctx).Model(&GitCredential{}).Select(gitCredCols)
}

// CreateGitCredential stores a credential. c.ID must be set (NewID("gc")): the AAD names it.
func (s *Store) CreateGitCredential(ctx context.Context, c GitCredential, sealed GitSealed) (GitCredential, error) {
	if c.ID == "" || c.UserID == "" {
		return GitCredential{}, errors.New("git credential: id and user_id are required")
	}
	row := gitSealedColumns(sealed)
	for k, v := range map[string]any{"id": c.ID, "user_id": c.UserID, "name": c.Name, "host": c.Host, "kind": c.Kind,
		"username": c.Username, "expires_at": c.ExpiresAt} {
		row[k] = v
	}
	if err := s.g(ctx).Table("at_git_credentials").Create(row).Error; err != nil {
		return GitCredential{}, mapErr(err)
	}
	return s.GitCredential(ctx, c.UserID, c.ID)
}

// GitCredentials lists the user's credentials by name.
func (s *Store) GitCredentials(ctx context.Context, userID string) ([]GitCredential, error) {
	out := []GitCredential{}
	err := s.gitCreds(ctx).Where("user_id = ?", userID).Order("name").Find(&out).Error
	return out, err
}

// GitCredential loads one of the user's credentials.
func (s *Store) GitCredential(ctx context.Context, userID, id string) (GitCredential, error) {
	var out GitCredential
	err := s.gitCreds(ctx).Where("id = ? AND user_id = ?", id, userID).Take(&out).Error
	return out, mapErr(err)
}

// GitCredentialSecret is a credential with its sealed value. Never send it to a client.
type GitCredentialSecret struct {
	GitCredential
	Sealed credvault.Sealed
}

type gitCredSecretRow struct {
	GitCredential
	CredCiphertext []byte
	CredNonce      []byte
	WrappedDEK     []byte `gorm:"column:wrapped_dek"`
	DEKNonce       []byte `gorm:"column:dek_nonce"`
	KEKID          string `gorm:"column:kek_id"`
}

func (r gitCredSecretRow) secret() GitCredentialSecret {
	return GitCredentialSecret{GitCredential: r.GitCredential, Sealed: credvault.Sealed{
		Ciphertext: r.CredCiphertext, Nonce: r.CredNonce, WrappedDEK: r.WrappedDEK, DEKNonce: r.DEKNonce, KEKID: r.KEKID}}
}

func (s *Store) gitCredSecrets(ctx context.Context) *gorm.DB {
	return s.g(ctx).Table("at_git_credentials").Select(gitCredCols + `, cred_ciphertext, cred_nonce, wrapped_dek, dek_nonce, kek_id`)
}

// GitCredentialSecret loads one of the user's credentials with its sealed value.
func (s *Store) GitCredentialSecret(ctx context.Context, userID, id string) (GitCredentialSecret, error) {
	var row gitCredSecretRow
	err := s.gitCredSecrets(ctx).Where("id = ? AND user_id = ?", id, userID).Take(&row).Error
	return row.secret(), mapErr(err)
}

// SealedGitCredentials lists every credential of every user, sealed (atctl admin reseal).
func (s *Store) SealedGitCredentials(ctx context.Context) ([]GitCredentialSecret, error) {
	var rows []gitCredSecretRow
	if err := s.gitCredSecrets(ctx).Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]GitCredentialSecret, len(rows))
	for i, r := range rows {
		out[i] = r.secret()
	}
	return out, nil
}

// ResealGitCredential replaces a credential's wrapped DEK (reseal; the ciphertext stays).
func (s *Store) ResealGitCredential(ctx context.Context, id string, sealed credvault.Sealed) error {
	return Changed(s.g(ctx).Table("at_git_credentials").Where("id = ?", id).Updates(map[string]any{
		"wrapped_dek": sealed.WrappedDEK, "dek_nonce": sealed.DEKNonce, "kek_id": sealed.KEKID}))
}

// GitCredentialPatch is a partial update; nil fields stay. The host and the kind never change
// (make a new credential instead).
type GitCredentialPatch struct {
	Name      *string
	Username  *string
	ExpiresAt **time.Time
}

// GitSealFunc seals a replacement for the credential as it will be after the update (its final
// username). It returns nil to keep the current value.
type GitSealFunc func(merged GitCredential) (*GitSealed, error)

// UpdateGitCredential changes a credential and, when seal returns a value, replaces it, in one
// transaction on the locked row. Changing the username without a new value is ErrCredentialRequired.
func (s *Store) UpdateGitCredential(ctx context.Context, userID, id string, p GitCredentialPatch, seal GitSealFunc) (GitCredential, error) {
	var out GitCredential
	err := s.Tx(ctx, func(tx *Store) error {
		var cur GitCredential
		if err := tx.gitCreds(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ?", id, userID).Take(&cur).Error; err != nil {
			return mapErr(err)
		}
		merged := cur
		row := map[string]any{"updated_at": gorm.Expr("now()")}
		bound := false
		if p.Name != nil && *p.Name != cur.Name {
			merged.Name, row["name"] = *p.Name, *p.Name
		}
		if p.Username != nil && *p.Username != cur.Username {
			merged.Username, row["username"] = *p.Username, *p.Username
			bound = true
		}
		if p.ExpiresAt != nil {
			merged.ExpiresAt, row["expires_at"] = *p.ExpiresAt, *p.ExpiresAt
		}
		var sealed *GitSealed
		if seal != nil {
			var err error
			if sealed, err = seal(merged); err != nil {
				return err
			}
		}
		if bound && sealed == nil {
			return ErrCredentialRequired
		}
		if sealed != nil {
			for k, v := range gitSealedColumns(*sealed) {
				row[k] = v
			}
		}
		if err := tx.g(ctx).Table("at_git_credentials").Where("id = ?", id).Updates(row).Error; err != nil {
			return mapErr(err)
		}
		var err error
		out, err = tx.GitCredential(ctx, userID, id)
		return err
	})
	return out, err
}

// DeleteGitCredential removes a credential no repository uses (else ErrInUse).
func (s *Store) DeleteGitCredential(ctx context.Context, userID, id string) error {
	return s.Tx(ctx, func(tx *Store) error {
		var n int64
		if err := tx.g(ctx).Table("at_git_repos").Where("credential_id = ? AND user_id = ?", id, userID).Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return ErrInUse
		}
		return Changed(tx.g(ctx).Exec(`DELETE FROM at_git_credentials WHERE id = ? AND user_id = ?`, id, userID))
	})
}

// GitCredentialUse counts the repositories using each of the user's credentials.
func (s *Store) GitCredentialUse(ctx context.Context, userID string) (map[string]int, error) {
	var rows []struct {
		CredentialID string
		N            int
	}
	err := s.g(ctx).Table("at_git_repos").Select("credential_id, count(*) AS n").
		Where("user_id = ? AND credential_id IS NOT NULL", userID).Group("credential_id").Find(&rows).Error
	out := map[string]int{}
	for _, r := range rows {
		out[r.CredentialID] = r.N
	}
	return out, err
}

// ---- repositories ---------------------------------------------------------------------------

// GitRepo is a row of at_git_repos.
type GitRepo struct {
	ID                 string     `json:"id"`
	UserID             string     `json:"-"`
	Slug               string     `json:"slug"`
	Name               string     `json:"name"`
	URL                string     `json:"url" gorm:"column:url"`
	Transport          string     `json:"transport"`
	Host               string     `json:"host"`
	CredentialID       *string    `json:"credential_id"`
	Provider           string     `json:"provider"`
	WebURL             string     `json:"web_url" gorm:"column:web_url"`
	DefaultBranch      string     `json:"default_branch"`
	Access             string     `json:"access"`
	HostKey            string     `json:"-"`
	HostKeyFP          string     `json:"host_key_fp" gorm:"column:host_key_fp"`
	HostKeyMAC         []byte     `json:"-" gorm:"column:host_key_mac"`
	HostKeyConfirmedAt *time.Time `json:"host_key_confirmed_at"`
	LastCheckedAt      *time.Time `json:"last_checked_at"`
	LastError          string     `json:"last_error"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// TableName maps GitRepo to at_git_repos.
func (GitRepo) TableName() string { return "at_git_repos" }

const gitRepoCols = `id, user_id, slug, name, url, transport, host, credential_id, provider, web_url, default_branch, access,
	host_key, host_key_fp, host_key_mac, host_key_confirmed_at, last_checked_at, last_error, created_at, updated_at`

func (s *Store) gitRepos(ctx context.Context) *gorm.DB {
	return s.g(ctx).Model(&GitRepo{}).Select(gitRepoCols)
}

// checkGitRepoCredential: the credential must be the user's and of the repository's host.
func (s *Store) checkGitRepoCredential(ctx context.Context, userID, host string, credID *string) error {
	if credID == nil {
		return nil
	}
	c, err := s.GitCredential(ctx, userID, *credID)
	if err != nil {
		return err
	}
	if c.Host != host {
		return ErrHostMismatch
	}
	return nil
}

// CreateGitRepo stores a repository (r.ID set by the caller, NewID("gr")). A slug the user already
// has is ErrConflict; a credential of another host is ErrHostMismatch.
func (s *Store) CreateGitRepo(ctx context.Context, r GitRepo) (GitRepo, error) {
	if r.ID == "" || r.UserID == "" {
		return GitRepo{}, errors.New("git repo: id and user_id are required")
	}
	err := s.Tx(ctx, func(tx *Store) error {
		if err := tx.checkGitRepoCredential(ctx, r.UserID, r.Host, r.CredentialID); err != nil {
			return err
		}
		return mapErr(tx.g(ctx).Table("at_git_repos").Create(map[string]any{"id": r.ID, "user_id": r.UserID, "slug": r.Slug,
			"name": r.Name, "url": r.URL, "transport": r.Transport, "host": r.Host, "credential_id": r.CredentialID,
			"provider": r.Provider, "web_url": r.WebURL}).Error)
	})
	if err != nil {
		return GitRepo{}, err
	}
	return s.GitRepo(ctx, r.UserID, r.ID)
}

// GitRepos lists the user's repositories by slug.
func (s *Store) GitRepos(ctx context.Context, userID string) ([]GitRepo, error) {
	out := []GitRepo{}
	err := s.gitRepos(ctx).Where("user_id = ?", userID).Order("slug").Find(&out).Error
	return out, err
}

// GitRepo loads one of the user's repositories.
func (s *Store) GitRepo(ctx context.Context, userID, id string) (GitRepo, error) {
	var out GitRepo
	err := s.gitRepos(ctx).Where("id = ? AND user_id = ?", id, userID).Take(&out).Error
	return out, mapErr(err)
}

// GitRepoPatch is a partial update; nil fields stay. The URL never changes (add another
// repository instead): the slug names it in projects and threads.
type GitRepoPatch struct {
	Name         *string
	CredentialID **string
	Provider     *string
	WebURL       *string
}

// UpdateGitRepo changes a repository.
func (s *Store) UpdateGitRepo(ctx context.Context, userID, id string, p GitRepoPatch) (GitRepo, error) {
	err := s.Tx(ctx, func(tx *Store) error {
		cur, err := tx.GitRepo(ctx, userID, id)
		if err != nil {
			return err
		}
		row := map[string]any{"updated_at": gorm.Expr("now()")}
		if p.Name != nil {
			row["name"] = *p.Name
		}
		if p.Provider != nil {
			row["provider"] = *p.Provider
		}
		if p.WebURL != nil {
			row["web_url"] = *p.WebURL
		}
		if p.CredentialID != nil {
			if err := tx.checkGitRepoCredential(ctx, userID, cur.Host, *p.CredentialID); err != nil {
				return err
			}
			row["credential_id"], row["access"] = *p.CredentialID, ""
		}
		return mapErr(tx.g(ctx).Table("at_git_repos").Where("id = ?", id).Updates(row).Error)
	})
	if err != nil {
		return GitRepo{}, err
	}
	return s.GitRepo(ctx, userID, id)
}

// DeleteGitRepo removes a repository no project uses (else ErrInUse).
func (s *Store) DeleteGitRepo(ctx context.Context, userID, id string) error {
	return s.Tx(ctx, func(tx *Store) error {
		var n int64
		if err := tx.g(ctx).Table("at_project_repos").Where("git_repo_id = ?", id).Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return ErrInUse
		}
		return Changed(tx.g(ctx).Exec(`DELETE FROM at_git_repos WHERE id = ? AND user_id = ?`, id, userID))
	})
}

// GitRepoCheck is the outcome of a test connection or a relay sync.
type GitRepoCheck struct {
	Access        string // '' | read | write ('' keeps the stored value)
	DefaultBranch string // '' keeps the stored value
	Error         string
}

// RecordGitRepoCheck stores the outcome of a check.
func (s *Store) RecordGitRepoCheck(ctx context.Context, id string, c GitRepoCheck) error {
	row := map[string]any{"last_checked_at": gorm.Expr("now()"), "last_error": c.Error}
	if c.Access != "" {
		row["access"] = c.Access
	}
	if c.DefaultBranch != "" {
		row["default_branch"] = c.DefaultBranch
	}
	return Changed(s.g(ctx).Table("at_git_repos").Where("id = ?", id).Updates(row))
}

// PinGitRepoHostKey stores the host key a test saw while the repository still has none; pinned
// reports whether this call stored it.
func (s *Store) PinGitRepoHostKey(ctx context.Context, userID, id, key, fp string, mac []byte) (bool, error) {
	res := s.g(ctx).Table("at_git_repos").Where("id = ? AND user_id = ? AND host_key = ''", id, userID).
		Updates(map[string]any{"host_key": key, "host_key_fp": fp, "host_key_mac": mac, "host_key_confirmed_at": gorm.Expr("now()"),
			"updated_at": gorm.Expr("now()")})
	return res.RowsAffected == 1, mapErr(res.Error)
}

// ResetGitRepoHostKey forgets the pinned host key (the next test pins again).
func (s *Store) ResetGitRepoHostKey(ctx context.Context, userID, id string) (GitRepo, error) {
	err := Changed(s.g(ctx).Table("at_git_repos").Where("id = ? AND user_id = ?", id, userID).Updates(map[string]any{
		"host_key": "", "host_key_fp": "", "host_key_mac": nil, "host_key_confirmed_at": nil, "updated_at": gorm.Expr("now()")}))
	if err != nil {
		return GitRepo{}, err
	}
	return s.GitRepo(ctx, userID, id)
}

// ResealGitRepoHostKey replaces a pinned host key's MAC (reseal).
func (s *Store) ResealGitRepoHostKey(ctx context.Context, id string, mac []byte) error {
	return Changed(s.g(ctx).Table("at_git_repos").Where("id = ?", id).Update("host_key_mac", mac))
}

// AllGitReposWithHostKeys lists every repository with a pinned host key (reseal).
func (s *Store) AllGitReposWithHostKeys(ctx context.Context) ([]GitRepo, error) {
	out := []GitRepo{}
	err := s.gitRepos(ctx).Where("host_key <> ''").Order("id").Find(&out).Error
	return out, err
}

// GitRepoProjects maps each of the user's repositories to the projects using it.
func (s *Store) GitRepoProjects(ctx context.Context, userID string) (map[string][]ProjectRef, error) {
	var rows []struct{ GitRepoID, ID, Name string }
	err := s.g(ctx).Table("at_project_repos r").Select("r.git_repo_id, p.id, p.name").
		Joins("JOIN at_projects p ON p.id = r.project_id").Where("p.user_id = ? AND r.git_repo_id IS NOT NULL", userID).
		Order("p.name").Find(&rows).Error
	out := map[string][]ProjectRef{}
	for _, r := range rows {
		out[r.GitRepoID] = append(out[r.GitRepoID], ProjectRef{ID: r.ID, Name: r.Name})
	}
	return out, err
}

// ProjectUsesGitRepo reports whether the project lists the repository (relay authorisation).
func (s *Store) ProjectUsesGitRepo(ctx context.Context, projectID, repoID string) (bool, error) {
	var n int64
	err := s.g(ctx).Table("at_project_repos").Where("project_id = ? AND git_repo_id = ?", projectID, repoID).Count(&n).Error
	return n > 0, err
}
