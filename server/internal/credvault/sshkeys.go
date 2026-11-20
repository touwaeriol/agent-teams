package credvault

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// at_settings keys of the platform's own SSH keys (remote-servers.md §4.1), both Ed25519 and
// independent of each other.
const (
	SessionCAKey      = "ssh_session_ca"       // signs session certificates; only the gateway trusts it
	GatewayHostKeyKey = "ssh_gateway_host_key" // the gateway's host key towards node ssh clients
)

// SettingStore is the part of store.Store EnsureKey needs.
type SettingStore interface {
	Setting(ctx context.Context, key string, dest any) (bool, error)
	SaveSetting(ctx context.Context, key string, value any) error
	InsertSettingIfAbsent(ctx context.Context, key string, value any) (bool, error)
}

// SealedKey is the at_settings value of an SSH key: the public half in clear, the private half
// (OpenSSH PEM) sealed with SettingAAD(key).
type SealedKey struct {
	ID          string    `json:"id"`          // fingerprint without the SHA256: prefix (certificate ca_id)
	PublicKey   string    `json:"public_key"`  // authorized_keys line
	Fingerprint string    `json:"fingerprint"` // SHA256:…
	Sealed      Sealed    `json:"sealed"`
	CreatedAt   time.Time `json:"created_at"`
}

// EnsureKey returns the SSH key stored under name, generating and storing it the first time.
// Replicas racing on the first use all end up with the row that was inserted first.
func EnsureKey(ctx context.Context, st SettingStore, v *Vault, name string) (ssh.Signer, SealedKey, error) {
	if !v.Ready() {
		return nil, SealedKey{}, ErrNoKey
	}
	var sk SealedKey
	found, err := st.Setting(ctx, name, &sk)
	if err != nil {
		return nil, SealedKey{}, err
	}
	if !found {
		fresh, err := newSealedKey(v, name)
		if err != nil {
			return nil, SealedKey{}, err
		}
		if _, err := st.InsertSettingIfAbsent(ctx, name, fresh); err != nil {
			return nil, SealedKey{}, err
		}
		if found, err = st.Setting(ctx, name, &sk); err != nil {
			return nil, SealedKey{}, err
		} else if !found {
			return nil, SealedKey{}, fmt.Errorf("%s: missing after insert", name)
		}
	}
	signer, err := openKey(v, name, sk)
	if err != nil {
		return nil, SealedKey{}, err
	}
	return signer, sk, nil
}

func newSealedKey(v *Vault, name string) (SealedKey, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return SealedKey{}, err
	}
	block, err := ssh.MarshalPrivateKey(priv, "agent-teams "+name)
	if err != nil {
		return SealedKey{}, err
	}
	raw := pem.EncodeToMemory(block)
	defer clear(raw)
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return SealedKey{}, err
	}
	sealed, err := v.Seal(SettingAAD(name), raw)
	if err != nil {
		return SealedKey{}, err
	}
	fp := ssh.FingerprintSHA256(signer.PublicKey())
	return SealedKey{
		ID:          strings.TrimPrefix(fp, "SHA256:"),
		PublicKey:   strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))),
		Fingerprint: fp,
		Sealed:      sealed,
		CreatedAt:   time.Now().UTC(),
	}, nil
}

func openKey(v *Vault, name string, sk SealedKey) (ssh.Signer, error) {
	raw, err := v.Open(SettingAAD(name), sk.Sealed)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	defer clear(raw)
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if ssh.FingerprintSHA256(signer.PublicKey()) != sk.Fingerprint {
		return nil, fmt.Errorf("%s: public key does not match the sealed private key", name)
	}
	return signer, nil
}

// ResealKey re-wraps the stored key's DEK with the current KEK (atctl admin reseal). It reports
// false when the key is absent or already current.
func ResealKey(ctx context.Context, st SettingStore, v *Vault, name string) (bool, error) {
	var sk SealedKey
	found, err := st.Setting(ctx, name, &sk)
	if err != nil || !found {
		return false, err
	}
	out, changed, err := v.Rewrap(SettingAAD(name), sk.Sealed)
	if err != nil {
		return false, fmt.Errorf("%s: %w", name, err)
	}
	if !changed {
		return false, nil
	}
	sk.Sealed = out
	return true, st.SaveSetting(ctx, name, sk)
}
