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
	// InVolume: the data is in the owner's shared folder (volume.go), not in the row.
	InVolume bool `json:"-" gorm:"column:in_volume"`
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

// save stores the rows of one version (one INSERT); inVolume rows keep no data.
func save(ctx context.Context, q store.DB, deliverableID string, version int, by Publisher, list []ArtifactFile, inVolume bool) error {
	if len(list) == 0 {
		return nil
	}
	rows := make([]fileRow, len(list))
	for i, f := range list {
		f.InVolume = inVolume
		if inVolume {
			f.Data = nil
		}
		rows[i] = fileRow{DeliverableID: deliverableID, Version: version, ArtifactFile: f, PublishedBy: by.Name, PublisherID: by.EmployeeID}
	}
	return q.Gorm(ctx).Create(&rows).Error
}

// Versions lists a deliverable's versions, newest first, with the publisher's thread number: its
// own thread, or for a project member the thread of the session it published from.
func Versions(ctx context.Context, q store.DB, deliverableID string) ([]ArtifactVersion, error) {
	out := []ArtifactVersion{}
	err := q.Gorm(ctx).Raw(`SELECT v.*, t.no AS publisher_thread_no FROM (SELECT version, count(*) AS files, sum(size) AS size,
		min(published_by) AS published_by, min(publisher_id) AS publisher_id, min(created_at) AS created_at
		FROM at_deliverable_files WHERE deliverable_id = ? GROUP BY version) v
		LEFT JOIN at_employees e ON e.id = v.publisher_id
		LEFT JOIN at_threads t ON t.id = COALESCE(e.thread_id, (SELECT a.thread_id FROM at_agent_sessions a
			WHERE a.employee_id = e.id AND a.opened_at <= v.created_at ORDER BY a.opened_at DESC LIMIT 1))
		ORDER BY v.version DESC`, deliverableID).Scan(&out).Error
	return out, err
}

// LoadVersion reads the rows of one version, by path (data only for files kept in the database).
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

// Load reads one row (data only for a file kept in the database).
func Load(ctx context.Context, q store.DB, deliverableID string, version int, path string) (ArtifactFile, error) {
	var f ArtifactFile
	err := files(ctx, q, deliverableID).Where("version = ? AND path = ?", version, path).Take(&f).Error
	if store.IsNoRows(err) {
		return ArtifactFile{Path: path}, ErrNotFound
	}
	return f, err
}
