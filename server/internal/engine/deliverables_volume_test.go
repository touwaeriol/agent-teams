package engine_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/artifacts"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/orchapi"
	"github.com/touwaeriol/agent-teams/server/internal/userfs"
)

// TestDeliverablesInSharedFolder: without a shared folder configured (local development) versions
// go to the database; once it is configured they go only to the owner's folder
// (projects/<pid>/.deliverables/<id>/v<n>/, rows without data): a publish before the folder is
// ready is refused, the automatic sweep switches the folder on and moves the older versions in;
// signed URLs and read_deliverable serve them from there; the page sees the folder but cannot
// write into it; deleting the deliverable removes its folder.
func TestDeliverablesInSharedFolder(t *testing.T) {
	tm, _ := startDesignTeam(t)
	page := upFile{"index.html", "<!doctype html><title>v1</title>"}
	var out protocol.PublishFileResult
	res := tm.dev.publish(map[string]any{"path": "site", "kind": "design", "title": "登录页"}, 1<<20, page)
	_ = json.Unmarshal(*res.Structured, &out)
	if !res.Ok {
		t.Fatalf("publish before the folder %+v", res.Content)
	}
	id := out.DeliverableID
	if countRows(t, tm.w, `SELECT count(*) FROM at_deliverable_files WHERE deliverable_id = $1 AND NOT in_volume AND data IS NOT NULL`, id) != 1 {
		t.Fatal("version 1 not in the database")
	}

	fakes := useFolder(t, tm.w)
	v2 := upFile{"index.html", "<!doctype html><title>v2</title>"}
	res = tm.dev.publish(map[string]any{"path": "site", "kind": "design", "title": "登录页", "deliverable_id": id}, 1<<20, v2)
	if res.Ok || !strings.Contains(fmt.Sprint(res.Content), "还在开通中") {
		t.Fatalf("publish before the folder is ready %+v", res.Content)
	}
	tm.w.e.SweepSharedFolders(tm.w.ctx) // switches the folder on and moves version 1 in
	volume := filepath.Join(fakes.root, userfs.NamesFor(tm.ownerID(t)).Volume)
	dir := filepath.Join(volume, "projects", tm.projectID, ".deliverables", id)

	res = tm.dev.publish(map[string]any{"path": "site", "kind": "design", "title": "登录页", "deliverable_id": id}, 1<<20, v2, upFile{"css/a.css", "b{}"})
	_ = json.Unmarshal(*res.Structured, &out)
	if !res.Ok || out.Version != 2 {
		t.Fatalf("publish into the folder %+v %+v", res.Content, out)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "v2", "css", "a.css")); err != nil || string(got) != "b{}" {
		t.Fatalf("volume copy %q %v", got, err)
	}
	if countRows(t, tm.w, `SELECT count(*) FROM at_deliverable_files WHERE deliverable_id = $1 AND version = 2 AND in_volume AND data IS NULL`, id) != 2 {
		t.Fatal("version 2 rows still carry data")
	}
	var files orchapi.DeliverableFiles
	tm.b.must(200, "GET", "/api/deliverables/"+id+"/files", nil, &files)
	if resp, body := fetch(t, tm.w, files.EntryURL); resp.StatusCode != 200 || body != v2.body {
		t.Fatalf("v2 served %d %q", resp.StatusCode, body)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "v1", "index.html")); err != nil || string(got) != page.body {
		t.Fatalf("moved v1 %q %v", got, err)
	}
	if countRows(t, tm.w, `SELECT count(*) FROM at_deliverable_files WHERE deliverable_id = $1 AND data IS NOT NULL`, id) != 0 {
		t.Fatal("data left in the database")
	}
	tm.b.must(200, "GET", "/api/deliverables/"+id+"/files?version=1", nil, &files)
	if resp, body := fetch(t, tm.w, files.EntryURL); resp.StatusCode != 200 || body != page.body {
		t.Fatalf("moved v1 served %d %q", resp.StatusCode, body)
	}
	if r, got := tm.dev.readCall(map[string]any{"deliverable_id": id}); !r.Ok || got["css/a.css"] != "b{}" || got["index.html"] != v2.body {
		t.Fatalf("read_deliverable %v %v", r.Ok, got)
	}

	// The page lists the folder but never writes into it.
	var listing orchapi.ProjectFiles
	tm.b.must(200, "GET", "/api/projects/"+tm.projectID+"/files?dir=.deliverables/"+id, nil, &listing)
	if len(listing.Entries) != 2 || listing.Entries[0].Name != "v1" || listing.Entries[1].Name != "v2" {
		t.Fatalf("deliverable folder %+v", listing.Entries)
	}
	tm.b.must(403, "DELETE", "/api/projects/"+tm.projectID+"/files?path=.deliverables/"+id, nil, nil)
	tm.b.must(403, "POST", "/api/projects/"+tm.projectID+"/files/folders", map[string]string{"path": ".deliverables/x"}, nil)

	// A file changed behind the platform's back is not served as the published one.
	_ = os.WriteFile(filepath.Join(dir, "v2", "css", "a.css"), []byte("changed!"), 0o644)
	tm.b.must(200, "GET", "/api/deliverables/"+id+"/files", nil, &files)
	if resp, _ := fetch(t, tm.w, files.BaseURL+"css/a.css"); resp.StatusCode != 404 {
		t.Fatalf("changed file = %d", resp.StatusCode)
	}

	// An @ reference to a deliverable whose entry file changed in the folder still goes out, with a note.
	_ = os.WriteFile(filepath.Join(dir, "v2", "index.html"), []byte("changed!"), 0o644)
	refs := []map[string]any{{"kind": "deliverable", "id": id}}
	tm.b.must(202, "POST", "/api/conversations/"+tm.thread.ID+"/messages", map[string]any{"content": "看看这个", "refs": refs}, nil)
	if send := tm.lead.expectSend("看看这个"); !strings.Contains(send.Text, "index.html 的内容现在取不到") {
		t.Fatalf("ref without content:\n%.2000s", send.Text)
	}

	tm.b.must(204, "DELETE", "/api/deliverables/"+id, nil, nil)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("folder left after delete: %v", err)
	}
	if countRows(t, tm.w, `SELECT count(*) FROM at_deliverable_files WHERE deliverable_id = $1`, id) != 0 {
		t.Fatal("rows left after delete")
	}
}

// TestDeliverableVersionsTakeTurns: two publishes of one deliverable take turns on its row
// (NextVersion locks it), so the second gets the next number; a new deliverable goes straight
// into the folder; a version that already has rows is
// refused without touching its folder, while a leftover folder of a failed attempt is replaced.
func TestDeliverableVersionsTakeTurns(t *testing.T) {
	tm, _ := startDesignTeam(t)
	useFolder(t, tm.w)
	tm.b.must(200, "POST", "/api/storage/fs", nil, nil)
	var out protocol.PublishFileResult
	res := tm.dev.publish(map[string]any{"path": "site", "kind": "design", "title": "页"}, 1<<20, upFile{"index.html", "<p>v1</p>"})
	_ = json.Unmarshal(*res.Structured, &out)
	if !res.Ok {
		t.Fatalf("publish %+v", res.Content)
	}
	id, ctx, st := out.DeliverableID, tm.w.ctx, tm.w.e.Core.Artifacts()
	if countRows(t, tm.w, `SELECT count(*) FROM at_deliverable_files WHERE deliverable_id = $1 AND version = 1 AND in_volume`, id) != 1 {
		t.Fatal("a new deliverable's first version is not in the folder")
	}
	fs, dir := folderOf(t, tm, id)
	file := func(body string) []artifacts.ArtifactFile {
		sum := sha256.Sum256([]byte(body))
		return []artifacts.ArtifactFile{{Path: "index.html", ContentType: "text/html", Size: len(body), SHA256: hex.EncodeToString(sum[:]), Data: []byte(body)}}
	}

	// A leftover v2 of a crashed attempt (files, no rows) is replaced by the next publish.
	_ = os.MkdirAll(filepath.Join(fs, dir, "v2"), 0o755)
	_ = os.WriteFile(filepath.Join(fs, dir, "v2", "junk.txt"), []byte("x"), 0o644)

	locked, second := make(chan struct{}), make(chan int, 1)
	err := tm.w.e.Core.InTx(ctx, func(tx *core.Tx) error {
		v, err := artifacts.NextVersion(ctx, tx, id)
		if err != nil || v != 2 {
			return fmt.Errorf("first next version %d %v", v, err)
		}
		go func() {
			close(locked)
			_ = tm.w.e.Core.InTx(ctx, func(tx2 *core.Tx) error {
				v2, err := artifacts.NextVersion(ctx, tx2, id)
				if err == nil {
					err = st.Save(ctx, tx2, id, v2, artifacts.Publisher{Name: "b"}, file("<p>b</p>"))
				}
				second <- v2
				return err
			})
		}()
		<-locked
		select {
		case v2 := <-second:
			return fmt.Errorf("second publish did not wait: got %d", v2)
		case <-time.After(200 * time.Millisecond):
		}
		return st.Save(ctx, tx, id, v, artifacts.Publisher{Name: "a"}, file("<p>a</p>"))
	})
	if err != nil {
		t.Fatal(err)
	}
	if v2 := <-second; v2 != 3 {
		t.Fatalf("second publish got version %d", v2)
	}
	for v, body := range map[int]string{2: "<p>a</p>", 3: "<p>b</p>"} {
		if got, err := os.ReadFile(filepath.Join(fs, dir, fmt.Sprintf("v%d", v), "index.html")); err != nil || string(got) != body {
			t.Fatalf("v%d = %q %v", v, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(fs, dir, "v2", "junk.txt")); !os.IsNotExist(err) {
		t.Fatalf("leftover kept: %v", err)
	}

	// Saving a stored version again is refused and leaves its files.
	err = tm.w.e.Core.InTx(ctx, func(tx *core.Tx) error {
		return st.Save(ctx, tx, id, 2, artifacts.Publisher{Name: "c"}, file("<p>c</p>"))
	})
	if err == nil {
		t.Fatal("stored version saved again")
	}
	if got, _ := os.ReadFile(filepath.Join(fs, dir, "v2", "index.html")); string(got) != "<p>a</p>" {
		t.Fatalf("stored v2 overwritten: %q", got)
	}
}

// folderOf is the volume's root on disk and the deliverable's folder in it.
func folderOf(t *testing.T, tm *team, id string) (string, string) {
	t.Helper()
	files, dir, ok, err := tm.w.e.FS.DeliverableFolder(tm.w.ctx, tm.w.db.Pool, id)
	if err != nil || !ok {
		t.Fatalf("deliverable folder %v %v", ok, err)
	}
	d, isDir := files.(userfs.DirFiles)
	if !isDir {
		t.Fatalf("volume is %T", files)
	}
	return d.Root, dir
}
