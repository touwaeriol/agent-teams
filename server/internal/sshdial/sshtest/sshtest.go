// Package sshtest runs a minimal in-process SSH server for tests: password and/or public key
// login, an "exec" channel that answers "ok", and a replaceable host key.
package sshtest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"
)

// Server is a running test SSH server on 127.0.0.1.
type Server struct {
	Host     string
	Port     int
	User     string
	Password string        // "" = password login off
	UserKey  ssh.PublicKey // nil = public key login off
	Logins   atomic.Int32  // accepted logins (counted when the credential is accepted, before the client's handshake returns)

	mu      sync.Mutex
	hostKey ssh.Signer
	ln      net.Listener
}

// NewKey returns a new Ed25519 signer and its private key in OpenSSH PEM (encrypted with
// passphrase when not "").
func NewKey(t testing.TB, passphrase string) (ssh.Signer, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var block *pem.Block
	if passphrase != "" {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	} else {
		block, err = ssh.MarshalPrivateKey(priv, "")
	}
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer, string(pem.EncodeToMemory(block))
}

// Start runs a server until the test ends.
func Start(t testing.TB, user, password string, userKey ssh.PublicKey) *Server {
	t.Helper()
	hk, _ := NewKey(t, "")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{User: user, Password: password, UserKey: userKey, hostKey: hk, ln: ln, Host: "127.0.0.1"}
	s.Port = ln.Addr().(*net.TCPAddr).Port
	t.Cleanup(func() { _ = ln.Close() })
	go s.serve()
	return s
}

// HostKey is the current host public key.
func (s *Server) HostKey() ssh.PublicKey {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hostKey.PublicKey()
}

// RotateHostKey gives the server a new host key (a "changed" or impersonated server).
func (s *Server) RotateHostKey(t testing.TB) {
	hk, _ := NewKey(t, "")
	s.mu.Lock()
	s.hostKey = hk
	s.mu.Unlock()
}

func (s *Server) config() *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{}
	if s.Password != "" {
		cfg.PasswordCallback = func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if c.User() == s.User && string(pw) == s.Password {
				s.Logins.Add(1)
				return nil, nil
			}
			return nil, ssh.ErrNoAuth
		}
	}
	if s.UserKey != nil {
		cfg.PublicKeyCallback = func(c ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if c.User() == s.User && bytes.Equal(k.Marshal(), s.UserKey.Marshal()) {
				s.Logins.Add(1)
				return nil, nil
			}
			return nil, ssh.ErrNoAuth
		}
	}
	s.mu.Lock()
	cfg.AddHostKey(s.hostKey)
	s.mu.Unlock()
	return cfg
}

func (s *Server) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	sc, chans, reqs, err := ssh.NewServerConn(conn, s.config())
	if err != nil {
		return
	}
	defer sc.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "no")
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer ch.Close()
			for r := range creqs {
				if r.Type == "exec" {
					_ = r.Reply(true, nil)
					_, _ = ch.Write([]byte("ok\n"))
					_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
					return
				}
				_ = r.Reply(false, nil)
			}
		}()
	}
}
