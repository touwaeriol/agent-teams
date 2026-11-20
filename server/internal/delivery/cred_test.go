package delivery

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/credvault"
	"github.com/touwaeriol/agent-teams/server/internal/store"
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

// TestCredentialFullSlotsDoNotBlock: with every slot taken, Credential returns at once (the read
// loop never waits) and drops the request; the daemon retries it after 10 s.
func TestCredentialFullSlotsDoNotBlock(t *testing.T) {
	for i := 0; i < credInflight; i++ {
		credSlots <- struct{}{}
	}
	defer func() {
		for i := 0; i < credInflight; i++ {
			<-credSlots
		}
	}()
	e := &Engine{log: slog.New(slog.NewTextHandler(io.Discard, nil)), now: time.Now}
	done := make(chan error, 1)
	go func() {
		done <- e.Credential(context.Background(), store.Node{ID: "n1"}, &protocol.CredRequest{ReqID: "r1", Host: "github.com", Path: "o/r.git", Purpose: "read"})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Credential with full slots: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Credential blocked the read loop while every slot was taken")
	}
	if len(credSlots) != credInflight {
		t.Fatalf("a dropped request took a slot: %d", len(credSlots))
	}
}

// TestNodeSSHKey: a passphrase-protected OpenSSH key reaches the node as unencrypted PKCS#8 with
// its public line, and the known_hosts line uses [host]:port for a non-default port.
func TestNodeSSHKey(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "k", []byte("pw"))
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := ssh.NewPublicKey(priv.Public())
	want := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	k, err := nodeSSHKey(credvault.Credential{PrivateKey: string(pem.EncodeToMemory(block)), Passphrase: "pw"}, "git.example.com:2222", "ssh-ed25519 AAAAHOST")
	if err != nil {
		t.Fatal(err)
	}
	if k.PublicKey != want || k.KnownHosts != "[git.example.com]:2222 ssh-ed25519 AAAAHOST" || !strings.HasPrefix(k.PrivateKey, "-----BEGIN PRIVATE KEY-----") {
		t.Fatalf("key %+v", k)
	}
	p, _ := pem.Decode([]byte(k.PrivateKey))
	if back, err := x509.ParsePKCS8PrivateKey(p.Bytes); err != nil || !back.(ed25519.PrivateKey).Equal(priv) {
		t.Fatalf("pkcs8: %v", err)
	}
	if k2, _ := nodeSSHKey(credvault.Credential{PrivateKey: string(pem.EncodeToMemory(block)), Passphrase: "pw"}, "gitlab.com", "ssh-rsa AAAA"); k2.KnownHosts != "gitlab.com ssh-rsa AAAA" {
		t.Fatalf("default port %q", k2.KnownHosts)
	}
	if _, err := nodeSSHKey(credvault.Credential{PrivateKey: string(pem.EncodeToMemory(block)), Passphrase: "wrong"}, "h", "k"); err == nil {
		t.Fatal("wrong passphrase accepted")
	}
}
