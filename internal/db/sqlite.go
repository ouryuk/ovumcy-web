package db

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func openSQLiteConnection(dbPath string) (*gorm.DB, error) {
	return openSQLiteConnectionWith(dbPath, sqliteReadWrite)
}

// sqliteOpenMode selects the DSN a SQLite connection is opened with.
//
// Two modes exist and the difference is a SAFETY property, not a preference:
// the read-write DSN below turns every connection of the pool into a potential
// writer (BEGIN IMMEDIATE, WAL journal, foreign keys), which is exactly what a
// server wants and exactly what a subcommand that only reads must not do to a
// live instance's database file.
//
// It is a typed constant rather than a bool so a call site cannot be misread as
// "not read-only" when it meant "also migrate" (OpenDatabase and
// OpenDatabaseReadOnly differ in BOTH, and only one of them is about the file).
type sqliteOpenMode bool

const (
	// sqliteReadWrite is the server's DSN: the file is created if missing and
	// every pooled connection may begin a write transaction.
	sqliteReadWrite sqliteOpenMode = false
	// sqliteReadOnly is the reporting commands' DSN: the file must exist and
	// SQLite itself refuses every write, including the journal-mode change.
	sqliteReadOnly sqliteOpenMode = true
)

func openSQLiteConnectionWith(dbPath string, mode sqliteOpenMode) (*gorm.DB, error) {
	if mode == sqliteReadOnly {
		return openSQLiteReadOnlyConnection(dbPath)
	}

	if err := os.MkdirAll(filepath.Dir(dbPath), 0o750); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	// glebarez/modernc honors only the `_pragma=NAME(VALUE)` DSN form; the
	// mattn-style `_foreign_keys=on&_busy_timeout=5000` is silently ignored,
	// leaving foreign_keys OFF (ON DELETE CASCADE never fires for pooled CRUD
	// connections) and the journal in rollback mode. Use the `_pragma` form so
	// every connection in the pool enforces FKs, waits on a busy lock, and runs
	// in WAL. Regression: sqlite_pragma_test.go.
	//
	// `_txlock=immediate` makes every write transaction open with BEGIN
	// IMMEDIATE. Without it, GORM's deferred write transactions take a read
	// snapshot (the day upsert's SELECT) then upgrade to a write lock; under WAL
	// a concurrent writer turns that upgrade into SQLITE_BUSY_SNAPSHOT (extended
	// code 517), which SQLite fails IMMEDIATELY without invoking the busy handler
	// — so busy_timeout never engages and concurrent day writes surface as 500s.
	// BEGIN IMMEDIATE acquires the write lock up front, so contending writers
	// queue on busy_timeout instead. Regression: sqlite_concurrency_test.go.
	dsn := fmt.Sprintf("%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate", dbPath)
	database, err := gorm.Open(sqlite.Open(dsn), newGORMConfig(os.Stdout))
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if err := capSQLitePool(database); err != nil {
		return nil, err
	}

	return database, nil
}

// openSQLiteReadOnlyConnection opens an EXISTING SQLite file for readers only.
//
// Why this is not just a DSN flag on the connection above: an operator
// subcommand that reads one owner's record (`export`, `summary`, `trends`,
// `predict`) used to reach the database through the server's own connection
// settings, so merely running it against a live instance opened the file
// read-write, switched a not-yet-WAL file INTO WAL mode (creating `-wal`/`-shm`
// files under the server's data dir), and then ran the embedded migrations —
// i.e. a read-only command could rewrite the operator's database.
//
// The DSN below is the same file with `mode=ro`: SQLite refuses to create the
// file, refuses to create `-wal`/`-shm` where they do not exist, and every
// write statement fails with SQLITE_READONLY instead of silently succeeding.
// `_txlock=deferred` matches that: a read transaction never asks for a write
// lock, so a concurrent server writer cannot contend with a long export pass
// the way BEGIN IMMEDIATE would. busy_timeout stays because a reader must wait
// out the writer's commit rather than fail.
//
// There is deliberately no journal_mode pragma and no foreign_keys pragma: the
// first is a write (setting it on a rollback-journal file is exactly the
// mutation above), and the second only governs writes, which cannot happen.
//
// The DSN spelling is not interchangeable: the driver hands anything starting
// with `file:` to SQLite UNTOUCHED (it is opened SQLITE_OPEN_URI), and SQLite
// then splits the URI at the first `?` and reads the path LITERALLY — no
// percent-decoding. A file name containing `%`, `+` or `#` therefore has to be
// written as-is, never escaped: escaping it would look for the wrong file
// (`%` → `%25`). (Paths containing a `?` outright are outside this form; the
// read-write DSN above has the same limit and no database path may contain
// one.)
func openSQLiteReadOnlyConnection(dbPath string) (*gorm.DB, error) {
	dsn := "file:" + dbPath + "?mode=ro&_pragma=busy_timeout(5000)&_txlock=deferred"
	database, err := gorm.Open(sqlite.Open(dsn), newGORMConfig(os.Stdout))
	if err != nil {
		return nil, fmt.Errorf("open sqlite read-only: %w", err)
	}

	if err := capSQLitePool(database); err != nil {
		return nil, err
	}

	return database, nil
}

// capSQLitePool caps the pool for SQLite. WAL mode supports concurrent reads
// but has a single writer; an unbounded pool causes unnecessary writer
// contention and goroutine churn. 4 conns cover the typical single-user
// workload with headroom for background tasks.
func capSQLitePool(database *gorm.DB) error {
	sqlDB, err := database.DB()
	if err != nil {
		return fmt.Errorf("get sqlite sql.DB: %w", err) // codecov:ignore -- database.DB() does not fail after a successful gorm.Open
	}
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(4)
	sqlDB.SetConnMaxLifetime(time.Hour)
	return nil
}
