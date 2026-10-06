// Package ids generates the prefixed, time-ordered IDs used across hostd,
// such as act_01J9… for actions and evt_01J9… for events.
//
// IDs are ULIDs: they sort by creation time, and IDs made by one Generator in
// the same millisecond still sort in creation order (monotonic entropy).
package ids

import (
	"crypto/rand"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/davitizhgenti/hostd/internal/clock"
)

// Prefixes of the ID kinds hostd issues.
const (
	Action = "act"
	Event  = "evt"
	Token  = "tok"
)

// Generator issues IDs. It is safe for concurrent use.
type Generator struct {
	clock   clock.Clock
	mu      sync.Mutex
	entropy *ulid.MonotonicEntropy
}

// NewGenerator returns a Generator that timestamps IDs with c.
func NewGenerator(c clock.Clock) *Generator {
	return &Generator{clock: c, entropy: ulid.Monotonic(rand.Reader, 0)}
}

var std = NewGenerator(clock.Real())

// New returns a new ID with the given prefix from the default generator.
func New(prefix string) string { return std.New(prefix) }

// New returns a new ID with the given prefix, e.g. "act_01J9ZQ3…".
func (g *Generator) New(prefix string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	id, err := ulid.New(ulid.Timestamp(g.clock.Now()), g.entropy)
	if err != nil {
		// Only possible when more than 2^80 IDs are issued in one
		// millisecond, or the system random source fails.
		panic(fmt.Sprintf("ids: %v", err))
	}
	return prefix + "_" + id.String()
}

// Parse splits an ID into its prefix and the time it was issued.
func Parse(id string) (prefix string, t time.Time, err error) {
	prefix, rest, ok := strings.Cut(id, "_")
	if !ok || prefix == "" {
		return "", time.Time{}, fmt.Errorf("ids: %q has no prefix", id)
	}
	u, err := ulid.ParseStrict(rest)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("ids: %q: %w", id, err)
	}
	return prefix, ulid.Time(u.Time()), nil
}

// HasPrefix reports whether id is a well-formed ID of the given kind.
func HasPrefix(id, prefix string) bool {
	p, _, err := Parse(id)
	return err == nil && p == prefix
}
