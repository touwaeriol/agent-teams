package gitrelay

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

// platformRef are branches only the platform moves: thread branches and merge queue trials
// (members push thread/<n>/<name>, which this does not match).
var platformRef = regexp.MustCompile(`^refs/heads/(thread/[0-9]+(-[^/]*)?|mq/.*)$`)

// Token modes: what a relay token lets git do (git-repos.md §5.2).
const (
	ModeRead     = "read"     // fetch only (prepare / status jobs)
	ModePush     = "push"     // fetch and update the one ref of a git.push job
	ModeEmployee = "employee" // lead / member sessions: fetch and push branches outside Deny
)

// Claims is what a relay token carries. It is minted for one repository and one caller by
// cred.request (whose ownership checks already ran) and lives TokenTTL.
type Claims struct {
	Repo string   `json:"r"`
	User string   `json:"u"`
	Mode string   `json:"m"`
	Ref  string   `json:"f,omitempty"` // push: the only ref it may update (refs/heads/…)
	Deny []string `json:"d,omitempty"` // employee: refs it may not update or delete
	Exp  int64    `json:"e"`
}

// TokenTTL is how long a relay token is valid; the daemon asks again after it.
const TokenTTL = time.Hour

const tokenPrefix = "atgr1."

var errToken = errors.New("bad relay token")

func macInput(payload string) []byte { return []byte("git-relay\x00" + payload) }

// Sign mints a token for c (Exp is set from now when zero).
func (r *Relay) Sign(c Claims) (string, time.Time, error) {
	if c.Exp == 0 {
		c.Exp = r.now().Add(TokenTTL).Unix()
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return "", time.Time{}, err
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	sum, err := r.Vault.MAC(macInput(payload))
	if err != nil {
		return "", time.Time{}, err
	}
	return tokenPrefix + payload + "." + base64.RawURLEncoding.EncodeToString(sum), time.Unix(c.Exp, 0).UTC(), nil
}

// Verify checks a token's MAC (any of the vault's keys) and expiry.
func (r *Relay) Verify(tok string) (Claims, error) {
	rest, ok := strings.CutPrefix(tok, tokenPrefix)
	if !ok {
		return Claims{}, errToken
	}
	payload, sig, ok := strings.Cut(rest, ".")
	if !ok {
		return Claims{}, errToken
	}
	sum, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return Claims{}, errToken
	}
	if ok, _ := r.Vault.VerifyMAC(macInput(payload), sum); !ok {
		return Claims{}, errToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return Claims{}, errToken
	}
	var c Claims
	if err := json.Unmarshal(raw, &c); err != nil || c.Repo == "" || c.User == "" {
		return Claims{}, errToken
	}
	if r.now().Unix() >= c.Exp {
		return Claims{}, errors.New("relay token expired")
	}
	return c, nil
}

// allows reports whether the token may set ref (refs/heads/… only; no tags or other namespaces).
func (c Claims) allows(ref string) bool {
	if !strings.HasPrefix(ref, "refs/heads/") {
		return false
	}
	switch c.Mode {
	case ModePush:
		return ref == c.Ref
	case ModeEmployee:
		if platformRef.MatchString(ref) {
			return false
		}
		for _, d := range c.Deny {
			if ref == d {
				return false
			}
		}
		return true
	}
	return false
}
