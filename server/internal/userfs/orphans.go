package userfs

import (
	"context"
	"errors"
	"path"
)

// SweepOrphans removes the folders left without an owner: a users/<id> folder no at_user_fs row
// names (the user was deleted and the removal that followed failed or was cut short). A folder is
// removed only when it was already ownerless at the previous sweep, so a removal or a provisioning
// still running is not raced. It returns how many it removed.
func (s *Service) SweepOrphans(ctx context.Context) (int, error) {
	if !s.Enabled() {
		return 0, nil
	}
	orphans, err := s.orphanDirs(ctx)
	if err != nil {
		return 0, err
	}
	s.orphanMu.Lock()
	seen := s.suspects
	s.suspects = map[string]bool{}
	for _, d := range orphans {
		s.suspects[d] = true
	}
	s.orphanMu.Unlock()
	done := 0
	for _, dir := range orphans {
		if !seen[dir] || ctx.Err() != nil {
			continue
		}
		if err := s.removeDir(ctx, dir); err != nil {
			s.log.Warn("shared folder: ownerless folder not removed", "subdir", dir, "err", err)
			continue
		}
		s.log.Info("shared folder: ownerless folder removed", "subdir", dir)
		done++
	}
	return done, nil
}

// orphanDirs are the folders under users/ that no at_user_fs row names.
func (s *Service) orphanDirs(ctx context.Context) ([]string, error) {
	f, err := s.sharedFiles(ctx)
	if err != nil {
		return nil, err
	}
	entries, err := f.List(ctx, UsersDir)
	if errors.Is(err, ErrNoFile) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, e := range entries {
		if e.Dir {
			dirs = append(dirs, path.Join(UsersDir, e.Name))
		}
	}
	if len(dirs) == 0 {
		return nil, nil
	}
	var owned []string
	if err := s.db.Gorm(ctx).Raw(`SELECT subdir FROM at_user_fs WHERE subdir IN ?`, dirs).Scan(&owned).Error; err != nil {
		return nil, err
	}
	has := map[string]bool{}
	for _, d := range owned {
		has[d] = true
	}
	var out []string
	for _, d := range dirs {
		if !has[d] {
			out = append(out, d)
		}
	}
	return out, nil
}
