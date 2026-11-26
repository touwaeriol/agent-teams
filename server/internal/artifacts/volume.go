package artifacts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/userfs"
)

// Deliverable versions live in the owner's shared folder when it is on: the files of version N at
// <dir>/v<N>/<path>, dir = projects/<pid>/.deliverables/<id> (the project's .project-files/ on the
// nodes). Their rows keep the metadata (in_volume, no data); versions published before the folder
// was on stay in the database. Employees still publish and fetch them with the MCP tools; the
// folder makes them browsable and keeps their bytes out of the database.

// Volume opens a deliverable's folder in its owner's shared folder.
type Volume interface {
	// DeliverableFolder is the owner's volume and the deliverable's folder in it; ok false when the
	// owner's shared folder is not on.
	DeliverableFolder(ctx context.Context, deliverableID string) (files userfs.Files, dir string, ok bool, err error)
}

// Store is the database plus, when set, the owners' shared folders.
type Store struct {
	DB     store.DB
	Volume Volume
}

func versionFile(dir string, version int, path string) string {
	return fmt.Sprintf("%s/v%d/%s", dir, version, path)
}

// Save stores the files of one version: in the owner's shared folder when it is on (the version's
// folder replaced, the rows without data), else in the database.
func (s Store) Save(ctx context.Context, q store.DB, deliverableID string, version int, by Publisher, list []ArtifactFile) error {
	if s.Volume != nil {
		f, dir, ok, err := s.Volume.DeliverableFolder(ctx, deliverableID)
		if err != nil {
			return err
		}
		if ok {
			if err := writeVersion(ctx, f, dir, version, list); err != nil {
				return fmt.Errorf("共享文件夹写入失败：%w", err)
			}
			return save(ctx, q, deliverableID, version, by, list, true)
		}
	}
	return save(ctx, q, deliverableID, version, by, list, false)
}

func writeVersion(ctx context.Context, f userfs.Files, dir string, version int, list []ArtifactFile) error {
	if err := f.Remove(ctx, fmt.Sprintf("%s/v%d", dir, version)); err != nil && !errors.Is(err, userfs.ErrNoFile) {
		return err
	}
	for _, a := range list {
		if err := f.Put(ctx, versionFile(dir, version, a.Path), bytes.NewReader(a.Data), int64(len(a.Data))); err != nil {
			return err
		}
	}
	return nil
}

// LoadVersion reads every file of one version with its data, by path.
func (s Store) LoadVersion(ctx context.Context, q store.DB, deliverableID string, version int) ([]ArtifactFile, error) {
	list, err := LoadVersion(ctx, q, deliverableID, version)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i], err = s.fill(ctx, deliverableID, version, list[i]); err != nil {
			return nil, err
		}
	}
	return list, nil
}

// Load reads one file with its data.
func (s Store) Load(ctx context.Context, q store.DB, deliverableID string, version int, path string) (ArtifactFile, error) {
	f, err := Load(ctx, q, deliverableID, version, path)
	if err != nil {
		return f, err
	}
	return s.fill(ctx, deliverableID, version, f)
}

// fill reads the data of a file kept in the owner's shared folder. A folder that is off or a file
// removed from it is ErrNotFound: the version can no longer be served.
func (s Store) fill(ctx context.Context, deliverableID string, version int, f ArtifactFile) (ArtifactFile, error) {
	if !f.InVolume {
		return f, nil
	}
	if s.Volume == nil {
		return f, ErrNotFound
	}
	files, dir, ok, err := s.Volume.DeliverableFolder(ctx, deliverableID)
	if err != nil || !ok {
		return f, errors.Join(ErrNotFound, err)
	}
	r, _, err := files.Open(ctx, versionFile(dir, version, f.Path))
	if errors.Is(err, userfs.ErrNoFile) {
		return f, ErrNotFound
	}
	if err != nil {
		return f, err
	}
	defer r.Close()
	if f.Data, err = io.ReadAll(io.LimitReader(r, int64(f.Size)+1)); err != nil {
		return f, err
	}
	if len(f.Data) != f.Size {
		return f, fmt.Errorf("%w: %s changed in the shared folder", ErrNotFound, f.Path)
	}
	return f, nil
}

// MoveReport is what MoveToVolumes did.
type MoveReport struct {
	Moved, Skipped, Failed int
}

// MoveToVolumes copies the versions kept in the database into their owners' shared folders (those
// that are on) and drops the copies from the database; a deliverable whose owner has no folder is
// skipped. Run again it continues where it stopped.
func (s Store) MoveToVolumes(ctx context.Context, log *slog.Logger) (MoveReport, error) {
	var rep MoveReport
	var versions []struct {
		DeliverableID string
		Version       int
	}
	if err := s.DB.Gorm(ctx).Raw(`SELECT DISTINCT deliverable_id, version FROM at_deliverable_files WHERE NOT in_volume
		ORDER BY deliverable_id, version`).Scan(&versions).Error; err != nil {
		return rep, err
	}
	for _, v := range versions {
		moved, err := s.moveVersion(ctx, v.DeliverableID, v.Version)
		switch {
		case err != nil:
			rep.Failed++
			log.Warn("move deliverable version", "deliverable_id", v.DeliverableID, "version", v.Version, "err", err)
		case moved:
			rep.Moved++
		default:
			rep.Skipped++
		}
	}
	return rep, nil
}

func (s Store) moveVersion(ctx context.Context, deliverableID string, version int) (bool, error) {
	f, dir, ok, err := s.Volume.DeliverableFolder(ctx, deliverableID)
	if err != nil || !ok {
		return false, err
	}
	list, err := LoadVersion(ctx, s.DB, deliverableID, version)
	if err != nil {
		return false, err
	}
	if err := writeVersion(ctx, f, dir, version, list); err != nil {
		return false, err
	}
	_, err = store.Exec(ctx, s.DB, `UPDATE at_deliverable_files SET in_volume = true, data = NULL
		WHERE deliverable_id = $1 AND version = $2 AND NOT in_volume`, deliverableID, version)
	return err == nil, err
}

// Remove deletes a deliverable's folder from its owner's shared folder (nothing when the folder is
// off or holds none), before its rows go.
func (s Store) Remove(ctx context.Context, deliverableID string) error {
	if s.Volume == nil {
		return nil
	}
	f, dir, ok, err := s.Volume.DeliverableFolder(ctx, deliverableID)
	if err != nil || !ok {
		return err
	}
	if err := f.Remove(ctx, dir); err != nil && !errors.Is(err, userfs.ErrNoFile) {
		return err
	}
	return nil
}
