package credvault

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"strings"

	"github.com/touwaeriol/agent-teams/server/internal/msg"
	"golang.org/x/crypto/ssh"
)

// Kinds of a standalone Git repository credential (at_git_credentials.kind).
const (
	GitHTTPSToken = "https_token"
	GitSSHKey     = "ssh_key"
)

// GitAuthKind maps a Git credential kind to the Credential auth kind that checks it: a token is
// carried in Password, an SSH key in PrivateKey (+ Passphrase).
func GitAuthKind(kind string) (string, error) {
	switch kind {
	case GitHTTPSToken:
		return AuthPassword, nil
	case GitSSHKey:
		return AuthPrivateKey, nil
	}
	return "", msg.Wrap(ErrInvalidCredential, "未知凭据类型 “{kind}”", "kind", kind)
}

// GenerateSSHKey makes a new Ed25519 key pair for a deploy key: the private key in OpenSSH PEM
// (to be sealed at once, never shown) and its public key info.
func GenerateSSHKey(comment string) (Credential, KeyInfo, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Credential{}, KeyInfo{}, err
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return Credential{}, KeyInfo{}, err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return Credential{}, KeyInfo{}, err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	if comment != "" {
		line += " " + comment
	}
	return Credential{PrivateKey: string(pem.EncodeToMemory(block))},
		KeyInfo{PublicKey: line, Fingerprint: ssh.FingerprintSHA256(signer.PublicKey())}, nil
}
