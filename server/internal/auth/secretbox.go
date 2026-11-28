package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/fernet/fernet-go"
	"github.com/touwaeriol/agent-teams/server/internal/msg"
)

// ErrNoSecret means AGENT_TEAMS_SECRET_KEY is not configured.
var ErrNoSecret = msg.Err("服务没有配置 AGENT_TEAMS_SECRET_KEY，不能保存 Git 凭证")

// ErrUnseal means a stored value cannot be decrypted with any configured key.
var ErrUnseal = msg.Err("无法解密已保存的凭证：AGENT_TEAMS_SECRET_KEY 变过？")

// SecretBox is v1's SecretBox: Fernet keyed by urlsafe_b64(sha256(secret)).
// With several secrets (AGENT_TEAMS_SECRET_KEYS=new,old) the first seals and
// every one opens, so keys can be rotated.
type SecretBox struct {
	secrets []string
	keys    []*fernet.Key
}

// NewSecretBox builds a box; with no secrets it is not Ready.
func NewSecretBox(secrets ...string) *SecretBox {
	box := &SecretBox{}
	for _, s := range secrets {
		if s == "" {
			continue
		}
		box.secrets = append(box.secrets, s)
		box.keys = append(box.keys, fernetKey(s))
	}
	return box
}

func fernetKey(secret string) *fernet.Key {
	sum := sha256.Sum256([]byte(secret))
	var k fernet.Key
	copy(k[:], sum[:])
	return &k
}

// FernetKeyString is the urlsafe base64 key v1 derives (for tests and tooling).
func FernetKeyString(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return base64.URLEncoding.EncodeToString(sum[:])
}

// Ready reports whether a secret is configured.
func (b *SecretBox) Ready() bool { return len(b.keys) > 0 }

// Seal encrypts with the first key.
func (b *SecretBox) Seal(plaintext string) (string, error) {
	if !b.Ready() {
		return "", ErrNoSecret
	}
	tok, err := fernet.EncryptAndSign([]byte(plaintext), b.keys[0])
	if err != nil {
		return "", fmt.Errorf("seal: %w", err)
	}
	return string(tok), nil
}

// Open decrypts with any configured key. Fernet tokens never expire here (ttl 0), like v1.
func (b *SecretBox) Open(ciphertext string) (string, error) {
	if !b.Ready() {
		return "", ErrNoSecret
	}
	msg := fernet.VerifyAndDecrypt([]byte(ciphertext), time.Duration(0), b.keys)
	if msg == nil {
		return "", ErrUnseal
	}
	return string(msg), nil
}

// Reseal re-encrypts a value with the first key (atctl admin reseal).
func (b *SecretBox) Reseal(ciphertext string) (string, error) {
	plain, err := b.Open(ciphertext)
	if err != nil {
		return "", err
	}
	return b.Seal(plain)
}

// Derive is v1's derive(label) = sha256("label:secret") for the sealing secret.
func (b *SecretBox) Derive(label string) []byte {
	return b.DeriveAll(label)[0]
}

// DeriveAll derives the label key for every configured secret (first = current).
func (b *SecretBox) DeriveAll(label string) [][]byte {
	secrets := b.secrets
	if len(secrets) == 0 {
		secrets = []string{""}
	}
	out := make([][]byte, len(secrets))
	for i, s := range secrets {
		sum := sha256.Sum256([]byte(label + ":" + s))
		out[i] = sum[:]
	}
	return out
}
