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
	folders, keys, err := s.orphans(ctx)
	if err != nil {
		return 0, err
	}
	s.orphanMu.Lock()
	seen := s.suspects
	s.suspects = map[string]bool{}
	for _, f := range folders {
		s.suspects["f:"+f.Bucket+":"+f.Prefix] = true
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
	for _, f := range folders {
		if !seen["f:"+f.Bucket+":"+f.Prefix] || ctx.Err() != nil {
			continue
		}
		if err := s.store.Remove(ctx, f); err != nil {
			s.log.Warn("shared folder: ownerless folder not removed", "bucket", f.Bucket, "prefix", f.Prefix, "err", err)
			continue
		}
		s.log.Info("shared folder: ownerless folder removed", "bucket", f.Bucket, "prefix", f.Prefix)
		done++
	}
	return done, nil
}

// orphans are the users' folders and keys the store holds that no row names.
func (s *Service) orphans(ctx context.Context) ([]Folder, []KeyInfo, error) {
	var rows []struct{ Bucket, Prefix, S3AccessKey string }
	if err := s.db.Gorm(ctx).Raw(`SELECT bucket, prefix, s3_access_key FROM at_user_fs`).Scan(&rows).Error; err != nil {
		return nil, nil, err
	}
	ownedFolder, ownedKey, ownedName := map[Folder]bool{}, map[string]bool{}, map[string]bool{}
	for _, r := range rows {
		f := Folder{Bucket: r.Bucket, Prefix: r.Prefix}
		ownedFolder[f] = true
		ownedKey[r.S3AccessKey] = true
		ownedName[namesOf(folderID(f)).Name] = true
	}
	all, err := s.store.Folders(ctx)
	if err != nil {
		return nil, nil, err
	}
	var folders []Folder
	for _, f := range all {
		if folderID(f) != "" && !ownedFolder[f] {
			folders = append(folders, f)
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
	return folders, keys, nil
}

// pendingKey: on RustFS a user's key id is its fixed IAM user name, recorded only once issued; a
// row still provisioning owns it already.
func (s *Service) pendingKey(k KeyInfo, ownedName map[string]bool) bool {
	return k.ID == k.Name && ownedName[namesOf(keyID(k.Name)).Name]
}
