package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	// owner's shared folder is not on. The deliverable's row is read through q (it may be new in
	// the caller's transaction).
	DeliverableFolder(ctx context.Context, q store.DB, deliverableID string) (files userfs.Files, dir string, ok bool, err error)
}

// Store is the database plus, when set, the owners' shared folders.
type Store struct {
	DB     store.DB
	Volume Volume
}

// folder is a deliverable's folder in its owner's volume.
type folder struct {
	files userfs.Files
	dir   string
}

func (f folder) file(version int, path string) string {
	return fmt.Sprintf("%s/v%d/%s", f.dir, version, path)
}

// folder resolves the deliverable's folder; ok false when there is no volume or the owner's
// shared folder is not on.
func (s Store) folder(ctx context.Context, q store.DB, deliverableID string) (folder, bool, error) {
	if s.Volume == nil {
		return folder{}, false, nil
	}
	files, dir, ok, err := s.Volume.DeliverableFolder(ctx, q, deliverableID)
	return folder{files: files, dir: dir}, ok && err == nil, err
}

// Save stores the files of one version: in the owner's shared folder when it is on (the rows
// without data), else in the database. Call it in the publishing transaction once the version
// number is settled (NextVersion locked the deliverable, or the deliverable is new in it): a
// version that already has rows is refused, so only a leftover of a failed attempt is replaced.
func (s Store) Save(ctx context.Context, q store.DB, deliverableID string, version int, by Publisher, list []ArtifactFile) error {
	f, ok, err := s.folder(ctx, q, deliverableID)
	if err != nil {
		return err
	}
	if !ok {
		return save(ctx, q, deliverableID, version, by, list, false)
	}
	if err := unstored(ctx, q, deliverableID, version); err != nil {
		return err
	}
	if err := f.writeVersion(ctx, version, list); err != nil {
		return fmt.Errorf("共享文件夹写入失败：%w", err)
	}
	return save(ctx, q, deliverableID, version, by, list, true)
}

// unstored refuses a version that already has rows (its folder must not be replaced).
func unstored(ctx context.Context, q store.DB, deliverableID string, version int) error {
	var n int64
	if err := files(ctx, q, deliverableID).Where("version = ?", version).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("交付物 %s 第 %d 版已经存在", deliverableID, version)
	}
	return nil
}

// writeVersion replaces the version's folder with list.
func (f folder) writeVersion(ctx context.Context, version int, list []ArtifactFile) error {
	if err := f.files.Remove(ctx, fmt.Sprintf("%s/v%d", f.dir, version)); err != nil && !errors.Is(err, userfs.ErrNoFile) {
		return err
	}
	for _, a := range list {
		if err := f.files.Put(ctx, f.file(version, a.Path), bytes.NewReader(a.Data), int64(len(a.Data))); err != nil {
			return err
		}
	}
	return nil
}

// LoadVersion reads every file of one version with its data, by path.
func (s Store) LoadVersion(ctx context.Context, q store.DB, deliverableID string, version int) ([]ArtifactFile, error) {
	list, err := LoadVersion(ctx, q, deliverableID, version)
	if err != nil || !anyInVolume(list) {
		return list, err
	}
	f, ok, err := s.folder(ctx, q, deliverableID)
	if err != nil || !ok {
		return nil, errors.Join(ErrNotFound, err)
	}
	for i := range list {
		if list[i], err = f.fill(ctx, version, list[i]); err != nil {
			return nil, err
		}
	}
	return list, nil
}

func anyInVolume(list []ArtifactFile) bool {
	for _, f := range list {
		if f.InVolume {
			return true
		}
	}
	return false
}

// Load reads one file with its data.
func (s Store) Load(ctx context.Context, q store.DB, deliverableID string, version int, path string) (ArtifactFile, error) {
	a, err := Load(ctx, q, deliverableID, version, path)
	if err != nil || !a.InVolume {
		return a, err
	}
	f, ok, err := s.folder(ctx, q, deliverableID)
	if err != nil || !ok {
		return a, errors.Join(ErrNotFound, err)
	}
	return f.fill(ctx, version, a)
}

// fill reads the data of a file kept in the owner's shared folder. A file removed from it or
// changed there (size or SHA-256) is ErrNotFound: the version can no longer be served.
func (f folder) fill(ctx context.Context, version int, a ArtifactFile) (ArtifactFile, error) {
	if !a.InVolume {
		return a, nil
	}
	r, _, err := f.files.Open(ctx, f.file(version, a.Path))
	if errors.Is(err, userfs.ErrNoFile) {
		return a, fmt.Errorf("%w: %s is gone from the shared folder", ErrNotFound, a.Path)
	}
	if err != nil {
		return a, err
	}
	defer r.Close()
	if a.Data, err = io.ReadAll(io.LimitReader(r, int64(a.Size)+1)); err != nil {
		return a, err
	}
	sum := sha256.Sum256(a.Data)
	if len(a.Data) != a.Size || hex.EncodeToString(sum[:]) != a.SHA256 {
		return a, fmt.Errorf("%w: %s changed in the shared folder", ErrNotFound, a.Path)
	}
	return a, nil
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
	f, ok, err := s.folder(ctx, s.DB, deliverableID)
	if err != nil || !ok {
		return false, err
	}
	list, err := LoadVersion(ctx, s.DB, deliverableID, version)
	if err != nil {
		return false, err
	}
	if err := f.writeVersion(ctx, version, list); err != nil {
		return false, err
	}
	_, err = store.Exec(ctx, s.DB, `UPDATE at_deliverable_files SET in_volume = true, data = NULL
		WHERE deliverable_id = $1 AND version = $2 AND NOT in_volume`, deliverableID, version)
	return err == nil, err
}

// Remover finds a deliverable's folder in its owner's shared folder while its row still exists and
// returns what deletes it: call that after the rows went (best effort, a folder left behind is
// only clutter). It does nothing when the folder is off or holds none.
func (s Store) Remover(ctx context.Context, deliverableID string) (func(context.Context) error, error) {
	f, ok, err := s.folder(ctx, s.DB, deliverableID)
	if err != nil || !ok {
		return func(context.Context) error { return nil }, err
	}
	return func(ctx context.Context) error {
		if err := f.files.Remove(ctx, f.dir); err != nil && !errors.Is(err, userfs.ErrNoFile) {
			return err
		}
		return nil
	}, nil
}

// MoveFromVolumes puts the versions of the user's deliverables kept in the user's shared folder back
// into the database (data in the rows, in_volume false), one version per transaction; run it before
// the volume is destroyed, while it is still ready. A file gone from the folder (or changed there)
// is logged and its row left as it is: it could not be served anyway. Any other failure, a folder
// that is not available included, stops it with an error naming the version.
func (s Store) MoveFromVolumes(ctx context.Context, userID string, log *slog.Logger) error {
	var versions []struct {
		DeliverableID string
		Version       int
	}
	if err := s.DB.Gorm(ctx).Raw(`SELECT DISTINCT f.deliverable_id, f.version FROM at_deliverable_files f
		JOIN at_deliverables d ON d.id = f.deliverable_id JOIN at_projects p ON p.id = d.project_id
		WHERE p.user_id = ? AND f.in_volume ORDER BY f.deliverable_id, f.version`, userID).Scan(&versions).Error; err != nil {
		return err
	}
	for _, v := range versions {
		if err := s.moveBack(ctx, v.DeliverableID, v.Version, log); err != nil {
			return fmt.Errorf("deliverable %s version %d: %w", v.DeliverableID, v.Version, err)
		}
	}
	return nil
}

// moveBack copies one version's files from the shared folder into its rows.
func (s Store) moveBack(ctx context.Context, deliverableID string, version int, log *slog.Logger) error {
	f, ok, err := s.folder(ctx, s.DB, deliverableID)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("the shared folder is not available")
	}
	return store.InTx(ctx, s.DB, func(tx store.DB) error {
		list, err := LoadVersion(ctx, tx, deliverableID, version)
		if errors.Is(err, ErrNotFound) {
			return nil // deleted meanwhile
		}
		if err != nil {
			return err
		}
		for _, a := range list {
			if err := moveFile(ctx, tx, f, deliverableID, version, a, log); err != nil {
				return err
			}
		}
		return nil
	})
}

func moveFile(ctx context.Context, tx store.DB, f folder, deliverableID string, version int, a ArtifactFile, log *slog.Logger) error {
	if !a.InVolume {
		return nil
	}
	a, err := f.fill(ctx, version, a)
	if errors.Is(err, ErrNotFound) {
		log.Warn("deliverable file unavailable in the shared folder; row left", "deliverable_id", deliverableID, "version", version, "path", a.Path, "err", err)
		return nil
	}
	if err != nil {
		return err
	}
	_, err = store.Exec(ctx, tx, `UPDATE at_deliverable_files SET data = $1, in_volume = false
		WHERE deliverable_id = $2 AND version = $3 AND path = $4 AND in_volume`, a.Data, deliverableID, version, a.Path)
	return err
}
