package sshgw

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"
)

// target is an in-process sshd standing in for the user's server: password login, exec (it echoes
// the command; "cat" copies stdin; "exit N" exits with N; "sleep" waits for the channel to close)
// and an "sftp" subsystem that echoes what it receives.
type target struct {
	addr      string
	hostKey   ssh.Signer
	password  string
	logins    atomic.Int32 // password attempts
	mu        sync.Mutex
	commands  []string
	envs      []string
	ln        net.Listener
	keyboard  bool // ask the password through keyboard-interactive instead
	sawAgent  atomic.Bool
	sawX11    atomic.Bool
	subsystem atomic.Value
}

func newTarget(t *testing.T, password string) *target {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	hk, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tg := &target{addr: ln.Addr().String(), hostKey: hk, password: password, ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go tg.accept()
	return tg
}

func (tg *target) port() int {
	_, p, _ := net.SplitHostPort(tg.addr)
	var n int
	_, _ = fmt.Sscan(p, &n)
	return n
}

func (tg *target) hostKeyLine() string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(tg.hostKey.PublicKey())))
}

func (tg *target) ran() []string {
	tg.mu.Lock()
	defer tg.mu.Unlock()
	return append([]string(nil), tg.commands...)
}

func (tg *target) accept() {
	for {
		c, err := tg.ln.Accept()
		if err != nil {
			return
		}
		go tg.serve(c)
	}
}

func (tg *target) serve(nc net.Conn) {
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(m ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			tg.logins.Add(1)
			if tg.keyboard || m.User() != "deploy" || string(pw) != tg.password {
				return nil, fmt.Errorf("denied")
			}
			return nil, nil
		},
		KeyboardInteractiveCallback: func(m ssh.ConnMetadata, ch ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			if !tg.keyboard {
				return nil, fmt.Errorf("denied")
			}
			ans, err := ch("", "", []string{"Password: "}, []bool{false})
			tg.logins.Add(1)
			if err != nil || len(ans) != 1 || ans[0] != tg.password || m.User() != "deploy" {
				return nil, fmt.Errorf("denied")
			}
			return nil, nil
		},
	}
	cfg.AddHostKey(tg.hostKey)
	sc, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		return
	}
	defer sc.Close()
	go ssh.DiscardRequests(reqs)
	for nch := range chans {
		if nch.ChannelType() != "session" {
			_ = nch.Reject(ssh.UnknownChannelType, "no")
			continue
		}
		ch, creqs, err := nch.Accept()
		if err != nil {
			continue
		}
		go tg.session(ch, creqs)
	}
}

func exitStatus(ch ssh.Channel, code uint32) {
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{code}))
	_ = ch.Close()
}

func (tg *target) session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	for r := range reqs {
		switch r.Type {
		case "env":
			var e struct{ Name, Value string }
			_ = ssh.Unmarshal(r.Payload, &e)
			tg.mu.Lock()
			tg.envs = append(tg.envs, e.Name)
			tg.mu.Unlock()
			_ = r.Reply(true, nil)
		case "auth-agent-req@openssh.com":
			tg.sawAgent.Store(true)
			_ = r.Reply(true, nil)
		case "x11-req":
			tg.sawX11.Store(true)
			_ = r.Reply(true, nil)
		case "pty-req", "window-change":
			if r.WantReply {
				_ = r.Reply(true, nil)
			}
		case "exec":
			var e struct{ Command string }
			_ = ssh.Unmarshal(r.Payload, &e)
			tg.mu.Lock()
			tg.commands = append(tg.commands, e.Command)
			tg.mu.Unlock()
			_ = r.Reply(true, nil)
			go tg.exec(ch, e.Command)
		case "subsystem":
			var s struct{ Name string }
			_ = ssh.Unmarshal(r.Payload, &s)
			tg.subsystem.Store(s.Name)
			_ = r.Reply(true, nil)
			go func() {
				_, _ = io.Copy(ch, ch) // echo, like a server answering packets
				exitStatus(ch, 0)
			}()
		default:
			if r.WantReply {
				_ = r.Reply(false, nil)
			}
		}
	}
}

func (tg *target) exec(ch ssh.Channel, cmd string) {
	switch {
	case cmd == "cat":
		_, _ = io.Copy(ch, ch)
		exitStatus(ch, 0)
	case cmd == "flood": // like `yes`: writes until the channel is closed
		line := []byte(strings.Repeat("y", 1023) + "\n")
		for {
			if _, err := ch.Write(line); err != nil {
				return
			}
		}
	case cmd == "sleep":
		_, _ = io.Copy(io.Discard, ch)
		exitStatus(ch, 0)
	case strings.HasPrefix(cmd, "exit "):
		var n uint32
		_, _ = fmt.Sscan(strings.TrimPrefix(cmd, "exit "), &n)
		_, _ = fmt.Fprintf(ch.Stderr(), "failing with %d\n", n)
		exitStatus(ch, n)
	default:
		_, _ = fmt.Fprintf(ch, "ran: %s\n", cmd)
		exitStatus(ch, 0)
	}
}
