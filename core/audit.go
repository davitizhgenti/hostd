package core

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/davitizhgenti/hostd/sdk"
)

// AuditEntry is one line of the audit trail: an action and what became of
// it, or a change observed outside hostd.
type AuditEntry struct {
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
	Record(e AuditEntry)
}

// MemoryAudit keeps the most recent entries in memory.
type MemoryAudit struct {
	mu      sync.Mutex
	max     int
	entries []AuditEntry
}

// NewMemoryAudit keeps up to max entries.
func NewMemoryAudit(max int) *MemoryAudit { return &MemoryAudit{max: max} }

func (m *MemoryAudit) Record(e AuditEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, e)
	if over := len(m.entries) - m.max; over > 0 {
		m.entries = append(m.entries[:0], m.entries[over:]...)
	}
}

// Entries returns the stored entries, oldest first.
func (m *MemoryAudit) Entries() []AuditEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]AuditEntry(nil), m.entries...)
}
