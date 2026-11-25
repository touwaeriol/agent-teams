package store

import (
	"crypto/rand"
	"io"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// ulidGen draws ULID entropy from crypto/rand. ulid.Make() uses a math/rand source seeded with the
// process start time, so its random part is not unpredictable (audit #55 N4). IDs stay monotonic
// within one millisecond, like ulid.Make(): the first ID of a millisecond gets 80 fresh random bits,
// later ones in the same millisecond add a random increment to the previous one.
// ulidEntropy is the entropy source (a variable only so tests can check it is used).
var ulidEntropy io.Reader = rand.Reader

var ulidGen = struct {
	sync.Mutex
	entropy *ulid.MonotonicEntropy
}{entropy: ulid.Monotonic(ulidEntropy, 0)}

// NewULID returns a new ULID with crypto/rand entropy, monotonic within the process.
func NewULID() ulid.ULID {
	ms := ulid.Timestamp(time.Now())
	ulidGen.Lock()
	defer ulidGen.Unlock()
	id, err := ulid.New(ms, ulidGen.entropy)
	if err != nil {
		// ErrMonotonicOverflow (2^80 IDs in one millisecond, practically impossible) or a
		// crypto/rand failure: restart the monotonic run with fresh entropy once, else give up.
		ulidGen.entropy = ulid.Monotonic(ulidEntropy, 0)
		id = ulid.MustNew(ms, ulidGen.entropy)
	}
	return id
}
