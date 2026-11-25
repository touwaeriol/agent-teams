package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/blobstore/blobtest"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/testdb"
)

// TestStorageCommand runs atctl storage end to end against PostgreSQL and the fake S3: migrate,
// verify, purge-data, restore; and the refusals (no S3 settings, unknown step).
func TestStorageCommand(t *testing.T) {
	ctx := context.Background()
	url := testdb.Migrated(t)
	s, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	u, _ := s.CreateUser(ctx, "cmd@example.com", "h")
	p, err := s.CreateProject(ctx, u.ID, store.NewProject{Name: "c", WorkMode: "worktree"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Exec(ctx, s.Pool, `INSERT INTO at_attachments (id, project_id, user_id, name, content_type, size, sha256, data)
		VALUES ('img_1', $1, $2, 'a.png', 'image/png', 2, '', '\x0102')`, p.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"DATABASE_URL": url, "AGENT_TEAMS_S3_ENDPOINT": blobtest.Server(t), "AGENT_TEAMS_S3_BUCKET": blobtest.Bucket,
		"AGENT_TEAMS_S3_ACCESS_KEY": "k", "AGENT_TEAMS_S3_SECRET_KEY": "s"}
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runStorage(ctx, args, func(k string) string { return env[k] }, &out)
		return out.String(), err
	}
	for _, step := range []struct{ args, want string }{
		{"migrate --dry-run", "migrate attachments: done 1, skipped 0, failed 0 (dry run)"},
		{"migrate", "migrate attachments: done 1"},
		{"verify", "verify attachments: done 1"},
		{"purge-data --table attachments", "purge-data attachments: done 1"},
		{"restore", "restore attachments: done 1"},
	} {
		out, err := run(strings.Fields(step.args)...)
		if err != nil || !strings.Contains(out, step.want) {
			t.Fatalf("%s: %v\n%s", step.args, err, out)
		}
	}
	if _, err := run("bogus"); err == nil || !strings.Contains(err.Error(), "unknown storage step") {
		t.Fatalf("unknown step: %v", err)
	}
	delete(env, "AGENT_TEAMS_S3_SECRET_KEY")
	if _, err := run("migrate"); err == nil {
		t.Fatal("partial S3 settings accepted")
	}
}
