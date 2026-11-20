package sshdial_test

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/credvault"
	"github.com/touwaeriol/agent-teams/server/internal/sshdial"
	"github.com/touwaeriol/agent-teams/server/internal/sshdial/sshtest"
)

type fakeResolver map[string][]netip.Addr

func (f fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if a, ok := f[host]; ok {
		return a, nil
	}
	return nil, errors.New("no such host")
}

func TestPolicy(t *testing.T) {
	ctx := context.Background()
	p := sshdial.Policy{Local: []netip.Prefix{netip.MustParsePrefix("172.18.0.0/16")}, Resolver: fakeResolver{
		"good.example":  {netip.MustParseAddr("203.0.113.5")},
		"mixed.example": {netip.MustParseAddr("203.0.113.5"), netip.MustParseAddr("127.0.0.1")},
		"meta.example":  {netip.MustParseAddr("169.254.169.254")},
	}}
	for _, ok := range []string{"203.0.113.5", "good.example", "10.1.2.3", "2001:db8::1", "192.168.1.10"} {
		if _, err := p.Resolve(ctx, ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for host, want := range map[string]error{
		"127.0.0.1": sshdial.ErrBlockedAddress, "::1": sshdial.ErrBlockedAddress, "0.0.0.0": sshdial.ErrBlockedAddress,
		"169.254.169.254": sshdial.ErrBlockedAddress, "fe80::1": sshdial.ErrBlockedAddress, "fd00:ec2::254": sshdial.ErrBlockedAddress,
		"100.100.100.200": sshdial.ErrBlockedAddress, "224.0.0.1": sshdial.ErrBlockedAddress, "172.18.0.5": sshdial.ErrBlockedAddress,
		"::ffff:127.0.0.1": sshdial.ErrBlockedAddress, "mixed.example": sshdial.ErrBlockedAddress, "meta.example": sshdial.ErrBlockedAddress,
		"nowhere.example": sshdial.ErrResolve, "-oProxyCommand=x": sshdial.ErrInvalidHost, "a b": sshdial.ErrInvalidHost,
		"fe80::1%eth0": sshdial.ErrInvalidHost, "": sshdial.ErrInvalidHost,
	} {
		if _, err := p.Resolve(ctx, host); !errors.Is(err, want) {
			t.Errorf("%s: %v, want %v", host, err, want)
		}
	}
	allowed, err := sshdial.ParseAllowed(sshdial.AllowedSetting{CIDRs: []string{"127.0.0.0/8", "172.18.0.5"}})
	if err != nil {
		t.Fatal(err)
	}
	p.Allowed = allowed
	for _, ok := range []string{"127.0.0.1", "172.18.0.5"} {
		if _, err := p.Resolve(ctx, ok); err != nil {
			t.Errorf("allowed %s: %v", ok, err)
		}
	}
	if _, err := p.Resolve(ctx, "172.18.0.6"); !errors.Is(err, sshdial.ErrBlockedAddress) {
		t.Errorf("not allowed 172.18.0.6: %v", err)
	}
	if _, err := sshdial.ParseAllowed(sshdial.AllowedSetting{CIDRs: []string{"nope"}}); err == nil {
		t.Error("bad cidr accepted")
	}
}

func loopback(t *testing.T) sshdial.Policy {
	allowed, err := sshdial.ParseAllowed(sshdial.AllowedSetting{CIDRs: []string{"127.0.0.1/32"}})
	if err != nil {
		t.Fatal(err)
	}
	return sshdial.Policy{Allowed: allowed, Local: []netip.Prefix{}}
}

func TestDial(t *testing.T) {
	ctx := context.Background()
	p := loopback(t)
	userKey, keyPEM := sshtest.NewKey(t, "pp")
	srv := sshtest.Start(t, "deploy", "s3cret", userKey.PublicKey())
	target := sshdial.Target{Host: srv.Host, Port: srv.Port, Username: "deploy", AuthKind: credvault.AuthPassword,
		Cred: credvault.Credential{Password: "s3cret"}}

	res, err := p.Dial(ctx, target, 5*time.Second)
	if err != nil || res.Client == nil || res.Seen == nil || !strings.HasPrefix(res.Seen.Fingerprint, "SHA256:") {
		t.Fatalf("password dial %+v %v", res, err)
	}
	sess, err := res.Client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	out, err := sess.Output("true")
	if err != nil || string(out) != "ok\n" {
		t.Fatalf("exec %q %v", out, err)
	}
	res.Client.Close()
	pin := res.Seen.Line

	// Private key with passphrase, pinned host key.
	keyTarget := target
	keyTarget.AuthKind, keyTarget.Cred, keyTarget.HostKey = credvault.AuthPrivateKey, credvault.Credential{PrivateKey: keyPEM, Passphrase: "pp"}, pin
	if res, err := p.Dial(ctx, keyTarget, 5*time.Second); err != nil {
		t.Fatalf("key dial %v", err)
	} else {
		res.Client.Close()
	}

	bad := target
	bad.Cred.Password = "wrong"
	if res, err := p.Dial(ctx, bad, 5*time.Second); !errors.Is(err, sshdial.ErrAuth) || res.Seen == nil {
		t.Fatalf("wrong password: %v", err)
	}

	// A changed host key is refused before any credential is sent.
	logins := srv.Logins.Load()
	srv.RotateHostKey(t)
	pinned := target
	pinned.HostKey = pin
	if res, err := p.Dial(ctx, pinned, 5*time.Second); !errors.Is(err, sshdial.ErrHostKeyMismatch) || res.Seen == nil || res.Seen.Line == pin {
		t.Fatalf("mismatch: %v %+v", err, res.Seen)
	}
	if srv.Logins.Load() != logins {
		t.Fatal("logged in despite the host key mismatch")
	}

	// Loopback without the allowance.
	if _, err := (sshdial.Policy{}).Dial(ctx, target, 5*time.Second); !errors.Is(err, sshdial.ErrBlockedAddress) {
		t.Fatalf("loopback: %v", err)
	}
	// Nothing listening.
	closed := target
	closed.Port = 1
	if _, err := p.Dial(ctx, closed, 2*time.Second); !errors.Is(err, sshdial.ErrConnect) && !errors.Is(err, sshdial.ErrTimeout) {
		t.Fatalf("closed port: %v", err)
	}
}
