// Package store keeps hostd's durable state in SQLite: API tokens and the
// audit trail. Everything else is rebuilt from the system on start.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // database/sql driver "sqlite"

	"github.com/davitizhgenti/hostd/core"
	"github.com/davitizhgenti/hostd/internal/clock"
)

// migrations[i] moves the schema from version i to i+1. Append only: never
// edit a released migration, because databases in the field already ran it.
var migrations = []string{
	// 1: tokens and the audit trail.
	`CREATE TABLE tokens (
		id        TEXT PRIMARY KEY,
		name      TEXT NOT NULL,
		hash      TEXT NOT NULL UNIQUE,
		kind      TEXT NOT NULL,
		scopes    TEXT NOT NULL,
		created   INTEGER NOT NULL,
		expires   INTEGER,
		revoked   INTEGER,
		last_used INTEGER
	);
	CREATE UNIQUE INDEX tokens_active_name ON tokens(name) WHERE revoked IS NULL;
	CREATE TABLE audit (
		seq    INTEGER PRIMARY KEY AUTOINCREMENT,
		time   INTEGER NOT NULL,
		action TEXT NOT NULL,
		type   TEXT NOT NULL,
		status TEXT NOT NULL,
		entry  TEXT NOT NULL
	);
	CREATE INDEX audit_action ON audit(action);`,
}

// SchemaVersion is the schema version this build writes.
var SchemaVersion = len(migrations)

// Options configure a Store.
type Options struct {
	Clock  clock.Clock
	Logger *slog.Logger
	// AuditMax is how many audit entries are kept (default 10,000).
	AuditMax int
}

// Store is hostd's SQLite database.
type Store struct {
	db    *sql.DB
	clock clock.Clock
	log   *slog.Logger

	auditMax int
	queue    chan core.AuditEntry
	flushReq chan chan struct{}
	stop     chan struct{}
	stopped  chan struct{}
	once     sync.Once
}

// Open opens (creating if needed) the database at path and migrates it to
// SchemaVersion. A database written by a newer hostd is refused, so a
// rolled-back binary never misreads data it does not understand.
func Open(ctx context.Context, path string, opts Options) (*Store, error) {
	if strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("store: database path %q must not contain ? or #", path)
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.AuditMax == 0 {
		opts.AuditMax = 10000
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection: writes are serialized anyway, and it rules out
	// SQLITE_BUSY between our own connections.
	db.SetMaxOpenConns(1)

	if err := migrate(ctx, db); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	s := &Store{
		db: db, clock: opts.Clock, log: opts.Logger, auditMax: opts.AuditMax,
		queue:    make(chan core.AuditEntry, 1024),
		flushReq: make(chan chan struct{}),
		stop:     make(chan struct{}),
		stopped:  make(chan struct{}),
	}
	go s.auditWriter()
	return s, nil
}

// Version reads the schema version of an open database.
func (s *Store) Version(ctx context.Context) (int, error) { return schemaVersion(ctx, s.db) }

// Close writes pending audit entries and closes the database.
func (s *Store) Close() error {
	s.once.Do(func() { close(s.stop) })
	<-s.stopped
	return s.db.Close()
}

func schemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	var v int
	err := db.QueryRowContext(ctx, `SELECT version FROM schema_version`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return v, err
}

func migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	have, err := schemaVersion(ctx, db)
	if err != nil {
		return fmt.Errorf("store: reading schema version: %w", err)
	}
	if have > SchemaVersion {
		return fmt.Errorf("store: database schema is version %d, newer than this hostd understands (%d); "+
			"restore the backup taken before the update", have, SchemaVersion)
	}
	for v := have; v < SchemaVersion; v++ {
		if err := applyMigration(ctx, db, v); err != nil {
			return fmt.Errorf("store: migration to version %d: %w", v+1, err)
		}
	}
	return nil
}

// applyMigration runs migrations[v] and records version v+1, atomically.
func applyMigration(ctx context.Context, db *sql.DB, v int) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err := tx.ExecContext(ctx, migrations[v]); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM schema_version`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (?)`, v+1); err != nil {
		return err
	}
	return tx.Commit()
}

func unixNano(t time.Time) int64 { return t.UnixNano() }

func fromNano(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := time.Unix(0, n.Int64).UTC()
	return &t
}

// Backup writes a consistent copy of the database to path, while hostd
// keeps running (VACUUM INTO).
func (s *Store) Backup(ctx context.Context, path string) error {
	if strings.ContainsAny(path, "'") {
		return fmt.Errorf("store: backup path %q must not contain quotes", path)
	}
	_ = os.Remove(path)
	_, err := s.db.ExecContext(ctx, "VACUUM INTO '"+path+"'")
	return err
}
