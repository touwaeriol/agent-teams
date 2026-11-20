package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm/schema"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/testdb"
	"github.com/touwaeriol/agent-teams/server/internal/timers"
)

func openPool(t *testing.T) *store.Pool {
	t.Helper()
	pool, err := store.OpenPool(context.Background(), testdb.Migrated(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// models are every GORM model that maps a table the goose migrations create.
var models = []any{
	&store.User{}, &store.Project{}, &store.Role{}, &store.OfficeLayoutRow{}, &store.GitConnection{},
	&store.MemoryFile{}, &store.Node{}, &store.NodeLog{}, &store.Session{}, &store.AuditEntry{},
	&core.Thread{}, &core.Employee{}, &core.AgentSession{}, &core.Task{}, &core.MainTask{}, &core.Question{},
	&core.Message{}, &core.Mail{}, &core.Deliverable{}, &core.MergeEntry{}, &timers.Schedule{},
	&store.RemoteServer{}, &store.SSHAudit{},
}

// TestModelsMatchTables checks every stored model field is a column of its table (the
// migrations own the schema; the models must follow them, never the other way round).
func TestModelsMatchTables(t *testing.T) {
	ctx := context.Background()
	g := openPool(t).Gorm(ctx)
	for _, m := range models {
		s, err := schema.Parse(m, &sync.Map{}, g.NamingStrategy)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := g.Migrator().ColumnTypes(s.Table)
		if err != nil || len(cols) == 0 {
			t.Fatalf("%s: table %s missing: %v", s.Name, s.Table, err)
		}
		have := map[string]bool{}
		for _, c := range cols {
			have[c.Name()] = true
		}
		for _, f := range s.Fields {
			if f.DBName != "" && (f.Creatable || f.Updatable) && !have[f.DBName] {
				t.Errorf("%s.%s: %s has no column %q", s.Name, f.Name, s.Table, f.DBName)
			}
		}
	}
}

// TestRawPassThrough pins the GORM behaviour hand-written SQL relies on: $n placeholders
// pass through, arrays and json decode into Go values, and '?' is refused.
func TestRawPassThrough(t *testing.T) {
	ctx := context.Background()
	pool := openPool(t)
	var tags []string
	var obj map[string]int
	var look store.Look
	var n int
	err := store.QueryRow(ctx, pool, `SELECT $1::text[], $2::jsonb, $3::jsonb, $4::int + $4::int`,
		[]string{"a", "b c", `q"uote`}, map[string]int{"x": 1}, store.Look{Color: "red"}, 21).Scan(&tags, &obj, &look, &n)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 3 || tags[2] != `q"uote` || obj["x"] != 1 || look.Color != "red" || n != 42 {
		t.Fatalf("got %v %v %v %d", tags, obj, look, n)
	}
	if _, err := store.Exec(ctx, pool, `SELECT $1 WHERE '{}'::jsonb ? 'k'`, 1); err == nil {
		t.Fatal("want the '?' guard to refuse")
	}
	var empty []string
	if err := store.QueryRow(ctx, pool, `SELECT 1 WHERE false`).Scan(&empty); !store.IsNoRows(err) {
		t.Fatalf("want no rows, got %v", err)
	}
}

// TestParamKeepsArrays checks builder arguments bind slices as one array.
func TestParamKeepsArrays(t *testing.T) {
	ctx := context.Background()
	g := openPool(t).Gorm(ctx)
	var n int64
	err := g.Raw(`SELECT cardinality(?::text[]) + jsonb_array_length(?::jsonb)`,
		store.Param([]string{"a", "b"}), store.Param([]int{1, 2, 3})).Row().Scan(&n)
	if err != nil || n != 5 {
		t.Fatalf("got %d, %v", n, err)
	}
	raw := store.Param(json.RawMessage(`{"k":1}`))
	if _, ok := raw.([]byte); !ok {
		t.Fatalf("raw json should bind as bytes, got %T", raw)
	}
}

// TestStoreRoundTrip exercises models with serializers (jsonb, text[]) through the Store API.
func TestStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, testdb.Migrated(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	u, err := s.CreateUser(ctx, "a@example.com", "h")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "a@example.com", "h"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
	ro := true
	roles := []store.Role{{Tier: "lead", Role: "lead", Runtime: "claude", Count: 1, NodeIDs: []string{"n1"},
		PermissionMode: store.PermBypass},
		{Tier: "member", Role: "dev", Runtime: "claude", Count: 2, ReadOnly: &ro, Skills: []string{"design"},
			Look: store.Look{Color: "blue"}, PermissionMode: store.PermAsk}}
	if err := s.Tx(ctx, func(tx *store.Store) error { return tx.ReplaceRoles(ctx, u.ID, roles) }); err != nil {
		t.Fatal(err)
	}
	got, err := s.Roles(ctx, u.ID)
	if err != nil || len(got) != 2 {
		t.Fatalf("roles %v %v", got, err)
	}
	dev := got[1]
	if dev.Skills[0] != "design" || dev.Look.Color != "blue" || !*dev.ReadOnly || dev.AllowedTools == nil ||
		got[0].NodeIDs[0] != "n1" {
		t.Fatalf("role round trip: %+v", dev)
	}
	n, err := s.CreateNode(ctx, u.ID, "box", "hash")
	if err != nil {
		t.Fatal(err)
	}
	n, err = s.UpdateNode(ctx, u.ID, n.ID, []store.Change{{Col: "labels", Val: []string{"gpu"}}})
	if err != nil || len(n.Labels) != 1 || n.Labels[0] != "gpu" {
		t.Fatalf("labels %v %v", n.Labels, err)
	}
	if err := s.DeleteNode(ctx, u.ID, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
	if err := s.SaveSetting(ctx, "k", map[string]int{"v": 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSetting(ctx, "k", map[string]int{"v": 2}); err != nil {
		t.Fatal(err)
	}
	var v map[string]int
	if ok, err := s.Setting(ctx, "k", &v); !ok || err != nil || v["v"] != 2 {
		t.Fatalf("setting %v %v %v", ok, err, v)
	}
	layout, err := s.SaveOfficeLayout(ctx, "l1", []byte(`{}`), u.ID)
	if err == nil {
		layout, err = s.SaveOfficeLayout(ctx, "l1", []byte(`{"a":1}`), u.ID)
	}
	if err != nil || layout.Version != 2 {
		t.Fatalf("layout %+v %v", layout, err)
	}
}

// TestNestedTxIsSavepoint checks a failing inner transaction rolls back only its own writes.
func TestNestedTxIsSavepoint(t *testing.T) {
	ctx := context.Background()
	pool := openPool(t)
	err := store.InTx(ctx, pool, func(tx store.DB) error {
		if _, err := store.Exec(ctx, tx, `INSERT INTO at_settings (key, value) VALUES ('outer', '1')`); err != nil {
			return err
		}
		inner := store.InTx(ctx, tx, func(sp store.DB) error {
			_, _ = store.Exec(ctx, sp, `INSERT INTO at_settings (key, value) VALUES ('inner', '1')`)
			return errors.New("roll back")
		})
		if inner == nil {
			t.Error("inner error lost")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	if err := pool.Gorm(ctx).Table("at_settings").Order("key").Pluck("key", &keys).Error; err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0] != "outer" {
		t.Fatalf("keys %v", keys)
	}
}

// TestOnReadsThroughTransaction: a Store bound to an open transaction reads on that
// transaction's connection, so it never waits for a second one (a pool of one would hang).
func TestOnReadsThroughTransaction(t *testing.T) {
	ctx := context.Background()
	url := testdb.Migrated(t)
	s, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.CreateUser(ctx, "on@example.com", "h")
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.CreateConnection(ctx, u.ID, "gh", "login", "sealed", nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	one, err := store.Open(ctx, url+sep+"pool_max_conns=1")
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err = store.InTx(tctx, one.Pool, func(tx store.DB) error {
		got, err := one.On(tx).Connection(tctx, u.ID, c.ID)
		if err == nil && got.ID != c.ID {
			t.Errorf("connection %q", got.ID)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestPoolDefaults: without pool_max_conns the pool is not the tiny pgx default, and sessions
// left idle inside a transaction are ended by the server.
func TestPoolDefaults(t *testing.T) {
	ctx := context.Background()
	pool := openPool(t)
	var timeout string
	if err := pool.Gorm(ctx).Raw(`SHOW idle_in_transaction_session_timeout`).Scan(&timeout).Error; err != nil {
		t.Fatal(err)
	}
	if timeout != "2min" {
		t.Fatalf("idle_in_transaction_session_timeout %q", timeout)
	}
	if got := pool.MaxConns(); got != 32 {
		t.Fatalf("max conns %d", got)
	}
}
