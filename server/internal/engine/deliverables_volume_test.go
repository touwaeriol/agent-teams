package engine_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/orchapi"
	"github.com/touwaeriol/agent-teams/server/internal/userfs"
)

// TestDeliverablesInSharedFolder: with the owner's shared folder on, publish_file keeps a version's
// files in projects/<pid>/.deliverables/<id>/v<n>/ (rows without data) and the signed URLs and
// read_deliverable serve them from there; the page sees the folder but cannot write into it;
// versions published before the folder was on are served from the database until MoveToVolumes
// moves them; deleting the deliverable removes its folder.
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
	tm.b.must(200, "POST", "/api/storage/fs", nil, nil)
	volume := filepath.Join(fakes.root, userfs.NamesFor(tm.ownerID(t)).Volume)
	dir := filepath.Join(volume, "projects", tm.projectID, ".deliverables", id)

	v2 := upFile{"index.html", "<!doctype html><title>v2</title>"}
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
	tm.b.must(200, "GET", "/api/deliverables/"+id+"/files?version=1", nil, &files)
	if resp, body := fetch(t, tm.w, files.EntryURL); resp.StatusCode != 200 || body != page.body {
		t.Fatalf("v1 from the database %d %q", resp.StatusCode, body)
	}
	if r, got := tm.dev.readCall(map[string]any{"deliverable_id": id}); !r.Ok || got["css/a.css"] != "b{}" || got["index.html"] != v2.body {
		t.Fatalf("read_deliverable %v %v", r.Ok, got)
	}

	// The page lists the folder but never writes into it.
	var listing orchapi.ProjectFiles
	tm.b.must(200, "GET", "/api/projects/"+tm.projectID+"/files?dir=.deliverables/"+id, nil, &listing)
	if len(listing.Entries) != 1 || listing.Entries[0].Name != "v2" {
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

	// Moving the older version out of the database.
	rep, err := tm.w.e.Core.Artifacts().MoveToVolumes(tm.w.ctx, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil || rep.Moved != 1 || rep.Failed != 0 {
		t.Fatalf("move %+v %v", rep, err)
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

	tm.b.must(204, "DELETE", "/api/deliverables/"+id, nil, nil)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("folder left after delete: %v", err)
	}
}
