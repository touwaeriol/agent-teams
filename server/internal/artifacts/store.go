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
	Version     int       `json:"version"`
	Files       int       `json:"files"`
	Size        int       `json:"size"`
	PublishedBy string    `json:"published_by"`
	CreatedAt   time.Time `json:"created_at"`
}

// fileRow is a row of at_deliverable_files.
type fileRow struct {
	DeliverableID string
	Version       int
	ArtifactFile  `gorm:"embedded"`
	PublishedBy   string
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
func Save(ctx context.Context, q store.DB, deliverableID string, version int, by string, list []ArtifactFile) error {
	if len(list) == 0 {
		return nil
	}
	rows := make([]fileRow, len(list))
	for i, f := range list {
		rows[i] = fileRow{DeliverableID: deliverableID, Version: version, ArtifactFile: f, PublishedBy: by}
	}
	return q.Gorm(ctx).Create(&rows).Error
}

// Versions lists a deliverable's versions, newest first.
func Versions(ctx context.Context, q store.DB, deliverableID string) ([]ArtifactVersion, error) {
	out := []ArtifactVersion{}
	err := files(ctx, q, deliverableID).
		Select("version, count(*) AS files, sum(size) AS size, min(published_by) AS published_by, min(created_at) AS created_at").
		Group("version").Order("version DESC").Find(&out).Error
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
