// Package audit is the audit trail's entries and where they are recorded.
package audit

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/davitizhgenti/hostd/sdk"
)

// Entry is one line of the audit trail: an action and what became of
// it, or a change observed outside hostd.
type Entry struct {
	Time     time.Time       `json:"time"`
	Action   string          `json:"action,omitempty"` // action ID; empty for observed changes
	Event    string          `json:"event,omitempty"`  // event ID for observed changes
	Type     string          `json:"type"`
	Args     json.RawMessage `json:"args,omitempty"`
	Source   sdk.Source      `json:"source"`
	Cause    string          `json:"cause,omitempty"`
	Parent   string          `json:"parent,omitempty"`
	Status   string          `json:"status"` // applied, skipped, observed, accepted, failed
	Code     sdk.Code        `json:"code,omitempty"`
	Reason   string          `json:"reason,omitempty"`
	HeldBy   sdk.SourceKind  `json:"held_by,omitempty"`
	Until    *time.Time      `json:"until,omitempty"`
	Resource string          `json:"resource,omitempty"`
	Version  uint64          `json:"version,omitempty"`
	Duration time.Duration   `json:"duration,omitempty"`
}

// StatusFailed is the audit status of an action that returned an error.
const StatusFailed = "failed"

// Auditor stores audit entries. The SQLite store implements it; Record must
// not block for long, since it runs on the action path.
type Auditor interface {
	Record(e Entry)
}

// Memory keeps the most recent entries in memory.
type Memory struct {
	mu      sync.Mutex
	max     int
	entries []Entry
}

// NewMemory keeps up to max entries.
func NewMemory(max int) *Memory { return &Memory{max: max} }

func (m *Memory) Record(e Entry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, e)
	if over := len(m.entries) - m.max; over > 0 {
		m.entries = append(m.entries[:0], m.entries[over:]...)
	}
}

// Entries returns the stored entries, oldest first.
func (m *Memory) Entries() []Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Entry(nil), m.entries...)
}
