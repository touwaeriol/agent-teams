package userfs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/credvault"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Volume is an at_user_fs row without its secrets: the user's bucket of the shared volume.
type Volume struct {
	UserID     string    `json:"-"`
	Volume     string    `json:"volume"`
	Bucket     string    `json:"bucket"`
	AccessKey  string    `json:"-"`
	Generation int       `json:"generation"`
	State      string    `json:"state"`
	Error      string    `json:"error"`
	CapacityGB int       `json:"capacity_gb"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

const volumeCols = `user_id, volume, bucket, s3_access_key, generation, state, error, capacity_gb, created_at, updated_at`

func scanVolume(row store.Scanner) (Volume, error) {
	var v Volume
	err := row.Scan(&v.UserID, &v.Volume, &v.Bucket, &v.AccessKey, &v.Generation, &v.State, &v.Error, &v.CapacityGB, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}

// Get returns the user's volume (ErrNotFound when none).
func Get(ctx context.Context, q store.DB, userID string) (Volume, error) {
	return scanVolume(store.QueryRow(ctx, q, `SELECT `+volumeCols+` FROM at_user_fs WHERE user_id = $1`, userID))
}

// staleAfter is how long a provisioning or deletion may stand before another attempt may take it
// over (the console stopped in the middle).
const staleAfter = "15 minutes"

// insert creates the user's row (provisioning) unless one exists; created tells whether this call
// made it (and so owns its provisioning). The generation comes from a sequence first, since the
// secret key is sealed with it.
func insert(ctx context.Context, q store.DB, v *credvault.Vault, volume, userID string, capacityGB int) (vol Volume, created bool, err error) {
	var gen int
	if err := store.QueryRow(ctx, q, `SELECT nextval('at_user_fs_generation')`).Scan(&gen); err != nil {
		return Volume{}, false, err
	}
	n := UserNamesFor(userID)
	raw, _ := json.Marshal(UserSecrets{SecretKey: randomSecret(30)})
	sealed, err := v.Seal(credvault.UserFsAAD(userID, n.Bucket, gen), raw)
	if err != nil {
		return Volume{}, false, err
	}
	sj, _ := json.Marshal(sealed)
	vol, err = scanVolume(store.QueryRow(ctx, q, `INSERT INTO at_user_fs (user_id, volume, bucket, s3_access_key, secrets, capacity_gb, generation)
		VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (user_id) DO NOTHING RETURNING `+volumeCols, userID, volume, n.Bucket, n.AccessKey, sj, capacityGB, gen))
	if IsNotFound(err) {
		vol, err = Get(ctx, q, userID)
		return vol, false, err
	}
	return vol, err == nil, err
}

// userSecrets opens the row's sealed secret key.
func userSecrets(ctx context.Context, q store.DB, v *credvault.Vault, vol Volume) (UserSecrets, error) {
	var raw []byte
	if err := store.QueryRow(ctx, q, `SELECT secrets FROM at_user_fs WHERE user_id = $1`, vol.UserID).Scan(&raw); err != nil {
		return UserSecrets{}, err
	}
	var sealed credvault.Sealed
	if err := json.Unmarshal(raw, &sealed); err != nil {
		return UserSecrets{}, err
	}
	plain, err := v.Open(credvault.UserFsAAD(vol.UserID, vol.Bucket, vol.Generation), sealed)
	if err != nil {
		return UserSecrets{}, err
	}
	var s UserSecrets
	return s, json.Unmarshal(plain, &s)
}

// Names are the row's gateway names (its bucket and key as stored, the policy derived).
func (v Volume) Names() UserNames {
	n := namesOf(userBucketID(v.Bucket))
	n.Bucket, n.AccessKey = v.Bucket, v.AccessKey
	return n
}

// claim moves the user's row to state when its current state is one of from, or when it has
// stood in one of stale for staleAfter; ok tells whether this call got it.
func claim(ctx context.Context, q store.DB, userID, state string, from, stale []string) (Volume, bool, error) {
	vol, err := scanVolume(store.QueryRow(ctx, q, `UPDATE at_user_fs SET state = $2, error = '', updated_at = now()
		WHERE user_id = $1 AND (state = ANY($3) OR (state = ANY($4) AND updated_at < now() - $5::interval)) RETURNING `+volumeCols,
		userID, state, from, stale, staleAfter))
	if IsNotFound(err) {
		return vol, false, nil
	}
	return vol, err == nil, err
}

// claimProvisioning takes a failed (or abandoned) provisioning over for another attempt.
func claimProvisioning(ctx context.Context, q store.DB, userID string) (Volume, bool, error) {
	return claim(ctx, q, userID, StateProvisioning, []string{StateError}, []string{StateProvisioning})
}

// claimDeleting starts the deletion of a ready or failed volume (or takes an abandoned one over).
func claimDeleting(ctx context.Context, q store.DB, userID string) (Volume, bool, error) {
	return claim(ctx, q, userID, StateDeleting, []string{StateReady, StateError}, []string{StateProvisioning, StateDeleting})
}

// mountedNodes are the user's nodes whose last fs.status says the folder is mounted (a node's
// status is reset when it connects or drops, so these are online); daemonOnly keeps those whose
// daemon mounts it itself (and so can unmount it).
func mountedNodes(ctx context.Context, q store.DB, userID, bucket string, daemonOnly bool) ([]string, error) {
	var ids []string
	err := store.Raw(ctx, q, `SELECT id FROM at_nodes WHERE user_id = $1 AND fs_status->>'state' = 'mounted'
		AND coalesce(fs_status->>'bucket', $2) = $2 AND (NOT $3 OR fs_status->>'mode' = 'daemon') ORDER BY id`,
		userID, bucket, daemonOnly).Scan(&ids).Error
	return ids, err
}

func setState(ctx context.Context, q store.DB, userID, state, msg string) error {
	_, err := store.Exec(ctx, q, `UPDATE at_user_fs SET state = $2, error = $3, updated_at = now() WHERE user_id = $1`, userID, state, msg)
	return err
}

func remove(ctx context.Context, q store.DB, userID string) error {
	_, err := store.Exec(ctx, q, `DELETE FROM at_user_fs WHERE user_id = $1`, userID)
	return err
}

// retryAfter is how long a failed volume waits before the automatic switch-on tries it again.
const retryAfter = "10 minutes"

// usersToProvision lists users without a volume, with one that failed retryAfter ago, or with a
// provisioning left standing (the console stopped in the middle): the automatic switch-on takes
// them, oldest account first.
func usersToProvision(ctx context.Context, q store.DB, limit int) ([]string, error) {
	var ids []string
	err := q.Gorm(ctx).Raw(`SELECT u.id FROM at_users u LEFT JOIN at_user_fs f ON f.user_id = u.id
		WHERE u.active AND (f.user_id IS NULL
		   OR (f.state = 'error' AND f.updated_at < now() - ?::interval)
		   OR (f.state = 'provisioning' AND f.updated_at < now() - ?::interval))
		ORDER BY u.created_at LIMIT ?`, retryAfter, staleAfter, limit).Scan(&ids).Error
	return ids, err
}
