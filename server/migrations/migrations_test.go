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
	assertV1AccountsKept(t, db)
	assertV1ColumnsKept(t, db, before)
	var rule string
	if err := db.QueryRow(`SELECT ptype || ',' || v0 || ',' || v1 FROM casbin_rule`).Scan(&rule); err != nil || rule != "g,u1,admin" {
		t.Fatalf("v1 admin's grouping rule %q (%v)", rule, err)
	}
}

// droppedV1 are the v1 schema elements later migrations removed on purpose (v1 is retired):
// at_users.role became a Casbin grouping rule (00110); 00120 dropped the v1-only table and
// columns (at_teams, conversation history/plan, task output, the project's single repository).
var droppedV1 = []string{"at_users.role ", "at_teams.", "constraint at_teams_", "index CREATE UNIQUE INDEX at_teams_pkey",
	"at_conversations.history ", "at_conversations.plan ", "at_conversations.kind ", "at_tasks.output ",
	"at_projects.repo ", "at_projects.branch ", "at_projects.git_connection_id ", "constraint at_projects_git_connection_id_fkey"}

// assertV1AccountsKept: accounts, sessions, nodes, Git connections, projects and settings
// survive; the v1 chat conversation and everything hanging off it are gone (00120).
func assertV1AccountsKept(t *testing.T, db *sql.DB) {
	t.Helper()
	var email string
	var pinned, requireAcceptance bool
	row := db.QueryRow(`SELECT u.email, p.pinned, p.require_acceptance FROM at_users u JOIN at_projects p ON p.user_id = u.id`)
	if err := row.Scan(&email, &pinned, &requireAcceptance); err != nil {
		t.Fatal(err)
	}
	if email != "a@example.com" || !pinned || !requireAcceptance {
		t.Fatalf("data changed: %s %v %v", email, pinned, requireAcceptance)
	}
	var sessions, nodes, conns, settings, repos int
	_ = db.QueryRow(`SELECT count(*) FROM at_sessions WHERE csrf_secret = ''`).Scan(&sessions)
	_ = db.QueryRow(`SELECT count(*) FROM at_nodes`).Scan(&nodes)
	_ = db.QueryRow(`SELECT count(*) FROM at_git_connections WHERE installation_id = 42`).Scan(&conns)
	_ = db.QueryRow(`SELECT count(*) FROM at_settings`).Scan(&settings)
	_ = db.QueryRow(`SELECT count(*) FROM at_project_repos WHERE repo = 'o/r' AND git_connection_id = 'g1'`).Scan(&repos)
	if sessions != 1 || nodes != 1 || conns != 1 || settings != 1 || repos != 1 {
		t.Fatalf("sessions %d nodes %d conns %d settings %d repos %d", sessions, nodes, conns, settings, repos)
	}
	var convs, msgs, tasks int
	_ = db.QueryRow(`SELECT count(*) FROM at_conversations`).Scan(&convs)
	_ = db.QueryRow(`SELECT count(*) FROM at_messages`).Scan(&msgs)
	_ = db.QueryRow(`SELECT count(*) FROM at_tasks`).Scan(&tasks)
	if convs != 0 || msgs != 0 || tasks != 0 {
		t.Fatalf("v1 chat left: conversations %d messages %d tasks %d", convs, msgs, tasks)
	}
	assertNoTable(t, db, "at_teams")
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
		if !strings.Contains(after, line) && !dropped(line) {
			t.Errorf("v1 schema element changed or removed: %s", line)
		}
	}
}

func dropped(line string) bool {
	for _, c := range droppedV1 {
		if strings.HasPrefix(line, c) {
			return true
		}
	}
	return false
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
	if migrations.Latest() != 230 {
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

// TestPermissionModesMigration: 00096 turns skip_permissions into permission_mode (true →
// bypass, else auto) on roles and on the role copies in at_employees.spec, and back on Down.
func TestPermissionModesMigration(t *testing.T) {
	db := open(t, testdb.URL(t))
	ctx := context.Background()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 95); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(modesSeed); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 96); err != nil {
		t.Fatal(err)
	}
	var modes, specs string
	_ = db.QueryRow(`SELECT string_agg(role || '=' || permission_mode, ',' ORDER BY role) FROM at_roles`).Scan(&modes)
	_ = db.QueryRow(`SELECT string_agg(name || '=' || (spec->>'permission_mode') || ':' || (spec ? 'skip_permissions')::text, ',' ORDER BY name)
		FROM at_employees`).Scan(&specs)
	if modes != "dev=auto,lead=auto,ops=bypass" || specs != "dev-1=auto:false,ops-1=bypass:false" {
		t.Fatalf("roles %s employees %s", modes, specs)
	}
	if _, err := db.Exec(`UPDATE at_roles SET read_only = true WHERE role = 'ops'`); err == nil {
		t.Fatal("a read-only role cannot bypass")
	}
	if _, err := p.DownTo(ctx, 95); err != nil {
		t.Fatal(err)
	}
	var skip bool
	_ = db.QueryRow(`SELECT skip_permissions FROM at_roles WHERE role = 'ops'`).Scan(&skip)
	if !skip {
		t.Fatal("down lost skip_permissions")
	}
}

const modesSeed = `
INSERT INTO at_users (id, email, password_hash, role) VALUES ('u1', 'a@example.com', 'x', 'admin');
INSERT INTO at_projects (id, user_id, name) VALUES ('p1', 'u1', 'A');
INSERT INTO at_roles (id, user_id, tier, role) VALUES ('r1', 'u1', 'lead', 'lead');
INSERT INTO at_roles (id, user_id, tier, role) VALUES ('r2', 'u1', 'member', 'dev');
INSERT INTO at_roles (id, user_id, tier, role, skip_permissions) VALUES ('r3', 'u1', 'member', 'ops', true);
INSERT INTO at_employees (id, project_id, name, tier, role, runtime, spec)
  VALUES ('e1', 'p1', 'dev-1', 'member', 'dev', 'claude', '{"role":"dev","skip_permissions":false}'),
         ('e2', 'p1', 'ops-1', 'member', 'ops', 'claude', '{"role":"ops","skip_permissions":true}');
`

// TestBypassDefaultMigration: 00097 drops auto (auto roles and employee copies become bypass,
// ask stays), lets read-only roles carry bypass, and maps bypass back to auto on Down.
func TestBypassDefaultMigration(t *testing.T) {
	db := open(t, testdb.URL(t))
	ctx := context.Background()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 96); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(bypassSeed); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 97); err != nil {
		t.Fatal(err)
	}
	var modes, specs string
	_ = db.QueryRow(`SELECT string_agg(role || '=' || permission_mode, ',' ORDER BY role) FROM at_roles`).Scan(&modes)
	_ = db.QueryRow(`SELECT string_agg(name || '=' || (spec->>'permission_mode'), ',' ORDER BY name) FROM at_employees`).Scan(&specs)
	if modes != "dev=bypass,lead=bypass,ops=bypass,qa=ask" || specs != "dev-1=bypass,ops-1=bypass,qa-1=ask,rev-1=bypass" {
		t.Fatalf("roles %s employees %s", modes, specs)
	}
	if _, err := db.Exec(`UPDATE at_roles SET read_only = true WHERE role = 'ops'`); err != nil {
		t.Fatalf("a read-only role may carry bypass now: %v", err)
	}
	if _, err := db.Exec(`UPDATE at_roles SET permission_mode = 'auto' WHERE role = 'dev'`); err == nil {
		t.Fatal("auto is no longer a mode")
	}
	var def string
	_ = db.QueryRow(`INSERT INTO at_roles (id, user_id, tier, role) VALUES ('r9', 'u1', 'member', 'new') RETURNING permission_mode`).Scan(&def)
	if def != "bypass" {
		t.Fatalf("default %q", def)
	}
	if _, err := p.DownTo(ctx, 96); err != nil {
		t.Fatal(err)
	}
	_ = db.QueryRow(`SELECT string_agg(role || '=' || permission_mode, ',' ORDER BY role) FROM at_roles`).Scan(&modes)
	_ = db.QueryRow(`SELECT string_agg(name || '=' || (spec->>'permission_mode'), ',' ORDER BY name) FROM at_employees`).Scan(&specs)
	if modes != "dev=auto,lead=auto,new=auto,ops=auto,qa=ask" || specs != "dev-1=auto,ops-1=auto,qa-1=ask,rev-1=auto" {
		t.Fatalf("down: roles %s employees %s", modes, specs)
	}
}

const bypassSeed = `
INSERT INTO at_users (id, email, password_hash, role) VALUES ('u1', 'a@example.com', 'x', 'admin');
INSERT INTO at_projects (id, user_id, name) VALUES ('p1', 'u1', 'A');
INSERT INTO at_roles (id, user_id, tier, role) VALUES ('r1', 'u1', 'lead', 'lead');
INSERT INTO at_roles (id, user_id, tier, role) VALUES ('r2', 'u1', 'member', 'dev');
INSERT INTO at_roles (id, user_id, tier, role, permission_mode) VALUES ('r3', 'u1', 'member', 'ops', 'bypass');
INSERT INTO at_roles (id, user_id, tier, role, permission_mode) VALUES ('r4', 'u1', 'member', 'qa', 'ask');
INSERT INTO at_employees (id, project_id, name, tier, role, runtime, spec)
  VALUES ('e1', 'p1', 'dev-1', 'member', 'dev', 'claude', '{"role":"dev","permission_mode":"auto"}'),
         ('e2', 'p1', 'ops-1', 'member', 'ops', 'claude', '{"role":"ops","permission_mode":"bypass"}'),
         ('e3', 'p1', 'qa-1', 'member', 'qa', 'claude', '{"role":"qa","permission_mode":"ask"}'),
         ('e4', 'p1', 'rev-1', 'member', 'rev', 'claude', '{"role":"rev"}');
`

// TestCasbinRuleMigration: 00110 turns at_users.role into one grouping row per user in
// casbin_rule and drops the column; Down restores the column from the rows.
func TestCasbinRuleMigration(t *testing.T) {
	db := open(t, testdb.URL(t))
	ctx := context.Background()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 97); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO at_users (id, email, password_hash, role) VALUES
		('u1', 'a@example.com', 'x', 'admin'), ('u2', 'b@example.com', 'x', 'user'), ('u3', 'c@example.com', 'x', 'odd')`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 110); err != nil {
		t.Fatal(err)
	}
	var rules string
	_ = db.QueryRow(`SELECT string_agg(ptype || ',' || v0 || ',' || v1 || ',' || v2 || v3 || v4 || v5, ';' ORDER BY v0) FROM casbin_rule`).Scan(&rules)
	if rules != "g,u1,admin,;g,u2,user,;g,u3,user," {
		t.Fatalf("rules %s", rules)
	}
	var hasRole bool
	_ = db.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'at_users' AND column_name = 'role')`).Scan(&hasRole)
	if hasRole {
		t.Fatal("at_users.role still there")
	}
	if _, err := db.Exec(`INSERT INTO casbin_rule (ptype, v0, v1) VALUES ('g', 'u1', 'admin')`); err == nil {
		t.Fatal("duplicate rule accepted")
	}
	if _, err := db.Exec(`UPDATE casbin_rule SET v1 = 'admin' WHERE v0 = 'u3'`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 97); err != nil {
		t.Fatal(err)
	}
	var roles string
	_ = db.QueryRow(`SELECT string_agg(id || '=' || role, ',' ORDER BY id) FROM at_users`).Scan(&roles)
	if roles != "u1=admin,u2=user,u3=admin" {
		t.Fatalf("down: roles %s", roles)
	}
	assertNoTable(t, db, "casbin_rule")
}

func assertNoTable(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	var found bool
	if err := db.QueryRow(`SELECT to_regclass('public.' || $1) IS NOT NULL`, name).Scan(&found); err != nil || found {
		t.Errorf("table %s still there (%v)", name, err)
	}
}

// TestDropV1Migration: 00120 deletes v1 chat conversations with their messages, tasks, mail
// and questions, keeps coordinator and thread conversations, folds at_tasks.output into an
// empty summary and drops the v1-only table and columns. Down is a no-op.
func TestDropV1Migration(t *testing.T) {
	db := open(t, testdb.URL(t))
	ctx := context.Background()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 110); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(dropV1Seed); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 120); err != nil {
		t.Fatal(err)
	}
	var convs, chatRows string
	_ = db.QueryRow(`SELECT string_agg(id || '=' || kind, ',' ORDER BY id) FROM at_conversations`).Scan(&convs)
	_ = db.QueryRow(`SELECT (SELECT count(*) FROM at_messages WHERE conversation_id = 'old1') || ',' ||
		(SELECT count(*) FROM at_tasks WHERE conversation_id = 'old1') || ',' ||
		(SELECT count(*) FROM at_mail WHERE conversation_id = 'old1') || ',' ||
		(SELECT count(*) FROM at_questions WHERE conversation_id = 'old1')`).Scan(&chatRows)
	if convs != "co1=coordinator,th1=thread" || chatRows != "0,0,0,0" {
		t.Fatalf("conversations %s chat rows %s", convs, chatRows)
	}
	var summaries string
	_ = db.QueryRow(`SELECT string_agg(id || '=' || summary, ',' ORDER BY id) FROM at_tasks`).Scan(&summaries)
	if summaries != "t1=old result,t2=kept" {
		t.Fatalf("summaries %s", summaries)
	}
	var msgs int
	_ = db.QueryRow(`SELECT count(*) FROM at_messages`).Scan(&msgs)
	if msgs != 2 {
		t.Fatalf("messages %d", msgs)
	}
	for _, col := range []string{"at_conversations.history", "at_conversations.plan", "at_tasks.output", "at_users.platform_version",
		"at_projects.repo", "at_projects.branch", "at_projects.git_connection_id"} {
		var found bool
		parts := strings.SplitN(col, ".", 2)
		_ = db.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = $1 AND column_name = $2)`,
			parts[0], parts[1]).Scan(&found)
		if found {
			t.Errorf("%s still there", col)
		}
	}
	assertNoTable(t, db, "at_teams")
	if _, err := db.Exec(`INSERT INTO at_conversations (id, user_id, title, project_id) VALUES ('x1', 'u1', 'x', 'p1')`); err == nil {
		t.Fatal("conversation without a kind accepted")
	}
	if _, err := db.Exec(`INSERT INTO at_conversations (id, user_id, title, project_id, kind) VALUES ('x2', 'u1', 'x', 'p1', 'chat')`); err == nil {
		t.Fatal("chat conversation accepted")
	}
	if _, err := p.DownTo(ctx, 110); err != nil {
		t.Fatalf("down is a no-op: %v", err)
	}
}

const dropV1Seed = `
INSERT INTO at_users (id, email, password_hash) VALUES ('u1', 'a@example.com', 'x');
INSERT INTO at_teams (user_id, config) VALUES ('u1', 'leader: lead');
INSERT INTO at_projects (id, user_id, name, repo, branch) VALUES ('p1', 'u1', 'A', 'o/r', 'main');
INSERT INTO at_conversations (id, user_id, title, project_id, history, plan) VALUES ('old1', 'u1', 'old chat', 'p1', '{"lead": "s"}', '[{"title": "x"}]');
INSERT INTO at_messages (conversation_id, role, content) VALUES ('old1', 'user', 'hi'), ('old1', 'leader', 'hello');
INSERT INTO at_tasks (conversation_id, id, assignee, instruction, depends_on, status, output) VALUES ('old1', 't1', 'dev', 'do', '[]', 'done', 'ok');
INSERT INTO at_mail (conversation_id, sender, recipient, body) VALUES ('old1', 'a', 'b', 'note');
INSERT INTO at_questions (conversation_id, id, asker, audience, kind, text, status) VALUES ('old1', 'q1', 'dev', 'user', 'ask', '?', 'open');
INSERT INTO at_conversations (id, user_id, title, project_id, kind) VALUES ('co1', 'u1', '项目统筹', 'p1', 'coordinator'), ('th1', 'u1', 't', 'p1', 'thread');
INSERT INTO at_threads (id, project_id, user_id, no, title, branch, base_branch) VALUES ('th1', 'p1', 'u1', 1, 't', 'thread/1-t', 'main');
INSERT INTO at_messages (conversation_id, role, content) VALUES ('co1', 'user', 'hi'), ('th1', 'user', 'go');
INSERT INTO at_tasks (conversation_id, id, thread_id, assignee, instruction, depends_on, status, output, summary)
  VALUES ('th1', 't1', 'th1', 'dev', 'do', '[]', 'done', 'old result', ''),
         ('th1', 't2', 'th1', 'dev', 'do', '[]', 'done', 'ignored', 'kept');
`

// TestMemoryFilesMigration: 00140 turns at_project_memory into memory files: notes become
// MEMORY.md bullets (pinned first), thread summaries become thread-summaries.md sections linked
// from MEMORY.md; Down puts every file back as one note.
func TestMemoryFilesMigration(t *testing.T) {
	db := open(t, testdb.URL(t))
	ctx := context.Background()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 130); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(memorySeed); err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	rows, err := db.Query(`SELECT project_id || '/' || name, content FROM at_memory_files`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var k, v string
		_ = rows.Scan(&k, &v)
		files[k] = v
	}
	rows.Close()
	want := map[string]string{
		"p1/MEMORY.md":           "# 项目记忆\n\n- 置顶 的一条\n- 用 pnpm\n- [thread-summaries.md](thread-summaries.md) — 已结束讨论串的总结（最近 30 个）",
		"p1/thread-summaries.md": "## #1 登录\n第一个总结\n\n## #2\n第二个总结",
		"p2/MEMORY.md":           "# 项目记忆\n\n- 只有一条",
	}
	if len(files) != len(want) {
		t.Fatalf("files %q", files)
	}
	for k, v := range want {
		if files[k] != v {
			t.Errorf("%s = %q, want %q", k, files[k], v)
		}
	}
	assertNoTable(t, db, "at_project_memory")
	if _, err := p.DownTo(ctx, 130); err != nil {
		t.Fatal(err)
	}
	var n, pinned int
	_ = db.QueryRow(`SELECT count(*), count(*) FILTER (WHERE pinned) FROM at_project_memory`).Scan(&n, &pinned)
	if n != 3 || pinned != 2 {
		t.Fatalf("down notes %d pinned %d", n, pinned)
	}
}

const memorySeed = `
INSERT INTO at_users (id, email, password_hash) VALUES ('u1', 'a@example.com', 'x');
INSERT INTO at_projects (id, user_id, name) VALUES ('p1', 'u1', 'A'), ('p2', 'u1', 'B'), ('p3', 'u1', 'C');
INSERT INTO at_conversations (id, user_id, title, project_id, kind) VALUES ('th1', 'u1', 't', 'p1', 'thread');
INSERT INTO at_threads (id, project_id, user_id, no, title, branch, base_branch) VALUES ('th1', 'p1', 'u1', 1, '登录', 'thread/1-x', 'main');
INSERT INTO at_project_memory (id, project_id, text, source, pinned, created_at) VALUES
  ('m1', 'p1', '用 pnpm', 'user', false, now() - interval '5 minutes'),
  ('m2', 'p1', '第一个总结', '#1', false, now() - interval '4 minutes'),
  ('m3', 'p1', E'置顶\n 的一条', 'coordinator', true, now() - interval '3 minutes'),
  ('m4', 'p1', '第二个总结', 'thread:2', false, now() - interval '2 minutes'),
  ('m5', 'p2', '只有一条', 'coordinator', false, now());
`

// TestThreadMenuMigration: 00160 adds pinned_at / remind_at / reminded_at to existing threads
// (null) and the at_feedback table; Down removes them again.
func TestThreadMenuMigration(t *testing.T) {
	db := open(t, testdb.URL(t))
	ctx := context.Background()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 140); err != nil {
		t.Fatal(err)
	}
	seed := `INSERT INTO at_users (id, email, password_hash) VALUES ('u1', 'a@example.com', 'x');
INSERT INTO at_projects (id, user_id, name) VALUES ('p1', 'u1', 'A');
INSERT INTO at_conversations (id, user_id, title, project_id, kind) VALUES ('th1', 'u1', 't', 'p1', 'thread');
INSERT INTO at_threads (id, project_id, user_id, no, title, branch, base_branch) VALUES ('th1', 'p1', 'u1', 1, 'x', 'thread/1-x', 'main');`
	if _, err := db.Exec(seed); err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	var unset int
	if err := db.QueryRow(`SELECT count(*) FROM at_threads WHERE pinned_at IS NULL AND remind_at IS NULL AND reminded_at IS NULL`).Scan(&unset); err != nil || unset != 1 {
		t.Fatalf("thread columns: %d %v", unset, err)
	}
	if _, err := db.Exec(`INSERT INTO at_feedback (id, user_id, text) VALUES ('f1', 'u1', '好用')`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 140); err != nil {
		t.Fatal(err)
	}
	assertNoTable(t, db, "at_feedback")
	if _, err := db.Exec(`SELECT pinned_at FROM at_threads`); err == nil {
		t.Fatal("pinned_at survived down")
	}
}

// TestModelsCommandsMigration: 00170 adds at_models with the platform Opus 5.5 default, the role
// command columns and at_employees.model with empty defaults on existing rows; Down removes them.
func TestModelsCommandsMigration(t *testing.T) {
	db := open(t, testdb.URL(t))
	ctx := context.Background()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 160); err != nil {
		t.Fatal(err)
	}
	seed := `INSERT INTO at_users (id, email, password_hash) VALUES ('u1', 'a@example.com', 'x');
INSERT INTO at_roles (id, user_id, tier, role) VALUES ('r1', 'u1', 'lead', 'lead');`
	if _, err := db.Exec(seed); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 170); err != nil { // 00210 moves the platform list to the users
		t.Fatal(err)
	}
	var hidden, cmds string
	if err := db.QueryRow(`SELECT hidden_commands::text, commands::text FROM at_roles WHERE id = 'r1'`).Scan(&hidden, &cmds); err != nil || hidden != "{}" || cmds != "[]" {
		t.Fatalf("role columns: %q %q %v", hidden, cmds, err)
	}
	var model string
	if err := db.QueryRow(`SELECT model FROM at_models WHERE user_id IS NULL AND runtime = 'claude' AND is_default`).Scan(&model); err != nil || model != "claude-opus-5-5" {
		t.Fatalf("default model: %q %v", model, err)
	}
	if _, err := db.Exec(`INSERT INTO at_models (id, runtime, model, is_default) VALUES ('m2', 'claude', 'x', true)`); err == nil {
		t.Fatal("second platform default accepted")
	}
	if _, err := p.DownTo(ctx, 160); err != nil {
		t.Fatal(err)
	}
	assertNoTable(t, db, "at_models")
	if _, err := db.Exec(`SELECT hidden_commands FROM at_roles`); err == nil {
		t.Fatal("hidden_commands survived down")
	}
}

// TestDeliverablePublisherMigration: 00180 adds at_deliverable_files.publisher_id (” on existing
// versions); Down removes it.
func TestDeliverablePublisherMigration(t *testing.T) {
	db := open(t, testdb.URL(t))
	ctx := context.Background()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 170); err != nil {
		t.Fatal(err)
	}
	seed := `INSERT INTO at_users (id, email, password_hash) VALUES ('u1', 'a@example.com', 'x');
INSERT INTO at_projects (id, user_id, name) VALUES ('p1', 'u1', 'A');
INSERT INTO at_deliverables (id, project_id, kind, title) VALUES ('dl_1', 'p1', 'design', 'x');
INSERT INTO at_deliverable_files (deliverable_id, version, path, content_type, size, sha256, data, published_by)
VALUES ('dl_1', 1, 'a.md', 'text/markdown', 1, 'x', 'a', 'designer-1');`
	if _, err := db.Exec(seed); err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := db.QueryRow(`SELECT publisher_id FROM at_deliverable_files WHERE deliverable_id = 'dl_1'`).Scan(&id); err != nil || id != "" {
		t.Fatalf("publisher_id: %q %v", id, err)
	}
	if _, err := p.DownTo(ctx, 170); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`SELECT publisher_id FROM at_deliverable_files`); err == nil {
		t.Fatal("publisher_id survived down")
	}
}

// TestPRNotDeliverableMigration: 00190 adds at_thread_repos.pr_opened_at, fills it from the
// created_at of the thread's kind='pr' deliverable (any case, surrounding blanks) with the same URL (null without one), deletes
// every pr deliverable (its comments cascade) and keeps the other deliverables; Down drops the column.
func TestPRNotDeliverableMigration(t *testing.T) {
	db := open(t, testdb.URL(t))
	ctx := context.Background()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 180); err != nil {
		t.Fatal(err)
	}
	seed := `INSERT INTO at_users (id, email, password_hash) VALUES ('u1', 'a@example.com', 'x');
INSERT INTO at_projects (id, user_id, name) VALUES ('p1', 'u1', 'A');
INSERT INTO at_conversations (id, user_id, title, project_id, kind) VALUES ('th1', 'u1', 't', 'p1', 'thread');
INSERT INTO at_threads (id, project_id, user_id, no, title, branch, base_branch) VALUES ('th1', 'p1', 'u1', 1, 'x', 'thread/1-x', 'main');
INSERT INTO at_thread_repos (thread_id, repo, pr_number, pr_url, pr_state) VALUES
  ('th1', 'o/a', 1, 'https://github.com/o/a/pull/1', 'open'),
  ('th1', 'o/b', 2, 'https://github.com/o/b/pull/2', 'merged'),
  ('th1', 'o/c', NULL, NULL, '');
INSERT INTO at_deliverables (id, project_id, thread_id, kind, title, url, created_at) VALUES
  ('dl_pr', 'p1', 'th1', 'pr', 'o/a#1', 'https://github.com/o/a/pull/1', '2026-01-02T03:04:05Z'),
  ('dl_pr_upper', 'p1', 'th1', 'PR', 'o/b#2', 'https://github.com/o/b/pull/2', '2026-01-03T00:00:00Z'),
  ('dl_pr_spaced', 'p1', NULL, ' pr ', 'x', 'https://github.com/o/x/pull/9', now()),
  ('dl_doc', 'p1', 'th1', 'doc', '设计', 'https://example.com/doc', now());
INSERT INTO at_deliverable_comments (id, deliverable_id, author, text) VALUES ('c1', 'dl_pr', 'user', 'x');`
	if _, err := db.Exec(seed); err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	var opened sql.NullTime
	if err := db.QueryRow(`SELECT pr_opened_at FROM at_thread_repos WHERE repo = 'o/a'`).Scan(&opened); err != nil ||
		!opened.Valid || opened.Time.UTC().Format("2006-01-02T15:04:05Z") != "2026-01-02T03:04:05Z" {
		t.Fatalf("o/a pr_opened_at: %v %v", opened, err)
	}
	if err := db.QueryRow(`SELECT pr_opened_at FROM at_thread_repos WHERE repo = 'o/b'`).Scan(&opened); err != nil ||
		!opened.Valid || opened.Time.UTC().Format("2006-01-02") != "2026-01-03" {
		t.Fatalf("o/b pr_opened_at from kind 'PR': %v %v", opened, err)
	}
	var unset int
	if err := db.QueryRow(`SELECT count(*) FROM at_thread_repos WHERE repo = 'o/c' AND pr_opened_at IS NULL`).Scan(&unset); err != nil || unset != 1 {
		t.Fatalf("unmatched rows: %d %v", unset, err)
	}
	var kinds string
	if err := db.QueryRow(`SELECT string_agg(kind, ',' ORDER BY kind) FROM at_deliverables`).Scan(&kinds); err != nil || kinds != "doc" {
		t.Fatalf("deliverables left: %q %v", kinds, err)
	}
	var comments int
	if err := db.QueryRow(`SELECT count(*) FROM at_deliverable_comments`).Scan(&comments); err != nil || comments != 0 {
		t.Fatalf("comments left: %d %v", comments, err)
	}
	if _, err := p.DownTo(ctx, 180); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`SELECT pr_opened_at FROM at_thread_repos`); err == nil {
		t.Fatal("pr_opened_at survived down")
	}
}

// userModelsSeed (at 00200): u1 already has Opus 5.5 (own label) and an own model, and its
// project, role and employee name models missing from every list; u2 has nothing. The platform
// list is Opus 5.5 (Claude default), Sonnet, gpt-p (Codex default).
const userModelsSeed = `INSERT INTO at_users (id, email, password_hash) VALUES ('u1', 'a@example.com', 'x'), ('u2', 'b@example.com', 'x');
INSERT INTO at_models (id, user_id, runtime, model, label, sort, is_default) VALUES
  ('mp2', NULL, 'claude', 'claude-sonnet-p', 'Sonnet', 1, FALSE),
  ('mp3', NULL, 'codex', 'gpt-p', '', 2, TRUE),
  ('mu1', 'u1', 'claude', 'claude-opus-5-5', 'mine', 0, FALSE),
  ('mu2', 'u1', 'claude', 'claude-mine', '', 1, FALSE);
INSERT INTO at_projects (id, user_id, name, coordinator_model, thread_model) VALUES ('p1', 'u1', 'A', 'claude-coord', 'claude-opus-5-5');
INSERT INTO at_roles (id, user_id, tier, role, runtime, model) VALUES ('r1', 'u1', 'member', 'dev', 'codex', 'gpt-role'),
  ('r2', 'u1', 'member', 'qa', 'claude', 'bad model id'), ('r3', 'u1', 'member', 'ops', 'claude', 'us.anthropic.claude-x:0[1m]');
INSERT INTO at_employees (id, project_id, name, tier, role, runtime, model, spec) VALUES
  ('e1', 'p1', 'lead', 'lead', 'lead', 'claude', 'claude-emp', '{"model": "claude-spec"}');`

func userModels(t *testing.T, db *sql.DB, user string) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT string_agg(runtime || ':' || model || ':' || label || CASE WHEN is_default THEN ':*' ELSE '' END, ' ' ORDER BY sort)
		FROM at_models WHERE user_id = $1`, user).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestUserModelsMigration: 00210 copies the platform list to every user (after their own rows,
// skipping models they have), adds the models their projects, roles and employees name, gives each
// user the platform defaults, deletes the platform rows and makes user_id required with one
// default per user and runtime. Down brings back the platform preset.
func TestUserModelsMigration(t *testing.T) {
	db := open(t, testdb.URL(t))
	ctx := context.Background()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 200); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(userModelsSeed); err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	// 'bad model id' (not a valid id) is left out; the id with : [ ] is kept.
	want1 := "claude:claude-opus-5-5:mine:* claude:claude-mine: claude:claude-sonnet-p:Sonnet codex:gpt-p::* " +
		"claude:claude-coord: claude:claude-emp: claude:claude-spec: claude:us.anthropic.claude-x:0[1m]: codex:gpt-role:"
	if got := userModels(t, db, "u1"); got != want1 {
		t.Fatalf("u1:\n got %s\nwant %s", got, want1)
	}
	want2 := "claude:claude-opus-5-5:Opus 5.5:* claude:claude-sonnet-p:Sonnet codex:gpt-p::*"
	if got := userModels(t, db, "u2"); got != want2 {
		t.Fatalf("u2:\n got %s\nwant %s", got, want2)
	}
	var platform int
	if err := db.QueryRow(`SELECT count(*) FROM at_models WHERE user_id IS NULL`).Scan(&platform); err != nil || platform != 0 {
		t.Fatalf("platform rows left: %d %v", platform, err)
	}
	if _, err := db.Exec(`INSERT INTO at_models (id, runtime, model) VALUES ('mx', 'claude', 'x')`); err == nil {
		t.Fatal("row without a user accepted")
	}
	if _, err := db.Exec(`INSERT INTO at_models (id, user_id, runtime, model, is_default) VALUES ('mx', 'u2', 'claude', 'x', TRUE)`); err == nil {
		t.Fatal("second claude default of u2 accepted")
	}
	if _, err := db.Exec(`INSERT INTO at_models (id, user_id, runtime, model) VALUES ('mx', 'u2', 'claude', 'claude-sonnet-p')`); err == nil {
		t.Fatal("duplicate model of u2 accepted")
	}

	if _, err := p.DownTo(ctx, 200); err != nil {
		t.Fatal(err)
	}
	var model string
	if err := db.QueryRow(`SELECT model FROM at_models WHERE user_id IS NULL AND is_default`).Scan(&model); err != nil || model != "claude-opus-5-5" {
		t.Fatalf("platform preset after down: %q %v", model, err)
	}
	var defaults int
	if err := db.QueryRow(`SELECT count(*) FROM at_models WHERE user_id IS NOT NULL AND is_default`).Scan(&defaults); err != nil || defaults != 0 {
		t.Fatalf("user defaults after down: %d %v", defaults, err)
	}
	if _, err := p.UpTo(ctx, 210); err != nil {
		t.Fatalf("up again: %v", err)
	}
}

// TestUserModelsMigrationCheck: when a user did not get every platform row, 00210 fails and
// rolls back: the platform list stays, no user row was written and the version stays at 00200.
func TestUserModelsMigrationCheck(t *testing.T) {
	db := open(t, testdb.URL(t))
	ctx := context.Background()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 200); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(userModelsSeed); err != nil {
		t.Fatal(err)
	}
	// Silently drop u2's copies, as a broken copy step would.
	block := `CREATE FUNCTION at_test_skip_u2() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF NEW.user_id = 'u2' THEN RETURN NULL; END IF; RETURN NEW; END $$;
CREATE TRIGGER at_test_skip_u2 BEFORE INSERT ON at_models FOR EACH ROW EXECUTE FUNCTION at_test_skip_u2();`
	if _, err := db.Exec(block); err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.Up(ctx, db); err == nil || !strings.Contains(err.Error(), "platform models not copied") {
		t.Fatalf("up = %v, want the copy check to fail", err)
	}
	var platform, u1 int
	if err := db.QueryRow(`SELECT count(*) FILTER (WHERE user_id IS NULL), count(*) FILTER (WHERE user_id = 'u1') FROM at_models`).Scan(&platform, &u1); err != nil ||
		platform != 3 || u1 != 2 {
		t.Fatalf("after the failed up: %d platform rows, %d u1 rows %v", platform, u1, err)
	}
	if v, err := p.GetDBVersion(ctx); err != nil || v != 200 {
		t.Fatalf("version %d %v", v, err)
	}
	if _, err := db.Exec(`DROP TRIGGER at_test_skip_u2 ON at_models`); err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
}

// TestThreadCodexModelMigration: 00230 adds at_projects.thread_codex_model (” for existing
// projects) and turns Codex leads into Claude leads following the defaults; other roles keep
// their runtime and model. Down drops the column and leaves the leads on Claude.
func TestThreadCodexModelMigration(t *testing.T) {
	db := open(t, testdb.URL(t))
	ctx := context.Background()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 210); err != nil {
		t.Fatal(err)
	}
	seed := `INSERT INTO at_users (id, email, password_hash) VALUES ('u1', 'a@example.com', 'x');
INSERT INTO at_projects (id, user_id, name, thread_model) VALUES ('p1', 'u1', 'A', 'claude-t');
INSERT INTO at_roles (id, user_id, tier, role, runtime, model) VALUES
  ('r1', 'u1', 'lead', 'lead', 'codex', 'gpt-lead'),
  ('r3', 'u1', 'member', 'dev', 'codex', 'gpt-dev');
INSERT INTO at_users (id, email, password_hash) VALUES ('u2', 'b@example.com', 'x');
INSERT INTO at_roles (id, user_id, tier, role, runtime, model) VALUES ('r2', 'u2', 'lead', 'lead', 'claude', 'claude-lead');`
	if _, err := db.Exec(seed); err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	var codex, thread string
	if err := db.QueryRow(`SELECT thread_codex_model, thread_model FROM at_projects WHERE id = 'p1'`).Scan(&codex, &thread); err != nil ||
		codex != "" || thread != "claude-t" {
		t.Fatalf("project: %q %q %v", codex, thread, err)
	}
	if _, err := db.Exec(`UPDATE at_projects SET thread_codex_model = 'gpt-x' WHERE id = 'p1'`); err != nil {
		t.Fatal(err)
	}
	roles := func() string {
		t.Helper()
		var s string
		if err := db.QueryRow(`SELECT string_agg(id || ':' || runtime || ':' || model, ' ' ORDER BY id) FROM at_roles`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	want := "r1:claude: r2:claude:claude-lead r3:codex:gpt-dev"
	if got := roles(); got != want {
		t.Fatalf("roles:\n got %s\nwant %s", got, want)
	}
	if _, err := p.DownTo(ctx, 210); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`SELECT thread_codex_model FROM at_projects`); err == nil {
		t.Fatal("thread_codex_model survived down")
	}
	if got := roles(); got != want {
		t.Fatalf("roles after down: %s", got)
	}
	if _, err := p.UpTo(ctx, 230); err != nil {
		t.Fatalf("up again: %v", err)
	}
}
