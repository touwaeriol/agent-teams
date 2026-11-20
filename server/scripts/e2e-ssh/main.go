// Command e2e-ssh supports the remote-server end-to-end run (run.sh, docs/v2/remote-servers-verification.md):
//
//	e2e-ssh target -listen 127.0.0.1:PORT -password PW -hostkey FILE
//	    a password-only SSH server that really runs exec requests with sh -c and the sftp subsystem
//	    with OpenSSH's sftp-server (a non-root OpenSSH sshd cannot check passwords);
//	e2e-ssh allow -cidr 127.0.0.1/32
//	    lets the console dial that private range (at_settings.ssh_allowed_private_cidrs, DATABASE_URL).
//
// Test tooling only: it is never built into the console image.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"golang.org/x/crypto/ssh"

	"github.com/touwaeriol/agent-teams/server/internal/sshdial"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: e2e-ssh target|allow ...")
	}
	switch os.Args[1] {
	case "target":
		target(os.Args[2:])
	case "allow":
		allow(os.Args[2:])
	default:
		log.Fatalf("unknown command %q", os.Args[1])
	}
}

func allow(args []string) {
	fs := flag.NewFlagSet("allow", flag.ExitOnError)
	cidr := fs.String("cidr", "127.0.0.1/32", "range to allow")
	_ = fs.Parse(args)
	ctx := context.Background()
	s, err := store.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()
	if err := s.SaveSetting(ctx, sshdial.SettingKey, map[string][]string{"cidrs": {*cidr}}); err != nil {
		log.Fatal(err)
	}
	fmt.Println("allowed", *cidr)
}

func target(args []string) {
	fs := flag.NewFlagSet("target", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:0", "address")
	password := fs.String("password", "", "the only accepted password")
	hostKey := fs.String("hostkey", "", "host key file (created when missing)")
	sftpServer := fs.String("sftp-server", "/usr/lib/openssh/sftp-server", "sftp subsystem binary")
	_ = fs.Parse(args)
	if *password == "" || *hostKey == "" {
		log.Fatal("-password and -hostkey are required")
	}
	signer := loadOrCreateKey(*hostKey)
	check := func(got []byte) bool { return subtle.ConstantTimeCompare(got, []byte(*password)) == 1 }
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
			if check(p) {
				return nil, nil
			}
			return nil, errors.New("wrong password")
		},
		KeyboardInteractiveCallback: func(_ ssh.ConnMetadata, ask ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			a, err := ask("", "", []string{"Password: "}, []bool{false})
			if err == nil && len(a) == 1 && check([]byte(a[0])) {
				return nil, nil
			}
			return nil, errors.New("wrong password")
		},
	}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("listening", ln.Addr())
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go serve(c, cfg, *sftpServer)
	}
}

func loadOrCreateKey(path string) ssh.Signer {
	if b, err := os.ReadFile(path); err == nil {
		s, err := ssh.ParsePrivateKey(b)
		if err != nil {
			log.Fatal(err)
		}
		return s
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		log.Fatal(err)
	}
	s, _ := ssh.NewSignerFromKey(priv)
	return s
}

func serve(c net.Conn, cfg *ssh.ServerConfig, sftpServer string) {
	sc, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	defer sc.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "only session channels")
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go session(ch, creqs, sftpServer)
	}
}

// session runs the first exec / subsystem request of a channel for real and reports its exit status.
func session(ch ssh.Channel, reqs <-chan *ssh.Request, sftpServer string) {
	defer ch.Close()
	var once sync.Once
	for r := range reqs {
		var cmd *exec.Cmd
		switch r.Type {
		case "exec", "subsystem":
			if len(r.Payload) < 4 {
				_ = r.Reply(false, nil)
				continue
			}
			arg := string(r.Payload[4:])
			if r.Type == "exec" {
				cmd = exec.Command("/bin/sh", "-c", arg)
			} else if arg == "sftp" {
				cmd = exec.Command(sftpServer)
			}
		case "env", "pty-req":
			_ = r.Reply(true, nil)
			continue
		}
		if cmd == nil {
			_ = r.Reply(false, nil)
			continue
		}
		_ = r.Reply(true, nil)
		once.Do(func() { go run(ch, cmd) })
	}
}

func run(ch ssh.Channel, cmd *exec.Cmd) {
	cmd.Stdout, cmd.Stderr = ch, ch.Stderr()
	in, _ := cmd.StdinPipe()
	code := 255
	if err := cmd.Start(); err == nil {
		go func() {
			_, _ = io.Copy(in, ch)
			_ = in.Close()
		}()
		err = cmd.Wait()
		code = 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				code = 128 + int(ws.Signal())
			}
		}
	}
	_ = ch.CloseWrite()
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(code))
	_, _ = ch.SendRequest("exit-status", false, b[:])
	_ = ch.Close()
}
