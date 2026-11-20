package gitremote

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Parsed is what a pasted clone URL says (git-repos.md §3.5).
type Parsed struct {
	URL       string // normalised: https://host[:port]/path.git or ssh://user@host[:port]/path.git
	Transport string // https | ssh
	Host      string // lowercase host, with :port when not the default
	Hostname  string
	Port      int
	Path      string // without leading / and .git
	User      string // ssh login user from the URL ("git" when absent)
	Slug      string // Host + "/" + Path
	Provider  string // github | gitlab | gitea | bitbucket | generic
	WebURL    string // https://hostname/path when the provider is known
	// DefaultUsername is the HTTPS user the provider expects with a token ('' = ask), or the
	// ssh user.
	DefaultUsername string
}

var (
	scpRE   = regexp.MustCompile(`^([A-Za-z0-9._-]+)@([A-Za-z0-9.-]+):([^/][^:]*)$`)
	pathRE  = regexp.MustCompile(`^[A-Za-z0-9._~-]+(/[A-Za-z0-9._~-]+)+$`)
	hostRE  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)
	userRE  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	knownGH = map[string][2]string{
		"github.com":    {"github", "x-access-token"},
		"gitlab.com":    {"gitlab", "oauth2"},
		"bitbucket.org": {"bitbucket", "x-token-auth"},
		"gitea.com":     {"gitea", ""},
		"codeberg.org":  {"gitea", ""},
	}
)

// ErrURL is a clone URL the platform does not accept.
var ErrURL = errors.New("bad clone url")

func urlErr(msg string) error { return fmt.Errorf("%w: %s", ErrURL, msg) }

// ParseURL checks and normalises a clone URL: https:// or ssh:// or scp-like user@host:path. No
// embedded password, no other scheme, a host name or IP and a path of at least two segments.
func ParseURL(raw string) (Parsed, error) {
	raw = strings.TrimSpace(raw)
	if m := scpRE.FindStringSubmatch(raw); m != nil && !strings.Contains(raw, "://") {
		raw = "ssh://" + m[1] + "@" + m[2] + "/" + m[3]
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return Parsed{}, urlErr("请贴 https://… 或 git@…:… 形式的克隆地址")
	}
	var p Parsed
	switch u.Scheme {
	case "https":
		p.Transport, p.Port = "https", 443
		if u.User != nil {
			return Parsed{}, urlErr("地址里不要带用户名或令牌，请填在凭据里")
		}
	case "ssh":
		p.Transport, p.Port, p.User = "ssh", 22, "git"
		if u.User != nil {
			if _, hasPw := u.User.Password(); hasPw {
				return Parsed{}, urlErr("地址里不要带密码")
			}
			p.User = u.User.Username()
		}
		if !userRE.MatchString(p.User) {
			return Parsed{}, urlErr("SSH 用户名不合法")
		}
	default:
		return Parsed{}, urlErr("只支持 https:// 和 SSH 地址")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return Parsed{}, urlErr("地址里不能有 ? 或 #")
	}
	p.Hostname = strings.ToLower(u.Hostname())
	if !hostRE.MatchString(p.Hostname) && !isIP(p.Hostname) {
		return Parsed{}, urlErr("主机名不合法")
	}
	if ps := u.Port(); ps != "" {
		n, err := strconv.Atoi(ps)
		if err != nil || n < 1 || n > 65535 {
			return Parsed{}, urlErr("端口不合法")
		}
		p.Port = n
	}
	p.Host = p.Hostname
	if (p.Transport == "https" && p.Port != 443) || (p.Transport == "ssh" && p.Port != 22) {
		p.Host += ":" + strconv.Itoa(p.Port)
	}
	p.Path = strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	if !pathRE.MatchString(p.Path) || strings.Contains(p.Path, "..") {
		return Parsed{}, urlErr("仓库路径不合法（至少要有 组/仓库 两级）")
	}
	p.Slug = p.Host + "/" + p.Path
	if p.Transport == "https" {
		p.URL = "https://" + p.Host + "/" + p.Path + ".git"
	} else {
		p.URL = "ssh://" + p.User + "@" + p.Host + "/" + p.Path + ".git"
	}
	p.Provider = "generic"
	if k, ok := knownGH[p.Hostname]; ok {
		p.Provider = k[0]
		p.WebURL = "https://" + p.Hostname + "/" + p.Path
		if p.Transport == "https" {
			p.DefaultUsername = k[1]
		}
	}
	if p.Transport == "ssh" {
		p.DefaultUsername = p.User
	}
	return p, nil
}

func isIP(h string) bool {
	h = strings.Trim(h, "[]")
	return regexp.MustCompile(`^[0-9.]+$|^[0-9a-f:]+$`).MatchString(h) && strings.ContainsAny(h, ".:")
}
