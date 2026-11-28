// Package sshdial is the console side of logging in to a user's remote server (remote-servers.md
// §5.3, §5.5, §5.6): which target addresses may be dialled, dialling the resolved address (never
// the name again, against DNS rebinding), the host key pin and the password / private key login.
// The "test connection" API uses it, and so can the SSH gateway's backend.
package sshdial

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strings"

	"github.com/touwaeriol/agent-teams/server/internal/msg"
)

// SettingKey is the at_settings key with the private networks an administrator lets servers
// live in: {"cidrs": ["10.0.0.0/8", "127.0.0.1/32"]}.
const SettingKey = "ssh_allowed_private_cidrs"

// AllowedSetting is the value of SettingKey.
type AllowedSetting struct {
	CIDRs []string `json:"cidrs"`
}

var (
	// ErrBlockedAddress: the target resolves to an address servers may not use (loopback,
	// link-local, cloud metadata, unspecified, multicast, the console's own private networks)
	// and no allowed CIDR covers it.
	ErrBlockedAddress = errors.New("address not allowed")
	// ErrResolve: the host name does not resolve.
	ErrResolve = errors.New("host does not resolve")
	// ErrInvalidHost: not a host name or IP literal.
	ErrInvalidHost = errors.New("invalid host")
)

// metadata are cloud metadata endpoints that are not link-local.
var metadata = []netip.Prefix{
	netip.MustParsePrefix("fd00:ec2::254/128"),  // AWS IPv6
	netip.MustParsePrefix("100.100.100.200/32"), // Alibaba Cloud
}

// cgnat is the shared address space (RFC 6598): carrier-grade NAT and Tailscale / other overlay
// networks the console host may be on, so it is blocked unless an administrator allows it.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

var hostName = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,62})(\.[A-Za-z0-9]([A-Za-z0-9-]{0,62}))*\.?$`)

// ValidHost reports whether host is an IP literal or a DNS name (no ports, brackets, spaces or a
// leading '-', so it can never be read as an ssh option).
func ValidHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return !strings.Contains(host, "%") // no zones
	}
	return hostName.MatchString(host)
}

// Policy decides which addresses may be dialled.
type Policy struct {
	// Allowed are administrator-approved networks (SettingKey); an address inside one is allowed
	// whatever it is.
	Allowed []netip.Prefix
	// Local are the console's own private networks (blocked); nil reads them from the host's
	// interfaces.
	Local []netip.Prefix
	// Resolver resolves names; nil is net.DefaultResolver.
	Resolver interface {
		LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
	}
}

// ParseAllowed turns the setting's CIDRs into prefixes; a bad entry is an error.
func ParseAllowed(s AllowedSetting) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(s.CIDRs))
	for _, c := range s.CIDRs {
		p, err := netip.ParsePrefix(strings.TrimSpace(c))
		if err != nil {
			if a, aerr := netip.ParseAddr(strings.TrimSpace(c)); aerr == nil {
				p = netip.PrefixFrom(a, a.BitLen())
			} else {
				return nil, fmt.Errorf("%s: %q: %w", SettingKey, c, err)
			}
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// LocalPrivateNetworks lists the private networks of the host's interfaces (where the console's
// database and cache usually live).
func LocalPrivateNetworks() []netip.Prefix {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []netip.Prefix
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		p, err := netip.ParsePrefix(n.String())
		if err != nil {
			continue
		}
		if addr := p.Addr().Unmap(); addr.IsPrivate() {
			out = append(out, netip.PrefixFrom(addr, p.Bits()).Masked())
		}
	}
	return out
}

func contains(list []netip.Prefix, a netip.Addr) bool {
	for _, p := range list {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Blocked returns why a may not be dialled, or "" when it may.
func (p Policy) Blocked(a netip.Addr) string {
	a = a.Unmap()
	if contains(p.Allowed, a) {
		return ""
	}
	local := p.Local
	if local == nil {
		local = LocalPrivateNetworks()
	}
	switch {
	case a.IsLoopback():
		return msg.K("回环地址")
	case a.IsUnspecified():
		return msg.K("未指定地址")
	case a.IsLinkLocalUnicast(), a.IsLinkLocalMulticast():
		return msg.K("链路本地地址（含云元数据 169.254.169.254）")
	case contains(metadata, a):
		return msg.K("云元数据地址")
	case cgnat.Contains(a):
		return msg.K("运营商级 NAT / Tailscale 地址（100.64.0.0/10）")
	case a.IsMulticast(), a.IsInterfaceLocalMulticast():
		return msg.K("组播地址")
	case contains(local, a):
		return msg.K("主节点所在的内网")
	}
	return ""
}

// Resolve resolves host and checks every address; any blocked address refuses the whole host (a
// name must not mix public and internal addresses). The returned addresses are what to dial.
func (p Policy) Resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if !ValidHost(host) {
		return nil, msg.Wrap(ErrInvalidHost, "主机不合法：“{host}”", "host", host)
	}
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{a}
	} else {
		r := p.Resolver
		if r == nil {
			r = net.DefaultResolver
		}
		addrs, err = r.LookupNetIP(ctx, "ip", host)
		if err != nil || len(addrs) == 0 {
			return nil, msg.Wrap(ErrResolve, "主机名解析不到地址：{host}", "host", host)
		}
	}
	for _, a := range addrs {
		if why := p.Blocked(a); why != "" {
			return nil, msg.Wrap(ErrBlockedAddress, "{host} 解析到 {addr}（{why}）；需要的话请管理员在 {setting} 里放行",
				"host", host, "addr", a.Unmap(), "why", msg.T(why), "setting", SettingKey)
		}
	}
	return addrs, nil
}
