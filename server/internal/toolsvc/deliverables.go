package toolsvc

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/artifacts"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
)

// DownloadDir is where read_deliverable puts a version, relative to the employee's working directory
// (the daemon writes a .gitignore of "*" into the top folder so it is never committed).
const DownloadDir = ".agent-deliverables"

// FeatureFileDownload is the hello feature of daemons that accept file.download.
const FeatureFileDownload = "file_download"

const listDeliverablesLimit = 50

// listDeliverables lists the project's deliverables with their version counts (every thread);
// hosted images published before screenshots stopped being deliverables are left out.
func (s *Service) listDeliverables(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.ListDeliverablesInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	f := core.DeliverableFilter{Limit: listDeliverablesLimit, NoImages: true}
	if in.Kind != nil {
		f.Kind = strings.TrimSpace(*in.Kind)
	}
	if in.Thread != nil {
		t, err := core.ThreadByNo(ctx, tx, c.emp().ProjectID, *in.Thread)
		if err != nil {
			return Result{}, refuse("no thread #%d", *in.Thread)
		}
		f.ThreadID = t.ID
	}
	list, err := core.Deliverables(ctx, tx, c.emp().ProjectID, f)
	if err != nil {
		return Result{}, err
	}
	out := protocol.ListDeliverablesResult{Deliverables: []protocol.DeliverableSummary{}}
	lines := []string{}
	for _, d := range list {
		sum, err := s.deliverableSummary(ctx, tx, d)
		if err != nil {
			return Result{}, err
		}
		out.Deliverables = append(out.Deliverables, sum)
		lines = append(lines, summaryLine(sum))
	}
	if len(lines) == 0 {
		return okJSON("This project has no deliverables yet.", out), nil
	}
	text := strings.Join(lines, "\n") + "\nFetch the files into your working directory with read_deliverable(deliverable_id); after changes, publish_file with the same deliverable_id publishes a new version."
	return okJSON(text, out), nil
}

func (s *Service) deliverableSummary(ctx context.Context, tx *core.Tx, d core.Deliverable) (protocol.DeliverableSummary, error) {
	v, err := core.DeliverableViewOf(ctx, tx, d)
	if err != nil {
		return protocol.DeliverableSummary{}, err
	}
	sum := protocol.DeliverableSummary{ID: d.ID, Title: d.Title, Kind: d.Kind, Hosted: d.Detail["hosted"] == true,
		PublishedBy: d.PublishedBy, Thread: v.ThreadNo, UpdatedAt: d.UpdatedAt}
	if !sum.Hosted {
		if d.URL != "" {
			sum.URL = &d.URL
		}
		return sum, nil
	}
	versions, err := artifacts.Versions(ctx, tx, d.ID)
	if err != nil || len(versions) == 0 {
		return sum, err
	}
	sum.Versions, sum.LatestVersion, sum.PublishedBy = len(versions), versions[0].Version, versions[0].PublishedBy
	return sum, nil
}

func summaryLine(d protocol.DeliverableSummary) string {
	where := "project"
	if d.Thread != nil {
		where = fmt.Sprintf("#%d", *d.Thread)
	}
	if !d.Hosted {
		url := ""
		if d.URL != nil {
			url = " · " + *d.URL
		}
		return fmt.Sprintf("- %s · %s · %q · %s · %s%s (external link, no files to fetch)", d.ID, d.Kind, d.Title, where, d.PublishedBy, url)
	}
	return fmt.Sprintf("- %s · %s · %q · %s · version %d (of %d, latest published by %s) · %s", d.ID, d.Kind, d.Title, where,
		d.LatestVersion, d.Versions, d.PublishedBy, stamp(d.UpdatedAt))
}

func stamp(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") }

// readDeliverable sends a version's files to the employee's working directory (file.download
// frames before the tool.result) and answers with the folder, files, versions and comments.
func (s *Service) readDeliverable(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.ReadDeliverableInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	d, err := s.projectHosted(ctx, tx, c, in.DeliverableID)
	if err != nil {
		return Result{}, err
	}
	versions, err := artifacts.Versions(ctx, tx, d.ID)
	if err != nil {
		return Result{}, err
	}
	version := pickVersion(versions, in.Version)
	if version == 0 && in.Version != nil {
		return Result{}, refuse("deliverable %s has no version %d", d.ID, *in.Version)
	}
	if version == 0 {
		return Result{}, refuse("deliverable %s has no hosted files", d.ID)
	}
	files, err := s.core.Artifacts().LoadVersion(ctx, tx, d.ID, version)
	if err != nil {
		return Result{}, err
	}
	comments, err := deliverableComments(ctx, tx, d.ID)
	if err != nil {
		return Result{}, err
	}
	download, err := s.sendDownload(ctx, tx, c, d.ID, version, files)
	if err != nil {
		return Result{}, err
	}
	out := readResult(d, version, files, versions, comments)
	return Result{OK: true, Text: readText(out), Structured: out, Download: download}, nil
}

// projectHosted loads a deliverable of the employee's project that has hosted files.
func (s *Service) projectHosted(ctx context.Context, tx *core.Tx, c *call, id string) (core.Deliverable, error) {
	d, err := core.DeliverableByID(ctx, tx, c.sc.OwnerID, id)
	if errors.Is(err, core.ErrNotFound) || (err == nil && d.ProjectID != c.emp().ProjectID) {
		return d, refuse("this project has no deliverable %s (see list_deliverables)", id)
	}
	if err == nil && d.Detail["hosted"] != true {
		return d, refuse("deliverable %s is an external link (%s), with no files to fetch", id, d.URL)
	}
	return d, err
}

func pickVersion(versions []artifacts.ArtifactVersion, want *int) int {
	if len(versions) == 0 {
		return 0
	}
	if want == nil {
		return versions[0].Version
	}
	for _, v := range versions {
		if v.Version == *want {
			return v.Version
		}
	}
	return 0
}

// sendDownload queues the files as file.download chunks for the session's node.
func (s *Service) sendDownload(ctx context.Context, tx *core.Tx, c *call, id string, version int,
	files []artifacts.ArtifactFile) (*protocol.ToolDownload, error) {
	sess, err := core.SessionByID(ctx, tx, c.sc.Session.ID)
	if err != nil {
		return nil, err
	}
	if sess.NodeID == nil {
		return nil, refuse("the session is on no node")
	}
	ok, err := core.NodeHasFeature(ctx, tx, *sess.NodeID, FeatureFileDownload)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, refuse("the node daemon needs an upgrade to a version with read_deliverable")
	}
	dl := &protocol.ToolDownload{DownloadID: protocol.DownloadID(core.NewID("dn")), DeliverableID: id, Version: version,
		Files: make([]protocol.DownloadFile, len(files))}
	var all []byte
	for i, f := range files {
		dl.Files[i] = protocol.DownloadFile{Path: f.Path, Size: len(f.Data), SHA256: f.SHA256}
		all = append(all, f.Data...)
	}
	for off := 0; off < len(all); off += artifacts.ChunkBytes {
		body := protocol.FileDownload{SessionID: protocol.SessionID(sess.ID), CallID: protocol.CallID(c.callID),
			DownloadID: dl.DownloadID, Index: dl.Chunks, Data: encode(all[off:min(off+artifacts.ChunkBytes, len(all))])}
		if _, err := s.gw.Enqueue(ctx, tx, *sess.NodeID, nodegw.Command{Type: protocol.TypeFileDownload, Body: body,
			SessionID: sess.ID, OpenGen: sess.OpenGen}); err != nil {
			return nil, err
		}
		dl.Chunks++
	}
	return dl, nil
}

func deliverableComments(ctx context.Context, tx *core.Tx, id string) ([]protocol.DeliverableComment, error) {
	out := []protocol.DeliverableComment{}
	err := tx.Gorm(ctx).Table("at_deliverable_comments").Select("version, text, sent_to, created_at").
		Where("deliverable_id = ?", id).Order("created_at, id").Find(&out).Error
	return out, err
}

func readResult(d core.Deliverable, version int, files []artifacts.ArtifactFile, versions []artifacts.ArtifactVersion,
	comments []protocol.DeliverableComment) protocol.ReadDeliverableResult {
	out := protocol.ReadDeliverableResult{DeliverableID: d.ID, Title: d.Title, Kind: d.Kind, Version: version,
		Dir: fmt.Sprintf("%s/%s/v%d", DownloadDir, d.ID, version), Files: make([]protocol.DeliverableFileInfo, len(files)),
		Versions: make([]protocol.DeliverableVersionInfo, len(versions)), Comments: comments}
	paths := make([]string, len(files))
	for i, f := range files {
		out.Files[i] = protocol.DeliverableFileInfo{Path: f.Path, Size: len(f.Data)}
		paths[i] = f.Path
	}
	out.Entry = artifacts.Entry(paths)
	for i, v := range versions {
		out.Versions[i] = protocol.DeliverableVersionInfo{Version: v.Version, PublishedBy: v.PublishedBy, Thread: v.PublisherThreadNo,
			CreatedAt: v.CreatedAt}
	}
	return out
}

func readText(r protocol.ReadDeliverableResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Fetched the %[5]d files of version %[4]d of deliverable %[1]q (%[2]s, %[3]s) into %[6]s/ of your working directory (never committed by git):", r.Title, r.DeliverableID,
		r.Kind, r.Version, len(r.Files), r.Dir)
	for _, f := range r.Files {
		fmt.Fprintf(&b, "\n- %s (%d bytes)", f.Path, f.Size)
	}
	b.WriteString("\nVersion history:")
	for _, v := range r.Versions {
		who := v.PublishedBy
		if v.Thread != nil {
			who += fmt.Sprintf(" (#%d)", *v.Thread)
		}
		fmt.Fprintf(&b, "\n- version %d · %s · %s", v.Version, who, stamp(v.CreatedAt))
	}
	if len(r.Comments) > 0 {
		b.WriteString("\nUser comments:")
	}
	for _, c := range r.Comments {
		version := ""
		if c.Version != nil {
			version = fmt.Sprintf(" (version %d)", *c.Version)
		}
		fmt.Fprintf(&b, "\n- %s%s %s", stamp(c.CreatedAt), version, c.Text)
	}
	target := r.Dir
	if len(r.Files) == 1 {
		target = r.Dir + "/" + r.Entry
	}
	fmt.Fprintf(&b, "\nTo revise: edit these files in place, then publish a new version with publish_file(path=%q, kind=%q, title=%q, deliverable_id=%q) (a lead cannot change files, so assign the revision to a member).",
		target, r.Kind, r.Title, r.DeliverableID)
	return b.String()
}

func encode(b []byte) string { return base64.StdEncoding.EncodeToString(b) }
