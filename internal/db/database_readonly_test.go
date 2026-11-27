package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ovumcy/ovumcy-web/internal/models"
)

// TestOpenDatabaseReadOnlyRefusesWritesAndLeavesTheFileAlone pins the two
// properties the read-only entry point exists for: a reporting subcommand can
// read an existing SQLite database, and it cannot change it — not through a
// statement, not through the journal mode, not through the `-wal`/`-shm`
// sidecars, and not by creating a missing file.
func TestOpenDatabaseReadOnlyRefusesWritesAndLeavesTheFileAlone(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "readonly.db")
	readWrite, err := OpenDatabase(Config{Driver: DriverSQLite, SQLitePath: databasePath})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	user := models.User{
		DisplayName:         "Read Only Owner",
		Email:               "read-only@example.com",
		PasswordHash:        "hash",
		LocalAuthEnabled:    true,
		Role:                models.RoleOwner,
		OnboardingCompleted: true,
		CycleLength:         models.DefaultCycleLength,
		PeriodLength:        models.DefaultPeriodLength,
		AutoPeriodFill:      true,
		UsageGoal:           models.UsageGoalHealth,
	}
	if err := readWrite.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	sqlDB, err := readWrite.DB()
	if err != nil {
		t.Fatalf("open sql handle: %v", err)
	}
	// Close the writer BEFORE the read-only open, so the test measures the file
	// the way a reporting command meets it: a database a server has just been
	// running against, not a handle still holding the locks.
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	before, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatalf("read database before: %v", err)
	}

	readOnly, err := OpenDatabaseReadOnly(Config{Driver: DriverSQLite, SQLitePath: databasePath})
	if err != nil {
		t.Fatalf("open read-only database: %v", err)
	}
	readOnlySQL, err := readOnly.DB()
	if err != nil {
		t.Fatalf("read-only sql handle: %v", err)
	}

	// The read the subcommands actually perform must work.
	var found models.User
	if err := readOnly.Where("email = ?", "read-only@example.com").First(&found).Error; err != nil {
		t.Fatalf("read-only query failed: %v", err)
	}
	if found.ID != user.ID {
		t.Fatalf("expected the seeded row, got %+v", found)
	}

	// A write must be refused by SQLite itself, not merely by convention.
	if err := readOnly.Create(&models.DailyLog{UserID: user.ID, Notes: "must not land"}).Error; err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "readonly") && !strings.Contains(strings.ToLower(err.Error()), "read-only") {
			t.Fatalf("expected a read-only refusal, got %v", err)
		}
	} else {
		t.Fatalf("expected the read-only handle to refuse an insert")
	}

	after, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatalf("read database after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("the database file changed under a read-only handle (%d -> %d bytes)", len(before), len(after))
	}

	if err := readOnlySQL.Close(); err != nil {
		t.Fatalf("close read-only handle: %v", err)
	}

	// The DATA file is the thing the subcommands must not touch, and it is
	// byte-identical: no migration ran, no journal mode changed, nothing was
	// written through the handle above. That is the contract.
	//
	// What SQLite still creates beside it is a zero-byte `-wal` and a `-shm`
	// shared-memory block, and that is not something this layer can avoid: a
	// read-only connection to a WAL-mode database needs the shared-memory
	// index to see the WAL's contents at all. `mode=ro` on its own does
	// exactly the same (measured), and the two alternatives are worse:
	// `nolock=1` cannot read the file, and `immutable=1` reads a LIVE
	// database as ZERO ROWS because it ignores the WAL entirely — an export
	// that silently reports no data. Asserted below instead: the scaffolding
	// carries no data (`-wal` is empty) and the file itself is untouched.
	for _, sidecar := range []string{"-wal", "-shm"} {
		info, err := os.Stat(databasePath + sidecar)
		if err != nil {
			continue
		}
		if !info.Mode().IsRegular() {
			t.Fatalf("%s is not a regular file", filepath.Base(databasePath+sidecar))
		}
		if strings.HasSuffix(sidecar, "-wal") && info.Size() != 0 {
			t.Fatalf("the read-only handle left %d bytes in %s: it wrote a transaction", info.Size(), filepath.Base(databasePath+sidecar))
		}
	}
}

// TestOpenDatabaseReadOnlyDoesNotCreateAMissingDatabase: a reporting subcommand
// pointed at the wrong path must say the database is not there, not report an
// owner with no data from an empty file it just created. The read-write entry
// point creates the file (that is what boot does); the read-only one must not.
func TestOpenDatabaseReadOnlyDoesNotCreateAMissingDatabase(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	missingPath := filepath.Join(directory, "nope", "ovumcy.db")

	database, err := OpenDatabaseReadOnly(Config{Driver: DriverSQLite, SQLitePath: missingPath})
	if err == nil {
		if sqlDB, handleErr := database.DB(); handleErr == nil {
			_ = sqlDB.Close()
		}
		t.Fatalf("expected a read-only open of a missing database to fail")
	}
	if _, statErr := os.Stat(filepath.Dir(missingPath)); statErr == nil {
		t.Fatalf("read-only open created the missing directory %s", filepath.Dir(missingPath))
	}
}

// TestOpenDatabaseReadOnlyValidatesTheConfig keeps the read-only path subject to
// the same config contract as every other entry point, rather than accepting an
// empty driver/path pair that would silently open nothing.
func TestOpenDatabaseReadOnlyValidatesTheConfig(t *testing.T) {
	t.Parallel()

	if _, err := OpenDatabaseReadOnly(Config{Driver: DriverSQLite}); err == nil {
		t.Fatalf("expected a missing DB_PATH to be refused")
	}
	if _, err := OpenDatabaseReadOnly(Config{Driver: "mysql", SQLitePath: "x"}); err == nil {
		t.Fatalf("expected an unsupported driver to be refused")
	}
}
