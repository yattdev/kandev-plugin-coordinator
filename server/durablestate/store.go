package durablestate

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	_ "modernc.org/sqlite"
)

// Store wraps a plugin-owned SQLite database implementing this package's
// schema. One Store is normally opened per plugin instance; every method
// takes a workspaceID so a single database file may (and normally does)
// hold every workspace's durable state, isolated by that key.
type Store struct {
	db *sql.DB
}

// Open opens (creating if necessary) the SQLite database at dsn and
// configures it for this package's durability/concurrency requirements:
// WAL journaling (so readers never block the single writer), a busy
// timeout (so lock contention retries instead of failing immediately), and
// foreign-key enforcement. It does not run migrations — call Migrate
// explicitly so callers can choose when schema changes happen.
func Open(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("durablestate: opening database: %w", err)
	}
	// This package's single-writer mutation lane (§6) relies on SQLite's
	// own writer serialization; WAL lets snapshot/health reads proceed
	// concurrently with it.
	if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		db.Close()
		return nil, fmt.Errorf("durablestate: setting WAL mode: %w", err)
	}
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		db.Close()
		return nil, fmt.Errorf("durablestate: setting busy_timeout: %w", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		db.Close()
		return nil, fmt.Errorf("durablestate: enabling foreign_keys: %w", err)
	}
	// A single writer connection avoids SQLITE_BUSY races between this
	// process's own goroutines while still allowing concurrent readers
	// under WAL.
	db.SetMaxOpenConns(1)
	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// currentSchemaVersion returns the highest applied migration version, or 0
// if schema_migrations does not exist yet (fresh database).
func (s *Store) currentSchemaVersion(ctx context.Context) (int, error) {
	var exists int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`,
	).Scan(&exists)
	if err != nil {
		return 0, err
	}
	if exists == 0 {
		return 0, nil
	}
	var version sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, err
	}
	return int(version.Int64), nil
}

// Migrate applies every not-yet-applied migration in ascending version
// order, each in its own transaction, recording it in schema_migrations.
// Safe to call repeatedly (a no-op once the schema is current).
func (s *Store) Migrate(ctx context.Context) error {
	current, err := s.currentSchemaVersion(ctx)
	if err != nil {
		return fmt.Errorf("durablestate: reading schema version: %w", err)
	}
	sorted := append([]migration(nil), migrations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].version < sorted[j].version })
	for _, m := range sorted {
		if m.version <= current {
			continue
		}
		if err := s.applyMigration(ctx, m); err != nil {
			return fmt.Errorf("durablestate: applying migration %d (%s): %w", m.version, m.name, err)
		}
	}
	return nil
}

func (s *Store) applyMigration(ctx context.Context, m migration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range m.up {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("statement %q: %w", stmt, err)
		}
	}
	// schema_migrations itself is created by migration 1's up statements,
	// so this insert only works from that point forward, exactly like
	// every later migration recording itself.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
		m.version, m.name, time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return err
	}
	return tx.Commit()
}

// MigrateDown reverses the `steps` most recently applied migrations, in
// descending version order, each in its own transaction. Reversal is only
// as safe as each migration's own down statements — see migrations.go.
func (s *Store) MigrateDown(ctx context.Context, steps int) error {
	current, err := s.currentSchemaVersion(ctx)
	if err != nil {
		return fmt.Errorf("durablestate: reading schema version: %w", err)
	}
	if current == 0 || steps <= 0 {
		return nil
	}
	sorted := append([]migration(nil), migrations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].version > sorted[j].version })
	applied := 0
	for _, m := range sorted {
		if applied >= steps {
			break
		}
		if m.version > current {
			continue
		}
		if err := s.revertMigration(ctx, m); err != nil {
			return fmt.Errorf("durablestate: reverting migration %d (%s): %w", m.version, m.name, err)
		}
		applied++
	}
	return nil
}

func (s *Store) revertMigration(ctx context.Context, m migration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range m.down {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("statement %q: %w", stmt, err)
		}
	}
	// If schema_migrations itself survived (down statements for version 1
	// drop it last), this delete is a no-op past that point; guard with a
	// table-existence check so reverting version 1 doesn't itself error.
	var exists int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`,
	).Scan(&exists); err != nil {
		return err
	}
	if exists == 1 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = ?`, m.version); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// execer is the subset of *sql.Tx / *sql.Conn this package's query helpers
// need, so they can run unchanged whether given a plain connection or one
// wrapped in an explicit BEGIN IMMEDIATE.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// withWriteTx runs fn inside an explicit BEGIN IMMEDIATE transaction, which
// acquires SQLite's write lock up front rather than at first write. Go's
// database/sql has no portable way to request BEGIN IMMEDIATE through
// *sql.Tx (it always issues a plain deferred BEGIN), so this borrows a
// single *sql.Conn from the pool and issues BEGIN IMMEDIATE/COMMIT/ROLLBACK
// as raw statements on it directly. Combined with SetMaxOpenConns(1), this
// serializes every write in this process through one lock, matching §6's
// single-writer mutation lane and making nextMutationID / fencing checks
// race-free without an additional external mutex.
func (s *Store) withWriteTx(ctx context.Context, fn func(tx execer) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("durablestate: BEGIN IMMEDIATE: %w", err)
	}
	if err := fn(conn); err != nil {
		_, _ = conn.ExecContext(ctx, `ROLLBACK`)
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		_, _ = conn.ExecContext(ctx, `ROLLBACK`)
		return fmt.Errorf("durablestate: COMMIT: %w", err)
	}
	return nil
}

func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
