package gitremote

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/touwaeriol/agent-teams/server/internal/credvault"
	"github.com/touwaeriol/agent-teams/server/internal/sshdial"
)

const (
	sockEnv  = "AGENT_TEAMS_GIT_SSH_SOCK"
	nonceEnv = "AGENT_TEAMS_GIT_SSH_NONCE"
	dialWait = 30 * time.Second
)

// sshCred is what the helper fetches from the console for one git command.
type sshCred struct {
	Key        string   `json:"key"`
	Passphrase string   `json:"passphrase,omitempty"`
	HostKey    string   `json:"host_key"`
	Allowed    []string `json:"allowed"`
}

type credServer struct {
	path, nonce string
	ln          net.Listener
	dir         string
}

func (h *credServer) close() {
	_ = h.ln.Close()
	_ = os.RemoveAll(h.dir)
}

// serveSSHCred listens on a fresh 0700 directory's socket and hands c to whoever presents the
// nonce, until close. Only the git command started with the nonce in its environment knows it.
func serveSSHCred(base string, c sshCred) (*credServer, error) {
	if base == "" {
		base = os.TempDir()
	}
	dir, err := os.MkdirTemp(base, "atgs-")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	path := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", path)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	h := &credServer{path: path, nonce: hex.EncodeToString(b), ln: ln, dir: dir}
	payload, _ := json.Marshal(c)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				line, err := bufio.NewReader(conn).ReadString('\n')
				if err != nil || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(line)), []byte(h.nonce)) != 1 {
					return
				}
				_, _ = conn.Write(payload)
			}()
		}
	}()
	return h, nil
}

func fetchSSHCred() (sshCred, error) {
	conn, err := net.DialTimeout("unix", os.Getenv(sockEnv), 5*time.Second)
	if err != nil {
		return sshCred{}, err
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, os.Getenv(nonceEnv)+"\n"); err != nil {
		return sshCred{}, err
	}
	var c sshCred
	if err := json.NewDecoder(conn).Decode(&c); err != nil {
		return sshCred{}, errors.New("no credential for this command")
	}
	return c, nil
}

// sshArgs is what git passes its ssh (GIT_SSH_VARIANT=ssh): options, [user@]host, command.
type sshArgs struct {
	User, Host string
	Port       int
	Command    string
	Env        map[string]string
}

func parseSSHArgs(args []string) (sshArgs, error) {
	a := sshArgs{Port: 22, Env: map[string]string{}}
	var pos []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-p":
			if i+1 >= len(args) {
				return a, errors.New("-p without a port")
			}
			p, err := strconv.Atoi(args[i+1])
			if err != nil {
				return a, fmt.Errorf("bad port %q", args[i+1])
			}
			a.Port = p
			i++
		case "-o":
			i++ // SendEnv=GIT_PROTOCOL and the like
		case "-4", "-6", "-T":
		default:
			pos = append(pos, args[i:]...)
			i = len(args)
		}
	}
	if len(pos) < 2 {
		return a, errors.New("usage: git-ssh [-p port] [user@]host command")
	}
	a.User, a.Host, _ = strings.Cut(pos[0], "@")
	if a.Host == "" {
		a.User, a.Host = "", pos[0]
	}
	a.Command = strings.Join(pos[1:], " ")
	if v := os.Getenv("GIT_PROTOCOL"); v != "" {
		a.Env["GIT_PROTOCOL"] = v
	}
	return a, nil
}

// RunHelper is "atctl git-ssh …": git's ssh for an upstream git command started by Command. It
// fetches the key from the console, logs in (pinned host key, address policy) and runs the
// command with this process's stdio. It returns the exit code.
func RunHelper(args []string) int {
	a, err := parseSSHArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "git-ssh:", err)
		return 255
	}
	c, err := fetchSSHCred()
	if err != nil {
		fmt.Fprintln(os.Stderr, "git-ssh:", err)
		return 255
	}
	ctx, cancel := context.WithTimeout(context.Background(), dialWait)
	client, err := dialSSH(ctx, a.Host, a.Port, a.User, c)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "git-ssh:", err)
		return 255
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		fmt.Fprintln(os.Stderr, "git-ssh:", err)
		return 255
	}
	defer sess.Close()
	for k, v := range a.Env {
		_ = sess.Setenv(k, v) // servers may refuse; git falls back to protocol v0
	}
	sess.Stdin, sess.Stdout, sess.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := sess.Run(a.Command); err != nil {
		var exit *ssh.ExitError
		if errors.As(err, &exit) {
			return exit.ExitStatus()
		}
		fmt.Fprintln(os.Stderr, "git-ssh:", err)
		return 255
	}
	return 0
}

func policyOf(allowed []string) sshdial.Policy {
	var p sshdial.Policy
	for _, s := range allowed {
		if pr, err := netip.ParsePrefix(s); err == nil {
			p.Allowed = append(p.Allowed, pr)
		}
	}
	return p
}

func dialSSH(ctx context.Context, host string, port int, user string, c sshCred) (*ssh.Client, error) {
	res, err := policyOf(c.Allowed).Dial(ctx, sshdial.Target{Host: host, Port: port, Username: user,
		AuthKind: credvault.AuthPrivateKey, Cred: credvault.Credential{PrivateKey: c.Key, Passphrase: c.Passphrase},
		HostKey: c.HostKey}, dialWait)
	return res.Client, err
}

// sshPath is the repository path of an ssh:// URL as git sends it to the server.
func sshPath(rawURL string) string {
	i := strings.Index(rawURL, "://")
	rest := rawURL[i+3:]
	if j := strings.Index(rest, "/"); j >= 0 {
		return rest[j:]
	}
	return "/"
}

func sshCanPush(ctx context.Context, r Remote) (bool, error) {
	host, port, err := r.HostPort()
	if err != nil {
		return false, err
	}
	client, err := dialSSH(ctx, host, port, r.Username, sshCred{Key: r.Cred.PrivateKey, Passphrase: r.Cred.Passphrase,
		HostKey: r.HostKey, Allowed: prefixes(r.Policy)})
	if err != nil {
		return false, err
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		return false, err
	}
	defer sess.Close()
	stdin, _ := sess.StdinPipe()
	stdout, _ := sess.StdoutPipe()
	if err := sess.Start("git-receive-pack '" + strings.ReplaceAll(sshPath(r.URL), "'", `'\''`) + "'"); err != nil {
		return false, err
	}
	head := make([]byte, 4)
	_, err = io.ReadFull(stdout, head)
	_, _ = io.WriteString(stdin, "0000") // end the conversation without pushing
	_ = stdin.Close()
	if err != nil {
		return false, nil // refused before advertising (read-only deploy key, no access)
	}
	_, err = strconv.ParseUint(string(head), 16, 16)
	return err == nil, nil
}

func httpsCanPush(ctx context.Context, r Remote, tlsConf *tls.Config) (bool, error) {
	client := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{DialContext: dialer(r.Policy), TLSClientConfig: tlsConf},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimSuffix(r.URL, "/")+"/info/refs?service=git-receive-pack", nil)
	if err != nil {
		return false, err
	}
	req.SetBasicAuth(r.Username, r.Cred.Password)
	req.Header.Set("User-Agent", "git/2.39 agent-teams")
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	_, _ = io.CopyN(io.Discard, resp.Body, 64<<10)
	return resp.StatusCode == http.StatusOK && strings.Contains(resp.Header.Get("Content-Type"), "git-receive-pack-advertisement"), nil
}
