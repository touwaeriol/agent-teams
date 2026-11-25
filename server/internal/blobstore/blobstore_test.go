package blobstore_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/blobstore"
	"github.com/touwaeriol/agent-teams/server/internal/blobstore/blobtest"
)

// TestFake runs the contract against the in-process fake.
func TestFake(t *testing.T) {
	contract(t, blobtest.New(t), "projects/test-fake/", false)
}

// TestLive runs the contract against a real S3-compatible store (RustFS), only when
// TEST_S3_URL is set; credentials come from TEST_S3_ACCESS_KEY / TEST_S3_SECRET_KEY,
// TEST_S3_BUCKET (default agent-teams), TEST_S3_REGION. Objects go under projects/test-58/ and
// are deleted at the end.
func TestLive(t *testing.T) {
	endpoint := os.Getenv("TEST_S3_URL")
	if endpoint == "" {
		t.Skip("TEST_S3_URL not set")
	}
	bucket := os.Getenv("TEST_S3_BUCKET")
	if bucket == "" {
		bucket = "agent-teams"
	}
	s, err := blobstore.New(blobstore.Config{Endpoint: endpoint, Bucket: bucket, AccessKey: os.Getenv("TEST_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("TEST_S3_SECRET_KEY"), Region: os.Getenv("TEST_S3_REGION"), PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Check(context.Background()); err != nil {
		t.Fatalf("head bucket: %v", err)
	}
	contract(t, s, "projects/test-58/", true)
}

func random(t *testing.T, n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func do(t *testing.T, method, url string, headers map[string]string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = int64(len(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func contract(t *testing.T, s blobstore.Store, prefix string, live bool) {
	ctx := context.Background()
	t.Cleanup(func() {
		if err := s.DeletePrefix(context.Background(), prefix); err != nil {
			t.Errorf("cleanup %s: %v", prefix, err)
		}
	})
	if !s.Configured() {
		t.Fatal("not configured")
	}

	// Presigned single PUT, HEAD, presigned GET with response overrides.
	put := prefix + "attachments/img_put"
	data := random(t, 300<<10)
	url, headers, err := s.PresignPut(ctx, put, 5*time.Minute, int64(len(data)), "application/octet-stream")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(url), "x-amz-checksum") || strings.Contains(strings.ToLower(url), "x-amz-sdk-checksum") {
		t.Fatalf("presigned URL carries a checksum parameter: %s", url)
	}
	if res := do(t, http.MethodPut, url, headers, data); res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("presigned PUT = %d %s", res.StatusCode, b)
	}
	o, err := s.Head(ctx, put)
	if err != nil || o.Size != int64(len(data)) {
		t.Fatalf("head %+v %v", o, err)
	}
	get, err := s.PresignGet(ctx, put, 5*time.Minute, blobstore.GetOptions{ContentType: "application/octet-stream",
		ContentDisposition: `attachment; filename*=UTF-8''%E6%95%B0%E6%8D%AE.bin`})
	if err != nil {
		t.Fatal(err)
	}
	res := do(t, http.MethodGet, get, nil, nil)
	got, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !bytes.Equal(got, data) {
		t.Fatalf("presigned GET = %d, %d bytes", res.StatusCode, len(got))
	}
	if live && !strings.HasPrefix(res.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("response-content-disposition ignored: %q", res.Header.Get("Content-Disposition"))
	}

	// Multipart with presigned part URLs (5 MB minimum except the last part).
	mp := prefix + "attachments/img_multi"
	part1, part2 := random(t, 5<<20), random(t, 1<<20)
	id, err := s.CreateMultipart(ctx, mp, "application/zip")
	if err != nil {
		t.Fatal(err)
	}
	var parts []blobstore.Part
	for i, p := range [][]byte{part1, part2} {
		u, err := s.PresignPart(ctx, mp, id, int32(i+1), int64(len(p)), 5*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if !signsContentLength(t, u) {
			t.Fatalf("part URL does not sign Content-Length: %s", u)
		}
		if live { // a body of another length than the signed one is refused (the fake does not check signatures)
			if res := do(t, http.MethodPut, u, nil, append(p, 0)); res.StatusCode == http.StatusOK {
				t.Fatalf("part %d with one byte too many = %d, want refused", i+1, res.StatusCode)
			}
		}
		res := do(t, http.MethodPut, u, nil, p)
		if res.StatusCode != http.StatusOK || res.Header.Get("ETag") == "" {
			b, _ := io.ReadAll(res.Body)
			t.Fatalf("upload part %d = %d etag=%q %s", i+1, res.StatusCode, res.Header.Get("ETag"), b)
		}
		parts = append(parts, blobstore.Part{Number: int32(i + 1), ETag: res.Header.Get("ETag")})
	}
	pending, err := s.ListMultipart(ctx, prefix, time.Now().Add(time.Minute))
	if err != nil || len(pending) != 1 || pending[0].UploadID != id {
		t.Fatalf("list multipart %+v %v", pending, err)
	}
	if err := s.CompleteMultipart(ctx, mp, id, []blobstore.Part{parts[1], parts[0]}); err != nil {
		t.Fatal(err)
	}
	if o, err := s.Head(ctx, mp); err != nil || o.Size != int64(len(part1)+len(part2)) {
		t.Fatalf("multipart head %+v %v", o, err)
	}
	head, err := s.ReadRange(ctx, mp, int64(len(part1))-3, 6)
	if err != nil || !bytes.Equal(head, append(append([]byte{}, part1[len(part1)-3:]...), part2[:3]...)) {
		t.Fatalf("read range across parts: %v", err)
	}

	// Abort: the upload disappears from the list; aborting twice is fine.
	ab := prefix + "attachments/img_abort"
	aid, err := s.CreateMultipart(ctx, ab, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AbortMultipart(ctx, ab, aid); err != nil {
		t.Fatal(err)
	}
	if err := s.AbortMultipart(ctx, ab, aid); err != nil {
		t.Fatalf("second abort: %v", err)
	}
	if pending, _ := s.ListMultipart(ctx, prefix, time.Now().Add(time.Minute)); len(pending) != 0 {
		t.Fatalf("aborted upload still listed: %+v", pending)
	}

	// Server-side Put, Open, Copy, Delete.
	small := prefix + "shared/sf_small"
	text := []byte("你好，共享文件")
	if err := s.Put(ctx, small, io.NopCloser(bytes.NewReader(text)), int64(len(text)), "text/plain; charset=utf-8"); err != nil {
		t.Fatal(err)
	}
	cp := prefix + "shared/sf_copy"
	if err := s.Copy(ctx, mp, cp); err != nil {
		t.Fatal(err)
	}
	rc, err := s.Open(ctx, cp)
	if err != nil {
		t.Fatal(err)
	}
	all, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(all, append(append([]byte{}, part1...), part2...)) {
		t.Fatalf("copy: %d bytes", len(all))
	}
	if b, err := s.ReadRange(ctx, small, 0, 1000); err != nil || !bytes.Equal(b, text) {
		t.Fatalf("read range past the end %q %v", b, err)
	}
	if err := s.Delete(ctx, small, prefix+"shared/missing"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Head(ctx, small); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("deleted object: %v", err)
	}

	// DeletePrefix removes the rest.
	if err := s.DeletePrefix(ctx, prefix); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{put, mp, cp} {
		if _, err := s.Head(ctx, k); !errors.Is(err, blobstore.ErrNotFound) {
			t.Fatalf("%s after DeletePrefix: %v", k, err)
		}
	}
}

func TestNone(t *testing.T) {
	var s blobstore.Store = blobstore.Or(nil)
	if s.Configured() {
		t.Fatal("None is configured")
	}
	if _, err := s.Head(context.Background(), "x"); !errors.Is(err, blobstore.ErrNotConfigured) {
		t.Fatal(err)
	}
	if blobstore.AttachmentKey("p1", "img_1") != "projects/p1/attachments/img_1" || blobstore.SharedKey("p1", "sf_1") != "projects/p1/shared/sf_1" {
		t.Fatal("keys")
	}
}

// signsContentLength reports whether a presigned URL's SigV4 signature covers Content-Length.
func signsContentLength(t *testing.T, raw string) bool {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range strings.Split(u.Query().Get("X-Amz-SignedHeaders"), ";") {
		if h == "content-length" {
			return true
		}
	}
	return false
}

// TestPartLength: every part but the last is PartSize, the last the rest; out of range is 0.
func TestPartLength(t *testing.T) {
	size := int64(2*blobstore.PartSize + 7)
	for _, c := range []struct {
		part int32
		want int64
	}{{0, 0}, {1, blobstore.PartSize}, {2, blobstore.PartSize}, {3, 7}, {4, 0}} {
		if got := blobstore.PartLength(size, c.part); got != c.want {
			t.Errorf("PartLength(%d, %d) = %d, want %d", size, c.part, got, c.want)
		}
	}
	if got := blobstore.PartLength(5, 1); got != 5 {
		t.Errorf("single part = %d", got)
	}
}
