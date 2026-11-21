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
		"AGENT_TEAMS_NODE=rc-erio", "AGENT_TEAMS_MAX_SESSIONS=\n"} {
		if !strings.Contains(envFile, want) {
			t.Errorf("node.env missing %s:\n%s", want, envFile)
		}
	}
	info, _ := os.Stat(filepath.Join(e.home, ".config/agent-teams/node.env"))
	if info.Mode().Perm() != 0o600 {
		t.Errorf("node.env mode %v", info.Mode().Perm())
	}
	link, _ := os.Readlink(filepath.Join(e.home, ".local/share/agent-teams/current"))
	if link != "releases/2.0.3" {
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

func TestInstallScriptKeepsNodeEnv(t *testing.T) {
	e := newInstallEnv(t)
	_ = os.MkdirAll(filepath.Join(e.home, ".config/agent-teams"), 0o700)
	old := "AGENT_TEAMS_CONSOLE=https://old.console\nAGENT_TEAMS_NODE=cvp\nAGENT_TEAMS_NODE_TOKEN=old-token\nAGENT_TEAMS_ROOT=" + e.home + "/agents\nAGENT_TEAMS_MAX_SESSIONS=6\nPATH=/x\n"
	_ = os.WriteFile(filepath.Join(e.home, ".config/agent-teams/node.env"), []byte(old), 0o600)
	script := renderInstallScript("https://console.test", platform.NodeRelease{Version: "2.1.0", SHA256: e.sha})
	if out, err := e.run(script, nil); err != nil {
		t.Fatalf("upgrade failed: %v\n%s", err, out)
	}
	envFile := e.read(".config/agent-teams/node.env")
	for _, want := range []string{"AGENT_TEAMS_NODE_TOKEN=old-token", "AGENT_TEAMS_CONSOLE=https://old.console",
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
	if link != "releases/2.0.3" {
		t.Fatalf("after rollback current -> %q", link)
	}
}

// TestInstallScriptLauncherLayout: a release with launch.mjs installs the launcher, starts
// through it, keeps the running release when the same id is reinstalled, and keeps node.env
// lines it does not manage.
func TestInstallScriptLauncherLayout(t *testing.T) {
	e := newInstallEnv(t)
	files := map[string]string{"package/package.json": `{}`, "package/npm-shrinkwrap.json": `{"v":1}`, "package/node.mjs": "a", "package/launch.mjs": "launcher"}
	e.writeTarballFiles(files)
	_ = os.MkdirAll(filepath.Join(e.home, ".config/agent-teams"), 0o700)
	_ = os.WriteFile(filepath.Join(e.home, ".config/agent-teams/node.env"), []byte("AGENT_TEAMS_NODE_TOKEN=t\nAGENT_TEAMS_NODE=n\nAGENT_TEAMS_PASS_ENV=HTTPS_PROXY\n"), 0o600)
	data := filepath.Join(e.home, ".local/share/agent-teams")
	// an old-style install the first run migrates from
	_ = os.MkdirAll(filepath.Join(data, "node-2.0.0", "node_modules", "dep"), 0o755)
	_ = os.WriteFile(filepath.Join(data, "node-2.0.0", "npm-shrinkwrap.json"), []byte(`{"v":1}`), 0o644)
	_ = os.Symlink("node-2.0.0", filepath.Join(data, "current"))
	install := func(v string) {
		t.Helper()
		script := renderInstallScript("https://console.test", platform.NodeRelease{Version: v, SHA256: e.sha})
		if out, err := e.run(script, nil); err != nil {
			t.Fatalf("install %s: %v\n%s", v, err, out)
		}
	}
	install("2.0.0+aaaaaaaa")
	if l, _ := os.Readlink(filepath.Join(data, "current")); l != "releases/2.0.0+aaaaaaaa" {
		t.Fatalf("current -> %q", l)
	}
	if l, _ := os.Readlink(filepath.Join(data, "previous")); l != "node-2.0.0" {
		t.Fatalf("previous -> %q", l)
	}
	if _, err := os.Stat(filepath.Join(data, "releases/2.0.0+aaaaaaaa/node_modules/dep")); err != nil {
		t.Fatalf("node_modules not reused: %v", err)
	}
	calls, _ := os.ReadFile(e.log)
	if strings.Contains(string(calls), "npm ci") {
		t.Fatalf("npm ran although the lock file did not change:\n%s", calls)
	}
	if e.read(".local/share/agent-teams/bin/launch.mjs") != "launcher" {
		t.Fatal("launcher not installed")
	}
	unit := e.read(".config/systemd/user/agent-teams-node.service")
	if !strings.Contains(unit, data+"/bin/launch.mjs run\n") || !strings.Contains(unit, "TimeoutStopSec=120") {
		t.Fatalf("unit:\n%s", unit)
	}
	if !strings.Contains(e.read(".config/agent-teams/node.env"), "AGENT_TEAMS_PASS_ENV=HTTPS_PROXY") {
		t.Fatalf("node.env lost a line:\n%s", e.read(".config/agent-teams/node.env"))
	}
	if !strings.Contains(e.read(".local/share/agent-teams/upgrade.json"), `"state":"confirmed"`) {
		t.Fatal("upgrade.json not reset")
	}
	// the same release again: nothing downloaded, nothing removed
	install("2.0.0+aaaaaaaa")
	if c, _ := os.ReadFile(e.log); strings.Count(string(c), "curl ") != 1 {
		t.Fatalf("downloaded again:\n%s", c)
	}
	// a third release: the old-style dir goes, current and previous stay
	files["package/node.mjs"] = "b"
	files["package/npm-shrinkwrap.json"] = `{"v":2}`
	e.writeTarballFiles(files)
	install("2.0.0+bbbbbbbb")
	if _, err := os.Stat(filepath.Join(data, "node-2.0.0")); !os.IsNotExist(err) {
		t.Fatal("old release kept")
	}
	for _, v := range []string{"2.0.0+aaaaaaaa", "2.0.0+bbbbbbbb"} {
		if _, err := os.Stat(filepath.Join(data, "releases", v, ".installed")); err != nil {
			t.Fatalf("%s missing", v)
		}
	}
}

func TestRenderRejectsUnsafeRelease(t *testing.T) {
	s := renderInstallScript("https://c", platform.NodeRelease{Version: "1; rm -rf /", SHA256: strings.Repeat("a", 64)})
	if strings.Contains(s, "rm -rf /") {
		t.Fatal("unsafe version rendered")
	}
}

func TestInstallScriptReinstallsWhenPackageChanges(t *testing.T) {
	e := newInstallEnv(t)
	script := renderInstallScript("https://console.test", platform.NodeRelease{Version: "2.0.3", SHA256: e.sha})
	if out, err := e.run(script, []string{"AGENT_TEAMS_NODE_TOKEN=tok-1"}, "--name", "rc-erio"); err != nil {
		t.Fatalf("install failed: %v\n%s", err, out)
	}
	count := func() int {
		calls, _ := os.ReadFile(e.log)
		return strings.Count(string(calls), "curl https://console.test/node/dist/")
	}
	if out, err := e.run(script, nil, "--name", "rc-erio"); err != nil || count() != 1 {
		t.Fatalf("same package was downloaded again (%d): %v\n%s", count(), err, out)
	}
	_ = os.WriteFile(filepath.Join(e.home, ".local/share/agent-teams/releases/2.0.3/.installed"), []byte("old-sha\n"), 0o644)
	if out, err := e.run(script, nil, "--name", "rc-erio"); err != nil || count() != 2 {
		t.Fatalf("rebuilt package with the same version was not reinstalled (%d): %v\n%s", count(), err, out)
	}
}
