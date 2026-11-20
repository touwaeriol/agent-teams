package gitrelay

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

const (
	hookSockEnv  = "AGENT_TEAMS_RELAY_SOCK"
	hookNonceEnv = "AGENT_TEAMS_RELAY_NONCE"
	hookExeEnv   = "AGENT_TEAMS_RELAY_EXE"
	// hookWait bounds the upstream push one pre-receive waits for.
	hookWait = 5 * time.Minute
)

// quarantineEnv are the variables receive-pack gives its hooks so they see the pushed objects.
var quarantineEnv = []string{"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_QUARANTINE_PATH"}

type hookRequest struct {
	Nonce   string            `json:"nonce"`
	Updates []Update          `json:"updates"`
	Env     map[string]string `json:"env"`
}

type hookReply struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type hookServer struct {
	path, nonce, dir string
	ln               net.Listener
}

func (h *hookServer) close() {
	_ = h.ln.Close()
	_ = os.RemoveAll(h.dir)
}

// serveHook listens for the pre-receive of one receive-pack (nonce-guarded unix socket in a
// fresh 0700 directory) and answers it with handle.
func (r *Relay) serveHook(ctx context.Context, repo store.GitRepo, c Claims, dir string) (*hookServer, error) {
	base := r.Git.TempDir
	if base == "" {
		base = os.TempDir()
	}
	tmp, err := os.MkdirTemp(base, "atgr-")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(tmp, "s")
	ln, err := net.Listen("unix", path)
	if err != nil {
		_ = os.RemoveAll(tmp)
		return nil, err
	}
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	h := &hookServer{path: path, nonce: hex.EncodeToString(b), dir: tmp, ln: ln}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go r.answerHook(ctx, conn, h.nonce, repo, c, dir)
		}
	}()
	return h, nil
}

func (r *Relay) answerHook(ctx context.Context, conn net.Conn, nonce string, repo store.GitRepo, c Claims, dir string) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(hookWait + 10*time.Second))
	var req hookRequest
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&req); err != nil {
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.Nonce), []byte(nonce)) != 1 {
		return
	}
	reply := hookReply{OK: true}
	if err := r.forward(ctx, repo, c, dir, req); err != nil {
		reply = hookReply{Message: err.Error()}
	}
	_ = json.NewEncoder(conn).Encode(reply)
}

// forward checks a push against the token and sends it to the upstream (atomically), with the
// quarantined objects visible to git. receive-pack updates the mirror once the hook succeeds.
func (r *Relay) forward(ctx context.Context, repo store.GitRepo, c Claims, dir string, req hookRequest) error {
	if len(req.Updates) == 0 {
		return nil
	}
	updates := make([]Update, 0, len(req.Updates))
	for _, u := range req.Updates {
		if !c.allows(u.Ref) {
			return fmt.Errorf("不允许推送 %s（平台分支、默认分支和标签只由平台更新）", u.Ref)
		}
		if u.New == zeroSHA && c.Mode == ModePush {
			return fmt.Errorf("不允许删除 %s", u.Ref)
		}
		updates = append(updates, Update{Ref: u.Ref, Old: unzero(u.Old), New: unzero(u.New)})
	}
	extra := []string{}
	for _, k := range quarantineEnv {
		if v := req.Env[k]; v != "" {
			extra = append(extra, k+"="+v)
		}
	}
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), hookWait)
	defer cancel()
	if err := r.push(pctx, repo, dir, extra, updates); err != nil {
		if errors.Is(err, ErrStale) {
			return err
		}
		return fmt.Errorf("推送到上游失败：%w", err)
	}
	return nil
}

func unzero(sha string) string {
	if sha == zeroSHA {
		return ""
	}
	return sha
}

// RunHook is "atctl git-hook pre-receive": run by receive-pack inside a relay push, it hands the
// ref updates and the quarantine to the console and prints its answer. It returns the exit code.
func RunHook(args []string) int {
	if len(args) == 0 || args[0] != "pre-receive" {
		fmt.Fprintln(os.Stderr, "usage: git-hook pre-receive")
		return 2
	}
	req := hookRequest{Nonce: os.Getenv(hookNonceEnv), Env: map[string]string{}}
	in, _ := io.ReadAll(os.Stdin)
	for _, line := range strings.Split(strings.TrimSpace(string(in)), "\n") {
		f := strings.Fields(line)
		if len(f) == 3 {
			req.Updates = append(req.Updates, Update{Old: f[0], New: f[1], Ref: f[2]})
		}
	}
	for _, k := range quarantineEnv {
		if v := os.Getenv(k); v != "" {
			req.Env[k] = abs(v)
		}
	}
	reply, err := askHook(os.Getenv(hookSockEnv), req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent-teams:", err)
		return 1
	}
	if !reply.OK {
		fmt.Fprintln(os.Stderr, "agent-teams:", reply.Message)
		return 1
	}
	return 0
}

// abs resolves the quarantine paths (git may give them relative to the repository, which is the
// hook's working directory) so the console can use them from anywhere.
func abs(list string) string {
	parts := strings.Split(list, string(os.PathListSeparator))
	for i, p := range parts {
		if a, err := filepath.Abs(p); err == nil && p != "" {
			parts[i] = a
		}
	}
	return strings.Join(parts, string(os.PathListSeparator))
}

func askHook(sock string, req hookRequest) (hookReply, error) {
	conn, err := net.DialTimeout("unix", sock, 5*time.Second)
	if err != nil {
		return hookReply{}, err
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return hookReply{}, err
	}
	var reply hookReply
	if err := json.NewDecoder(conn).Decode(&reply); err != nil {
		return hookReply{}, errors.New("the console did not answer the push")
	}
	return reply, nil
}
