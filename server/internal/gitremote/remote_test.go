package gitremote

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/touwaeriol/agent-teams/server/internal/credvault"
	"github.com/touwaeriol/agent-teams/server/internal/sshdial"
)

// The test binary doubles as "atctl git-ssh".
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "git-ssh" {
		os.Exit(RunHelper(os.Args[2:]))
	}
	os.Exit(m.Run())
}

var loopback = sshdial.Policy{Allowed: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// upstream makes root/<name>.git, a bare repository with one commit on main.
func upstream(t *testing.T, root, name string) string {
	t.Helper()
	bare := filepath.Join(root, name+".git")
	git(t, root, "init", "-q", "--bare", "-b", "main", bare)
	git(t, bare, "config", "http.receivepack", "true")
	work := t.TempDir()
	git(t, work, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, work, "add", ".")
	git(t, work, "commit", "-qm", "first")
	git(t, work, "push", "-q", bare, "main")
	return bare
}

// httpUpstream serves root over smart HTTP with Basic auth user:token (git http-backend).
func httpUpstream(t *testing.T, root, user, token string) *httptest.Server {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	backend := &cgi.Handler{Path: gitPath, Args: []string{"http-backend"},
		Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=t"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != user || p != token {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "auth", http.StatusUnauthorized)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestHTTPS(t *testing.T) {
	root := t.TempDir()
	upstream(t, root, "web")
	srv := httpUpstream(t, root, "oauth2", "tok")
	g := Git{TempDir: t.TempDir()}
	ctx := context.Background()
	r := Remote{URL: srv.URL + "/web.git", Transport: "https", Kind: credvault.GitHTTPSToken, Username: "oauth2",
		Cred: credvault.Credential{Password: "tok"}, Policy: loopback}
	refs, err := g.LsRemote(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if refs.Head != "main" || refs.Branches["main"] == "" {
		t.Fatalf("refs %+v", refs)
	}
	if ok, err := g.CanPush(ctx, r); err != nil || !ok {
		t.Fatalf("can push %v %v", ok, err)
	}
	bad := r
	bad.Cred.Password = "nope"
	if _, err := g.LsRemote(ctx, bad); !errors.Is(err, ErrAuth) {
		t.Fatalf("bad token: %v", err)
	}
	blocked := r
	blocked.Policy = sshdial.Policy{}
	if _, err := g.LsRemote(ctx, blocked); !errors.Is(err, sshdial.ErrBlockedAddress) {
		t.Fatalf("loopback not blocked: %v", err)
	}
}

// sshUpstream is an SSH server that accepts key and runs git-upload-pack / git-receive-pack on root.
func sshUpstream(t *testing.T, root string, key ssh.PublicKey) (addr string, hostKey ssh.PublicKey) {
	t.Helper()
	hostSigner, _ := sshtestKey(t)
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if string(k.Marshal()) == string(key.Marshal()) {
			return nil, nil
		}
		return nil, errors.New("denied")
	}}
	cfg.AddHostKey(hostSigner)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveGitSSH(c, cfg, root)
		}
	}()
	return ln.Addr().String(), hostSigner.PublicKey()
}

func serveGitSSH(c net.Conn, cfg *ssh.ServerConfig, root string) {
	_, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer ch.Close()
			for r := range creqs {
				if r.Type != "exec" {
					_ = r.Reply(r.Type == "env", nil)
					continue
				}
				var p struct{ Cmd string }
				_ = ssh.Unmarshal(r.Payload, &p)
				_ = r.Reply(true, nil)
				verb, path, _ := strings.Cut(p.Cmd, " ")
				path = strings.Trim(path, "'")
				cmd := exec.Command("git", strings.TrimPrefix(verb, "git-"), filepath.Join(root, path))
				cmd.Stdin, cmd.Stdout, cmd.Stderr = ch, ch, ch.Stderr()
				status := uint32(0)
				if err := cmd.Run(); err != nil {
					status = 1
				}
				_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ S uint32 }{status}))
				return
			}
		}()
	}
}

func sshtestKey(t *testing.T) (ssh.Signer, credvault.Credential) {
	t.Helper()
	c, _, err := credvault.GenerateSSHKey("test")
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.Signer()
	if err != nil {
		t.Fatal(err)
	}
	return s, c
}

func TestSSH(t *testing.T) {
	root := t.TempDir()
	upstream(t, root, "web")
	userSigner, cred := sshtestKey(t)
	addr, hostKey := sshUpstream(t, root, userSigner.PublicKey())
	_, port, _ := net.SplitHostPort(addr)
	g := Git{TempDir: t.TempDir()}
	ctx := context.Background()
	pinned := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostKey)))
	r := Remote{URL: "ssh://git@127.0.0.1:" + port + "/web.git", Transport: "ssh", Kind: credvault.GitSSHKey, Username: "git",
		Cred: cred, HostKey: pinned, Policy: loopback}
	refs, err := g.LsRemote(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if refs.Branches["main"] == "" {
		t.Fatalf("refs %+v", refs)
	}
	if ok, err := g.CanPush(ctx, r); err != nil || !ok {
		t.Fatalf("can push %v %v", ok, err)
	}
	// Push a branch through the helper.
	mirror := filepath.Join(t.TempDir(), "m.git")
	git(t, "", "init", "-q", "--bare", mirror)
	if _, err := g.Run(ctx, r, mirror, "fetch", "-q", r.URL, "+refs/heads/*:refs/heads/*"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(ctx, r, mirror, "push", "-q", r.URL, "main:refs/heads/copy"); err != nil {
		t.Fatal(err)
	}
	if got := git(t, filepath.Join(root, "web.git"), "rev-parse", "copy"); got != refs.Branches["main"] {
		t.Fatalf("pushed %s want %s", got, refs.Branches["main"])
	}
	other, _ := sshtestKey(t)
	wrong := r
	wrong.HostKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(other.PublicKey())))
	if _, err := g.LsRemote(ctx, wrong); !errors.Is(err, ErrHostKey) {
		t.Fatalf("wrong host key: %v", err)
	}
}

func TestParseSSHArgs(t *testing.T) {
	a, err := parseSSHArgs([]string{"-o", "SendEnv=GIT_PROTOCOL", "-p", "2222", "git@example.com", "git-upload-pack", "'/a/b.git'"})
	if err != nil || a.User != "git" || a.Host != "example.com" || a.Port != 2222 || a.Command != "git-upload-pack '/a/b.git'" {
		t.Fatalf("%+v %v", a, err)
	}
	if got := sshPath("ssh://git@h:22/team/app.git"); got != "/team/app.git" {
		t.Fatal(got)
	}
}
