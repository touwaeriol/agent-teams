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
// TEST_S3_SECRET_KEY name one (an admin key): the bucket, files under a prefix, usage, and user keys
// that reach their own prefix only, cannot write when read-only, and stop working once revoked.
func TestRustFS(t *testing.T) {
	endpoint := os.Getenv("TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_S3_ENDPOINT not set")
	}
	ctx := context.Background()
	run := strconv.FormatInt(time.Now().UnixNano()%1e9, 36)
	bucket := "test-fs-" + run
	r := NewRustFS(endpoint, "", os.Getenv("TEST_S3_ACCESS_KEY"), os.Getenv("TEST_S3_SECRET_KEY"), bucket)
	a, b := NamesOf("ua"+run), NamesOf("ub"+run)
	t.Cleanup(func() {
		ctx := context.Background()
		for _, n := range []UserNames{a, b} {
			_ = r.RevokeKey(ctx, n, n.User)
			_ = r.RemovePrefix(ctx, n.Prefix)
		}
		_, _ = r.c.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: &bucket})
	})
	for range 2 {
		if err := r.Prepare(ctx); err != nil {
			t.Fatalf("prepare: %v", err)
		}
	}
	lc, err := r.c.GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{Bucket: &bucket})
	if err != nil || len(lc.Rules) != 1 || lc.Rules[0].AbortIncompleteMultipartUpload == nil {
		t.Fatalf("lifecycle %+v %v", lc, err)
	}
	exerciseFiles(t, r.Files(a.Prefix))
	// A body that cannot be rewound (an attachment's reader, an object-store download) goes as an
	// unsigned payload.
	if err := r.Files(a.Prefix).Put(ctx, "projects/p1/stream.txt", io.NopCloser(strings.NewReader("streamed")), 8); err != nil {
		t.Fatalf("non-seekable put: %v", err)
	}
	if err := r.Files(b.Prefix).Put(ctx, "shared/b.txt", strings.NewReader("b"), 1); err != nil {
		t.Fatal(err)
	}
	if u, err := r.Usage(ctx, a.Prefix); err != nil || u.UsedBytes < 8 || u.Objects < 2 {
		t.Fatalf("usage %+v %v", u, err)
	}
	if ps, err := r.Prefixes(ctx); err != nil || !slices.Contains(ps, a.Prefix) || !slices.Contains(ps, b.Prefix) {
		t.Fatalf("prefixes %v %v", ps, err)
	}
	if _, err := r.IssueKey(ctx, a, true); err != nil {
		t.Fatal(err)
	}
	key, err := r.IssueKey(ctx, a, true)
	if err != nil || key.ID != a.User {
		t.Fatalf("issue again: %+v %v", key, err)
	}
	own := NewS3Files(endpoint, "", key.ID, key.Secret, bucket, a.Prefix)
	if err := own.Put(ctx, "shared/x.txt", strings.NewReader("x"), 1); err != nil {
		t.Fatalf("user writes its folder: %v", err)
	}
	if _, err := own.List(ctx, "shared"); err != nil {
		t.Fatalf("user lists its folder: %v", err)
	}
	other := NewS3Files(endpoint, "", key.ID, key.Secret, bucket, b.Prefix)
	if err := other.Put(ctx, "x.txt", strings.NewReader("x"), 1); err == nil {
		t.Fatal("user wrote another user's folder")
	}
	if _, err := other.List(ctx, "shared"); err == nil {
		t.Fatal("user listed another user's folder")
	}
	if _, _, err := other.Open(ctx, "shared/b.txt"); err == nil {
		t.Fatal("user read another user's file")
	}
	if _, err := NewS3Files(endpoint, "", key.ID, key.Secret, bucket, "").List(ctx, ""); err == nil {
		t.Fatal("user listed the bucket root")
	}
	ro, err := r.IssueKey(ctx, a, false)
	if err != nil {
		t.Fatal(err)
	}
	locked := NewS3Files(endpoint, "", ro.ID, ro.Secret, bucket, a.Prefix)
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
	if err := r.RemovePrefix(ctx, a.Prefix); err != nil {
		t.Fatal(err)
	}
	if u, err := r.Usage(ctx, a.Prefix); err != nil || u.Objects != 0 {
		t.Fatalf("after remove %+v %v", u, err)
	}
	if u, err := r.Usage(ctx, b.Prefix); err != nil || u.Objects != 1 {
		t.Fatalf("other folder touched %+v %v", u, err)
	}
	if err := r.RemovePrefix(ctx, ""); err == nil {
		t.Fatal("emptied the whole bucket")
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
