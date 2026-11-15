package httpapi

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/platform"
)

// installEnv is a fake machine: HOME, stub binaries on PATH and a release tarball.
type installEnv struct {
	t       *testing.T
	home    string
	bin     string
	log     string
	tgz     string
	sha     string
	nodeVer string
}

func newInstallEnv(t *testing.T) *installEnv {
	t.Helper()
	for _, tool := range []string{"bash", "tar", "sha256sum", "sed", "mktemp"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	dir := t.TempDir()
	e := &installEnv{t: t, home: filepath.Join(dir, "home"), bin: filepath.Join(dir, "bin"), log: filepath.Join(dir, "calls.log"),
		tgz: filepath.Join(dir, "release.tgz"), nodeVer: "22"}
	_ = os.MkdirAll(e.home, 0o755)
	_ = os.MkdirAll(e.bin, 0o755)
	e.writeTarball()
	e.stubs()
	return e
}

func (e *installEnv) writeTarball() {
	e.writeTarballFiles(map[string]string{"package/package.json": `{"name":"@agent-teams/node"}`,
		"package/package-lock.json": `{}`, "package/dist/node.mjs": "console.log('daemon')"})
}

func (e *installEnv) writeTarballFiles(files map[string]string) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()
	_ = os.WriteFile(e.tgz, buf.Bytes(), 0o644)
	sum := sha256.Sum256(buf.Bytes())
	e.sha = hex.EncodeToString(sum[:])
}

func (e *installEnv) stubs() {
	scripts := map[string]string{
		"node":      `case "$1" in -p) echo "$NODE_MAJOR";; --version) echo "v$NODE_MAJOR.0.0";; esac`,
		"npm":       `echo "npm $* in $PWD" >> "$CALLS"`,
		"curl":      `while [ $# -gt 0 ]; do case "$1" in -o) out="$2"; shift 2;; -*) shift;; *) url="$1"; shift;; esac; done; echo "curl $url" >> "$CALLS"; cp "$FIXTURE" "$out"`,
		"systemctl": `echo "systemctl $*" >> "$CALLS"`,
		"loginctl":  `exit 0`,
		"claude":    `exit 0`,
	}
	for name, body := range scripts {
		_ = os.WriteFile(filepath.Join(e.bin, name), []byte("#!/bin/bash\n"+body+"\n"), 0o755)
	}
}

func (e *installEnv) run(script string, env []string, args ...string) (string, error) {
	e.t.Helper()
	path := filepath.Join(e.t.TempDir(), "install-node.sh")
	_ = os.WriteFile(path, []byte(script), 0o755)
	cmd := exec.Command("bash", append([]string{path}, args...)...)
	cmd.Env = append([]string{"HOME=" + e.home, "USER=tester", "PATH=" + e.bin + ":/usr/bin:/bin", "CALLS=" + e.log,
		"FIXTURE=" + e.tgz, "NODE_MAJOR=" + e.nodeVer}, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (e *installEnv) read(rel string) string {
	raw, _ := os.ReadFile(filepath.Join(e.home, rel))
	return string(raw)
}

func TestInstallScriptFreshInstall(t *testing.T) {
	e := newInstallEnv(t)
	script := renderInstallScript("https://console.test", platform.NodeRelease{Version: "2.0.3", SHA256: e.sha})
	out, err := e.run(script, []string{"AGENT_TEAMS_NODE_TOKEN=tok-1"}, "--name", "rc-erio")
	if err != nil {
		t.Fatalf("install failed: %v\n%s", err, out)
	}
	envFile := e.read(".config/agent-teams/node.env")
	for _, want := range []string{"AGENT_TEAMS_NODE_TOKEN=tok-1", "AGENT_TEAMS_CONSOLE=https://console.test",
		"AGENT_TEAMS_NODE=rc-erio", "AGENT_TEAMS_MAX_SESSIONS=4"} {
		if !strings.Contains(envFile, want) {
			t.Errorf("node.env missing %s:\n%s", want, envFile)
		}
	}
	info, _ := os.Stat(filepath.Join(e.home, ".config/agent-teams/node.env"))
	if info.Mode().Perm() != 0o600 {
		t.Errorf("node.env mode %v", info.Mode().Perm())
	}
	link, _ := os.Readlink(filepath.Join(e.home, ".local/share/agent-teams/current"))
	if link != "node-2.0.3" {
		t.Fatalf("current -> %q", link)
	}
	unit := e.read(".config/systemd/user/agent-teams-node.service")
	if !strings.Contains(unit, "/current/dist/node.mjs run\n") || strings.Contains(unit, "2.0.3") || !strings.Contains(unit, e.bin+"/node ") {
		t.Fatalf("unit:\n%s", unit)
	}
	calls, _ := os.ReadFile(e.log)
	if !strings.Contains(string(calls), "curl https://console.test/node/dist/agent-teams-node-2.0.3.tgz") ||
		!strings.Contains(string(calls), "npm ci --omit=dev") || !strings.Contains(string(calls), "systemctl --user restart agent-teams-node.service") {
		t.Fatalf("calls:\n%s", calls)
	}
}

func TestInstallScriptRootEntryLayout(t *testing.T) {
	e := newInstallEnv(t)
	e.writeTarballFiles(map[string]string{"package/package.json": `{}`, "package/npm-shrinkwrap.json": `{}`, "package/node.mjs": "x"})
	script := renderInstallScript("https://console.test", platform.NodeRelease{Version: "2.0.0", SHA256: e.sha})
	if out, err := e.run(script, []string{"AGENT_TEAMS_NODE_TOKEN=t"}, "--name", "n"); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	unit := e.read(".config/systemd/user/agent-teams-node.service")
	if !strings.Contains(unit, "/current/node.mjs run\n") {
		t.Fatalf("unit:\n%s", unit)
	}
}

func TestInstallScriptUpgradesV1NodeEnv(t *testing.T) {
	e := newInstallEnv(t)
	_ = os.MkdirAll(filepath.Join(e.home, ".config/agent-teams"), 0o700)
	v1 := "AGENT_TEAMS_CONSOLE=https://old.console\nAGENT_TEAMS_NODE=cvp\nAGENT_TEAMS_NODE_TOKEN=v1-token\nAGENT_TEAMS_ROOT=" + e.home + "/agents\nAGENT_TEAMS_MAX_RUNS=6\nPATH=/x\n"
	_ = os.WriteFile(filepath.Join(e.home, ".config/agent-teams/node.env"), []byte(v1), 0o600)
	script := renderInstallScript("https://console.test", platform.NodeRelease{Version: "2.1.0", SHA256: e.sha})
	if out, err := e.run(script, nil); err != nil {
		t.Fatalf("upgrade failed: %v\n%s", err, out)
	}
	envFile := e.read(".config/agent-teams/node.env")
	for _, want := range []string{"AGENT_TEAMS_NODE_TOKEN=v1-token", "AGENT_TEAMS_CONSOLE=https://old.console",
		"AGENT_TEAMS_NODE=cvp", "AGENT_TEAMS_ROOT=" + e.home + "/agents", "AGENT_TEAMS_MAX_SESSIONS=6"} {
		if !strings.Contains(envFile, want) {
			t.Errorf("node.env missing %s:\n%s", want, envFile)
		}
	}
}

func TestInstallScriptRefusesBadInputs(t *testing.T) {
	e := newInstallEnv(t)
	bad := renderInstallScript("https://console.test", platform.NodeRelease{Version: "2.0.3", SHA256: strings.Repeat("0", 64)})
	if out, err := e.run(bad, []string{"AGENT_TEAMS_NODE_TOKEN=t"}, "--name", "n"); err == nil || !strings.Contains(out, "校验失败") {
		t.Fatalf("bad checksum accepted: %s", out)
	}
	none := renderInstallScript("https://console.test", platform.NodeRelease{})
	if out, err := e.run(none, []string{"AGENT_TEAMS_NODE_TOKEN=t"}, "--name", "n"); err == nil || !strings.Contains(out, "发布包") {
		t.Fatalf("missing release accepted: %s", out)
	}
	if out, err := e.run(none, nil, "--name", "n"); err == nil || !strings.Contains(out, "令牌") {
		t.Fatalf("missing token accepted: %s", out)
	}
	e.nodeVer = "20"
	good := renderInstallScript("https://console.test", platform.NodeRelease{Version: "2.0.3", SHA256: e.sha})
	if out, err := e.run(good, []string{"AGENT_TEAMS_NODE_TOKEN=t"}, "--name", "n"); err == nil || !strings.Contains(out, "22") {
		t.Fatalf("old node accepted: %s", out)
	}
}

func TestInstallScriptRollback(t *testing.T) {
	e := newInstallEnv(t)
	for _, v := range []string{"2.0.3", "2.0.4"} {
		script := renderInstallScript("https://console.test", platform.NodeRelease{Version: v, SHA256: e.sha})
		if out, err := e.run(script, []string{"AGENT_TEAMS_NODE_TOKEN=t"}, "--name", "n"); err != nil {
			t.Fatalf("install %s: %v\n%s", v, err, out)
		}
	}
	script := renderInstallScript("https://console.test", platform.NodeRelease{})
	if out, err := e.run(script, nil, "--rollback"); err != nil {
		t.Fatalf("rollback: %v\n%s", err, out)
	}
	link, _ := os.Readlink(filepath.Join(e.home, ".local/share/agent-teams/current"))
	if link != "node-2.0.3" {
		t.Fatalf("after rollback current -> %q", link)
	}
}

func TestRenderRejectsUnsafeRelease(t *testing.T) {
	s := renderInstallScript("https://c", platform.NodeRelease{Version: "1; rm -rf /", SHA256: strings.Repeat("a", 64)})
	if strings.Contains(s, "rm -rf /") {
		t.Fatal("unsafe version rendered")
	}
}
