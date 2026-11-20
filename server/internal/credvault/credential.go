package credvault

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// Auth kinds of a remote server (at_remote_servers.auth_kind).
const (
	AuthPassword   = "password"
	AuthPrivateKey = "private_key"
)

// Credential is the plaintext of a server credential. It lives only in the console's memory.
type Credential struct {
	Password   string `json:"password,omitempty"`
	PrivateKey string `json:"private_key,omitempty"` // PEM / OpenSSH format
	Passphrase string `json:"passphrase,omitempty"`
}

// ErrInvalidCredential is a credential that does not fit its auth kind.
var ErrInvalidCredential = errors.New("invalid credential")

// KeyInfo describes a private key credential without revealing it.
type KeyInfo struct {
	PublicKey   string // authorized_keys line
	Fingerprint string // SHA256:…
}

// Check validates c for kind. A private key must parse (with its passphrase when it has one);
// the public key's fingerprint is returned for display.
func (c Credential) Check(kind string) (KeyInfo, error) {
	switch kind {
	case AuthPassword:
		if c.Password == "" || c.PrivateKey != "" || c.Passphrase != "" {
			return KeyInfo{}, fmt.Errorf("%w：密码认证只填密码", ErrInvalidCredential)
		}
		return KeyInfo{}, nil
	case AuthPrivateKey:
		if c.PrivateKey == "" || c.Password != "" {
			return KeyInfo{}, fmt.Errorf("%w：私钥认证要填私钥", ErrInvalidCredential)
		}
		signer, err := c.Signer()
		if err != nil {
			return KeyInfo{}, err
		}
		pub := signer.PublicKey()
		return KeyInfo{
			PublicKey:   strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))),
			Fingerprint: ssh.FingerprintSHA256(pub),
		}, nil
	default:
		return KeyInfo{}, fmt.Errorf("%w：未知认证方式 %q", ErrInvalidCredential, kind)
	}
}

// Signer parses the private key (with the passphrase when set).
func (c Credential) Signer() (ssh.Signer, error) {
	var (
		s   ssh.Signer
		err error
	)
	if c.Passphrase != "" {
		s, err = ssh.ParsePrivateKeyWithPassphrase([]byte(c.PrivateKey), []byte(c.Passphrase))
	} else {
		s, err = ssh.ParsePrivateKey([]byte(c.PrivateKey))
	}
	var missing *ssh.PassphraseMissingError
	switch {
	case errors.As(err, &missing):
		return nil, fmt.Errorf("%w：私钥有口令，请一并填写", ErrInvalidCredential)
	case err != nil:
		return nil, fmt.Errorf("%w：私钥无法解析（%v）", ErrInvalidCredential, err)
	}
	return s, nil
}

// SealCredential checks c for kind and seals it with aad (ServerAAD).
func (v *Vault) SealCredential(aad []byte, kind string, c Credential) (Sealed, KeyInfo, error) {
	info, err := c.Check(kind)
	if err != nil {
		return Sealed{}, KeyInfo{}, err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return Sealed{}, KeyInfo{}, err
	}
	defer clear(raw)
	s, err := v.Seal(aad, raw)
	return s, info, err
}

// OpenCredential opens a credential sealed by SealCredential.
func (v *Vault) OpenCredential(aad []byte, s Sealed) (Credential, error) {
	raw, err := v.Open(aad, s)
	if err != nil {
		return Credential{}, err
	}
	defer clear(raw)
	var c Credential
	if err := json.Unmarshal(raw, &c); err != nil {
		return Credential{}, ErrOpen
	}
	return c, nil
}
