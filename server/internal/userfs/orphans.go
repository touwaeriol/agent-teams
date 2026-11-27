package userfs

import (
	"context"
)

// SweepOrphans removes the buckets left without an owner: a u-<id> bucket no at_user_fs row names
// (the user was deleted and the removal that followed failed or was cut short), with its IAM user.
// A bucket is removed only when it was already ownerless at the previous sweep, so a removal or a
// provisioning still running is not raced. It returns how many it removed.
func (s *Service) SweepOrphans(ctx context.Context) (int, error) {
	if !s.Enabled() {
		return 0, nil
	}
	v, err := s.volume(ctx)
	if err != nil {
		return 0, err
	}
	orphans, err := s.orphanBuckets(ctx, v.gw)
	if err != nil {
		return 0, err
	}
	s.orphanMu.Lock()
	seen := s.suspects
	s.suspects = map[string]bool{}
	for _, b := range orphans {
		s.suspects[b] = true
	}
	s.orphanMu.Unlock()
	done := 0
	for _, bucket := range orphans {
		if !seen[bucket] || ctx.Err() != nil {
			continue
		}
		n := namesOf(userBucketID(bucket))
		if err := v.gw.RemoveUser(ctx, n); err != nil {
			s.log.Warn("shared folder: ownerless key not removed", "bucket", bucket, "err", err)
			continue
		}
		if err := s.removeBucket(ctx, bucket); err != nil {
			s.log.Warn("shared folder: ownerless bucket not removed", "bucket", bucket, "err", err)
			continue
		}
		s.log.Info("shared folder: ownerless bucket removed", "bucket", bucket)
		done++
	}
	return done, nil
}

// orphanBuckets are the users' buckets that no at_user_fs row names (other buckets are left alone).
func (s *Service) orphanBuckets(ctx context.Context, gw Gateway) ([]string, error) {
	all, err := gw.Buckets(ctx)
	if err != nil {
		return nil, err
	}
	var buckets []string
	for _, b := range all {
		if userBucketID(b) != "" {
			buckets = append(buckets, b)
		}
	}
	if len(buckets) == 0 {
		return nil, nil
	}
	var owned []string
	if err := s.db.Gorm(ctx).Raw(`SELECT bucket FROM at_user_fs WHERE bucket IN ?`, buckets).Scan(&owned).Error; err != nil {
		return nil, err
	}
	has := map[string]bool{}
	for _, b := range owned {
		has[b] = true
	}
	var out []string
	for _, b := range buckets {
		if !has[b] {
			out = append(out, b)
		}
	}
	return out, nil
}
