package engine_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
)

type upgradeView struct {
	Version         string `json:"version"`
	LatestVersion   string `json:"latest_version"`
	UpdateAvailable bool   `json:"update_available"`
	SelfUpdate      bool   `json:"self_update"`
	Upgrade         *struct {
		Target      string `json:"target"`
		Phase       string `json:"phase"`
		Mode        string `json:"mode"`
		ActiveTurns int    `json:"active_turns"`
		Error       string `json:"error"`
	} `json:"upgrade"`
}

// TestNodeSelfUpdate: the console offers the tarball it serves, sends node.upgrade only to a
// daemon under the launcher, and shows the progress the daemon reports.
func TestNodeSelfUpdate(t *testing.T) {
	w := newWorld(t, nil)
	b := w.signUp("ops@example.com")
	var node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "n1"}, &node)
	tgz := []byte("release bytes")
	if err := os.WriteFile(filepath.Join(w.dist, "agent-teams-node-2.0.0+bbbbbbbb.tgz"), tgz, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(tgz)

	old := w.connect(node.Token, nodetest.Options{})
	if code, raw := b.do("POST", "/api/nodes/"+node.Node.ID+"/upgrade", nil); code != 409 || !strings.Contains(string(raw), "upgrade_unsupported") {
		t.Fatalf("upgrade without launcher = %d %s", code, raw)
	}
	old.Close()
	waitClosed(t, old)

	n := w.connect(node.Token, nodetest.Options{Build: "aaaaaaaa",
		Features: []string{"sessions", "git_worktree", "cred_helper", "file_download", "self_update"}})
	view := func() upgradeView {
		var list []upgradeView
		b.must(200, "GET", "/api/nodes", nil, &list)
		return list[0]
	}
	v := view()
	if v.Version != "2.0.0+aaaaaaaa" || v.LatestVersion != "2.0.0+bbbbbbbb" || !v.UpdateAvailable || !v.SelfUpdate || v.Upgrade != nil {
		t.Fatalf("before update %+v", v)
	}
	b.must(202, "POST", "/api/nodes/"+node.Node.ID+"/upgrade", map[string]any{"deadline_s": 600}, nil)
	f, err := n.Wait(w.ctx, protocol.TypeNodeUpgrade, nil)
	if err != nil {
		t.Fatal(err)
	}
	var cmd protocol.NodeUpgrade
	if err := f.Decode(&cmd); err != nil {
		t.Fatal(err)
	}
	if cmd.Version != "2.0.0+bbbbbbbb" || cmd.SHA256 != hex.EncodeToString(sum[:]) || cmd.Mode == nil || *cmd.Mode != "wait_idle" ||
		cmd.DeadlineS == nil || *cmd.DeadlineS != 600 || cmd.Signature != nil {
		t.Fatalf("node.upgrade %+v", cmd)
	}
	if v := view(); v.Upgrade == nil || v.Upgrade.Phase != "requested" || v.Upgrade.Target != "2.0.0+bbbbbbbb" {
		t.Fatalf("requested %+v", v.Upgrade)
	}
	report := func(phase string, turns int, msg string) {
		t.Helper()
		ph := protocol.NodeUpgradeResultPhase(phase)
		body := protocol.NodeUpgradeResult{Ok: phase != "failed", Phase: &ph, Version: ptr("2.0.0+bbbbbbbb")}
		if turns > 0 {
			body.ActiveTurns = &turns
		}
		if msg != "" {
			body.Error = &msg
		}
		seq, err := n.Send(w.ctx, protocol.TypeNodeUpgradeResult, body)
		if err != nil {
			t.Fatal(err)
		}
		if err := n.WaitAck(w.ctx, seq); err != nil {
			t.Fatal(err)
		}
	}
	report("draining", 2, "")
	if v := view(); v.Upgrade.Phase != "draining" || v.Upgrade.ActiveTurns != 2 {
		t.Fatalf("draining %+v", v.Upgrade)
	}
	b.must(202, "POST", "/api/nodes/"+node.Node.ID+"/upgrade", map[string]any{"mode": "now"}, nil)
	if v := view(); v.Upgrade.Phase != "draining" || v.Upgrade.Mode != "now" {
		t.Fatalf("now during the drain %+v", v.Upgrade)
	}
	report("failed", 0, "sha256 mismatch")
	if v := view(); v.Upgrade.Phase != "failed" || v.Upgrade.Error != "sha256 mismatch" {
		t.Fatalf("failed %+v", v.Upgrade)
	}
	n.Close()
	waitClosed(t, n)

	// the new release connects and confirms itself
	n2 := w.connect(node.Token, nodetest.Options{Build: "bbbbbbbb", Epoch: "e2",
		Features: []string{"sessions", "git_worktree", "cred_helper", "file_download", "self_update"}})
	if v := view(); v.UpdateAvailable || v.Version != "2.0.0+bbbbbbbb" {
		t.Fatalf("after update %+v", v)
	}
	n = n2
	report("done", 0, "")
	if v := view(); v.Upgrade.Phase != "done" {
		t.Fatalf("done %+v", v.Upgrade)
	}
}

func ptr[T any](v T) *T { return &v }
