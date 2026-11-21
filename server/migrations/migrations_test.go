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
	var pinned bool
	var policy string
	row := db.QueryRow(`SELECT u.email, p.pinned, p.merge_policy FROM at_users u JOIN at_projects p ON p.user_id = u.id`)
	if err := row.Scan(&email, &pinned, &policy); err != nil {
		t.Fatal(err)
	}
	if email != "a@example.com" || !pinned || policy != "pr" {
		t.Fatalf("data changed: %s %v %v", email, pinned, policy)
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
	if migrations.Latest() != 360 {
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
	if _, err := p.UpTo(ctx, 210); err != nil { // 00320 drops at_models
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

// TestUnlimitedSessionsMigration: 00240 clears the stored running-thread and node-session limits
// (platform setting to 0, user and node overrides to NULL) and leaves other platform settings alone.
func TestUnlimitedSessionsMigration(t *testing.T) {
	db := open(t, testdb.URL(t))
	ctx := context.Background()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 230); err != nil {
		t.Fatal(err)
	}
	seed := `INSERT INTO at_users (id, email, password_hash, max_running_threads) VALUES ('u1', 'a@example.com', 'x', 2);
INSERT INTO at_nodes (id, user_id, name, token_hash, max_active_sessions) VALUES ('n1', 'u1', 'box', 'h', 4);
INSERT INTO at_settings (key, value) VALUES ('platform', '{"registration_open": false, "default_max_nodes": 5, "max_running_threads": 3}');`
	if _, err := db.Exec(seed); err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	var platform string
	if err := db.QueryRow(`SELECT value::text FROM at_settings WHERE key = 'platform'`).Scan(&platform); err != nil ||
		platform != `{"default_max_nodes": 5, "registration_open": false, "max_running_threads": 0}` {
		t.Fatalf("platform %s %v", platform, err)
	}
	var user, node sql.NullInt64
	if err := db.QueryRow(`SELECT (SELECT max_running_threads FROM at_users WHERE id = 'u1'), (SELECT max_active_sessions FROM at_nodes WHERE id = 'n1')`).Scan(&user, &node); err != nil || user.Valid || node.Valid {
		t.Fatalf("user %v node %v %v", user, node, err)
	}
}

// TestThreadSourceMessageMigration: 00220 adds source_message_id (NULL on existing threads, set
// to NULL when the message is deleted) and forwarded_brief, backfilled for threads whose brief is
// forwarded user messages; Down removes both.
func TestThreadSourceMessageMigration(t *testing.T) {
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
INSERT INTO at_projects (id, user_id, name) VALUES ('p1', 'u1', 'A');
INSERT INTO at_conversations (id, user_id, title, project_id, kind) VALUES ('th1', 'u1', 't', 'p1', 'thread'), ('th2', 'u1', 't', 'p1', 'thread'),
  ('th3', 'u1', 't', 'p1', 'thread');
INSERT INTO at_threads (id, project_id, user_id, no, title, branch, brief) VALUES
  ('th1', 'p1', 'u1', 1, 'x', 'thread/1-x', '修登录页'),
  ('th2', 'p1', 'u1', 2, 'y', 'thread/2-y', '[用户把用户在项目对话里说的 1 条消息原样转发给你。下面是用户本人的原话]' || chr(10) || '加暗色'),
  ('th3', 'p1', 'u1', 3, 'z', 'thread/3-z', '[项目统筹把用户在项目对话里说的 2 条消息原样转发给你。下面是用户本人的原话]');`
	if _, err := db.Exec(seed); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 220); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRow(`SELECT string_agg(id || ':' || forwarded_brief || ':' || (source_message_id IS NULL), ' ' ORDER BY id) FROM at_threads`).Scan(&got); err != nil ||
		got != "th1:false:true th2:true:true th3:true:true" {
		t.Fatalf("threads after 00220: %q %v", got, err)
	}
	var msg int64
	if err := db.QueryRow(`INSERT INTO at_messages (conversation_id, role, content) VALUES ('th1', 'user', 'hi') RETURNING id`).Scan(&msg); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE at_threads SET source_message_id = $1 WHERE id = 'th1'`, msg); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM at_messages WHERE id = $1`, msg); err != nil {
		t.Fatal(err)
	}
	var left *int64
	if err := db.QueryRow(`SELECT source_message_id FROM at_threads WHERE id = 'th1'`).Scan(&left); err != nil || left != nil {
		t.Fatalf("source after delete: %v %v", left, err)
	}
	if _, err := p.DownTo(ctx, 210); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`SELECT source_message_id FROM at_threads`); err == nil {
		t.Fatal("source_message_id survived down")
	}
	if _, err := db.Exec(`SELECT forwarded_brief FROM at_threads`); err == nil {
		t.Fatal("forwarded_brief survived down")
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
	if _, err := p.UpTo(ctx, 230); err != nil {
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

// TestMergePolicyMigration: 00250 turns require_acceptance into merge_policy — acceptance required
// (the old default) becomes pr, automatic merging stays auto — and Down maps back (manual and pr
// required acceptance, auto did not).
func TestMergePolicyMigration(t *testing.T) {
	db := open(t, testdb.URL(t))
	ctx := context.Background()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 240); err != nil {
		t.Fatal(err)
	}
	seed := `INSERT INTO at_users (id, email, password_hash) VALUES ('u1', 'a@example.com', 'x');
INSERT INTO at_projects (id, user_id, name, require_acceptance) VALUES ('p1', 'u1', 'A', TRUE), ('p2', 'u1', 'B', FALSE);`
	if _, err := db.Exec(seed); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 250); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRow(`SELECT string_agg(id || ':' || merge_policy, ' ' ORDER BY id) FROM at_projects`).Scan(&got); err != nil || got != "p1:pr p2:auto" {
		t.Fatalf("after 00250: %q %v", got, err)
	}
	if _, err := db.Exec(`INSERT INTO at_projects (id, user_id, name, merge_policy) VALUES ('p3', 'u1', 'C', 'manual')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE at_projects SET merge_policy = 'bogus' WHERE id = 'p1'`); err == nil {
		t.Fatal("merge_policy accepted an unknown value")
	}
	if _, err := p.DownTo(ctx, 240); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT string_agg(id || ':' || require_acceptance, ' ' ORDER BY id) FROM at_projects`).Scan(&got); err != nil || got != "p1:true p2:false p3:true" {
		t.Fatalf("after down: %q %v", got, err)
	}
}

// TestTaskHandoffMigration: 00260 adds the pre-task sync, hand-off and merged-range columns
// to at_tasks and base_sha to at_merge_queue, with empty defaults; Down drops them.
func TestTaskHandoffMigration(t *testing.T) {
	db := open(t, testdb.URL(t))
	ctx := context.Background()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 260); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRow(`SELECT string_agg(column_name || '=' || coalesce(column_default, ''), ' ' ORDER BY column_name)
		FROM information_schema.columns WHERE (table_name = 'at_tasks' AND column_name IN ('sync','sync_pending','sync_round','handoff','handoff_asked','merged'))
		OR (table_name = 'at_merge_queue' AND column_name = 'base_sha')`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	want := "base_sha=''::text handoff=''::text handoff_asked=false merged='{}'::jsonb sync='{}'::jsonb sync_pending=0 sync_round=''::text"
	if got != want {
		t.Fatalf("columns:\n got %s\nwant %s", got, want)
	}
	if _, err := p.DownTo(ctx, 250); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`SELECT handoff FROM at_tasks`); err == nil {
		t.Fatal("handoff survived down")
	}
}

// TestSharedMembersMigration: 00270 adds the shared-member columns, backfills a session's thread
// from its employee (coordinator sessions stay NULL) and ready_at of the pending tasks whose
// dependencies are done, and current_thread_id / session thread_id go NULL when the thread is
// deleted. Down drops them.
func TestSharedMembersMigration(t *testing.T) {
	db := open(t, testdb.URL(t))
	ctx := context.Background()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 260); err != nil {
		t.Fatal(err)
	}
	seed := `INSERT INTO at_users (id, email, password_hash) VALUES ('u1', 'a@example.com', 'x');
INSERT INTO at_projects (id, user_id, name) VALUES ('p1', 'u1', 'A');
INSERT INTO at_conversations (id, user_id, title, project_id, kind) VALUES ('th1', 'u1', 't', 'p1', 'thread');
INSERT INTO at_threads (id, project_id, user_id, no, title, branch) VALUES ('th1', 'p1', 'u1', 1, 'x', 'thread/1-x');
INSERT INTO at_employees (id, project_id, thread_id, name, tier, role, runtime) VALUES
  ('e1', 'p1', 'th1', 'dev-1', 'member', 'dev', 'claude'), ('e2', 'p1', NULL, 'coordinator', 'coordinator', 'coordinator', 'claude');
INSERT INTO at_agent_sessions (id, employee_id, runtime, cwd, state) VALUES ('s1', 'e1', 'claude', '/w', 'closed'), ('s2', 'e2', 'claude', '/w', 'closed');
INSERT INTO at_tasks (conversation_id, id, thread_id, assignee, instruction, depends_on, status) VALUES
  ('th1', 't1', 'th1', 'dev', 'free', '[]', 'pending'), ('th1', 't2', 'th1', 'dev', 'waits', '["t3"]', 'pending'),
  ('th1', 't3', 'th1', 'dev', 'runs', '[]', 'running'), ('th1', 't4', 'th1', 'dev', 'dep done', '["t5"]', 'pending'),
  ('th1', 't5', 'th1', 'dev', 'done', '[]', 'done');`
	if _, err := db.Exec(seed); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 270); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRow(`SELECT string_agg(id || ':' || (ready_at IS NOT NULL)::text, ' ' ORDER BY id) FROM at_tasks`).Scan(&got); err != nil ||
		got != "t1:true t2:false t3:false t4:true t5:false" {
		t.Fatalf("ready_at after 00270: %q %v", got, err)
	}
	if err := db.QueryRow(`SELECT count(*)::text FROM at_agent_sessions WHERE NOT in_turn`).Scan(&got); err != nil || got != "2" {
		t.Fatalf("in_turn after 00270: %q %v", got, err)
	}
	if err := db.QueryRow(`SELECT string_agg(id || ':' || COALESCE(thread_id, '-'), ' ' ORDER BY id) FROM at_agent_sessions`).Scan(&got); err != nil ||
		got != "s1:th1 s2:-" {
		t.Fatalf("sessions after 00270: %q %v", got, err)
	}
	if err := db.QueryRow(`SELECT string_agg(column_name || '=' || coalesce(column_default, '') || ':' || is_nullable, ' ' ORDER BY column_name)
		FROM information_schema.columns WHERE table_name = 'at_tasks' AND column_name IN ('reserved_for','reserved_until','ready_at')`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if want := "ready_at=:YES reserved_for=''::text:NO reserved_until=:YES"; got != want {
		t.Fatalf("task columns:\n got %s\nwant %s", got, want)
	}
	if _, err := db.Exec(`UPDATE at_employees SET current_thread_id = 'th1' WHERE id = 'e2';
UPDATE at_threads SET only_roles = '["dev"]' WHERE id = 'th1';
DELETE FROM at_tasks WHERE thread_id = 'th1';
DELETE FROM at_employees WHERE id = 'e1';
DELETE FROM at_threads WHERE id = 'th1'`); err != nil {
		t.Fatal(err)
	}
	var cur *string
	if err := db.QueryRow(`SELECT current_thread_id FROM at_employees WHERE id = 'e2'`).Scan(&cur); err != nil || cur != nil {
		t.Fatalf("current_thread_id after delete: %v %v", cur, err)
	}
	if _, err := p.DownTo(ctx, 260); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`SELECT current_thread_id FROM at_employees`, `SELECT thread_id FROM at_agent_sessions`,
		`SELECT reserved_for FROM at_tasks`, `SELECT only_roles FROM at_threads`, `SELECT in_turn FROM at_agent_sessions`} {
		if _, err := db.Exec(q); err == nil {
			t.Fatalf("%s survived down", q)
		}
	}
}

// TestRoleContextMigration: 00280 adds the session-management columns to at_roles with the defaults
// of each kind of role (dev 60/80/8 + focus, gates —/80/1, lead and coordinator —/70/—) and copies
// them into the employees' role snapshots. Down drops them again.
func TestRoleContextMigration(t *testing.T) {
	db := open(t, testdb.URL(t))
	ctx := context.Background()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 270); err != nil {
		t.Fatal(err)
	}
	seed := `INSERT INTO at_users (id, email, password_hash) VALUES ('u1', 'a@example.com', 'x');
INSERT INTO at_projects (id, user_id, name) VALUES ('p1', 'u1', 'A');
INSERT INTO at_roles (id, user_id, tier, role, runtime, gate) VALUES ('r1', 'u1', 'coordinator', 'coordinator', 'claude', ''),
  ('r2', 'u1', 'lead', 'lead', 'claude', ''), ('r3', 'u1', 'member', 'dev', 'claude', ''), ('r4', 'u1', 'member', 'review', 'claude', 'review');
INSERT INTO at_employees (id, project_id, name, tier, role, role_id, runtime, spec) VALUES
  ('e1', 'p1', 'dev-1', 'member', 'dev', 'r3', 'claude', '{"role": "dev"}');`
	if _, err := db.Exec(seed); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 280); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRow(`SELECT string_agg(role || ':' || COALESCE(context_limit::text, '-') || '/' || COALESCE(compact_between_pct::text, '-')
		|| '/' || COALESCE(compact_in_task_pct::text, '-') || '/' || COALESCE(clear_after_tasks::text, '-') || '/' || (compact_focus <> '')::text,
		' ' ORDER BY id) FROM at_roles`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if want := "coordinator:-/-/70/-/false lead:-/-/70/-/false dev:-/60/80/8/true review:-/-/80/1/false"; got != want {
		t.Fatalf("roles after 00280:\n got %s\nwant %s", got, want)
	}
	if err := db.QueryRow(`SELECT spec->>'role' || ' ' || (spec->>'compact_between_pct') || ' ' || (spec->>'clear_after_tasks') || ' ' ||
		COALESCE(spec->>'context_limit', 'null') FROM at_employees WHERE id = 'e1'`).Scan(&got); err != nil || got != "dev 60 8 null" {
		t.Fatalf("spec after 00280: %q %v", got, err)
	}
	if _, err := p.DownTo(ctx, 270); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`SELECT context_limit FROM at_roles`); err == nil {
		t.Fatal("context_limit survived down")
	}
	if err := db.QueryRow(`SELECT spec::text FROM at_employees WHERE id = 'e1'`).Scan(&got); err != nil || got != `{"role": "dev"}` {
		t.Fatalf("spec after down: %q %v", got, err)
	}
}

// TestSessionContextMigration: 00290 adds the session counters and the task's compaction wait,
// progress and metrics columns with their defaults; Down drops them.
func TestSessionContextMigration(t *testing.T) {
	db := open(t, testdb.URL(t))
	ctx := context.Background()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 290); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRow(`SELECT string_agg(table_name || '.' || column_name || '=' || coalesce(column_default, '') || ':' || is_nullable, ' '
		ORDER BY table_name, column_name) FROM information_schema.columns WHERE (table_name = 'at_agent_sessions'
		AND column_name IN ('context_used','context_max','tasks_done')) OR (table_name = 'at_tasks' AND column_name IN
		('compact_id','wait_since','progress','compactions','ctx_start','ctx_end','split_hinted'))`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	want := "at_agent_sessions.context_max=:YES at_agent_sessions.context_used=:YES at_agent_sessions.tasks_done=0:NO " +
		"at_tasks.compact_id=''::text:NO at_tasks.compactions=0:NO at_tasks.ctx_end=:YES at_tasks.ctx_start=:YES " +
		"at_tasks.progress=''::text:NO at_tasks.split_hinted=false:NO at_tasks.wait_since=:YES"
	if got != want {
		t.Fatalf("columns:\n got %s\nwant %s", got, want)
	}
	if _, err := p.DownTo(ctx, 280); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`SELECT tasks_done FROM at_agent_sessions`, `SELECT compact_id FROM at_tasks`} {
		if _, err := db.Exec(q); err == nil {
			t.Fatalf("%s survived down", q)
		}
	}
}

// TestTaskDownstreamMigration: 00300 counts each task's distinct unfinished transitive
// dependents (a diamond counts once, a cancelled dependent not at all), stamps ready_at on
// pending tasks whose dependencies are done, and Down drops the column again.
func TestTaskDownstreamMigration(t *testing.T) {
	db := open(t, testdb.URL(t))
	ctx := context.Background()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 290); err != nil {
		t.Fatal(err)
	}
	seed := `INSERT INTO at_users (id, email, password_hash) VALUES ('u1', 'a@example.com', 'x');
INSERT INTO at_projects (id, user_id, name) VALUES ('p1', 'u1', 'A');
INSERT INTO at_conversations (id, user_id, title, project_id, kind) VALUES ('th1', 'u1', 't', 'p1', 'thread');
INSERT INTO at_threads (id, project_id, user_id, no, title, branch) VALUES ('th1', 'p1', 'u1', 1, 'x', 'thread/1-x');
INSERT INTO at_tasks (conversation_id, id, thread_id, assignee, instruction, depends_on, status) VALUES
  ('th1', 't1', 'th1', 'dev', 'root', '[]', 'pending'), ('th1', 't2', 'th1', 'dev', 'left', '["t1"]', 'pending'),
  ('th1', 't3', 'th1', 'dev', 'right', '["t1"]', 'pending'), ('th1', 't4', 'th1', 'dev', 'join', '["t2","t3"]', 'pending'),
  ('th1', 't5', 'th1', 'dev', 'done', '[]', 'done'), ('th1', 't6', 'th1', 'dev', 'gone', '["t2"]', 'cancelled'),
  ('th1', 't7', 'th1', 'dev', 'after done', '["t5"]', 'pending');`
	if _, err := db.Exec(seed); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 300); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRow(`SELECT string_agg(id || ':' || downstream || ':' || (ready_at IS NOT NULL)::text, ' ' ORDER BY id)
		FROM at_tasks`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if want := "t1:3:true t2:1:false t3:1:false t4:0:false t5:1:false t6:0:false t7:0:true"; got != want {
		t.Fatalf("after 00300:\n got %s\nwant %s", got, want)
	}
	if _, err := p.DownTo(ctx, 290); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`SELECT downstream FROM at_tasks`); err == nil {
		t.Fatal("downstream survived down")
	}
}

// TestFixedLeadModelsMigration: 00310 clears every model the coordinator and leads could carry
// (role templates and project copies, the project coordinator model, the /model override and the
// role snapshot of their employees); members keep theirs. Down changes nothing.
func TestFixedLeadModelsMigration(t *testing.T) {
	db := open(t, testdb.URL(t))
	ctx := context.Background()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 300); err != nil {
		t.Fatal(err)
	}
	seed := `INSERT INTO at_users (id, email, password_hash) VALUES ('u1', 'a@example.com', 'x');
INSERT INTO at_projects (id, user_id, name, coordinator_model, thread_model, thread_codex_model) VALUES ('p1', 'u1', 'A', 'm-coord', 'm-thread', 'm-codex');
INSERT INTO at_roles (id, user_id, project_id, tier, role, runtime, gate, model) VALUES
  ('r1', 'u1', NULL, 'coordinator', 'coordinator', 'claude', '', 'm1'), ('r2', 'u1', NULL, 'lead', 'lead', 'claude', '', 'm2'),
  ('r3', 'u1', NULL, 'member', 'dev', 'claude', '', 'm3'), ('r4', 'u1', 'p1', 'lead', 'lead', 'claude', '', 'm4');
INSERT INTO at_employees (id, project_id, name, tier, role, role_id, runtime, model, spec) VALUES
  ('e1', 'p1', 'coordinator', 'coordinator', 'coordinator', 'r1', 'claude', 'o1', '{"role": "coordinator", "model": "m1"}'),
  ('e2', 'p1', 'lead-1', 'lead', 'lead', 'r2', 'claude', 'o2', '{"role": "lead", "model": "m2"}'),
  ('e3', 'p1', 'dev-1', 'member', 'dev', 'r3', 'claude', 'o3', '{"role": "dev", "model": "m3"}');`
	if _, err := db.Exec(seed); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 310); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRow(`SELECT string_agg(id || ':' || model, ' ' ORDER BY id) FROM at_roles`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if want := "r1: r2: r3:m3 r4:"; got != want {
		t.Fatalf("roles after 00310:\n got %s\nwant %s", got, want)
	}
	if err := db.QueryRow(`SELECT string_agg(id || ':' || model || ':' || COALESCE(spec->>'model', '-'), ' ' ORDER BY id) FROM at_employees`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if want := "e1:: e2:: e3:o3:m3"; got != want {
		t.Fatalf("employees after 00310:\n got %s\nwant %s", got, want)
	}
	if err := db.QueryRow(`SELECT coordinator_model || '|' || thread_model || '|' || thread_codex_model FROM at_projects`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if want := "|m-thread|m-codex"; got != want {
		t.Fatalf("project after 00310: got %s want %s", got, want)
	}
	if _, err := p.DownTo(ctx, 300); err != nil {
		t.Fatal(err)
	}
}

// TestDropUserModelsMigration: 00320 writes each owner's default of a runtime into the roles
// (templates and project copies, every tier) and the employees' role copies that had no model,
// matching the runtime, skipping a Claude default equal to the built-in claude-opus-5-5 and
// leaving set models and other users alone; then drops at_models. Down brings back the empty
// table (00210's structure), the backfill stays.
func TestDropUserModelsMigration(t *testing.T) {
	db := open(t, testdb.URL(t))
	ctx := context.Background()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 311); err != nil {
		t.Fatal(err)
	}
	// u1: Claude default claude-haiku-y, Codex default gpt-def. u2: Claude default = the built-in, no Codex default.
	seed := `INSERT INTO at_users (id, email, password_hash) VALUES ('u1', 'a@example.com', 'x'), ('u2', 'b@example.com', 'x');
INSERT INTO at_models (id, user_id, runtime, model, sort, is_default) VALUES
  ('m1', 'u1', 'claude', 'claude-opus-5-5', 0, FALSE), ('m2', 'u1', 'claude', 'claude-haiku-y', 1, TRUE),
  ('m3', 'u1', 'codex', 'gpt-def', 2, TRUE), ('m4', 'u2', 'claude', 'claude-opus-5-5', 0, TRUE), ('m5', 'u2', 'codex', 'gpt-other', 1, FALSE);
INSERT INTO at_projects (id, user_id, name) VALUES ('p1', 'u1', 'A'), ('p2', 'u2', 'B');
INSERT INTO at_roles (id, user_id, project_id, tier, role, runtime, gate, model) VALUES
  ('r1', 'u1', NULL, 'coordinator', 'coordinator', 'claude', '', ''), ('r2', 'u1', NULL, 'lead', 'lead', 'claude', '', ''),
  ('r3', 'u1', NULL, 'member', 'dev', 'claude', '', 'claude-set'), ('r4', 'u1', NULL, 'member', 'cx', 'codex', '', ''),
  ('r5', 'u1', 'p1', 'lead', 'lead', 'claude', '', ''), ('r6', 'u1', 'p1', 'member', 'qa', 'claude', '', ''),
  ('r7', 'u2', NULL, 'lead', 'lead', 'claude', '', ''), ('r8', 'u2', NULL, 'member', 'cx', 'codex', '', '');
INSERT INTO at_employees (id, project_id, name, tier, role, runtime, model, spec) VALUES
  ('e1', 'p1', 'coordinator', 'coordinator', 'coordinator', 'claude', '', '{"role": "coordinator", "model": ""}'),
  ('e2', 'p1', 'lead-1', 'lead', 'lead', 'claude', '', '{"role": "lead"}'),
  ('e3', 'p1', 'dev-1', 'member', 'dev', 'claude', 'claude-picked', '{"role": "dev", "model": "claude-set"}'),
  ('e4', 'p1', 'cx-1', 'member', 'cx', 'codex', '', '{"role": "cx", "model": ""}'),
  ('e5', 'p2', 'lead-1', 'lead', 'lead', 'claude', '', '{"role": "lead", "model": ""}'),
  ('e6', 'p2', 'cx-1', 'member', 'cx', 'codex', '', '{"role": "cx"}');`
	if _, err := db.Exec(seed); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 320); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRow(`SELECT string_agg(id || ':' || model, ' ' ORDER BY id) FROM at_roles`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if want := "r1:claude-haiku-y r2:claude-haiku-y r3:claude-set r4:gpt-def r5:claude-haiku-y r6:claude-haiku-y r7: r8:"; got != want {
		t.Fatalf("roles after 00320:\n got %s\nwant %s", got, want)
	}
	if err := db.QueryRow(`SELECT string_agg(id || ':' || model || ':' || COALESCE(spec->>'model', '-') || ':' || (spec->>'role'), ' ' ORDER BY id) FROM at_employees`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if want := "e1::claude-haiku-y:coordinator e2::claude-haiku-y:lead e3:claude-picked:claude-set:dev e4::gpt-def:cx e5:::lead e6::-:cx"; got != want {
		t.Fatalf("employees after 00320:\n got %s\nwant %s", got, want)
	}
	assertNoTable(t, db, "at_models")

	if _, err := p.DownTo(ctx, 311); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM at_models`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("at_models after down: %d %v", n, err)
	}
	if _, err := db.Exec(`INSERT INTO at_models (id, runtime, model) VALUES ('mx', 'claude', 'x')`); err == nil {
		t.Fatal("row without a user accepted after down")
	}
	if err := db.QueryRow(`SELECT model FROM at_roles WHERE id = 'r1'`).Scan(&got); err != nil || got != "claude-haiku-y" {
		t.Fatalf("backfill undone: %q %v", got, err)
	}
	if _, err := p.UpTo(ctx, 320); err != nil {
		t.Fatalf("up again: %v", err)
	}
}

// TestGitReposMigration: 00340 adds standalone repositories and their credentials. A project
// repository names a GitHub connection or a standalone repository, never both; a credential or
// repository still in use cannot be deleted; a slug must look like host/path. Down removes the
// project rows that named a standalone repository, then the tables.
func TestGitReposMigration(t *testing.T) {
	db := open(t, testdb.URL(t))
	ctx := context.Background()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 340); err != nil {
		t.Fatal(err)
	}
	seed := `INSERT INTO at_users (id, email, password_hash) VALUES ('u1', 'a@example.com', 'x'), ('u2', 'b@example.com', 'x');
INSERT INTO at_projects (id, user_id, name) VALUES ('p1', 'u1', 'A');
INSERT INTO at_git_connections (id, user_id, name, provider, login, token_secret, kind) VALUES ('g1', 'u1', 'gh', 'github', 'me', '', 'app');
INSERT INTO at_git_credentials (id, user_id, name, host, kind, cred_ciphertext, cred_nonce, wrapped_dek, dek_nonce, kek_id)
  VALUES ('gc1', 'u1', 'token', 'gitlab.com', 'https_token', '\x00', '\x00', '\x00', '\x00', 'k');
INSERT INTO at_git_repos (id, user_id, slug, url, transport, host, credential_id)
  VALUES ('gr1', 'u1', 'gitlab.com/acme/web', 'https://gitlab.com/acme/web.git', 'https', 'gitlab.com', 'gc1');
INSERT INTO at_project_repos (project_id, repo, git_repo_id) VALUES ('p1', 'gitlab.com/acme/web', 'gr1');
INSERT INTO at_project_repos (project_id, repo, git_connection_id) VALUES ('p1', 'acme/api', 'g1');`
	if _, err := db.Exec(seed); err != nil {
		t.Fatal(err)
	}
	bad := map[string]string{
		"both sources":      `INSERT INTO at_project_repos (project_id, repo, git_connection_id, git_repo_id) VALUES ('p1', 'x/y', 'g1', 'gr1')`,
		"credential in use": `DELETE FROM at_git_credentials WHERE id = 'gc1'`,
		"repo in use":       `DELETE FROM at_git_repos WHERE id = 'gr1'`,
		"bad slug":          `INSERT INTO at_git_repos (id, user_id, slug, url, transport, host) VALUES ('gr2', 'u1', 'no-path', 'https://x/y', 'https', 'x')`,
		"other user's cred": `INSERT INTO at_git_repos (id, user_id, slug, url, transport, host, credential_id) VALUES ('gr3', 'u2', 'gitlab.com/b/c', 'https://gitlab.com/b/c', 'https', 'gitlab.com', 'gc1')`,
	}
	for name, q := range bad {
		if _, err := db.Exec(q); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	if _, err := p.DownTo(ctx, 330); err != nil {
		t.Fatal(err)
	}
	var repos string
	if err := db.QueryRow(`SELECT string_agg(repo, ',') FROM at_project_repos`).Scan(&repos); err != nil {
		t.Fatal(err)
	}
	if repos != "acme/api" {
		t.Fatalf("project repos after down: %s", repos)
	}
}

// TestRoleRuntimesMigration: 00350 gives every role and role snapshot its single runtime as the
// runtimes list and empty further models; Down drops them again.
func TestRoleRuntimesMigration(t *testing.T) {
	db := open(t, testdb.URL(t))
	ctx := context.Background()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 340); err != nil {
		t.Fatal(err)
	}
	seed := `INSERT INTO at_users (id, email, password_hash) VALUES ('u1', 'a@example.com', 'x');
INSERT INTO at_projects (id, user_id, name) VALUES ('p1', 'u1', 'A');
INSERT INTO at_roles (id, user_id, tier, role, runtime) VALUES ('r1', 'u1', 'member', 'dev', 'claude'), ('r2', 'u1', 'member', 'qa', 'codex');
INSERT INTO at_employees (id, project_id, name, tier, role, role_id, runtime, spec) VALUES
  ('e1', 'p1', 'qa-1', 'member', 'qa', 'r2', 'codex', '{"role": "qa", "runtime": "codex"}');`
	if _, err := db.Exec(seed); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 350); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRow(`SELECT string_agg(role || ':' || array_to_string(runtimes, ',') || ':' || models::text, ' ' ORDER BY id) FROM at_roles`).Scan(&got); err != nil ||
		got != "dev:claude:{} qa:codex:{}" {
		t.Fatalf("roles after 00350: %q %v", got, err)
	}
	if err := db.QueryRow(`SELECT (spec->'runtimes')::text || ' ' || (spec->'models')::text FROM at_employees WHERE id = 'e1'`).Scan(&got); err != nil || got != `["codex"] {}` {
		t.Fatalf("spec after 00350: %q %v", got, err)
	}
	if _, err := p.DownTo(ctx, 340); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT spec::text FROM at_employees WHERE id = 'e1'`).Scan(&got); err != nil || got != `{"role": "qa", "runtime": "codex"}` {
		t.Fatalf("spec after down: %q %v", got, err)
	}
}

// TestNodeRuntimesMigration: 00360 adds at_node_runtimes (one row per node and runtime, removed
// with the node) and at_session_holds (one per employee); Down drops both.
func TestNodeRuntimesMigration(t *testing.T) {
	db := open(t, testdb.URL(t))
	ctx := context.Background()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 360); err != nil {
		t.Fatal(err)
	}
	seed := `INSERT INTO at_users (id, email, password_hash) VALUES ('u1', 'a@example.com', 'x');
INSERT INTO at_nodes (id, user_id, name, token_hash) VALUES ('n1', 'u1', 'n1', 'h');
INSERT INTO at_projects (id, user_id, name) VALUES ('p1', 'u1', 'A');
INSERT INTO at_employees (id, project_id, name, tier, role, runtime) VALUES ('e1', 'p1', 'lead-1', 'lead', 'lead', 'claude');
INSERT INTO at_node_runtimes (node_id, runtime, installed, auth, checked_at) VALUES ('n1', 'claude', true, 'subscription', now());
INSERT INTO at_session_holds (employee_id, project_id, reason, first) VALUES ('e1', 'p1', 'r', '{"msg_id": "m1", "text": "x"}');`
	if _, err := db.Exec(seed); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO at_node_runtimes (node_id, runtime, checked_at) VALUES ('n1', 'claude', now())`); err == nil {
		t.Fatal("duplicate (node, runtime) accepted")
	}
	var more string
	if err := db.QueryRow(`SELECT more::text FROM at_session_holds`).Scan(&more); err != nil || more != "[]" {
		t.Fatalf("more default %q %v", more, err)
	}
	if _, err := db.Exec(`DELETE FROM at_nodes WHERE id = 'n1'; DELETE FROM at_employees WHERE id = 'e1'`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM at_node_runtimes) + (SELECT count(*) FROM at_session_holds)`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows left after delete: %d %v", n, err)
	}
	if _, err := p.DownTo(ctx, 350); err != nil {
		t.Fatal(err)
	}
	var reg *string
	if err := db.QueryRow(`SELECT to_regclass('at_node_runtimes')::text`).Scan(&reg); err != nil || reg != nil {
		t.Fatalf("table after down: %v %v", reg, err)
	}
}
