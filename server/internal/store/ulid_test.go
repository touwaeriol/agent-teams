package store

import (
	"bytes"
	"crypto/rand"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
)

// countingReader counts bytes drawn from crypto/rand.
type countingReader struct {
	mu sync.Mutex
	n  int
}

func (c *countingReader) Read(p []byte) (int, error) {
	c.mu.Lock()
	c.n += len(p)
	c.mu.Unlock()
	return rand.Read(p)
}

// TestNewULIDUsesCryptoEntropy: the random part comes from the configured crypto/rand reader, not
// ulid's default math/rand source (audit #55 N4).
func TestNewULIDUsesCryptoEntropy(t *testing.T) {
	if ulidEntropy != rand.Reader {
		t.Fatal("default entropy source is not crypto/rand.Reader")
	}
	cr := &countingReader{}
	ulidGen.Lock()
	old := ulidGen.entropy
	ulidGen.entropy = ulid.Monotonic(cr, 0)
	ulidGen.Unlock()
	defer func() { ulidGen.Lock(); ulidGen.entropy = old; ulidGen.Unlock() }()

	a := NewULID()
	time.Sleep(2 * time.Millisecond)
	b := NewULID()
	if cr.n < 20 { // 10 bytes of fresh entropy per new millisecond
		t.Fatalf("read %d bytes of entropy, want >= 20", cr.n)
	}
	if bytes.Equal(a.Entropy(), b.Entropy()) {
		t.Fatal("entropy repeated across milliseconds")
	}
}

// TestNewULIDMonotonicAndFormat: IDs keep the ULID format and sort in generation order, even when
// many are made within one millisecond.
func TestNewULIDMonotonicAndFormat(t *testing.T) {
	prev := NewULID()
	for i := 0; i < 20000; i++ {
		id := NewULID()
		if id.Compare(prev) <= 0 {
			t.Fatalf("id %d not increasing: %s after %s", i, id, prev)
		}
		prev = id
	}
	s := NewID("th")
	if !strings.HasPrefix(s, "th_") || len(s) != 3+26 || s != strings.ToLower(s) {
		t.Fatalf("NewID format: %q", s)
	}
	if _, err := ulid.ParseStrict(strings.ToUpper(s[3:])); err != nil {
		t.Fatalf("NewID not a ULID: %v", err)
	}
	if d := time.Since(ulid.Time(prev.Time())); d < 0 || d > time.Minute {
		t.Fatalf("timestamp off by %v", d)
	}
}

// TestNewULIDConcurrentUnique: concurrent callers never get the same ID.
func TestNewULIDConcurrentUnique(t *testing.T) {
	const workers, per = 16, 2000
	var mu sync.Mutex
	seen := make(map[ulid.ULID]bool, workers*per)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids := make([]ulid.ULID, per)
			for i := range ids {
				ids[i] = NewULID()
			}
			mu.Lock()
			defer mu.Unlock()
			for _, id := range ids {
				if seen[id] {
					t.Errorf("duplicate id %s", id)
				}
				seen[id] = true
			}
		}()
	}
	wg.Wait()
}

var _ io.Reader = (*countingReader)(nil)
