package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/golang-migrate/migrate/v4"
	migratesqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "modernc.org/sqlite" // pure-Go sqlite driver (no cgo)

	"github.com/HBulgat/mini-agent/internal/session/migrations"
)

// Open opens (or creates) the SQLite database at `path`, applies
// PRAGMAs (D16: WAL + foreign_keys=ON + busy_timeout=5000), and returns
// the *sql.DB ready to use. Migrations are NOT applied here — call
// Migrate explicitly so callers can run them once at process start.
func Open(path string) (*sql.DB, error) {
	if path == "" {
		return nil, errors.New("store: empty database path")
	}
	// modernc/sqlite supports the standard `?_pragma=` DSN params, but
	// we prefer issuing PRAGMAs explicitly so failures surface with a
	// clear error rather than a silent default.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := applyPragmas(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// applyPragmas issues the three PRAGMAs every connection in the pool
// must honor. We rely on the fact that modernc/sqlite holds a single
// underlying connection per pool entry — PRAGMAs persist for that
// connection's lifetime. We also set max-open-conns=1 so write
// serialization is enforced regardless of caller concurrency (SQLite
// does not benefit from a multi-connection pool for writes anyway).
func applyPragmas(db *sql.DB) error {
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	pragmas := []string{
		`PRAGMA foreign_keys = ON`,
		`PRAGMA journal_mode = WAL`,
		`PRAGMA busy_timeout = 5000`,
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			return fmt.Errorf("store: %s: %w", p, err)
		}
	}
	return nil
}

// Migrate runs every pending migration against an already-open database.
// It is idempotent — `migrate.ErrNoChange` is treated as success so this
// can be called on every process startup (D15) without spamming logs.
//
// Returns the migration version that was active *before* this call so
// the migrate subcommand can print "applied N → M" diagnostics. Returns
// (0, nil) on a fresh database with no prior version.
func Migrate(db *sql.DB) (preVersion uint, err error) {
	src, err := iofs.New(migrations.FS, migrations.Subdir)
	if err != nil {
		return 0, fmt.Errorf("store: load embedded migrations: %w", err)
	}
	drv, err := migratesqlite.WithInstance(db, &migratesqlite.Config{})
	if err != nil {
		return 0, fmt.Errorf("store: migrate driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "sqlite", drv)
	if err != nil {
		return 0, fmt.Errorf("store: migrate instance: %w", err)
	}

	v, _, verr := m.Version()
	if verr != nil && !errors.Is(verr, migrate.ErrNilVersion) {
		return 0, fmt.Errorf("store: read migrate version: %w", verr)
	}
	preVersion = v

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return preVersion, fmt.Errorf("store: migrate up: %w", err)
	}
	return preVersion, nil
}

// OpenAndMigrate is the one-call helper most callers want: open, set
// PRAGMAs, run migrations, return the *sql.DB. Used by the agent
// bootstrap and the `mini-agent migrate` subcommand alike.
func OpenAndMigrate(path string) (*sql.DB, error) {
	db, err := Open(path)
	if err != nil {
		return nil, err
	}
	if _, err := Migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// EnsureDir creates the parent directory of `path` if it doesn't exist.
// SQLite happily creates the .db file but bails on missing parents, so
// callers (config Loader, migrate command) typically run this first.
func EnsureDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	return ensureDir(dir)
}
