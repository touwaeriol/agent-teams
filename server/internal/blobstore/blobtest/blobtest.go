// Package blobtest runs the real S3 store against an in-process fake (gofakes3) for tests: CI
// cannot add a MinIO service container (the platform token has no workflows permission), and the
// fake covers what the server uses (presigned PUT/GET, multipart, copy, list multipart uploads).
package blobtest

import (
	"net/http/httptest"
	"testing"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/touwaeriol/agent-teams/server/internal/blobstore"
)

// Bucket is the fake's bucket name.
const Bucket = "agent-teams"

// Server starts a fake S3 server with the bucket for the test and returns its URL (for code that
// builds its own store from AGENT_TEAMS_S3_*; any access key works).
func Server(t testing.TB) string {
	t.Helper()
	backend := s3mem.New()
	if err := backend.CreateBucket(Bucket); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(gofakes3.New(backend).Server())
	t.Cleanup(srv.Close)
	return srv.URL
}

// New starts a fake S3 server for the test and returns a store on it.
func New(t testing.TB) *blobstore.S3 {
	t.Helper()
	s, err := blobstore.New(blobstore.Config{Endpoint: Server(t), Bucket: Bucket, AccessKey: "test", SecretKey: "test-secret", PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
