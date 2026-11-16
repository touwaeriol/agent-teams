package artifacts

import (
	"context"
	"errors"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// ErrNotFound is a missing deliverable version or file.
var ErrNotFound = errors.New("artifact not found")

// ArtifactFile is one stored file of a version.
type ArtifactFile struct {
	Path        string `json:"path"`
	ContentType string `json:"content_type"`
	Size        int    `json:"size"`
	SHA256      string `json:"sha256"`
	Data        []byte `json:"-"`
}

// ArtifactVersion is one published version of a deliverable.
type ArtifactVersion struct {
	Version     int       `json:"version"`
	Files       int       `json:"files"`
	Size        int       `json:"size"`
	PublishedBy string    `json:"published_by"`
	CreatedAt   time.Time `json:"created_at"`
}

// NextVersion is 1 + the highest stored version (1 for a new deliverable).
func NextVersion(ctx context.Context, q store.DB, deliverableID string) (int, error) {
	var v int
	err := store.QueryRow(ctx, q, `SELECT COALESCE(MAX(version), 0) + 1 FROM at_deliverable_files WHERE deliverable_id = $1`,
		deliverableID).Scan(&v)
	return v, err
}

// Save stores the files of one version.
func Save(ctx context.Context, q store.DB, deliverableID string, version int, by string, files []ArtifactFile) error {
	for _, f := range files {
		if _, err := store.Exec(ctx, q, `INSERT INTO at_deliverable_files (deliverable_id, version, path, content_type, size, sha256, data,
			published_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, deliverableID, version, f.Path, f.ContentType, f.Size, f.SHA256,
			f.Data, by); err != nil {
			return err
		}
	}
	return nil
}

// Versions lists a deliverable's versions, newest first.
func Versions(ctx context.Context, q store.DB, deliverableID string) ([]ArtifactVersion, error) {
	rows, err := store.Query(ctx, q, `SELECT version, count(*), sum(size), min(published_by), min(created_at) FROM at_deliverable_files
		WHERE deliverable_id = $1 GROUP BY version ORDER BY version DESC`, deliverableID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ArtifactVersion{}
	for rows.Next() {
		var v ArtifactVersion
		if err := rows.Scan(&v.Version, &v.Files, &v.Size, &v.PublishedBy, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Files lists one version's files (without data).
func Files(ctx context.Context, q store.DB, deliverableID string, version int) ([]ArtifactFile, error) {
	rows, err := store.Query(ctx, q, `SELECT path, content_type, size, sha256 FROM at_deliverable_files
		WHERE deliverable_id = $1 AND version = $2 ORDER BY path`, deliverableID, version)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ArtifactFile{}
	for rows.Next() {
		var f ArtifactFile
		if err := rows.Scan(&f.Path, &f.ContentType, &f.Size, &f.SHA256); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	if len(out) == 0 && rows.Err() == nil {
		return nil, ErrNotFound
	}
	return out, rows.Err()
}

// Load reads one file with its data.
func Load(ctx context.Context, q store.DB, deliverableID string, version int, path string) (ArtifactFile, error) {
	f := ArtifactFile{Path: path}
	err := store.QueryRow(ctx, q, `SELECT content_type, size, sha256, data FROM at_deliverable_files
		WHERE deliverable_id = $1 AND version = $2 AND path = $3`, deliverableID, version, path).
		Scan(&f.ContentType, &f.Size, &f.SHA256, &f.Data)
	if store.IsNoRows(err) {
		return f, ErrNotFound
	}
	return f, err
}
