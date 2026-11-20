package sshdial

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/touwaeriol/agent-teams/server/internal/credvault"
)

var (
	// ErrHostKeyMismatch: the target showed a host key other than the pinned one. No credential
	// was sent.
	ErrHostKeyMismatch = errors.New("host key mismatch")
	// ErrAuth: the target refused the credential.
	ErrAuth = errors.New("authentication failed")
	// ErrConnect: no TCP connection to any address.
	ErrConnect = errors.New("connect failed")
	// ErrTimeout: connecting or the SSH handshake took too long.
	ErrTimeout = errors.New("timeout")
	// ErrHandshake: the SSH handshake failed for another reason (not an SSH server, no common
	// algorithm…).
	ErrHandshake = errors.New("ssh handshake failed")
)

// Target is a server to log in to.
type Target struct {
	Host     string
	Port     int
	Username string
	AuthKind string // credvault.AuthPassword | credvault.AuthPrivateKey
	Cred     credvault.Credential
	// HostKey is the pinned host key (authorized_keys line); "" accepts whatever the target shows
	// (the first test connection, which then pins it).
	HostKey string
}

// HostKey describes a host key a target showed.
type HostKey struct {
	Line        string // authorized_keys line (type + base64)
	Type        string
	Fingerprint string // SHA256:…
}

func describe(k ssh.PublicKey) HostKey {
	return HostKey{Line: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))), Type: k.Type(), Fingerprint: ssh.FingerprintSHA256(k)}
}

// Result is what a dial learned, also on failure: Seen is set once the target showed its host key.
type Result struct {
	Client *ssh.Client // nil on failure; the caller closes it
	Seen   *HostKey
	Addr   netip.AddrPort // the address dialled
}

// HostKeyAlgorithms are the algorithms that make a target present a key of the pinned type.
func HostKeyAlgorithms(pinned ssh.PublicKey) []string {
	switch pinned.Type() {
	case ssh.KeyAlgoRSA:
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	default:
		return []string{pinned.Type()}
	}
}

// ParseHostKey parses a pinned host key line.
func ParseHostKey(line string) (ssh.PublicKey, error) {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	return k, err
}

// AuthMethods are the login methods for a credential: a private key, or a password offered both as
// "password" and as a keyboard-interactive answer to one hidden prompt (§5.5; real 2FA is not
// supported).
func AuthMethods(kind string, c credvault.Credential) ([]ssh.AuthMethod, error) {
	switch kind {
	case credvault.AuthPrivateKey:
		signer, err := c.Signer()
		if err != nil {
			return nil, err
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	case credvault.AuthPassword:
		pw := c.Password
		return []ssh.AuthMethod{
			ssh.Password(pw),
			ssh.KeyboardInteractive(func(_, _ string, questions []string, echos []bool) ([]string, error) {
				if len(questions) == 0 {
					return nil, nil
				}
				if len(questions) != 1 || echos[0] {
					return nil, errors.New("unsupported keyboard-interactive prompt")
				}
				return []string{pw}, nil
			}),
		}, nil
	}
	return nil, fmt.Errorf("%w：未知认证方式 %q", credvault.ErrInvalidCredential, kind)
}

// Dial resolves and checks the target's address (Policy.Resolve), connects to the resolved
// address, verifies the host key against the pin (before any credential is sent) and logs in.
// timeout bounds the whole dial and handshake.
func (p Policy) Dial(ctx context.Context, t Target, timeout time.Duration) (Result, error) {
	var res Result
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	addrs, err := p.Resolve(ctx, t.Host)
	if err != nil {
		return res, err
	}
	var pinned ssh.PublicKey
	if t.HostKey != "" {
		if pinned, err = ParseHostKey(t.HostKey); err != nil {
			return res, fmt.Errorf("pinned host key: %w", err)
		}
	}
	auth, err := AuthMethods(t.AuthKind, t.Cred)
	if err != nil {
		return res, err
	}
	var conn net.Conn
	var d net.Dialer
	for _, a := range addrs {
		res.Addr = netip.AddrPortFrom(a.Unmap(), uint16(t.Port))
		if conn, err = d.DialContext(ctx, "tcp", res.Addr.String()); err == nil {
			break
		}
	}
	if conn == nil {
		if ctx.Err() != nil {
			return res, fmt.Errorf("%w：连接 %s:%d 超时", ErrTimeout, t.Host, t.Port)
		}
		return res, fmt.Errorf("%w：%v", ErrConnect, err)
	}
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	cfg := &ssh.ClientConfig{
		User: t.Username,
		Auth: auth,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			seen := describe(key)
			res.Seen = &seen
			if pinned != nil && !bytes.Equal(key.Marshal(), pinned.Marshal()) {
				return ErrHostKeyMismatch
			}
			return nil
		},
		Timeout: timeout,
	}
	if pinned != nil {
		cfg.HostKeyAlgorithms = HostKeyAlgorithms(pinned)
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, net.JoinHostPort(t.Host, strconv.Itoa(t.Port)), cfg)
	if err != nil {
		_ = conn.Close()
		switch {
		case errors.Is(err, ErrHostKeyMismatch):
			return res, ErrHostKeyMismatch
		case strings.Contains(err.Error(), "unable to authenticate"):
			return res, fmt.Errorf("%w：%v", ErrAuth, err)
		case ctx.Err() != nil || isTimeout(err):
			return res, fmt.Errorf("%w：SSH 握手超时", ErrTimeout)
		}
		return res, fmt.Errorf("%w：%v", ErrHandshake, err)
	}
	_ = conn.SetDeadline(time.Time{})
	res.Client = ssh.NewClient(c, chans, reqs)
	return res, nil
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
