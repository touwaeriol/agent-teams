// Package gitremote runs git against a standalone repository's upstream from the console, with the
// credential that only the console holds (git-repos.md §4.3, §5): test connection, the relay's
// mirror sync and the pushes it forwards.
//
// The credential never reaches a file or the git process's command line:
//   - HTTPS: an Authorization header in GIT_CONFIG_* (environment of that one git process);
//     credential helpers are switched off.
//   - SSH: git runs this binary as its ssh ("atctl git-ssh"), which fetches the key and the pinned
//     host key over a per-command unix socket and logs in with golang.org/x/crypto/ssh (sshdial),
//     so the key stays in memory and the host key pin and address policy apply.
//
// Every upstream address is checked by the same policy as remote servers (sshdial.Policy): HTTPS
// requests are pinned to the checked address with http.curloptResolve.
package gitremote

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/credvault"
	"github.com/touwaeriol/agent-teams/server/internal/sshdial"
)

// Remote is an upstream repository with its opened credential (console memory only).
type Remote struct {
	URL       string // https://host/path.git or ssh://user@host:port/path.git
	Transport string // https | ssh
	Kind      string // '' (public) | credvault.GitHTTPSToken | credvault.GitSSHKey
	Username  string
	Cred      credvault.Credential
	HostKey   string // ssh: pinned host key ('' accepts any: only the first test connection)
	Policy    sshdial.Policy
}

// Git is how the console runs git.
type Git struct {
	Bin     string // "git" when empty
	Exe     string // this binary, run as ssh ("<exe> git-ssh"); os.Executable() when empty
	TempDir string // where the per-command ssh sockets go; os.TempDir() when empty
	// Env is added to every upstream git process, TLS to the console's own HTTPS probes (tests:
	// a private CA).
	Env []string
	TLS *tls.Config
}

// Errors the callers map to messages.
var (
	ErrAuth     = errors.New("认证失败")
	ErrNotFound = errors.New("仓库不存在，或凭据看不到它")
	ErrHostKey  = errors.New("主机密钥和已固定的不一样")
	ErrConnect  = errors.New("连不上仓库所在的主机")
)

func (g Git) bin() string {
	if g.Bin != "" {
		return g.Bin
	}
	return "git"
}

func (g Git) exe() (string, error) {
	if g.Exe != "" {
		return g.Exe, nil
	}
	return os.Executable()
}

// baseEnv is the environment of every upstream git process: no prompts, no user or system
// config, no helpers, no local or ext:: transports.
func baseEnv() []string {
	env := []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ASKPASS=/bin/false",
		"SSH_ASKPASS=/bin/false", "GIT_LFS_SKIP_SMUDGE=1", "LC_ALL=C", "HOME=" + os.TempDir()}
	if p := os.Getenv("PATH"); p != "" {
		env = append(env, "PATH="+p)
	}
	return env
}

type gitConfig [][2]string

func (c gitConfig) env() []string {
	out := []string{"GIT_CONFIG_COUNT=" + strconv.Itoa(len(c))}
	for i, kv := range c {
		out = append(out, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, kv[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, kv[1]))
	}
	return out
}

// HostPort splits the URL's host and port (default 443 for https, 22 for ssh).
func (r Remote) HostPort() (string, int, error) {
	u, err := url.Parse(r.URL)
	if err != nil || u.Hostname() == "" {
		return "", 0, fmt.Errorf("bad url %q", r.URL)
	}
	port := 443
	if r.Transport == "ssh" {
		port = 22
	}
	if p := u.Port(); p != "" {
		if port, err = strconv.Atoi(p); err != nil {
			return "", 0, fmt.Errorf("bad port in %q", r.URL)
		}
	}
	return u.Hostname(), port, nil
}

// Command builds a git command against the upstream, run in dir (a bare mirror, or "" for
// ls-remote). The returned cleanup must run after the command finished.
func (g Git) Command(ctx context.Context, r Remote, dir string, args ...string) (*exec.Cmd, func(), error) {
	cfg := gitConfig{{"credential.helper", ""}, {"protocol.file.allow", "never"}, {"protocol.ext.allow", "never"},
		{"core.hooksPath", "/dev/null"}, {"core.fsmonitor", "false"}}
	env := baseEnv()
	cleanup := func() {}
	host, port, err := r.HostPort()
	if err != nil {
		return nil, nil, err
	}
	switch r.Transport {
	case "https":
		addrs, err := r.Policy.Resolve(ctx, host)
		if err != nil {
			return nil, nil, err
		}
		cfg = append(cfg, [2]string{"http.curloptResolve", fmt.Sprintf("%s:%d:%s", host, port, addrs[0].Unmap())})
		if r.Kind == credvault.GitHTTPSToken {
			auth := base64.StdEncoding.EncodeToString([]byte(r.Username + ":" + r.Cred.Password))
			cfg = append(cfg, [2]string{"http.extraHeader", "Authorization: Basic " + auth})
		}
	case "ssh":
		exe, err := g.exe()
		if err != nil {
			return nil, nil, err
		}
		h, err := serveSSHCred(g.TempDir, sshCred{Key: r.Cred.PrivateKey, Passphrase: r.Cred.Passphrase, HostKey: r.HostKey,
			Allowed: prefixes(r.Policy)})
		if err != nil {
			return nil, nil, err
		}
		cleanup = h.close
		env = append(env, "GIT_SSH_COMMAND="+shellQuote(exe)+" git-ssh", "GIT_SSH_VARIANT=ssh",
			sockEnv+"="+h.path, nonceEnv+"="+h.nonce)
	default:
		return nil, nil, fmt.Errorf("unknown transport %q", r.Transport)
	}
	cmd := exec.CommandContext(ctx, g.bin(), args...)
	cmd.Dir = dir
	cmd.Env = append(append(env, g.Env...), cfg.env()...)
	cmd.WaitDelay = 5 * time.Second
	return cmd, cleanup, nil
}

func prefixes(p sshdial.Policy) []string {
	out := make([]string, len(p.Allowed))
	for i, a := range p.Allowed {
		out[i] = a.String()
	}
	return out
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Run runs a git command against the upstream and returns its stdout; stderr is folded into the
// error (classified into ErrAuth / ErrNotFound / ErrHostKey / ErrConnect when recognisable).
func (g Git) Run(ctx context.Context, r Remote, dir string, args ...string) ([]byte, error) {
	cmd, cleanup, err := g.Command(ctx, r, dir, args...)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), classify(stderr.String(), err)
	}
	return stdout.Bytes(), nil
}

// RunErr is a failed upstream git command: its classified kind and git's own words.
type RunErr struct {
	Kind   error
	Stderr string
}

func (e *RunErr) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if len(msg) > 600 {
		msg = msg[len(msg)-600:]
	}
	if e.Kind != nil {
		return e.Kind.Error() + "：" + msg
	}
	return msg
}

func (e *RunErr) Unwrap() error { return e.Kind }

func classify(stderr string, err error) error {
	s := strings.ToLower(stderr)
	kind := error(nil)
	switch {
	case strings.Contains(s, "host key mismatch"):
		kind = ErrHostKey
	case strings.Contains(s, "authentication failed"), strings.Contains(s, "permission denied"),
		strings.Contains(s, "http basic: access denied"), strings.Contains(s, "returned error: 401"),
		strings.Contains(s, "returned error: 403"), strings.Contains(s, "could not read username"):
		kind = ErrAuth
	case strings.Contains(s, "not found"), strings.Contains(s, "returned error: 404"), strings.Contains(s, "does not appear to be a git repository"),
		strings.Contains(s, "does not exist"):
		kind = ErrNotFound
	case strings.Contains(s, "could not resolve"), strings.Contains(s, "connection refused"), strings.Contains(s, "connect failed"),
		strings.Contains(s, "timed out"), strings.Contains(s, "timeout"):
		kind = ErrConnect
	}
	if stderr == "" {
		stderr = err.Error()
	}
	return &RunErr{Kind: kind, Stderr: stderr}
}

// Refs is what ls-remote saw.
type Refs struct {
	Head     string            // default branch (HEAD's symref target without refs/heads/)
	Branches map[string]string // branch → sha
}

// LsRemote lists the upstream's branches and default branch.
func (g Git) LsRemote(ctx context.Context, r Remote) (Refs, error) {
	out, err := g.Run(ctx, r, "", "ls-remote", "--symref", r.URL, "HEAD", "refs/heads/*")
	if err != nil {
		return Refs{}, err
	}
	return parseLsRemote(string(out)), nil
}

func parseLsRemote(out string) Refs {
	refs := Refs{Branches: map[string]string{}}
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(line, "ref: "); ok {
			target, name, _ := strings.Cut(rest, "\t")
			if name == "HEAD" {
				refs.Head = strings.TrimPrefix(target, "refs/heads/")
			}
			continue
		}
		sha, name, ok := strings.Cut(line, "\t")
		if b, isBranch := strings.CutPrefix(name, "refs/heads/"); ok && isBranch {
			refs.Branches[b] = sha
		}
	}
	return refs
}

// CanPush reports whether the credential may push: the upstream answers a receive-pack
// advertisement (HTTPS: GET info/refs?service=git-receive-pack; SSH: git-receive-pack exec).
// Nothing is pushed.
func (g Git) CanPush(ctx context.Context, r Remote) (bool, error) {
	if r.Kind == "" {
		return false, nil
	}
	if r.Transport == "ssh" {
		return sshCanPush(ctx, r)
	}
	return httpsCanPush(ctx, r, g.TLS)
}

// dialer connects only to addresses the policy allows.
func dialer(p sshdial.Policy) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		addrs, err := p.Resolve(ctx, host)
		if err != nil {
			return nil, err
		}
		var d net.Dialer
		return d.DialContext(ctx, network, net.JoinHostPort(addrs[0].Unmap().String(), port))
	}
}
