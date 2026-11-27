package userfs

import "context"

// QuotaResult is what a quota round changed.
type QuotaResult struct {
	Checked, Locked, Unlocked, Failed int
}

// EnforceQuotas is the soft capacity limit (the object store has no quota of its own): it counts
// every ready folder with a capacity, switches one over it to a key that cannot write (reading
// and deleting still work, so the user can make room) and one locked that is back under it to a
// full key again; the nodes get the new key in fs.mount. It stops between folders when ctx ends.
func (s *Service) EnforceQuotas(ctx context.Context) (QuotaResult, error) {
	var r QuotaResult
	if !s.Enabled() {
		return r, nil
	}
	vols, err := readyVolumes(ctx, s.db)
	if err != nil {
		return r, err
	}
	for _, vol := range vols {
		if ctx.Err() != nil {
			return r, ctx.Err()
		}
		r.Checked++
		s.enforce(ctx, vol, &r)
	}
	return r, nil
}

func (s *Service) enforce(ctx context.Context, vol Volume, r *QuotaResult) {
	u, err := s.store.Usage(ctx, vol.Prefix)
	if err != nil {
		r.Failed++
		s.log.Warn("shared folder: usage not counted", "user_id", vol.UserID, "err", err)
		return
	}
	over := u.UsedBytes > int64(vol.CapacityGB)<<30
	if over == vol.ReadOnly {
		return
	}
	if _, err := s.rotate(ctx, vol, over); err != nil {
		r.Failed++
		s.log.Warn("shared folder: key not switched", "user_id", vol.UserID, "read_only", over, "err", err)
		return
	}
	if over {
		r.Locked++
	} else {
		r.Unlocked++
	}
	s.log.Info("shared folder capacity", "user_id", vol.UserID, "used_bytes", u.UsedBytes, "capacity_gb", vol.CapacityGB, "read_only", over)
	s.changed(ctx, vol.UserID)
}
