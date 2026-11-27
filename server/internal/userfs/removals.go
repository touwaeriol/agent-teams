package userfs

import (
	"context"
	"errors"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// removalBatch is the most failed removals one RetryRemovals run takes.
const removalBatch = 50

// Remove deletes path (a file, or a folder with everything in it) from the user's volume. Nothing
// to do when the user has no ready volume or the path is not there. Any other failure is recorded,
// so RetryRemovals tries again, and returned.
func (s *Service) Remove(ctx context.Context, userID, path string) error {
	if s == nil {
		return nil
	}
	err := s.remove(ctx, userID, path)
	if err == nil {
		return nil
	}
	if _, rerr := store.Exec(ctx, s.db, `INSERT INTO at_fs_removals (user_id, path, error) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, path) DO UPDATE SET error = EXCLUDED.error, updated_at = now()`, userID, path, err.Error()); rerr != nil {
		return errors.Join(err, rerr)
	}
	return err
}

// remove is one attempt: nil when it is gone or there is no volume to remove it from.
func (s *Service) remove(ctx context.Context, userID, path string) error {
	f, err := s.Files(ctx, userID)
	if Unavailable(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := f.Remove(ctx, path); err != nil && !errors.Is(err, ErrNoFile) {
		return err
	}
	return nil
}

// RetryRemovals tries the recorded failed removals again (oldest first) and returns how many are
// done; a row whose removal fails again stays, with its attempt counted.
func (s *Service) RetryRemovals(ctx context.Context) (int, error) {
	if !s.Enabled() {
		return 0, nil
	}
	var rows []struct{ UserID, Path string }
	if err := s.db.Gorm(ctx).Raw(`SELECT user_id, path FROM at_fs_removals ORDER BY updated_at LIMIT ?`, removalBatch).
		Scan(&rows).Error; err != nil {
		return 0, err
	}
	done := 0
	for _, r := range rows {
		if ctx.Err() != nil {
			return done, ctx.Err()
		}
		if err := s.remove(ctx, r.UserID, r.Path); err != nil {
			_, _ = store.Exec(ctx, s.db, `UPDATE at_fs_removals SET attempts = attempts + 1, error = $3, updated_at = now()
				WHERE user_id = $1 AND path = $2`, r.UserID, r.Path, err.Error())
			continue
		}
		if _, err := store.Exec(ctx, s.db, `DELETE FROM at_fs_removals WHERE user_id = $1 AND path = $2`, r.UserID, r.Path); err != nil {
			return done, err
		}
		done++
	}
	return done, nil
}
