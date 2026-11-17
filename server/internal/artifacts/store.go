package artifacts

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// ErrNotFound is a missing deliverable version or file.
var ErrNotFound = errors.New("artifact not found")

// ArtifactFile is one stored file of a version.
type ArtifactFile struct {
	Path        string `json:"path"`
	ContentType string `json:"content_type"`
	Size        int    `json:"size"`
	SHA256      string `json:"sha256" gorm:"column:sha256"`
	Data        []byte `json:"-"`
}

// ArtifactVersion is one published version of a deliverable.
type ArtifactVersion struct {
	Version     int    `json:"version"`
	Files       int    `json:"files"`
	Size        int    `json:"size"`
	PublishedBy string `json:"published_by"`
	// PublisherID is the employee who published it ('' for the user and older versions).
	PublisherID       string    `json:"-"`
	PublisherThreadNo *int      `json:"publisher_thread_no,omitempty" doc:"Thread number of the employee who published this version"`
	CreatedAt         time.Time `json:"created_at"`
}

// Publisher is who publishes a version: the employee's name (or "user") and id (” for the user).
type Publisher struct {
	Name       string
	EmployeeID string
}

// fileRow is a row of at_deliverable_files.
type fileRow struct {
	DeliverableID string
	Version       int
	ArtifactFile  `gorm:"embedded"`
	PublishedBy   string
	PublisherID   string
}

func (fileRow) TableName() string { return "at_deliverable_files" }

func files(ctx context.Context, q store.DB, deliverableID string) *gorm.DB {
	return q.Gorm(ctx).Model(&fileRow{}).Where("deliverable_id = ?", deliverableID)
}

// NextVersion is 1 + the highest stored version (1 for a new deliverable).
func NextVersion(ctx context.Context, q store.DB, deliverableID string) (int, error) {
	var v int
	err := files(ctx, q, deliverableID).Select("COALESCE(MAX(version), 0) + 1").Row().Scan(&v)
	return v, err
}

// Save stores the files of one version (one INSERT).
func Save(ctx context.Context, q store.DB, deliverableID string, version int, by Publisher, list []ArtifactFile) error {
	if len(list) == 0 {
		return nil
	}
	rows := make([]fileRow, len(list))
	for i, f := range list {
		rows[i] = fileRow{DeliverableID: deliverableID, Version: version, ArtifactFile: f, PublishedBy: by.Name, PublisherID: by.EmployeeID}
	}
	return q.Gorm(ctx).Create(&rows).Error
}

// Versions lists a deliverable's versions, newest first, with the publisher's thread number.
func Versions(ctx context.Context, q store.DB, deliverableID string) ([]ArtifactVersion, error) {
	out := []ArtifactVersion{}
	err := q.Gorm(ctx).Raw(`SELECT v.*, t.no AS publisher_thread_no FROM (SELECT version, count(*) AS files, sum(size) AS size,
		min(published_by) AS published_by, min(publisher_id) AS publisher_id, min(created_at) AS created_at
		FROM at_deliverable_files WHERE deliverable_id = ? GROUP BY version) v
		LEFT JOIN at_employees e ON e.id = v.publisher_id LEFT JOIN at_threads t ON t.id = e.thread_id
		ORDER BY v.version DESC`, deliverableID).Scan(&out).Error
	return out, err
}

// LoadVersion reads every file of one version with its data, by path.
func LoadVersion(ctx context.Context, q store.DB, deliverableID string, version int) ([]ArtifactFile, error) {
	out := []ArtifactFile{}
	err := files(ctx, q, deliverableID).Where("version = ?", version).Order("path").Find(&out).Error
	if err == nil && len(out) == 0 {
		return nil, ErrNotFound
	}
	return out, err
}

// Files lists one version's files (without data).
func Files(ctx context.Context, q store.DB, deliverableID string, version int) ([]ArtifactFile, error) {
	out := []ArtifactFile{}
	err := files(ctx, q, deliverableID).Select("path, content_type, size, sha256").Where("version = ?", version).
		Order("path").Find(&out).Error
	if err == nil && len(out) == 0 {
		return nil, ErrNotFound
	}
	return out, err
}

// Load reads one file with its data.
func Load(ctx context.Context, q store.DB, deliverableID string, version int, path string) (ArtifactFile, error) {
	var f ArtifactFile
	err := files(ctx, q, deliverableID).Where("version = ? AND path = ?", version, path).Take(&f).Error
	if store.IsNoRows(err) {
		return ArtifactFile{Path: path}, ErrNotFound
	}
	return f, err
}
