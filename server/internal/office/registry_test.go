package office

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type memDB struct{ rows map[string]DBRow }

func (m *memDB) LoadLayouts(context.Context) ([]DBRow, error) {
	var out []DBRow
	for _, r := range m.rows {
		out = append(out, r)
	}
	return out, nil
}

func (m *memDB) SaveLayout(_ context.Context, id string, doc []byte, _ string) (DBRow, error) {
	row := DBRow{ID: id, Doc: doc, UpdatedAt: time.Now()}
	m.rows[id] = row
	return row, nil
}

func (m *memDB) DeleteLayout(_ context.Context, id string) error {
	if _, ok := m.rows[id]; !ok {
		return ErrNotFound
	}
	delete(m.rows, id)
	return nil
}

func newRegistry(t *testing.T, dir string) (*Registry, *memDB) {
	t.Helper()
	v, err := NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	db := &memDB{rows: map[string]DBRow{}}
	r := NewRegistry(dir, v, db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := r.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return r, db
}

func withName(t *testing.T, name string) []byte {
	t.Helper()
	var doc map[string]any
	_ = json.Unmarshal(embeddedDefault, &doc)
	doc["name"] = name
	raw, _ := json.Marshal(doc)
	return raw
}

func TestBuiltinDefaultIsValid(t *testing.T) {
	r, _ := newRegistry(t, "")
	l, err := r.Get(DefaultID)
	if err != nil || l.Source != "builtin" || len(l.Version) != 12 {
		t.Fatalf("default %+v %v", l, err)
	}
	if err := r.validator.Check(embeddedDefault); err != nil {
		t.Fatalf("embedded default invalid: %v", err)
	}
}

func TestFileOverridesBuiltinAndBadEditKeepsLastGood(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "default.json")
	_ = os.WriteFile(path, withName(t, "From file"), 0o644)
	r, _ := newRegistry(t, dir)
	l, _ := r.Get(DefaultID)
	if l.Source != "file" {
		t.Fatalf("source %s", l.Source)
	}
	good := l.Version
	_ = os.WriteFile(path, []byte(`{"format":1}`), 0o644)
	r.ReloadFiles()
	l, _ = r.Get(DefaultID)
	if l.Version != good {
		t.Fatal("invalid edit replaced the good layout")
	}
	st := r.List()[0]
	if st.Valid || len(st.Errors) == 0 {
		t.Fatalf("status %+v", st)
	}
}

func TestExtendsMergesByID(t *testing.T) {
	dir := t.TempDir()
	var base struct {
		Rooms []map[string]any `json:"rooms"`
	}
	_ = json.Unmarshal(embeddedDefault, &base)
	room := base.Rooms[0]
	room["label"] = "改名的房间"
	child, _ := json.Marshal(map[string]any{"extends": "default", "id": "compact", "name": "Compact", "rooms": []any{room}})
	_ = os.WriteFile(filepath.Join(dir, "compact.json"), child, 0o644)
	r, _ := newRegistry(t, dir)
	l, err := r.Get("compact")
	if err != nil {
		st := r.List()
		t.Fatalf("compact missing: %v %+v", err, st)
	}
	var doc struct {
		Name    string           `json:"name"`
		Extends *string          `json:"extends"`
		Rooms   []map[string]any `json:"rooms"`
	}
	_ = json.Unmarshal(l.Doc, &doc)
	if doc.Name != "Compact" || doc.Extends != nil {
		t.Fatalf("merged %+v", doc)
	}
	if len(doc.Rooms) != len(base.Rooms) {
		t.Fatalf("rooms %d want %d", len(doc.Rooms), len(base.Rooms))
	}
	found := false
	for _, room := range doc.Rooms {
		if room["id"] == base.Rooms[0]["id"] {
			found = room["label"] == "改名的房间"
		}
	}
	if !found {
		t.Fatal("room not replaced by id")
	}
}

func TestDatabaseOverridesAndNotifies(t *testing.T) {
	r, _ := newRegistry(t, "")
	var mu sync.Mutex
	var got []string
	r.OnChange = func(id, v string) { mu.Lock(); got = append(got, id); mu.Unlock() }
	if _, err := r.Put(context.Background(), DefaultID, withName(t, "From DB"), "user:1"); err != nil {
		t.Fatal(err)
	}
	l, _ := r.Get(DefaultID)
	if l.Source != "db" || len(got) != 1 {
		t.Fatalf("source %s changes %v", l.Source, got)
	}
	if _, err := r.Put(context.Background(), DefaultID, []byte(`{"format":1}`), "u"); err == nil {
		t.Fatal("invalid layout saved")
	}
	if _, err := r.Put(context.Background(), "Bad ID", withName(t, "x"), "u"); err != ErrBadID {
		t.Fatalf("bad id: %v", err)
	}
	_ = r.Delete(context.Background(), DefaultID)
	if l, _ := r.Get(DefaultID); l.Source != "builtin" {
		t.Fatal("delete did not fall back")
	}
}

func TestMissingRequiredLooks(t *testing.T) {
	var doc map[string]any
	_ = json.Unmarshal(embeddedDefault, &doc)
	delete(doc["looks"].(map[string]any)["outfits"].(map[string]any), "coat")
	raw, _ := json.Marshal(doc)
	v, _ := NewValidator()
	err := v.Check(raw)
	ve, ok := err.(*ValidationError)
	if !ok || ve.Problems[0].Path != "/looks/outfits" {
		t.Fatalf("err %v", err)
	}
}

func TestWatchReloads(t *testing.T) {
	dir := t.TempDir()
	r, _ := newRegistry(t, dir)
	changed := make(chan string, 4)
	r.OnChange = func(id, _ string) { changed <- id }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Watch(ctx); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "default.json"), withName(t, "Watched"), 0o644)
	select {
	case id := <-changed:
		if id != DefaultID {
			t.Fatalf("changed %s", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no reload")
	}
}

func TestLooksOf(t *testing.T) {
	l := LooksOf(embeddedDefault)
	if len(l.Outfits) < 4 || len(l.Accessories) < 4 || len(l.Colors) == 0 {
		t.Fatalf("looks %+v", l)
	}
}
