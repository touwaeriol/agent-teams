package userfs

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestCleanPath(t *testing.T) {
	for in, want := range map[string]string{"": "", "/": "", "a/b.txt": "a/b.txt", "/a/b/": "a/b", "报告 v2.pdf": "报告 v2.pdf"} {
		if got, err := CleanPath(in); err != nil || got != want {
			t.Errorf("CleanPath(%q) = %q %v", in, got, err)
		}
	}
	for _, in := range []string{"../x", "a/../b", "a//b", "./a", "a\\b", "a\x00", "a\nb", strings.Repeat("x", MaxPathBytes+1)} {
		if _, err := CleanPath(in); !errors.Is(err, ErrBadPath) {
			t.Errorf("CleanPath(%q) accepted", in)
		}
	}
	if _, err := Join("projects/p1", "../p2/x"); !errors.Is(err, ErrBadPath) {
		t.Error("Join left its base")
	}
}

// exerciseFiles runs the same checks on any Files.
func exerciseFiles(t *testing.T, f Files) {
	ctx := context.Background()
	if err := f.Put(ctx, "projects/p1/docs/a.txt", strings.NewReader("hello"), 5); err != nil {
		t.Fatal(err)
	}
	if err := f.Put(ctx, "projects/p1/b.csv", strings.NewReader("1,2"), 3); err != nil {
		t.Fatal(err)
	}
	if err := f.Mkdir(ctx, "projects/p1/empty"); err != nil {
		t.Fatal(err)
	}
	es, err := f.List(ctx, "projects/p1")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name)
	}
	if strings.Join(names, ",") != "docs,empty,b.csv" || !es[0].Dir || es[2].Size != 3 {
		t.Fatalf("list: %+v", es)
	}
	r, e, err := f.Open(ctx, "projects/p1/docs/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r)
	r.Close()
	if string(b) != "hello" || e.Size != 5 || e.Name != "a.txt" {
		t.Fatalf("open: %q %+v", b, e)
	}
	if _, _, err := f.Open(ctx, "projects/p1/nope"); !errors.Is(err, ErrNoFile) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := f.List(ctx, "projects/nope"); !errors.Is(err, ErrNoFile) {
		t.Fatalf("missing dir: %v", err)
	}
	if err := f.Remove(ctx, "projects/p1/docs"); err != nil {
		t.Fatal(err)
	}
	if es, _ := f.List(ctx, "projects/p1"); len(es) != 2 {
		t.Fatalf("after remove: %+v", es)
	}
	if err := f.Remove(ctx, ""); !errors.Is(err, ErrBadPath) {
		t.Fatalf("remove root: %v", err)
	}
}

func TestDirFiles(t *testing.T) {
	exerciseFiles(t, DirFiles{Root: t.TempDir()})
}

// TestRustFS runs Store against a real RustFS or MinIO when TEST_S3_ENDPOINT, TEST_S3_ACCESS_KEY and
// TEST_S3_SECRET_KEY name one (an admin key): a bucket per user with lifecycle, CORS and quota, files,
// usage, and user keys that reach their own bucket only, cannot write when read-only, and stop
// working once revoked.
func TestRustFS(t *testing.T) {
	endpoint := os.Getenv("TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_S3_ENDPOINT not set")
	}
	ctx := context.Background()
	run := strconv.FormatInt(time.Now().UnixNano(), 36)
	r := NewRustFS(endpoint, "", os.Getenv("TEST_S3_ACCESS_KEY"), os.Getenv("TEST_S3_SECRET_KEY"), []string{"https://console.example"})
	a, b := NamesOf("ua"+run), NamesOf("ub"+run)
	fa, fb := r.Folder(a), r.Folder(b)
	if fa != (Folder{Bucket: a.Name}) {
		t.Fatalf("folder %+v", fa)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		for _, n := range []UserNames{a, b} {
			_ = r.RevokeKey(ctx, n, n.User)
			_ = r.Remove(ctx, r.Folder(n))
		}
	})
	for _, f := range []Folder{fa, fa, fb} {
		if err := r.Prepare(ctx, f, 1); err != nil {
			t.Fatalf("prepare %s: %v", f.Bucket, err)
		}
	}
	lc, err := r.c.GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{Bucket: &fa.Bucket})
	if err != nil || len(lc.Rules) != 1 || lc.Rules[0].AbortIncompleteMultipartUpload == nil {
		t.Fatalf("lifecycle %+v %v", lc, err)
	}
	cors, err := r.c.GetBucketCors(ctx, &s3.GetBucketCorsInput{Bucket: &fa.Bucket})
	if err != nil || len(cors.CORSRules) != 1 || !slices.Contains(cors.CORSRules[0].AllowedOrigins, "https://console.example") {
		t.Fatalf("cors %+v %v", cors, err)
	}
	if q, err := r.quota(ctx, fa.Bucket); err != nil || q != 1<<30 {
		t.Fatalf("quota %d %v", q, err)
	}
	exerciseFiles(t, r.Files(fa))
	// A body that cannot be rewound (an attachment's reader, an object-store download) goes as an
	// unsigned payload.
	if err := r.Files(fa).Put(ctx, "projects/p1/stream.txt", io.NopCloser(strings.NewReader("streamed")), 8); err != nil {
		t.Fatalf("non-seekable put: %v", err)
	}
	if err := r.Files(fb).Put(ctx, "shared/b.txt", strings.NewReader("b"), 1); err != nil {
		t.Fatal(err)
	}
	if u, err := r.Usage(ctx, fa); err != nil || u.UsedBytes < 8 || u.Objects < 2 {
		t.Fatalf("usage %+v %v", u, err)
	}
	if fs, err := r.Folders(ctx); err != nil || !slices.Contains(fs, fa) || !slices.Contains(fs, fb) {
		t.Fatalf("folders %v %v", fs, err)
	}
	if _, err := r.IssueKey(ctx, a, fa, true); err != nil {
		t.Fatal(err)
	}
	key, err := r.IssueKey(ctx, a, fa, true)
	if err != nil || key.ID != a.User {
		t.Fatalf("issue again: %+v %v", key, err)
	}
	own := NewS3Files(endpoint, "", key.ID, key.Secret, fa.Bucket, "")
	if err := own.Put(ctx, "shared/x.txt", strings.NewReader("x"), 1); err != nil {
		t.Fatalf("user writes its bucket: %v", err)
	}
	if _, err := own.List(ctx, "shared"); err != nil {
		t.Fatalf("user lists its bucket: %v", err)
	}
	// Over the quota, a write is refused.
	big := strings.Repeat("x", 1<<30+1)
	if err := own.Put(ctx, "shared/big.bin", strings.NewReader(big), int64(len(big))); err == nil {
		t.Fatal("write over the quota accepted")
	}
	other := NewS3Files(endpoint, "", key.ID, key.Secret, fb.Bucket, "")
	if err := other.Put(ctx, "x.txt", strings.NewReader("x"), 1); err == nil {
		t.Fatal("user wrote another user's bucket")
	}
	if _, err := other.List(ctx, "shared"); err == nil {
		t.Fatal("user listed another user's bucket")
	}
	if _, _, err := other.Open(ctx, "shared/b.txt"); err == nil {
		t.Fatal("user read another user's file")
	}
	ro, err := r.IssueKey(ctx, a, fa, false)
	if err != nil {
		t.Fatal(err)
	}
	locked := NewS3Files(endpoint, "", ro.ID, ro.Secret, fa.Bucket, "")
	if err := locked.Put(ctx, "shared/y.txt", strings.NewReader("y"), 1); err == nil {
		t.Fatal("read-only key wrote")
	}
	if err := locked.Remove(ctx, "shared/x.txt"); err != nil {
		t.Fatalf("read-only key cannot delete: %v", err)
	}
	if _, err := own.List(ctx, "shared"); err == nil {
		t.Fatal("previous secret still works after a new key")
	}
	if ks, err := r.Keys(ctx); err != nil || !slices.Contains(ks, KeyInfo{ID: a.User, Name: a.User}) {
		t.Fatalf("keys %v %v", ks, err)
	}
	for range 2 {
		if err := r.RevokeKey(ctx, a, ro.ID); err != nil {
			t.Fatalf("revoke: %v", err)
		}
	}
	if _, err := locked.List(ctx, ""); err == nil {
		t.Fatal("revoked key still works")
	}
	for range 2 {
		if err := r.Remove(ctx, fa); err != nil {
			t.Fatalf("remove: %v", err)
		}
	}
	if u, err := r.Usage(ctx, fb); err != nil || u.Objects != 1 {
		t.Fatalf("other bucket touched %+v %v", u, err)
	}
}

// TestServiceFiles: only a ready folder opens, as the user's bucket.
func TestServiceFiles(t *testing.T) {
	s, fs, _ := newTest(t)
	ctx := context.Background()
	if _, err := s.Files(ctx, "u1"); !IsNotFound(err) {
		t.Fatalf("no folder: %v", err)
	}
	if _, err := s.Enable(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	f, err := s.Files(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	exerciseFiles(t, f)
	if err := f.Put(ctx, "projects/p1/a.txt", strings.NewReader("hi"), 2); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(fs.userPath("u1", "projects", "p1", "a.txt")); err != nil || string(b) != "hi" {
		t.Fatalf("stored %q %v", b, err)
	}
}
