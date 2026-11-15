// Package office serves the office layouts: JSON files in OFFICE_DIR
// (hot-reloaded with fsnotify) overlaid by layouts stored in
// at_office_layouts. Every layout is resolved (extends) and validated before
// it replaces the previous version; a broken edit keeps the last good one.
package office

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultID is the layout used when nothing else is configured.
const DefaultID = "default"

//go:embed default.json
var embeddedDefault []byte

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ErrNotFound means no layout has that id.
var ErrNotFound = errors.New("没有这个布局")

// ErrBadID means the id is not a valid layout id.
var ErrBadID = errors.New("布局 id 只能用小写字母、数字、- 和 _")

// Layout is one resolved, validated layout.
type Layout struct {
	ID        string    `json:"id"`
	Source    string    `json:"source"` // file | db | builtin
	Version   string    `json:"version"`
	Doc       []byte    `json:"-"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Status is a row of the admin listing.
type Status struct {
	ID        string     `json:"id"`
	Source    string     `json:"source"`
	Version   string     `json:"version"`
	UpdatedAt *time.Time `json:"updated_at"`
	Valid     bool       `json:"valid"`
	Errors    []Problem  `json:"errors,omitempty"`
}

// DBRow is a stored layout.
type DBRow struct {
	ID        string
	Doc       []byte
	UpdatedAt time.Time
}

// DB is the at_office_layouts access the registry needs.
type DB interface {
	LoadLayouts(ctx context.Context) ([]DBRow, error)
	SaveLayout(ctx context.Context, id string, doc []byte, by string) (DBRow, error)
	DeleteLayout(ctx context.Context, id string) error
}

// Registry holds the current layouts.
type Registry struct {
	dir       string
	validator *Validator
	db        DB
	log       *slog.Logger
	// OnChange is called with (layout id, new version) after a layout changes.
	OnChange func(id, version string)

	mu       sync.RWMutex
	raw      map[string][]byte // file id -> raw bytes (pre-extends)
	files    map[string]*Layout
	fileErrs map[string][]Problem
	dbRaw    map[string]DBRow
	dbs      map[string]*Layout
}

// NewRegistry creates an empty registry; call Load, then Watch.
func NewRegistry(dir string, validator *Validator, db DB, log *slog.Logger) *Registry {
	return &Registry{dir: dir, validator: validator, db: db, log: log, OnChange: func(string, string) {},
		raw: map[string][]byte{}, files: map[string]*Layout{}, fileErrs: map[string][]Problem{},
		dbRaw: map[string]DBRow{}, dbs: map[string]*Layout{}}
}

// Load reads every file and database layout.
func (r *Registry) Load(ctx context.Context) error {
	r.readFiles()
	rows, err := r.loadDB(ctx)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, row := range rows {
		r.dbRaw[row.ID] = row
	}
	r.rebuild()
	return nil
}

func (r *Registry) loadDB(ctx context.Context) ([]DBRow, error) {
	if r.db == nil {
		return nil, nil
	}
	return r.db.LoadLayouts(ctx)
}

func (r *Registry) readFiles() {
	raw := map[string][]byte{}
	for _, path := range r.jsonFiles() {
		data, err := os.ReadFile(path)
		if err != nil {
			r.log.Warn("office layout unreadable", "path", path, "err", err)
			continue
		}
		raw[fileID(path)] = data
	}
	r.mu.Lock()
	r.raw = raw
	r.mu.Unlock()
}

func (r *Registry) jsonFiles() []string {
	if r.dir == "" {
		return nil
	}
	matches, _ := filepath.Glob(filepath.Join(r.dir, "*.json"))
	var out []string
	for _, m := range matches {
		if idPattern.MatchString(fileID(m)) {
			out = append(out, m)
		}
	}
	return out
}

func fileID(path string) string { return strings.TrimSuffix(filepath.Base(path), ".json") }

// rebuild re-resolves every layout; a file that fails keeps its last good version. Caller holds mu.
func (r *Registry) rebuild() map[string]string {
	before := r.versions()
	r.rebuildFiles()
	r.rebuildDB()
	changed := map[string]string{}
	for id, v := range r.versions() {
		if before[id] != v {
			changed[id] = v
		}
	}
	return changed
}

func (r *Registry) rebuildFiles() {
	lookup := r.rawLookup()
	for id, data := range r.raw {
		layout, problems := r.build(id, "file", data, lookup)
		r.fileErrs[id] = problems
		if layout != nil {
			r.files[id] = layout
		}
	}
	for id := range r.files {
		if _, ok := r.raw[id]; !ok {
			delete(r.files, id)
			delete(r.fileErrs, id)
		}
	}
	if _, ok := r.files[DefaultID]; !ok {
		r.files[DefaultID] = &Layout{ID: DefaultID, Source: "builtin", Version: hash(embeddedDefault), Doc: embeddedDefault}
	}
}

func (r *Registry) rebuildDB() {
	lookup := r.rawLookup()
	r.dbs = map[string]*Layout{}
	for id, row := range r.dbRaw {
		layout, problems := r.build(id, "db", row.Doc, lookup)
		if layout == nil {
			r.log.Warn("database office layout invalid", "id", id, "problems", problems)
			continue
		}
		layout.UpdatedAt = row.UpdatedAt
		r.dbs[id] = layout
	}
}

func (r *Registry) rawLookup() func(string) ([]byte, bool) {
	return func(id string) ([]byte, bool) {
		if row, ok := r.dbRaw[id]; ok {
			return row.Doc, true
		}
		if data, ok := r.raw[id]; ok {
			return data, true
		}
		if id == DefaultID {
			return embeddedDefault, true
		}
		return nil, false
	}
}

func (r *Registry) build(id, source string, data []byte, lookup func(string) ([]byte, bool)) (*Layout, []Problem) {
	doc, err := resolve(data, lookup)
	if err == nil {
		err = r.validator.Check(doc)
	}
	if err != nil {
		return nil, problemsOf(err)
	}
	return &Layout{ID: id, Source: source, Version: hash(doc), Doc: doc, UpdatedAt: time.Now().UTC()}, nil
}

func problemsOf(err error) []Problem {
	var ve *ValidationError
	if errors.As(err, &ve) {
		return ve.Problems
	}
	return []Problem{{"", err.Error()}}
}

func (r *Registry) versions() map[string]string {
	out := map[string]string{}
	for id, l := range r.files {
		out[id] = l.Version
	}
	for id, l := range r.dbs {
		out[id] = l.Version
	}
	return out
}

func hash(doc []byte) string {
	sum := sha256.Sum256(doc)
	return hex.EncodeToString(sum[:])[:12]
}

// Get returns the current layout by id (database overrides file).
func (r *Registry) Get(id string) (*Layout, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if l, ok := r.dbs[id]; ok {
		return l, nil
	}
	if l, ok := r.files[id]; ok {
		return l, nil
	}
	return nil, ErrNotFound
}

// List describes every known layout for the admin page.
func (r *Registry) List() []Status {
	r.mu.RLock()
	defer r.mu.RUnlock()
	byID := map[string]Status{}
	for id, l := range r.files {
		byID[id] = Status{ID: id, Source: l.Source, Version: l.Version, Valid: len(r.fileErrs[id]) == 0, Errors: r.fileErrs[id]}
	}
	for id, problems := range r.fileErrs {
		if _, ok := byID[id]; !ok && len(problems) > 0 {
			byID[id] = Status{ID: id, Source: "file", Errors: problems}
		}
	}
	for id, l := range r.dbs {
		at := l.UpdatedAt
		byID[id] = Status{ID: id, Source: "db", Version: l.Version, UpdatedAt: &at, Valid: true}
	}
	out := make([]Status, 0, len(byID))
	for _, s := range byID {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Validate resolves and checks a candidate document without saving it.
func (r *Registry) Validate(id string, doc []byte) ([]byte, error) {
	if !idPattern.MatchString(id) {
		return nil, ErrBadID
	}
	r.mu.RLock()
	lookup := r.rawLookup()
	r.mu.RUnlock()
	resolved, err := resolve(doc, func(ref string) ([]byte, bool) {
		if ref == id {
			return nil, false
		}
		return lookup(ref)
	})
	if err != nil {
		return nil, &ValidationError{[]Problem{{"/extends", err.Error()}}}
	}
	return resolved, r.validator.Check(resolved)
}

// Put validates and stores a database layout, then notifies listeners.
func (r *Registry) Put(ctx context.Context, id string, doc []byte, by string) (*Layout, error) {
	if _, err := r.Validate(id, doc); err != nil {
		return nil, err
	}
	row, err := r.db.SaveLayout(ctx, id, doc, by)
	if err != nil {
		return nil, err
	}
	r.apply(func() { r.dbRaw[id] = row })
	return r.Get(id)
}

// Delete removes a database layout (the file version, if any, takes over).
func (r *Registry) Delete(ctx context.Context, id string) error {
	if err := r.db.DeleteLayout(ctx, id); err != nil {
		return err
	}
	r.apply(func() { delete(r.dbRaw, id) })
	return nil
}

// ReloadFiles re-reads OFFICE_DIR (called by the watcher).
func (r *Registry) ReloadFiles() {
	raw := map[string][]byte{}
	for _, path := range r.jsonFiles() {
		if data, err := os.ReadFile(path); err == nil {
			raw[fileID(path)] = data
		}
	}
	r.apply(func() { r.raw = raw })
}

func (r *Registry) apply(mutate func()) {
	r.mu.Lock()
	mutate()
	changed := r.rebuild()
	r.mu.Unlock()
	for id, v := range changed {
		r.log.Info("office layout updated", "id", id, "version", v)
		r.OnChange(id, v)
	}
}
