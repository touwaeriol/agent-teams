package delivery

import (
	"testing"
	"time"
)

// TestCredExpiry: an installation token reports its own expiry; a PAT (no expiry) patTTL from now.
func TestCredExpiry(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.FixedZone("x", 3600))
	real := now.Add(17 * time.Minute)
	if got := credExpiry(real, now); !got.Equal(real) || got.Location() != time.UTC {
		t.Fatalf("installation token expiry %v", got)
	}
	if got := credExpiry(time.Time{}, now); !got.Equal(now.Add(patTTL)) {
		t.Fatalf("PAT expiry %v", got)
	}
}
