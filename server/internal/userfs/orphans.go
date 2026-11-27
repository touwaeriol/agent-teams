package userfs

import (
	"context"
)

// SweepOrphans removes what is left without an owner: a user's folder or key no at_user_fs row
// names (the user was deleted and the removal that followed failed or was cut short, or a
// provisioning issued a key and stopped before recording it). Each is removed only when it was
// already ownerless at the previous sweep, so a removal or a provisioning still running is not
// raced. Other prefixes and keys of the store are left alone. It returns how many it removed.
func (s *Service) SweepOrphans(ctx context.Context) (int, error) {
	if !s.Enabled() {
		return 0, nil
	}
	prefixes, keys, err := s.orphans(ctx)
	if err != nil {
		return 0, err
	}
	s.orphanMu.Lock()
	seen := s.suspects
	s.suspects = map[string]bool{}
	for _, p := range prefixes {
		s.suspects["p:"+p] = true
	}
	for _, k := range keys {
		s.suspects["k:"+k.ID] = true
	}
	s.orphanMu.Unlock()
	done := 0
	for _, k := range keys {
		if !seen["k:"+k.ID] || ctx.Err() != nil {
			continue
		}
		if err := s.store.RevokeKey(ctx, namesOf(keyID(k.Name)), k.ID); err != nil {
			s.log.Warn("shared folder: ownerless key not revoked", "key", k.Name, "err", err)
			continue
		}
		s.log.Info("shared folder: ownerless key revoked", "key", k.Name)
		done++
	}
	for _, p := range prefixes {
		if !seen["p:"+p] || ctx.Err() != nil {
			continue
		}
		if err := s.store.RemovePrefix(ctx, p); err != nil {
			s.log.Warn("shared folder: ownerless folder not removed", "prefix", p, "err", err)
			continue
		}
		s.log.Info("shared folder: ownerless folder removed", "prefix", p)
		done++
	}
	return done, nil
}

// orphans are the users' folders in the bucket and the users' keys that no row names.
func (s *Service) orphans(ctx context.Context) ([]string, []KeyInfo, error) {
	var rows []struct{ Prefix, S3AccessKey string }
	if err := s.db.Gorm(ctx).Raw(`SELECT prefix, s3_access_key FROM at_user_fs WHERE bucket = ?`, s.cfg.Bucket).Scan(&rows).Error; err != nil {
		return nil, nil, err
	}
	ownedPrefix, ownedKey, ownedName := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, r := range rows {
		ownedPrefix[r.Prefix] = true
		ownedKey[r.S3AccessKey] = true
		ownedName[namesOf(prefixID(r.Prefix)).Name] = true
	}
	all, err := s.store.Prefixes(ctx)
	if err != nil {
		return nil, nil, err
	}
	var prefixes []string
	for _, p := range all {
		if prefixID(p) != "" && !ownedPrefix[p] {
			prefixes = append(prefixes, p)
		}
	}
	allKeys, err := s.store.Keys(ctx)
	if err != nil {
		return nil, nil, err
	}
	var keys []KeyInfo
	for _, k := range allKeys {
		if keyID(k.Name) != "" && !ownedKey[k.ID] && !s.pendingKey(k, ownedName) {
			keys = append(keys, k)
		}
	}
	return prefixes, keys, nil
}

// pendingKey: on RustFS a user's key id is its fixed IAM user name, recorded only once issued; a
// row still provisioning owns it already.
func (s *Service) pendingKey(k KeyInfo, ownedName map[string]bool) bool {
	return k.ID == k.Name && ownedName[namesOf(keyID(k.Name)).Name]
}
