// Package blobmigrate moves the bytes of attachments and shared files from before #58 (the data
// column) to the object store, in steps that can each be re-run and interrupted (design §2.1):
//
//	migrate     copy data → object, set storage_key (data is kept)
//	verify      for rows that have both: HEAD size + streamed sha256 == sha256 of data
//	purge-data  verify each row again, then clear data (only rows that pass)
//	restore     rollback: copy objects back into data for rows whose data was cleared
//
// Every step goes row by row (one row's bytes in memory: at most 5 MB for attachments, 20 MB for
// shared files), in key order, and only touches rows in the state it expects, so a crash or
// Ctrl-C loses nothing and the next run picks up where it stopped.
package blobmigrate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/touwaeriol/agent-teams/server/internal/blobgc"
	"github.com/touwaeriol/agent-teams/server/internal/blobstore"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Tables.
const (
	Attachments = "attachments"
	Shared      = "shared"
	All         = "all"
)

// MaxRestoreBytes is the largest object restore copies back into the database (the limit of the
// data column's era: 20 MB shared files; attachments were at most 5 MB).
const MaxRestoreBytes = 20 << 20

// Options select and pace a run.
type Options struct {
	Table  string    // attachments, shared or all (default all)
	Batch  int       // rows per query (default 50)
	Limit  int       // stop after this many rows per table (0 = no limit)
	DryRun bool      // count only
	Out    io.Writer // progress lines (nil = none)
}

// Report counts what a run did per table.
type Report struct {
	Table   string
	Done    int      // migrated / verified / purged / restored
	Skipped int      // already in the target state or changed meanwhile
	Failed  int      // checks that did not pass (see Errors)
	Errors  []string // one line per failure (row id and reason)
}

func (r Report) String() string {
	return fmt.Sprintf("%s: done %d, skipped %d, failed %d", r.Table, r.Done, r.Skipped, r.Failed)
}

// Runner holds the database and the store.
type Runner struct {
	DB    store.DB
	Blobs blobstore.Store
}

// row is one attachment or shared file, addressed by its table's primary key.
type row struct {
	ProjectID string
	ID        string // attachment id, or shared file path
	Size      int64
	SHA256    *string
	Key       *string
	Data      []byte
	Type      string
}

type table struct {
	name    string
	keyCol  string // the second ordering column (id or path)
	from    string
	keyFor  func(r row) (key, objectID string)
	idLabel func(r row) string
}

var tables = map[string]table{
	Attachments: {
		name: Attachments, keyCol: "id", from: "at_attachments",
		keyFor:  func(r row) (string, string) { return blobstore.AttachmentKey(r.ProjectID, r.ID), "" },
		idLabel: func(r row) string { return r.ID },
	},
	Shared: {
		name: Shared, keyCol: "path", from: "at_shared_files",
		keyFor: func(r row) (string, string) {
			obj := LegacyObjectID(r.ProjectID, r.ID)
			return blobstore.SharedKey(r.ProjectID, obj), obj
		},
		idLabel: func(r row) string { return r.ProjectID + ":" + r.ID },
	},
}

// LegacyObjectID is the object id a shared file from before #58 gets: derived from project and
// path so a migrate interrupted between the upload and the update writes the same key again.
func LegacyObjectID(projectID, p string) string {
	s := sha256.Sum256([]byte(projectID + "\x00" + p))
	return "sf_legacy_" + hex.EncodeToString(s[:13])
}

func (o Options) tables() ([]table, error) {
	switch o.Table {
	case "", All:
		return []table{tables[Attachments], tables[Shared]}, nil
	case Attachments, Shared:
		return []table{tables[o.Table]}, nil
	}
	return nil, fmt.Errorf("unknown table %q (attachments, shared, all)", o.Table)
}

func (o Options) batch() int {
	if o.Batch > 0 {
		return o.Batch
	}
	return 50
}

func (o Options) printf(format string, args ...any) {
	if o.Out != nil {
		fmt.Fprintf(o.Out, format+"\n", args...)
	}
}

// scan walks the rows matching where (keyset pagination on project_id, id/path), one page at a
// time, and calls fn for each with its data loaded only when withData.
func (r *Runner) scan(ctx context.Context, t table, where string, withData bool, o Options, fn func(row) error) error {
	lastP, lastK, n := "", "", 0
	cols := fmt.Sprintf("project_id, %s AS id, size, sha256, storage_key AS key, content_type AS type", t.keyCol)
	for {
		var page []row
		q := fmt.Sprintf(`SELECT %s FROM %s WHERE (%s) AND (project_id, %s) > ($1, $2) ORDER BY project_id, %s LIMIT $3`,
			cols, t.from, where, t.keyCol, t.keyCol)
		if err := store.Raw(ctx, r.DB, q, lastP, lastK, o.batch()).Scan(&page).Error; err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}
		for _, row := range page {
			if o.Limit > 0 && n >= o.Limit {
				return nil
			}
			n++
			if withData {
				var data []byte
				err := store.QueryRow(ctx, r.DB, fmt.Sprintf(`SELECT data FROM %s WHERE project_id = $1 AND %s = $2`, t.from, t.keyCol),
					row.ProjectID, row.ID).Scan(&data)
				if store.IsNoRows(err) {
					continue // deleted meanwhile
				}
				if err != nil {
					return err
				}
				row.Data = data
			}
			if err := fn(row); err != nil {
				return err
			}
			lastP, lastK = row.ProjectID, row.ID
		}
		lastP, lastK = page[len(page)-1].ProjectID, page[len(page)-1].ID
	}
}

func sum(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

func (rep *Report) fail(id, format string, args ...any) {
	rep.Failed++
	rep.Errors = append(rep.Errors, id+": "+fmt.Sprintf(format, args...))
}

func (r *Runner) ready() error {
	if !blobstore.Or(r.Blobs).Configured() {
		return blobstore.ErrNotConfigured
	}
	return nil
}

// Migrate copies the bytes of rows without a storage key into the object store and records the
// key. data is kept (purge-data clears it after verify).
func (r *Runner) Migrate(ctx context.Context, o Options) ([]Report, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	ts, err := o.tables()
	if err != nil {
		return nil, err
	}
	var out []Report
	for _, t := range ts {
		rep := Report{Table: t.name}
		err := r.scan(ctx, t, "storage_key IS NULL AND data IS NOT NULL", true, o, func(x row) error {
			id := t.idLabel(x)
			got := sum(x.Data)
			if int64(len(x.Data)) != x.Size {
				rep.fail(id, "data has %d bytes, size column says %d", len(x.Data), x.Size)
				return nil
			}
			if x.SHA256 != nil && *x.SHA256 != "" && *x.SHA256 != got {
				rep.fail(id, "sha256 of data %s does not match the row's %s", got, *x.SHA256)
				return nil
			}
			if o.DryRun {
				rep.Done++
				return nil
			}
			key, obj := t.keyFor(x)
			if err := r.Blobs.Put(ctx, key, bytes.NewReader(x.Data), x.Size, x.Type); err != nil {
				return fmt.Errorf("%s: put: %w", id, err)
			}
			if h, err := r.Blobs.Head(ctx, key); err != nil || h.Size != x.Size {
				rep.fail(id, "object %s after upload: size %d, err %v", key, h.Size, err)
				return nil
			}
			var res store.Result
			if t.name == Attachments {
				res, err = store.Exec(ctx, r.DB, `UPDATE at_attachments SET storage_key = $2 WHERE id = $1 AND storage_key IS NULL
					AND data IS NOT NULL`, x.ID, key)
			} else {
				// sha256 guard: a file overwritten meanwhile is not given the old bytes' object.
				res, err = store.Exec(ctx, r.DB, `UPDATE at_shared_files SET storage_key = $3, object_id = $4
					WHERE project_id = $1 AND path = $2 AND storage_key IS NULL AND sha256 = $5`, x.ProjectID, x.ID, key, obj, got)
			}
			if err != nil {
				return err
			}
			if res.RowsAffected() == 0 {
				rep.Skipped++
				return blobgc.Enqueue(ctx, r.DB, key) // the row changed: drop the copy
			}
			rep.Done++
			if rep.Done%100 == 0 {
				o.printf("%s: migrated %d", t.name, rep.Done)
			}
			return nil
		})
		out = append(out, rep)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// check compares a row's object with its data (or, without data, with its sha256 column).
func (r *Runner) check(ctx context.Context, x row) error {
	h, err := r.Blobs.Head(ctx, *x.Key)
	if err != nil {
		return fmt.Errorf("head %s: %w", *x.Key, err)
	}
	if h.Size != x.Size {
		return fmt.Errorf("object %s has %d bytes, want %d", *x.Key, h.Size, x.Size)
	}
	want := ""
	if x.Data != nil {
		want = sum(x.Data)
	} else if x.SHA256 != nil {
		want = *x.SHA256
	}
	rc, err := r.Blobs.Open(ctx, *x.Key)
	if err != nil {
		return err
	}
	defer rc.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, rc); err != nil {
		return err
	}
	if got := hex.EncodeToString(hash.Sum(nil)); want != "" && got != want {
		return fmt.Errorf("object %s sha256 %s, want %s", *x.Key, got, want)
	}
	return nil
}

// Verify checks every migrated row that still has its data: size and sha256 of the object.
func (r *Runner) Verify(ctx context.Context, o Options) ([]Report, error) {
	return r.verify(ctx, o, false)
}

// PurgeData verifies each migrated row again and clears its data when the object matches.
func (r *Runner) PurgeData(ctx context.Context, o Options) ([]Report, error) {
	return r.verify(ctx, o, true)
}

func (r *Runner) verify(ctx context.Context, o Options, purge bool) ([]Report, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	ts, err := o.tables()
	if err != nil {
		return nil, err
	}
	var out []Report
	for _, t := range ts {
		rep := Report{Table: t.name}
		err := r.scan(ctx, t, "storage_key IS NOT NULL AND data IS NOT NULL", true, o, func(x row) error {
			id := t.idLabel(x)
			if err := r.check(ctx, x); err != nil {
				if errors.Is(err, context.Canceled) {
					return err
				}
				rep.fail(id, "%v", err)
				return nil
			}
			if !purge || o.DryRun {
				rep.Done++
				return nil
			}
			// Same key and same bytes as checked: a row changed meanwhile keeps its data.
			res, err := store.Exec(ctx, r.DB, fmt.Sprintf(`UPDATE %s SET data = NULL WHERE project_id = $1 AND %s = $2
				AND storage_key = $3 AND sha256(data) = decode($4, 'hex')`, t.from, t.keyCol), x.ProjectID, x.ID, *x.Key, sum(x.Data))
			if err != nil {
				return err
			}
			if res.RowsAffected() == 0 {
				rep.Skipped++
				return nil
			}
			rep.Done++
			if rep.Done%100 == 0 {
				o.printf("%s: purged %d", t.name, rep.Done)
			}
			return nil
		})
		out = append(out, rep)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// Restore copies objects back into data for rows whose data was cleared (rollback before going
// back to a binary from before #58). Rows over MaxRestoreBytes, or still uploading, are reported
// and left alone. storage_key is kept: the object stays the source of truth for this version.
func (r *Runner) Restore(ctx context.Context, o Options) ([]Report, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	ts, err := o.tables()
	if err != nil {
		return nil, err
	}
	var out []Report
	for _, t := range ts {
		rep := Report{Table: t.name}
		err := r.scan(ctx, t, "storage_key IS NOT NULL AND data IS NULL AND state = 'ready'", false, o, func(x row) error {
			id := t.idLabel(x)
			if x.Size > MaxRestoreBytes {
				rep.fail(id, "%d bytes is over the %d MB a database row held before #58; left in the object store", x.Size, MaxRestoreBytes>>20)
				return nil
			}
			if o.DryRun {
				rep.Done++
				return nil
			}
			rc, err := r.Blobs.Open(ctx, *x.Key)
			if err != nil {
				rep.fail(id, "open %s: %v", *x.Key, err)
				return nil
			}
			data, err := io.ReadAll(io.LimitReader(rc, x.Size+1))
			rc.Close()
			if err != nil {
				return err
			}
			if int64(len(data)) != x.Size || (x.SHA256 != nil && *x.SHA256 != "" && sum(data) != *x.SHA256) {
				rep.fail(id, "object %s does not match the row (%d bytes)", *x.Key, len(data))
				return nil
			}
			res, err := store.Exec(ctx, r.DB, fmt.Sprintf(`UPDATE %s SET data = $4, sha256 = COALESCE(sha256, $5)
				WHERE project_id = $1 AND %s = $2 AND storage_key = $3 AND data IS NULL`, t.from, t.keyCol),
				x.ProjectID, x.ID, *x.Key, data, sum(data))
			if err != nil {
				return err
			}
			if res.RowsAffected() == 0 {
				rep.Skipped++
				return nil
			}
			rep.Done++
			return nil
		})
		out = append(out, rep)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}
