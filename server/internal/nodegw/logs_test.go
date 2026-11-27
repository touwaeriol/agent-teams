package nodegw

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/testdb"
)

// TestLogBucket: a link may send logBurst log frames at once, then logRate per second.
func TestLogBucket(t *testing.T) {
	var b logBucket
	now := time.Unix(1000, 0)
	for i := 0; i < int(logBurst); i++ {
		if !b.allow(now) {
			t.Fatalf("frame %d of the burst refused", i)
		}
	}
	if b.allow(now) {
		t.Fatal("frame past the burst allowed")
	}
	now = now.Add(time.Second)
	n := 0
	for b.allow(now) {
		n++
	}
	if n != int(logRate) {
		t.Fatalf("refilled %d frames in 1s, want %v", n, logRate)
	}
	if !b.allow(now.Add(time.Hour)) || b.tokens > logBurst {
		t.Fatalf("bucket not capped at the burst: %v", b.tokens)
	}
}

// TestLogFieldsAndMsgCapped: oversized fields become a marker, msg is cut without splitting UTF-8.
func TestLogFieldsAndMsgCapped(t *testing.T) {
	if got := string(logFields(nil)); got != `{}` {
		t.Fatalf("nil fields: %s", got)
	}
	small := json.RawMessage(`{"a":1}`)
	if got := string(logFields(&small)); got != `{"a":1}` {
		t.Fatalf("small fields: %s", got)
	}
	huge := json.RawMessage(`{"a":"` + strings.Repeat("x", 3<<20) + `"}`)
	got := logFields(&huge)
	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil || m["truncated"] != true || len(got) > 100 {
		t.Fatalf("huge fields: %s (%v)", got, err)
	}
	for _, in := range []string{strings.Repeat("é", 3000), "a" + strings.Repeat("é", 3000)} { // even and odd cut
		s := truncateUTF8(in, logMsgMax)
		if len(s) > logMsgMax || len(s) < logMsgMax-3 || !utf8.ValidString(s) {
			t.Fatalf("msg %d bytes valid=%v", len(s), utf8.ValidString(s))
		}
	}
}

// TestStoreLogLimitsAndPrunes: a flood of log frames stores only the burst with capped rows, and
// the table keeps at most logKeepPerNode rows per node (none older than logMaxAge) without
// touching other nodes' rows.
func TestStoreLogLimitsAndPrunes(t *testing.T) {
	ctx := context.Background()
	url := testdb.Migrated(t)
	pool, err := store.OpenPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	testdb.Exec(t, url, `INSERT INTO at_users (id, email, password_hash) VALUES ('u1', 'a@example.com', 'x');
INSERT INTO at_nodes (id, user_id, name, token_hash) VALUES ('n1', 'u1', 'one', 'h1'), ('n2', 'u1', 'two', 'h2');
INSERT INTO at_node_logs (node_id, level, msg, created_at)
SELECT 'n2', 'warn', 'm' || i, now() - interval '1 minute' FROM generate_series(1, `+strconv.Itoa(logKeepPerNode+50)+`) i;
INSERT INTO at_node_logs (node_id, level, msg, created_at) VALUES ('n2', 'warn', 'old', now() - interval '40 days');
INSERT INTO at_node_logs (node_id, level, msg) SELECT 'n1', 'warn', 'keep' FROM generate_series(1, 10);`)
	g := &Gateway{core: core.New(pool, nil), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	count := func(node string) int {
		var n int
		if err := pool.Gorm(ctx).Raw(`SELECT count(*) FROM at_node_logs WHERE node_id = ?`, node).Scan(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}

	big := json.RawMessage(`{"blob":"` + strings.Repeat("x", 1<<20) + `"}`)
	l := &link{g: g, node: store.Node{ID: "n2"}}
	frozen := time.Now()
	l.logs.clock = func() time.Time { return frozen } // slow inserts must not refill the bucket
	for i := 0; i < int(logBurst)+100; i++ {
		if err := l.storeLog(ctx, &protocol.Log{Level: "warn", Msg: strings.Repeat("y", 10000), Fields: &big}); err != nil {
			t.Fatal(err)
		}
	}
	if l.logs.dropped != 100 || l.logs.stored != int(logBurst) {
		t.Fatalf("stored %d dropped %d, want %v and 100", l.logs.stored, l.logs.dropped, logBurst)
	}
	if n := count("n2"); n > logKeepPerNode+logPruneEvery {
		t.Fatalf("n2 has %d rows after the first prune", n)
	}
	var maxMsg, maxFields int
	if err := pool.Gorm(ctx).Raw(`SELECT max(length(msg)), max(length(fields::text)) FROM at_node_logs WHERE node_id = 'n2'`).
		Row().Scan(&maxMsg, &maxFields); err != nil {
		t.Fatal(err)
	}
	if maxMsg > logMsgMax || maxFields > 100 {
		t.Fatalf("stored msg %d / fields %d bytes", maxMsg, maxFields)
	}
	var old int
	if err := pool.Gorm(ctx).Raw(`SELECT count(*) FROM at_node_logs WHERE msg = 'old'`).Scan(&old).Error; err != nil || old != 0 {
		t.Fatalf("rows older than logMaxAge kept: %d %v", old, err)
	}
	if err := g.pruneLogs(ctx, "n2"); err != nil {
		t.Fatal(err)
	}
	if n := count("n2"); n != logKeepPerNode {
		t.Fatalf("n2 has %d rows after prune, want %d", n, logKeepPerNode)
	}
	if n := count("n1"); n != 10 {
		t.Fatalf("n1 has %d rows, want 10", n)
	}
}
