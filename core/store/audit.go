package store

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/davitizhgenti/hostd/core"
)

// AuditRecord is a stored audit entry with its sequence number, which
// pages through the trail.
type AuditRecord struct {
	Seq int64 `json:"seq"`
	core.AuditEntry
}

// AuditQuery selects audit records, newest first.
type AuditQuery struct {
	Limit  int    // default 100, at most 1,000
	Before int64  // only records with seq below this (0: from the newest)
	Type   string // type pattern such as "audio.*" (see sdk.MatchType)
	Action string // one action's entries
}

// Record queues an audit entry for writing. It never blocks the action
// path: if the writer falls more than 1,024 entries behind, the entry is
// logged and dropped.
func (s *Store) Record(e core.AuditEntry) {
	select {
	case s.queue <- e:
	default:
		s.log.Warn("audit writer is behind; entry dropped", "type", e.Type, "action", e.Action)
	}
}

// Flush waits until every entry recorded so far is written.
func (s *Store) Flush(ctx context.Context) error {
	done := make(chan struct{})
	select {
	case s.flushReq <- done:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// auditWriter writes queued entries in batches, one transaction each, and
// trims the table to auditMax.
func (s *Store) auditWriter() {
	defer close(s.stopped)
	var batch []core.AuditEntry
	write := func() {
		if len(batch) == 0 {
			return
		}
		if err := s.writeAudit(batch); err != nil {
			s.log.Error("writing audit entries", "err", err, "count", len(batch))
		}
		batch = batch[:0]
	}
	// drain moves everything queued right now into the batch.
	drain := func() {
		for {
			select {
			case e := <-s.queue:
				batch = append(batch, e)
			default:
				return
			}
		}
	}
	for {
		select {
		case e := <-s.queue:
			batch = append(batch, e)
			drain()
			write()
		case done := <-s.flushReq:
			drain()
			write()
			close(done)
		case <-s.stop:
			drain()
			write()
			return
		}
	}
}

func (s *Store) writeAudit(entries []core.AuditEntry) error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO audit (time, action, type, status, entry) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, e := range entries {
		body, err := json.Marshal(e)
		if err != nil {
			return err
		}
		if _, err := stmt.ExecContext(ctx, unixNano(e.Time), e.Action, e.Type, e.Status, string(body)); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM audit WHERE seq <= (SELECT max(seq) FROM audit) - ?`, s.auditMax); err != nil {
		return err
	}
	return tx.Commit()
}

// QueryAudit returns audit records matching q, newest first.
func (s *Store) QueryAudit(ctx context.Context, q AuditQuery) ([]AuditRecord, error) {
	if q.Limit <= 0 {
		q.Limit = 100
	}
	if q.Limit > 1000 {
		q.Limit = 1000
	}
	where := []string{"1=1"}
	var args []any
	if q.Before > 0 {
		where = append(where, "seq < ?")
		args = append(args, q.Before)
	}
	if q.Action != "" {
		where = append(where, "action = ?")
		args = append(args, q.Action)
	}
	switch {
	case q.Type == "" || q.Type == "*":
	case strings.HasSuffix(q.Type, ".*"):
		where = append(where, `type LIKE ? ESCAPE '\'`)
		args = append(args, likeEscape(strings.TrimSuffix(q.Type, "*"))+"_%")
	default:
		where = append(where, "type = ?")
		args = append(args, q.Type)
	}
	args = append(args, q.Limit)
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, entry FROM audit WHERE `+strings.Join(where, " AND ")+` ORDER BY seq DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditRecord
	for rows.Next() {
		var r AuditRecord
		var body string
		if err := rows.Scan(&r.Seq, &body); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(body), &r.AuditEntry); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
