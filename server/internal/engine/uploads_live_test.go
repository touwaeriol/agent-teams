package engine_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/blobstore"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/engine"
	"github.com/touwaeriol/agent-teams/server/internal/orchapi"
	"github.com/touwaeriol/agent-teams/server/internal/sharedfiles"
)

// TestLiveUploads runs the upload and download routes against a real object store (RustFS) when
// TEST_S3_URL is set: a browser-style multipart upload of an attachment (two parts) and its
// completion, the page's presigned download, a node fetching its URL, a shared file in parts and
// a node's presigned PUT. Everything is under the test project's prefix, deleted at the end.
func TestLiveUploads(t *testing.T) {
	endpoint := os.Getenv("TEST_S3_URL")
	if endpoint == "" {
		t.Skip("TEST_S3_URL not set")
	}
	bucket := os.Getenv("TEST_S3_BUCKET")
	if bucket == "" {
		bucket = "agent-teams"
	}
	live, err := blobstore.New(blobstore.Config{Endpoint: endpoint, Bucket: bucket, AccessKey: os.Getenv("TEST_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("TEST_S3_SECRET_KEY"), Region: os.Getenv("TEST_S3_REGION"), PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := live.Check(ctx); err != nil {
		t.Fatalf("head bucket: %v", err)
	}
	w := newWorldWith(t, nil, func(e *engine.Engine) { e.Core.Blobs = live })
	tm := teamIn(t, w)
	b, pid := tm.b, tm.projectID
	prefix := blobstore.ProjectPrefix(pid)
	t.Cleanup(func() {
		if err := live.DeletePrefix(ctx, prefix); err != nil {
			t.Errorf("cleanup %s: %v", prefix, err)
		}
		if left, err := live.ListMultipart(ctx, prefix, time.Now().Add(time.Hour)); err == nil {
			for _, p := range left {
				_ = live.AbortMultipart(ctx, p.Key, p.UploadID)
			}
		}
	})

	// Attachment: 16 MB + 1 KB in two parts, Chinese name.
	body := bytes.Repeat([]byte("live-58!"), (blobstore.PartSize+1024)/8)
	done := b.upload(pid, map[string]any{"kind": "attachment", "name": "实测 包.tar"}, body)
	att := done.Attachment
	if att == nil || att.Size != len(body) || att.ContentType != "application/x-tar" {
		t.Fatalf("attachment %+v", att)
	}
	resp, _ := b.redirect("/api/attachments/" + att.ID)
	if resp.StatusCode != 302 || !strings.HasPrefix(resp.Header.Get("Location"), endpoint) {
		t.Fatalf("page download %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	obj, raw := fetchURL(t, resp.Header.Get("Location"))
	if obj.StatusCode != 200 || !bytes.Equal(raw, body) || obj.Header.Get("Content-Type") != "application/octet-stream" ||
		!strings.Contains(obj.Header.Get("Content-Disposition"), "filename*=UTF-8''%E5%AE%9E%E6%B5%8B%20%E5%8C%85.tar") {
		t.Fatalf("presigned download %d %d bytes %v", obj.StatusCode, len(raw), obj.Header)
	}
	code, raw := nodeCall(t, w, "GET", "/nodes/attachments/"+att.ID+"/url", tm.token, nil)
	var nu orchapi.NodeAttachmentURL
	if code != 200 || json.Unmarshal(raw, &nu) != nil || nu.URL == nil {
		t.Fatalf("node url %d %s", code, raw)
	}
	if r, got := fetchURL(t, *nu.URL); r.StatusCode != 200 || !bytes.Equal(got, body) {
		t.Fatalf("node download %d %d bytes", r.StatusCode, len(got))
	}
	t.Logf("attachment %s: %d bytes in %d parts, page 302 + presigned GET ok, node url ok", att.ID, len(body), blobstore.PartCount(int64(len(body))))

	// An image inline.
	png := b.upload(pid, map[string]any{"kind": "attachment", "name": "图.png"}, onePixelPNG).Attachment
	resp, _ = b.redirect("/api/attachments/" + png.ID)
	if obj, _ := fetchURL(t, resp.Header.Get("Location")); obj.Header.Get("Content-Type") != "image/png" ||
		!strings.HasPrefix(obj.Header.Get("Content-Disposition"), "inline;") {
		t.Fatalf("inline image %v", obj.Header)
	}

	// Shared file in parts, then its download.
	shared := b.upload(pid, map[string]any{"kind": "shared", "name": "d.bin", "path": "live/d.bin"}, body).SharedFile
	if shared == nil || shared.Size != int64(len(body)) {
		t.Fatalf("shared %+v", shared)
	}
	resp, _ = b.redirect("/api/projects/" + pid + "/shared-files/download?path=live/d.bin")
	if obj, got := fetchURL(t, resp.Header.Get("Location")); obj.StatusCode != 200 || !bytes.Equal(got, body) {
		t.Fatalf("shared download %d", obj.StatusCode)
	}

	// A node's presigned PUT, finalized with its sha256 checked.
	out := []byte("node output for RustFS")
	code, raw = nodeCall(t, w, "POST", "/nodes/uploads", tm.token, map[string]any{"session_id": tm.dev.session, "path": "live/n.txt",
		"size": len(out), "sha256": hexSum(out)})
	var up orchapi.NodeUpload
	if code != 201 || json.Unmarshal(raw, &up) != nil {
		t.Fatalf("node upload %d %s", code, raw)
	}
	req, _ := http.NewRequest("PUT", up.URL, bytes.NewReader(out))
	for k, v := range up.Headers {
		req.Header.Set(k, v)
	}
	put, err := http.DefaultClient.Do(req)
	if err != nil || put.StatusCode != 200 {
		t.Fatalf("presigned put %v %v", put, err)
	}
	put.Body.Close()
	var f sharedfiles.SharedFile
	if err := w.e.Core.InTx(ctx, func(tx *core.Tx) error {
		var err error
		f, _, err = sharedfiles.FinalizeUpload(ctx, tx, live, pid, up.UploadID, sharedfiles.FinalizeOptions{})
		return err
	}); err != nil || f.SHA256 != hexSum(out) {
		t.Fatalf("finalize %+v %v", f, err)
	}
	t.Logf("shared file in parts and node PUT ok (%s)", f.Path)
}
