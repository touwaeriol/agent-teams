package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// State purposes, identical to v1 (github_app_routes.py).
const (
	PurposeCreate  = "github-app-create"
	PurposeInstall = "github-app-install"
	stateLifetime  = time.Hour
)

// StateSigner signs the OAuth "state" that ties a GitHub redirect to the user
// who started it: "<user_id>.<issued>.<hmac[:32]>", keyed by derive("oauth-state").
// States signed by v1 verify here and the other way round.
type StateSigner struct {
	keys [][]byte
	now  func() time.Time
}

// NewStateSigner derives the signing keys from the SecretBox.
func NewStateSigner(box *SecretBox) *StateSigner {
	return &StateSigner{keys: box.DeriveAll("oauth-state"), now: time.Now}
}

// Sign returns a state for purpose and user.
func (s *StateSigner) Sign(purpose, userID string) string {
	issued := s.now().Unix()
	return fmt.Sprintf("%s.%d.%s", userID, issued, mac(s.keys[0], purpose, userID, issued))
}

// Verify returns the user id of a valid, fresh state, or "".
func (s *StateSigner) Verify(purpose, state string) string {
	parts := strings.Split(state, ".")
	if len(parts) != 3 || !allDigits(parts[1]) {
		return ""
	}
	issued, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || s.now().Unix()-issued >= int64(stateLifetime/time.Second) {
		return ""
	}
	for _, key := range s.keys {
		if hmac.Equal([]byte(mac(key, purpose, parts[0], issued)), []byte(parts[2])) {
			return parts[0]
		}
	}
	return ""
}

func mac(key []byte, purpose, userID string, issued int64) string {
	h := hmac.New(sha256.New, key)
	fmt.Fprintf(h, "%s:%s:%d", purpose, userID, issued)
	return hex.EncodeToString(h.Sum(nil))[:32]
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
