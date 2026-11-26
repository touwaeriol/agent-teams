package userfs

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanPath(t *testing.T) {
	for in, want := range map[string]string{"": "", "/": "", "a/b.txt": "a/b.txt", "/a/b/": "a/b", "报告 v2.pdf": "报告 v2.pdf"} {
		if got, err := CleanPath(in); err != nil || got != want {
			t.Errorf("CleanPath(%q) = %q %v", in, got, err)
		}
	}
	for _, in := range []string{"../x", "a/../b", "a//b", "./a", "a\\b", "a\x00", "a\nb", strings.Repeat("x", MaxPathBytes+1)} {
		if _, err := CleanPath(in); !errors.Is(err, ErrBadPath) {
			t.Errorf("CleanPath(%q) accepted", in)
		}
	}
	if _, err := Join("projects/p1", "../p2/x"); !errors.Is(err, ErrBadPath) {
		t.Error("Join left its base")
	}
}

// exerciseFiles runs the same checks on any Files.
func exerciseFiles(t *testing.T, f Files) {
	ctx := context.Background()
	if err := f.Put(ctx, "projects/p1/docs/a.txt", strings.NewReader("hello"), 5); err != nil {
		t.Fatal(err)
	}
	if err := f.Put(ctx, "projects/p1/b.csv", strings.NewReader("1,2"), 3); err != nil {
		t.Fatal(err)
	}
	if err := f.Mkdir(ctx, "projects/p1/empty"); err != nil {
		t.Fatal(err)
	}
	es, err := f.List(ctx, "projects/p1")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name)
	}
	if strings.Join(names, ",") != "docs,empty,b.csv" || !es[0].Dir || es[2].Size != 3 {
		t.Fatalf("list: %+v", es)
	}
	r, e, err := f.Open(ctx, "projects/p1/docs/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r)
	r.Close()
	if string(b) != "hello" || e.Size != 5 || e.Name != "a.txt" {
		t.Fatalf("open: %q %+v", b, e)
	}
	if _, _, err := f.Open(ctx, "projects/p1/nope"); !errors.Is(err, ErrNoFile) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := f.List(ctx, "projects/nope"); !errors.Is(err, ErrNoFile) {
		t.Fatalf("missing dir: %v", err)
	}
	if err := f.Remove(ctx, "projects/p1/docs"); err != nil {
		t.Fatal(err)
	}
	if es, _ := f.List(ctx, "projects/p1"); len(es) != 2 {
		t.Fatalf("after remove: %+v", es)
	}
	if err := f.Remove(ctx, ""); !errors.Is(err, ErrBadPath) {
		t.Fatalf("remove root: %v", err)
	}
}

func TestDirFiles(t *testing.T) {
	exerciseFiles(t, DirFiles{Root: t.TempDir()})
}

// TestGatewayFiles runs a real juicefs gateway (sqlite metadata, local storage) when JUICEFS_BIN
// names a juicefs binary.
func TestGatewayFiles(t *testing.T) {
	bin := os.Getenv("JUICEFS_BIN")
	if bin == "" {
		t.Skip("JUICEFS_BIN not set")
	}
	d := t.TempDir()
	meta := "sqlite3://" + filepath.Join(d, "meta.db")
	out, err := exec.Command(bin, "format", "--storage", "file", "--bucket", filepath.Join(d, "data")+"/", "--no-update", meta, "at-test").CombinedOutput()
	if err != nil {
		t.Fatalf("format: %v %s", err, out)
	}
	g := &Gateways{Bin: bin}
	f, err := g.Files(context.Background(), "at-test", meta, "")
	if err != nil {
		t.Fatal(err)
	}
	defer g.Stop("at-test")
	again, _ := g.Files(context.Background(), "at-test", meta, "")
	if again != f {
		t.Fatal("a running gateway is reused")
	}
	exerciseFiles(t, f)
	g.Stop("at-test")
	if _, err := f.List(context.Background(), ""); err == nil {
		t.Fatal("stopped gateway still answers")
	}
}

// TestServiceFiles: only a ready volume opens, through the console's own database address; deleting closes it first.
func TestServiceFiles(t *testing.T) {
	s, _, _, vols, _ := newTest(t)
	ctx := context.Background()
	if _, err := s.Files(ctx, "u1"); !IsNotFound(err) {
		t.Fatalf("no volume: %v", err)
	}
	if _, err := s.Enable(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	f, err := s.Files(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	exerciseFiles(t, f)
	if len(vols.opened) != 1 || !strings.Contains(vols.opened[0], "@db.internal:5432/agent_teams") || !strings.Contains(vols.opened[0], "sslmode=disable") {
		t.Fatalf("meta url: %v", vols.opened)
	}
	if err := s.Disable(ctx, "u1"); err != nil || len(vols.closed) != 1 || vols.closed[0] != NamesFor("u1").Volume {
		t.Fatalf("disable: %v closed %v", err, vols.closed)
	}
}
