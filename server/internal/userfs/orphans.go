package userfs

import (
	"context"
	"fmt"
	"strings"
)

// SweepOrphans destroys the volumes left without an owner: a volume role and schema whose
// at_user_fs row is gone (the user was deleted and the teardown that followed failed or was cut
// short). A role is destroyed only when it was already ownerless at the previous sweep, so a
// teardown still running is not raced. The volume is destroyed with the admin credentials: the
// role gets a new password, the volume's objects and metadata go (juicefs destroy), then its
// object-store account and its role. It returns how many it destroyed.
func (s *Service) SweepOrphans(ctx context.Context) (int, error) {
	if !s.Enabled() {
		return 0, nil
	}
	orphans, err := s.orphanRoles(ctx)
	if err != nil {
		return 0, err
	}
	s.orphanMu.Lock()
	seen := s.suspects
	s.suspects = map[string]bool{}
	for _, r := range orphans {
		s.suspects[r] = true
	}
	s.orphanMu.Unlock()
	done := 0
	for _, role := range orphans {
		if !seen[role] || ctx.Err() != nil {
			continue
		}
		if err := s.destroyOrphan(ctx, role); err != nil {
			s.log.Warn("shared folder: ownerless volume not destroyed", "role", role, "err", err)
			continue
		}
		s.log.Info("shared folder: ownerless volume destroyed", "role", role)
		done++
	}
	return done, nil
}

// orphanRoles are the metadata roles no at_user_fs row names.
func (s *Service) orphanRoles(ctx context.Context) ([]string, error) {
	roles, err := s.meta.Roles(ctx)
	if err != nil || len(roles) == 0 {
		return nil, err
	}
	var owned []string
	if err := s.db.Gorm(ctx).Raw(`SELECT meta_role FROM at_user_fs WHERE meta_role IN ?`, roles).Scan(&owned).Error; err != nil {
		return nil, err
	}
	has := map[string]bool{}
	for _, r := range owned {
		has[r] = true
	}
	var out []string
	for _, r := range roles {
		if !has[r] {
			out = append(out, r)
		}
	}
	return out, nil
}

// namesOfRole is the names of the volume whose metadata role is role (they share the id).
func namesOfRole(role string) Names {
	id := strings.TrimPrefix(role, "jfs_")
	return Names{Volume: "at-" + id, Role: role, AccessKey: "jfs" + id, Policy: "jfs-" + id}
}

func (s *Service) destroyOrphan(ctx context.Context, role string) error {
	n := namesOfRole(role)
	password := randomSecret(24)
	if err := s.meta.Ensure(ctx, role, password); err != nil {
		return fmt.Errorf("元数据库：%w", err)
	}
	db, err := s.cfg.database()
	if err != nil {
		return err
	}
	s.volumes.Close(n.Volume)
	if err := s.volumes.Destroy(ctx, metaURL(role, s.cfg.adminHost(), db, s.cfg.adminTLS()), password); err != nil {
		return fmt.Errorf("juicefs destroy：%w", err)
	}
	if err := s.iam.Remove(ctx, n); err != nil {
		return fmt.Errorf("对象存储账号：%w", err)
	}
	return s.meta.Drop(ctx, role)
}
