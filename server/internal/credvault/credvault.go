// Package credvault is the envelope encryption of remote server credentials and of the SSH
// gateway's own keys (remote-servers.md §11.1, scheme B).
//
// Every sealed value gets a fresh random 32-byte DEK; the plaintext is AES-256-GCM under the DEK
// with an AAD naming where the value belongs (ServerAAD, SettingAAD), and the DEK is AES-256-GCM
// under a KEK with the same AAD plus the KEK id. KEKs are derived from AGENT_TEAMS_SECRET_KEYS
// (HKDF-SHA256, one per key, the first seals and every one opens), so rotating the master key only
// re-wraps DEKs (Rewrap, atctl admin reseal) and never touches plaintext.
//
// What the AAD buys: a sealed credential copied to another row, or left in place after the row's
// host, port, username or auth kind changed, no longer opens (Open fails with ErrOpen).
package credvault

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
)

const (
	kekInfo = "agent-teams/remote-cred-kek/v1"
	macInfo = "agent-teams/remote-hostkey-mac/v1"
	dekSize = 32
)

var (
	// ErrNoKey means AGENT_TEAMS_SECRET_KEYS is not configured.
	ErrNoKey = errors.New("服务没有配置 AGENT_TEAMS_SECRET_KEYS，不能保存远程服务器凭证")
	// ErrOpen means the value does not open: no configured key wrapped it, or it was sealed for
	// another context (row moved, host/port/username/auth kind changed, tampered).
	ErrOpen = errors.New("无法解开已保存的凭证：主密钥变过，或凭证不属于这台服务器的当前配置")
)

// Sealed is one envelope-encrypted value as stored (at_remote_servers' cred_* columns, or the
// sealed field of an at_settings key). []byte fields marshal to base64 in JSON.
type Sealed struct {
	Ciphertext []byte `json:"ciphertext"`
	Nonce      []byte `json:"nonce"`
	WrappedDEK []byte `json:"wrapped_dek"`
	DEKNonce   []byte `json:"dek_nonce"`
	KEKID      string `json:"kek_id"`
}

type kek struct {
	id     string
	aead   cipher.AEAD
	macKey []byte
}

// Vault seals with the first key and opens with any.
type Vault struct {
	keks []kek
}

// New builds a vault from the master secrets (config.SecretKeys); empty strings are skipped. With
// none it is not Ready.
func New(secrets ...string) (*Vault, error) {
	v := &Vault{}
	for _, s := range secrets {
		if s == "" {
			continue
		}
		key, err := hkdf.Key(sha256.New, []byte(s), nil, kekInfo, 32)
		if err != nil {
			return nil, fmt.Errorf("derive kek: %w", err)
		}
		mac, err := hkdf.Key(sha256.New, []byte(s), nil, macInfo, 32)
		if err != nil {
			return nil, fmt.Errorf("derive mac key: %w", err)
		}
		a, err := newAEAD(key)
		if err != nil {
			return nil, err
		}
		v.keks = append(v.keks, kek{id: KEKID(key), aead: a, macKey: mac})
	}
	return v, nil
}

// KEKID is the id of a derived KEK: the first 8 bytes of its SHA-256, hex.
func KEKID(key []byte) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:8])
}

// Ready reports whether a master key is configured.
func (v *Vault) Ready() bool { return v != nil && len(v.keks) > 0 }

// CurrentKEKID is the id of the KEK that seals (reseal skips values already under it).
func (v *Vault) CurrentKEKID() string {
	if !v.Ready() {
		return ""
	}
	return v.keks[0].id
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func random(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

func wrapAAD(aad []byte, kekID string) []byte {
	out := make([]byte, 0, len(aad)+len(kekID)+5)
	out = append(out, aad...)
	out = append(out, "|kek:"...)
	return append(out, kekID...)
}

// Seal encrypts plaintext for the context aad with a new DEK wrapped by the first KEK.
func (v *Vault) Seal(aad, plaintext []byte) (Sealed, error) {
	if !v.Ready() {
		return Sealed{}, ErrNoKey
	}
	dek := random(dekSize)
	defer clear(dek)
	a, err := newAEAD(dek)
	if err != nil {
		return Sealed{}, err
	}
	s := Sealed{Nonce: random(a.NonceSize())}
	s.Ciphertext = a.Seal(nil, s.Nonce, plaintext, aad)
	if err := v.wrap(&s, aad, dek); err != nil {
		return Sealed{}, err
	}
	return s, nil
}

func (v *Vault) wrap(s *Sealed, aad, dek []byte) error {
	k := v.keks[0]
	s.KEKID = k.id
	s.DEKNonce = random(k.aead.NonceSize())
	s.WrappedDEK = k.aead.Seal(nil, s.DEKNonce, dek, wrapAAD(aad, k.id))
	return nil
}

// unwrap returns the DEK, unwrapped by the configured KEK s.KEKID names (the id is part of the
// wrapping AAD, so a changed kek_id never opens).
func (v *Vault) unwrap(s Sealed, aad []byte) ([]byte, error) {
	if !v.Ready() {
		return nil, ErrNoKey
	}
	try := func(k kek) ([]byte, bool) {
		if len(s.DEKNonce) != k.aead.NonceSize() {
			return nil, false
		}
		dek, err := k.aead.Open(nil, s.DEKNonce, s.WrappedDEK, wrapAAD(aad, s.KEKID))
		return dek, err == nil && len(dek) == dekSize
	}
	for _, k := range v.keks {
		if k.id == s.KEKID {
			if dek, ok := try(k); ok {
				return dek, nil
			}
		}
	}
	return nil, ErrOpen
}

// Open decrypts s for the context aad.
func (v *Vault) Open(aad []byte, s Sealed) ([]byte, error) {
	dek, err := v.unwrap(s, aad)
	if err != nil {
		return nil, err
	}
	defer clear(dek)
	a, err := newAEAD(dek)
	if err != nil {
		return nil, err
	}
	if len(s.Nonce) != a.NonceSize() {
		return nil, ErrOpen
	}
	out, err := a.Open(nil, s.Nonce, s.Ciphertext, aad)
	if err != nil {
		return nil, ErrOpen
	}
	return out, nil
}

// Rewrap re-wraps the DEK of s with the first KEK (atctl admin reseal); the ciphertext stays.
// changed is false when s is already under the first KEK.
func (v *Vault) Rewrap(aad []byte, s Sealed) (out Sealed, changed bool, err error) {
	if !v.Ready() {
		return s, false, ErrNoKey
	}
	if s.KEKID == v.keks[0].id {
		// Still check it opens, so reseal reports values that are already broken.
		if _, err := v.unwrap(s, aad); err != nil {
			return s, false, err
		}
		return s, false, nil
	}
	dek, err := v.unwrap(s, aad)
	if err != nil {
		return s, false, err
	}
	defer clear(dek)
	out = s
	if err := v.wrap(&out, aad, dek); err != nil {
		return s, false, err
	}
	return out, true, nil
}

// MAC is HMAC-SHA256 of data under the first key's MAC key (host keys the user confirmed: a
// host key written straight into the table without a valid MAC is not trusted).
func (v *Vault) MAC(data []byte) ([]byte, error) {
	if !v.Ready() {
		return nil, ErrNoKey
	}
	return mac(v.keks[0].macKey, data), nil
}

// VerifyMAC reports whether sum is the MAC of data under any configured key; current is true when
// it is under the first one (reseal recomputes the others).
func (v *Vault) VerifyMAC(data, sum []byte) (ok, current bool) {
	if !v.Ready() || len(sum) == 0 {
		return false, false
	}
	for i, k := range v.keks {
		if hmac.Equal(mac(k.macKey, data), sum) {
			return true, i == 0
		}
	}
	return false, false
}

func mac(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

// fields joins a domain tag and length-prefixed fields, so no field value can shift another.
func fields(tag string, vals ...string) []byte {
	var b bytes.Buffer
	b.WriteString(tag)
	for _, v := range vals {
		b.WriteByte('|')
		b.WriteString(strconv.Itoa(len(v)))
		b.WriteByte(':')
		b.WriteString(v)
	}
	return b.Bytes()
}

// ServerAAD is the AAD of a remote server's credential: user, server id, auth kind, host, port and
// username. Changing any of them means the credential must be entered again.
func ServerAAD(userID, serverID, authKind, host string, port int, username string) []byte {
	return fields("at-remote-cred:v1", userID, serverID, authKind, host, strconv.Itoa(port), username)
}

// SettingAAD is the AAD of a sealed at_settings value (the session CA, the gateway host key).
func SettingAAD(key string) []byte { return fields("at-setting:v1", key) }

// HostKeyMACData is what the MAC of a confirmed host key covers: the server, where it lives and
// the key itself.
func HostKeyMACData(serverID, host string, port int, hostKey string) []byte {
	return fields("at-remote-hostkey:v1", serverID, host, strconv.Itoa(port), hostKey)
}

// GitCredAAD is the AAD of a standalone Git repository credential (git-repos.md §4.1): user,
// credential id, kind, host and username. Changing any of them means entering it again, so a
// database edit that points the credential at another host cannot make the console send it there.
func GitCredAAD(userID, credID, kind, host, username string) []byte {
	return fields("at-git-cred:v1", userID, credID, kind, host, username)
}

// GitHostKeyMACData is what the MAC of a Git repository's pinned SSH host key covers.
func GitHostKeyMACData(repoID, host, hostKey string) []byte {
	return fields("at-git-hostkey:v1", repoID, host, hostKey)
}
