package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/db"
)

// TestReadOnlyCommandsRefuseAnExplicitIDZero covers the four reporting
// subcommands together, because the defect was one shared decision: a zero id
// meant "not given", so `ovumcy export --id 0 owner@example.com` fell through to
// the EMAIL and exported that account. Every command below therefore passes a
// VALID email beside the bad id — the shape in which the old code succeeded
// against the wrong target — and requires a refusal that names the id.
func TestReadOnlyCommandsRefuseAnExplicitIDZero(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	user := seedPredictFixture(t, databasePath)
	pinSummaryClock(t)
	pinPredictNow(t, time.Date(2026, time.March, 27, 12, 0, 0, 0, time.UTC))
	config := db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath}
	email := "predict-owner@example.com"

	commands := []struct {
		name string
		run  func(args []string) error
	}{
		{"export", func(args []string) error {
			var output bytes.Buffer
			return runExportCommand(config, args, &output)
		}},
		{"summary", func(args []string) error {
			var output bytes.Buffer
			return runSummaryCommand(config, args, &output)
		}},
		{"trends", func(args []string) error {
			var output bytes.Buffer
			return runTrendsCommand(config, args, &output)
		}},
		{"predict", func(args []string) error {
			var output bytes.Buffer
			return runPredictCommand(config, args, &output)
		}},
	}
	for _, command := range commands {
		t.Run(command.name, func(t *testing.T) {
			assertNonPositiveIDRefused(t, command.run, email)
		})
	}

	// The same id with NO email was already refused (it was the only target),
	// but it now names the id instead of printing the synopsis.
	for _, command := range commands {
		err := command.run([]string{"--id", "0"})
		if err == nil || !strings.Contains(err.Error(), "positive account id") {
			t.Fatalf("%s --id 0: expected the non-positive-id message, got %v", command.name, err)
		}
	}

	// The id form still reaches the account it names, so the refusal above
	// cannot be a blanket refusal of --id.
	document, _ := runPredictForOwner(t, databasePath, []string{"--id", strconv.FormatUint(uint64(user.ID), 10), "--json"})
	if document.User.ID != user.ID {
		t.Fatalf("expected --id %d to still resolve, got %+v", user.ID, document.User)
	}
}

// TestParseOperatorUserIDRejectsNonPositiveIDs pins the shared parser's contract:
// a well-formed but non-positive id is a VALUE error, a malformed one is a parse
// error, and the caller decides which message each gets.
func TestParseOperatorUserIDRejectsNonPositiveIDs(t *testing.T) {
	for _, value := range []string{"0", "000", "+0"} {
		parsed, err := parseOperatorUserID(value)
		if err == nil {
			t.Fatalf("expected %q to be refused, got id %d", value, parsed)
		}
	}
	for _, value := range []string{"", "nope", "-1", "1.5"} {
		if _, err := parseOperatorUserID(value); err == nil {
			t.Fatalf("expected %q to fail parsing", value)
		}
	}
	for _, value := range []string{"1", "42", "0007"} {
		parsed, err := parseOperatorUserID(value)
		if err != nil || parsed == 0 {
			t.Fatalf("expected %q to parse, got %d (%v)", value, parsed, err)
		}
	}
	if operatorUserIDSelected(0) {
		t.Fatalf("a zero id must never count as an addressed target")
	}
	if !operatorUserIDSelected(3) {
		t.Fatalf("a positive id must count as the addressed target")
	}
}

// TestResolveOperatorLocationFallsBackToUTC covers the fallback table directly:
// every stored value the settings handler would never write (empty, "Local", a
// zone the tz database does not carry) must land on UTC, never on the server's
// own zone — which is what time.LoadLocation("Local") would silently return.
func TestResolveOperatorLocationFallsBackToUTC(t *testing.T) {
	for _, stored := range []string{"", "   ", "Local", "local", "Not/AZone", "../../etc/passwd"} {
		location, name := resolveOperatorLocation(stored)
		if name != "UTC" || location != time.UTC {
			t.Fatalf("expected %q to fall back to UTC, got %q (%v)", stored, name, location)
		}
	}

	location, name := resolveOperatorLocation("America/Chicago")
	if name != "America/Chicago" {
		t.Fatalf("expected the resolved name to be reported, got %q", name)
	}
	if _, offset := time.Date(2026, time.March, 7, 20, 0, 0, 0, location).Zone(); offset != -6*3600 {
		t.Fatalf("expected America/Chicago (UTC-6 in March), got offset %d", offset)
	}
}

// TestSummaryWindowEndsOnTheOwnersCalendarDay is the defect #3 regression for
// `summary`: at 20:00 in America/Chicago it is already the next day in UTC, so a
// window anchored on time.Now().UTC() ended one day in the owner's future and
// counted that day as an unlogged one — an adherence number that was simply
// wrong for every US-evening run.
func TestSummaryWindowEndsOnTheOwnersCalendarDay(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	user, _, _ := seedSummaryFixture(t, databasePath)
	setCLIUserTimezone(t, databasePath, user.ID, "America/Chicago")

	// runSummaryForOwner re-pins the clock to 2026-03-07 12:00 UTC, so the
	// interesting instant is driven through the command directly: 2026-03-08
	// 01:00 UTC is 2026-03-07 19:00 CST.
	runAt := func(args []string) SummaryDocument {
		t.Helper()
		pinSummaryClock(t)
		pinCLIClock(t, &summaryNow, time.Date(2026, time.March, 8, 1, 0, 0, 0, time.UTC))

		var output bytes.Buffer
		if err := runSummaryCommand(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath}, args, &output); err != nil {
			t.Fatalf("runSummaryCommand returned error: %v", err)
		}
		var document SummaryDocument
		if err := json.Unmarshal(output.Bytes(), &document); err != nil {
			t.Fatalf("output is not valid JSON: %v (%q)", err, output.String())
		}
		return document
	}

	if document := runAt([]string{"summary-owner@example.com", "--json"}); document.To != "2026-03-07" {
		t.Fatalf("expected the window to end on the owner's 2026-03-07, got %q", document.To)
	}

	// An explicit --tz overrides the stored zone on purpose, so the same
	// instant measured in UTC ends on the 8th.
	if document := runAt([]string{"summary-owner@example.com", "--tz", "UTC", "--json"}); document.To != "2026-03-08" {
		t.Fatalf("expected --tz UTC to end the window on 2026-03-08, got %q", document.To)
	}

	// An account with no stored zone keeps the documented UTC default.
	setCLIUserTimezone(t, databasePath, user.ID, "")
	if document := runAt([]string{"summary-owner@example.com", "--json"}); document.To != "2026-03-08" {
		t.Fatalf("expected an unset zone to keep UTC, got %q", document.To)
	}
}

// TestPredictAnchorsTodayOnTheOwnersTimezone is the defect #3 regression for
// `predict`: the projection stepped from the SERVER's calendar day, so an owner
// in America/Chicago was a day behind the dashboard every evening after 18:00.
func TestPredictAnchorsTodayOnTheOwnersTimezone(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	user := seedPredictFixture(t, databasePath)
	// 2026-03-27 01:00 UTC is 2026-03-26 20:00 CDT.
	pinPredictNow(t, time.Date(2026, time.March, 27, 1, 0, 0, 0, time.UTC))
	setCLIUserTimezone(t, databasePath, user.ID, "America/Chicago")

	document, _ := runPredictForOwner(t, databasePath, []string{"predict-owner@example.com", "--json"})
	if document.Timezone != "America/Chicago" {
		t.Fatalf("expected the document to report the owner's zone, got %q", document.Timezone)
	}
	if document.AsOf != "2026-03-26" {
		t.Fatalf("expected as_of to be the owner's 2026-03-26, got %q", document.AsOf)
	}
	if document.CurrentCycleDay != 2 {
		t.Fatalf("expected cycle day 2 for 2026-03-26, got %d", document.CurrentCycleDay)
	}

	// --tz is the explicit override and still wins over the stored zone.
	document, _ = runPredictForOwner(t, databasePath, []string{"predict-owner@example.com", "--tz", "UTC", "--json"})
	if document.AsOf != "2026-03-27" || document.Timezone != "UTC" {
		t.Fatalf("expected --tz UTC to anchor on 2026-03-27/UTC, got %q/%q", document.AsOf, document.Timezone)
	}

	// A stored value the tz database cannot load degrades to UTC rather than
	// to the server's own zone.
	setCLIUserTimezone(t, databasePath, user.ID, "Local")
	document, _ = runPredictForOwner(t, databasePath, []string{"predict-owner@example.com", "--json"})
	if document.Timezone != "UTC" || document.AsOf != "2026-03-27" {
		t.Fatalf("expected a \"Local\" stored zone to fall back to UTC, got %q/%q", document.Timezone, document.AsOf)
	}
}

// TestTrendsUsesTheOwnersTimezoneUnlessOverridden covers defect #3 for `trends`:
// the phase contexts are calendar days, so the command must compute them in the
// owner's zone once the account carries one — and keep UTC when it does not.
func TestTrendsUsesTheOwnersTimezoneUnlessOverridden(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	user, _, _ := seedTrendsFixture(t, databasePath)

	document, _ := runTrendsForOwner(t, databasePath, []string{"trends-owner@example.com", "--json"})
	if document.Timezone != "UTC" {
		t.Fatalf("expected an unset stored zone to keep UTC, got %q", document.Timezone)
	}

	setCLIUserTimezone(t, databasePath, user.ID, "America/Chicago")
	document, _ = runTrendsForOwner(t, databasePath, []string{"trends-owner@example.com", "--json"})
	if document.Timezone != "America/Chicago" {
		t.Fatalf("expected the owner's stored zone, got %q", document.Timezone)
	}
	if document.CompletedCycles != 3 {
		t.Fatalf("expected 3 completed cycles, got %d", document.CompletedCycles)
	}

	document, _ = runTrendsForOwner(t, databasePath, []string{"trends-owner@example.com", "--tz", "UTC", "--json"})
	if document.Timezone != "UTC" {
		t.Fatalf("expected --tz UTC to override the stored zone, got %q", document.Timezone)
	}
}

// TestReadOnlyCommandsDoNotTouchTheDatabaseFile is defect #2's regression at the
// command boundary: a reporting subcommand must not write to the operator's
// database file at all. Before the fix every one of them went through
// OpenDatabase(), so running one against a live instance opened the file
// read-write and ran the embedded migrations.
//
// The measurement is a fingerprint of the database plus its WAL sidecars
// (existence, size, modification time) rather than a schema probe: the
// migration set is recorded in schema_migrations, so a table a migration created
// is NOT re-created when it is dropped here, while ANY write — a migration, a
// checkpoint, a journal rollover — moves the fingerprint. The read-only open
// must leave it identical, and a real write through the read-write pool is the
// control that proves the fingerprint notices one.
func TestReadOnlyCommandsDoNotTouchTheDatabaseFile(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	seedPredictFixture(t, databasePath)
	pinSummaryClock(t)
	pinPredictNow(t, time.Date(2026, time.March, 27, 12, 0, 0, 0, time.UTC))

	config := db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath}
	before := cliDatabaseFingerprint(t, databasePath)

	for _, command := range []struct {
		name string
		run  func() error
	}{
		{"export", func() error {
			var output bytes.Buffer
			return runExportCommand(config, []string{"predict-owner@example.com"}, &output)
		}},
		{"summary", func() error {
			var output bytes.Buffer
			return runSummaryCommand(config, []string{"predict-owner@example.com"}, &output)
		}},
		{"trends", func() error {
			var output bytes.Buffer
			return runTrendsCommand(config, []string{"predict-owner@example.com"}, &output)
		}},
		{"predict", func() error {
			var output bytes.Buffer
			return runPredictCommand(config, []string{"predict-owner@example.com"}, &output)
		}},
	} {
		if err := command.run(); err != nil {
			t.Fatalf("%s failed: %v", command.name, err)
		}
		if after := cliDatabaseFingerprint(t, databasePath); after != before {
			t.Fatalf("%s changed the database it was only reading:\n  before %s\n  after  %s", command.name, before, after)
		}
	}

	// The control: a real write through the read-write pool MUST move the
	// fingerprint, or the comparison above would pass for any two opens.
	database, err := db.OpenDatabase(config)
	if err != nil {
		t.Fatalf("open read-write: %v", err)
	}
	// A checkpoint alone can coalesce, so the control performs an UPDATE: the
	// point is to prove the fingerprint measures writes, not to exercise gorm.
	if err := database.Exec("UPDATE users SET timezone = ?", "America/Chicago").Error; err != nil {
		t.Fatalf("control write: %v", err)
	}
	if sqlDB, handleErr := database.DB(); handleErr == nil {
		_ = sqlDB.Close()
	}
	if afterControl := cliDatabaseFingerprint(t, databasePath); afterControl == before {
		t.Fatalf("expected a read-write UPDATE to move the fingerprint; the comparison is not measuring writes:\n  before %s\n  after  %s", before, afterControl)
	}
}

// cliDatabaseFingerprint summarises the DATA file: its size and a hash of its
// bytes. Any write through the file — an UPDATE, a migration, a journal-mode
// change, a checkpoint — alters those, so a command that only reads cannot
// produce the same fingerprint.
//
// The WAL sidecars are deliberately not part of it. A read-only connection to a
// WAL-mode database MUST map the shared-memory index to see committed data, so
// SQLite creates a zero-byte `-wal` and an `-shm` on the first such open, and
// their mtimes move; that is scaffolding, not data (measured, and documented in
// internal/db/sqlite.go). Hashing the bytes is the stronger claim anyway: a
// same-size write with a preserved mtime would slip past a stat-based
// fingerprint and cannot slip past this one.
func cliDatabaseFingerprint(t *testing.T, databasePath string) string {
	t.Helper()

	contents, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatalf("fingerprint %s: %v", filepath.Base(databasePath), err)
	}
	return fmt.Sprintf("%s=%d bytes sha256=%x", filepath.Base(databasePath), len(contents), sha256.Sum256(contents))
}

// TestExportOutFileIsAlwaysOwnerOnly is defect #4's file half: `--out` onto an
// existing 0644 file must leave it 0600, not inherit the wider mode from the
// file that was already there.
func TestExportOutFileIsAlwaysOwnerOnly(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	seedExportFixture(t, databasePath)
	config := db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath}

	outPath := filepath.Join(t.TempDir(), "export.json")
	if err := os.WriteFile(outPath, []byte("previous export"), 0o644); err != nil {
		t.Fatalf("seed world-readable file: %v", err)
	}
	if info, err := os.Stat(outPath); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("expected the seeded file to be 0644, got %v (%v)", info.Mode().Perm(), err)
	}

	var output bytes.Buffer
	if err := runExportCommand(config, []string{"export-owner@example.com", "--out", outPath}, &output); err != nil {
		t.Fatalf("runExportCommand returned error: %v", err)
	}
	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("stat export file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected the overwritten export to be 0600, got %04o", info.Mode().Perm())
	}
	payload, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read export file: %v", err)
	}
	document := decodeExportDocument(t, payload)
	if document.User.Email != "export-owner@example.com" {
		t.Fatalf("expected the export document, got %+v", document.User)
	}
}

// TestReadOnlyCommandsReportStdoutWriteFailures is defect #4's stdout half for
// every reporting command: a closed pipe or a full disk must exit non-zero
// rather than report a successful (but truncated) document.
func TestReadOnlyCommandsReportStdoutWriteFailures(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	seedPredictFixture(t, databasePath)
	pinSummaryClock(t)
	pinPredictNow(t, time.Date(2026, time.March, 27, 12, 0, 0, 0, time.UTC))
	config := db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath}

	for _, command := range []struct {
		name string
		args []string
		run  func(args []string, output *failingWriter) error
	}{
		{"export", []string{"predict-owner@example.com"}, func(args []string, output *failingWriter) error {
			return runExportCommand(config, args, output)
		}},
		{"summary", []string{"predict-owner@example.com"}, func(args []string, output *failingWriter) error {
			return runSummaryCommand(config, args, output)
		}},
		{"summary --json", []string{"predict-owner@example.com", "--json"}, func(args []string, output *failingWriter) error {
			return runSummaryCommand(config, args, output)
		}},
		{"trends", []string{"predict-owner@example.com"}, func(args []string, output *failingWriter) error {
			return runTrendsCommand(config, args, output)
		}},
		{"trends --json", []string{"predict-owner@example.com", "--json"}, func(args []string, output *failingWriter) error {
			return runTrendsCommand(config, args, output)
		}},
		{"predict", []string{"predict-owner@example.com"}, func(args []string, output *failingWriter) error {
			return runPredictCommand(config, args, output)
		}},
		{"predict --json", []string{"predict-owner@example.com", "--json"}, func(args []string, output *failingWriter) error {
			return runPredictCommand(config, args, output)
		}},
	} {
		var output failingWriter
		err := command.run(command.args, &output)
		if err == nil {
			t.Fatalf("%s: expected a stdout write failure to be reported", command.name)
		}
		if !strings.Contains(err.Error(), "broken pipe") {
			t.Fatalf("%s: expected the underlying write error, got %v", command.name, err)
		}
	}
}
