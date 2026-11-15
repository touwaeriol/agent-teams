package migrations_test

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"

	"github.com/touwaeriol/agent-teams/server/internal/testdb"
	"github.com/touwaeriol/agent-teams/server/migrations"
)

func open(t *testing.T, url string) *sql.DB {
	t.Helper()
	db, err := migrations.Open(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestUpOnEmptyDatabase(t *testing.T) {
	db := open(t, testdb.URL(t))
	ctx := context.Background()
	applied, err := migrations.Up(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) == 0 || applied[len(applied)-1] != migrations.Latest() {
		t.Fatalf("applied %v latest %d", applied, migrations.Latest())
	}
	again, err := migrations.Up(ctx, db)
	if err != nil || len(again) != 0 {
		t.Fatalf("second up: %v %v", again, err)
	}
	assertTables(t, db, "at_users", "at_roles", "at_threads", "at_events_"+monthSuffix(t, db), "at_office_layouts", "at_audit_log",
		"at_schedules", "at_rate_limit_waits")
}

// v1Seed is a little production-like data written with the v1 schema.
const v1Seed = `
INSERT INTO public.at_users (id, email, password_hash, role) VALUES
  ('u1', 'a@example.com', 'scrypt$16384$8$1$PxUEquqEbArdkCvV9hG8TA$VMWl4PC4Nq4DTX5a7C4Pa1GLyuvATGtrCtibICk7EfgH8kbk-O41sEOTFU0Qob-1C24pivfi8yZLPi0Qjl0HTw', 'admin');
INSERT INTO public.at_sessions (token_hash, user_id, expires_at) VALUES ('c8963414bf6c4c869eeac5f8a057c3dc574d422f1b108397b66f67bab3d2f981', 'u1', now() + interval '1 day');
INSERT INTO public.at_nodes (id, user_id, name, token_hash) VALUES ('n1', 'u1', 'rc-erio', 'abc');
INSERT INTO public.at_git_connections (id, user_id, name, login, token_secret, kind, installation_id) VALUES ('g1', 'u1', 'GitHub App · me', 'me', '', 'app', 42);
INSERT INTO public.at_projects (id, user_id, name, repo, branch, git_connection_id, pinned) VALUES ('p1', 'u1', 'Proj', 'o/r', 'main', 'g1', true);
INSERT INTO public.at_conversations (id, user_id, title, project_id) VALUES ('c00000000001', 'u1', 'old chat', 'p1');
INSERT INTO public.at_messages (conversation_id, role, content) VALUES ('c00000000001', 'user', 'hi');
INSERT INTO public.at_tasks (conversation_id, id, assignee, instruction, depends_on, status, output) VALUES ('c00000000001', 't1', 'dev', 'do', '[]', 'done', 'ok');
INSERT INTO public.at_teams (user_id, config) VALUES ('u1', 'leader: lead');
INSERT INTO public.at_settings (key, value) VALUES ('platform', '{"registration_open": false, "default_max_nodes": 3}');
`

func TestUpOnProductionV1Schema(t *testing.T) {
	url := testdb.URL(t)
	schema, err := os.ReadFile("testdata/v1_schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	testdb.Exec(t, url, string(schema))
	testdb.Exec(t, url, v1Seed)
	db := open(t, url)
	ctx := context.Background()
	before := snapshot(t, db)

	// P0 exit criterion: the baseline changes nothing on the production schema.
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(t, db); after != before {
		t.Fatalf("baseline changed the v1 schema:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if _, err := migrations.Up(ctx, db); err != nil {
		t.Fatalf("migrate up on v1 schema: %v", err)
	}
	assertV1DataIntact(t, db)
	assertV1ColumnsKept(t, db, before)
}

func assertV1DataIntact(t *testing.T, db *sql.DB) {
	t.Helper()
	var email, kind string
	var pv int
	var pinned, requireAcceptance bool
	row := db.QueryRow(`SELECT u.email, u.platform_version, p.pinned, p.require_acceptance, c.kind
		FROM at_users u JOIN at_projects p ON p.user_id = u.id JOIN at_conversations c ON c.project_id = p.id`)
	if err := row.Scan(&email, &pv, &pinned, &requireAcceptance, &kind); err != nil {
		t.Fatal(err)
	}
	if email != "a@example.com" || pv != 1 || !pinned || !requireAcceptance || kind != "chat" {
		t.Fatalf("data changed: %s %d %v %v %s", email, pv, pinned, requireAcceptance, kind)
	}
	var tasks, sessions int
	_ = db.QueryRow(`SELECT count(*) FROM at_tasks WHERE thread_id IS NULL AND kind = 'work'`).Scan(&tasks)
	_ = db.QueryRow(`SELECT count(*) FROM at_sessions WHERE csrf_secret = ''`).Scan(&sessions)
	if tasks != 1 || sessions != 1 {
		t.Fatalf("tasks %d sessions %d", tasks, sessions)
	}
}

// snapshot lists every column of the public schema with its type, default and nullability,
// plus every index and constraint.
func snapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query(`
		SELECT table_name || '.' || column_name || ' ' || data_type || ' ' || is_nullable || ' ' || COALESCE(column_default, '')
		FROM information_schema.columns WHERE table_schema = 'public'
		UNION ALL SELECT 'index ' || indexdef FROM pg_indexes WHERE schemaname = 'public'
		UNION ALL SELECT 'constraint ' || conname || ' ' || pg_get_constraintdef(oid) FROM pg_constraint
		  WHERE connamespace = 'public'::regnamespace
		ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(l, "goose_db_version") {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}

func assertV1ColumnsKept(t *testing.T, db *sql.DB, before string) {
	t.Helper()
	after := snapshot(t, db)
	for _, line := range strings.Split(before, "\n") {
		if !strings.Contains(after, line) {
			t.Errorf("v1 schema element changed or removed: %s", line)
		}
	}
}

func assertTables(t *testing.T, db *sql.DB, names ...string) {
	t.Helper()
	for _, n := range names {
		var found bool
		if err := db.QueryRow(`SELECT to_regclass('public.' || $1) IS NOT NULL`, n).Scan(&found); err != nil || !found {
			t.Errorf("table %s missing (%v)", n, err)
		}
	}
}

func monthSuffix(t *testing.T, db *sql.DB) string {
	var s string
	if err := db.QueryRow(`SELECT to_char(now(), 'YYYY_MM')`).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestLatest(t *testing.T) {
	if migrations.Latest() != 90 {
		t.Fatalf("latest %d", migrations.Latest())
	}
}

// TestProjectReposMigration: 00070 moves each project's repository into at_project_repos, writes a
// non-default base branch into the context, and gives existing threads and queue entries their repository.
func TestProjectReposMigration(t *testing.T) {
	db := open(t, testdb.URL(t))
	ctx := context.Background()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 50); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(reposSeed); err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	var repos, conn, ctxDev, ctxMain string
	if err := db.QueryRow(`SELECT string_agg(r.repo || '@' || r.position, ',' ORDER BY r.project_id), max(r.git_connection_id)
		FROM at_project_repos r`).Scan(&repos, &conn); err != nil {
		t.Fatal(err)
	}
	if repos != "o/api@0,o/web@0" || conn != "g1" {
		t.Fatalf("project repos %s conn %s", repos, conn)
	}
	_ = db.QueryRow(`SELECT context FROM at_projects WHERE id = 'p1'`).Scan(&ctxDev)
	_ = db.QueryRow(`SELECT context FROM at_projects WHERE id = 'p2'`).Scan(&ctxMain)
	if ctxDev != "约定\n\n仓库 o/api 的基准分支：develop" || ctxMain != "" {
		t.Fatalf("contexts %q %q", ctxDev, ctxMain)
	}
	var threadRepo, base, queueRepo string
	var created bool
	if err := db.QueryRow(`SELECT repo, base_branch, branch_created FROM at_thread_repos WHERE thread_id = 'th1'`).Scan(&threadRepo, &base, &created); err != nil {
		t.Fatal(err)
	}
	_ = db.QueryRow(`SELECT repo FROM at_merge_queue WHERE id = 'mq1'`).Scan(&queueRepo)
	if threadRepo != "o/api" || base != "develop" || !created || queueRepo != "o/api" {
		t.Fatalf("thread repo %s %s %v queue %s", threadRepo, base, created, queueRepo)
	}
}

const reposSeed = `
INSERT INTO at_users (id, email, password_hash, role) VALUES ('u1', 'a@example.com', 'x', 'admin');
INSERT INTO at_git_connections (id, user_id, name, login, token_secret) VALUES ('g1', 'u1', 'gh', 'me', 's');
INSERT INTO at_projects (id, user_id, name, context, repo, branch, git_connection_id)
  VALUES ('p1', 'u1', 'A', '约定', 'https://github.com/o/api.git', 'develop', 'g1'),
         ('p2', 'u1', 'B', '', 'o/web', 'main', 'g1'),
         ('p3', 'u1', 'C', '', NULL, '', NULL);
INSERT INTO at_conversations (id, user_id, title, project_id, kind) VALUES ('th1', 'u1', 't', 'p1', 'thread');
INSERT INTO at_threads (id, project_id, user_id, no, title, branch, base_branch, started_at)
  VALUES ('th1', 'p1', 'u1', 1, 't', 'thread/1-t', 'develop', now());
INSERT INTO at_merge_queue (id, thread_id, employee, head_branch) VALUES ('mq1', 'th1', 'dev-1', 'thread/1/dev-1');
`
