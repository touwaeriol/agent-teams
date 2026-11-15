// Package artifacts stores the files employees publish with publish_file (versioned
// deliverable files) and serves them on an isolated origin (architecture.md §6.5):
// either AGENT_TEAMS_ARTIFACT_ORIGIN, or /files/ on the console with a CSP sandbox, always
// through short-lived signed URLs so the session cookie is never needed.
package artifacts

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// URLLifetime is how long a signed file URL works.
const URLLifetime = 2 * time.Hour

// Signer signs "<deliverable_id>.<version>.<expires>.<mac>" tokens.
type Signer struct {
	keys [][]byte
	now  func() time.Time
}

// NewSigner uses the first key to sign and all of them to verify (key rotation).
func NewSigner(keys [][]byte) *Signer { return &Signer{keys: keys, now: time.Now} }

// Sign returns a token for one version of a deliverable and when it expires.
func (s *Signer) Sign(deliverableID string, version int) (string, time.Time) {
	exp := s.now().Add(URLLifetime).Truncate(time.Second)
	return fmt.Sprintf("%s.%d.%d.%s", deliverableID, version, exp.Unix(), s.mac(s.keys[0], deliverableID, version, exp.Unix())), exp
}

// Verify returns the deliverable and version of a valid, unexpired token.
func (s *Signer) Verify(token string) (string, int, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 4 {
		return "", 0, false
	}
	version, err1 := strconv.Atoi(parts[1])
	exp, err2 := strconv.ParseInt(parts[2], 10, 64)
	if err1 != nil || err2 != nil || version < 1 || s.now().Unix() >= exp {
		return "", 0, false
	}
	for _, key := range s.keys {
		if hmac.Equal([]byte(s.mac(key, parts[0], version, exp)), []byte(parts[3])) {
			return parts[0], version, true
		}
	}
	return "", 0, false
}

func (s *Signer) mac(key []byte, id string, version int, exp int64) string {
	h := hmac.New(sha256.New, key)
	fmt.Fprintf(h, "artifact:%s:%d:%d", id, version, exp)
	return hex.EncodeToString(h.Sum(nil))[:32]
}
