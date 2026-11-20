package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/db"
	"github.com/ovumcy/ovumcy-web/internal/models"
)

// seedExportFixture seeds one owner with a full record: a custom symptom, a
// daily log that carries the symptom, a cycle-start marker, and a period day —
// the round-trip `export` must reproduce.
func seedExportFixture(t *testing.T, databasePath string) models.User {
	t.Helper()

	user := createCLIUsersUser(t, databasePath, "export-owner@example.com", "Export Owner", models.RoleOwner, true, time.Date(2026, time.March, 1, 8, 0, 0, 0, time.UTC))

	database, err := db.OpenDatabase(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatalf("open sql db: %v", err)
	}
	defer func() { _ = sqlDB.Close() }()

	symptom := models.SymptomType{
		UserID:    user.ID,
		Name:      "Cramps Custom",
		Icon:      "🩸",
		Color:     "#FF4444",
		IsBuiltin: false,
	}
	if err := database.Create(&symptom).Error; err != nil {
		t.Fatalf("create symptom: %v", err)
	}

	bbt := 36.6
	logEntry := models.DailyLog{
		UserID:          user.ID,
		Date:            time.Date(2026, time.March, 10, 0, 0, 0, 0, time.UTC),
		IsPeriod:        true,
		CycleStart:      true,
		IsUncertain:     true,
		Flow:            models.FlowMedium,
		Mood:            2,
		SexActivity:     models.SexActivityProtected,
		BBT:             &bbt,
		CervicalMucus:   models.CervicalMucusCreamy,
		PregnancyTest:   models.PregnancyTestNegative,
		CycleFactorKeys: []string{models.CycleFactorStress},
		SymptomIDs:      []uint{symptom.ID},
		Notes:           "export fixture note",
	}
	if err := database.Create(&logEntry).Error; err != nil {
		t.Fatalf("create daily log: %v", err)
	}
	return user
}

func decodeExportDocument(t *testing.T, payload []byte) ExportDocument {
	t.Helper()
	var document ExportDocument
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatalf("decode export document: %v", err)
	}
	return document
}

func TestRunExportCommandRoundTripsSeededFixtureToStdout(t *testing.T) {
	t.Parallel()

	databasePath := createCLIUsersDatabase(t)
	user := seedExportFixture(t, databasePath)

	var output bytes.Buffer
	err := runExportCommand(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath}, []string{"export-owner@example.com"}, &output)
	if err != nil {
		t.Fatalf("runExportCommand returned error: %v", err)
	}

	var document ExportDocument
	if err := json.Unmarshal(output.Bytes(), &document); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if document.Schema != exportSchemaVersion {
		t.Fatalf("expected schema %q, got %q", exportSchemaVersion, document.Schema)
	}
	if document.User.ID != user.ID || document.User.Email != "export-owner@example.com" {
		t.Fatalf("unexpected user in export: %+v", document.User)
	}
	if len(document.DailyLogs) != 1 || len(document.SymptomTypes) != 1 {
		t.Fatalf("expected 1 log and 1 symptom, got %d and %d", len(document.DailyLogs), len(document.SymptomTypes))
	}

	logEntry := document.DailyLogs[0]
	if logEntry.Date != "2026-03-10" || !logEntry.IsPeriod || !logEntry.CycleStart || !logEntry.IsUncertain {
		t.Fatalf("unexpected daily log export: %+v", logEntry)
	}
	if logEntry.Flow != models.FlowMedium || logEntry.SexActivity != models.SexActivityProtected {
		t.Fatalf("unexpected enum values: %+v", logEntry)
	}
	if logEntry.BBT == nil || *logEntry.BBT != 36.6 {
		t.Fatalf("unexpected bbt: %+v", logEntry.BBT)
	}
	if len(logEntry.SymptomIDs) != 1 || len(logEntry.CycleFactorKeys) != 1 || logEntry.CycleFactorKeys[0] != models.CycleFactorStress {
		t.Fatalf("unexpected relations: %+v", logEntry)
	}
	if logEntry.Notes != "export fixture note" {
		t.Fatalf("unexpected notes: %q", logEntry.Notes)
	}

	symptom := document.SymptomTypes[0]
	if symptom.Name != "Cramps Custom" || symptom.IsBuiltin {
		t.Fatalf("unexpected symptom export: %+v", symptom)
	}
	if len(document.Cycles) != 1 || document.Cycles[0].Date != "2026-03-10" {
		t.Fatalf("expected one cycle marker on 2026-03-10, got %+v", document.Cycles)
	}

	// No auth material may ever reach the payload.
	rendered := output.String()
	for _, secret := range []string{"StrongPass1", "$2a$", "recovery", "password_hash", "webhook"} {
		if strings.Contains(strings.ToLower(rendered), strings.ToLower(secret)) {
			t.Fatalf("export leaked %q in payload: %s", secret, rendered)
		}
	}
	// Indented output, one claim of indentation deep.
	if !strings.Contains(rendered, "\n  \"daily_logs\": [") {
		t.Fatalf("expected indented JSON, got: %s", rendered)
	}
}

func TestRunExportCommandByIDAddressingAndOutFileMode(t *testing.T) {
	t.Parallel()

	databasePath := createCLIUsersDatabase(t)
	user := seedExportFixture(t, databasePath)

	outPath := filepath.Join(t.TempDir(), "export.json")
	var output bytes.Buffer
	err := runExportCommand(
		db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath},
		[]string{"--id", strconv.FormatUint(uint64(user.ID), 10), "--out", outPath},
		&output,
	)
	if err != nil {
		t.Fatalf("runExportCommand(--id, --out) returned error: %v", err)
	}

	if output.Len() != 0 {
		t.Fatalf("expected nothing on stdout when --out is given, got %q", output.String())
	}
	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("stat export file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("expected export file mode 0600, got %#o", perm)
	}

	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read export file: %v", err)
	}
	document := decodeExportDocument(t, raw)
	if document.User.ID != user.ID || len(document.DailyLogs) != 1 {
		t.Fatalf("unexpected file export: %+v", document)
	}
}

func TestRunExportCommandUnknownUserReturnsAdvice(t *testing.T) {
	t.Parallel()

	databasePath := createCLIUsersDatabase(t)
	seedExportFixture(t, databasePath)

	var output bytes.Buffer
	err := runExportCommand(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath}, []string{"nobody@example.com"}, &output)
	if err == nil || !strings.Contains(err.Error(), "user nobody@example.com not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("expected no output on failure, got %q", output.String())
	}
}

func TestRunExportCommandUnknownIDReturnsAdvice(t *testing.T) {
	t.Parallel()

	databasePath := createCLIUsersDatabase(t)
	seedExportFixture(t, databasePath)

	var output bytes.Buffer
	err := runExportCommand(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath}, []string{"--id", "9999"}, &output)
	if err == nil || !strings.Contains(err.Error(), "no account carries id 9999") {
		t.Fatalf("expected not-found-by-id error, got %v", err)
	}
}

func TestRunExportCommandEmptyUserRendersEmptyCollections(t *testing.T) {
	t.Parallel()

	databasePath := createCLIUsersDatabase(t)
	createCLIUsersUser(t, databasePath, "empty@example.com", "Empty", models.RoleOwner, false, time.Date(2026, time.March, 2, 9, 0, 0, 0, time.UTC))

	var output bytes.Buffer
	err := runExportCommand(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath}, []string{"empty@example.com"}, &output)
	if err != nil {
		t.Fatalf("runExportCommand returned error: %v", err)
	}

	document := decodeExportDocument(t, output.Bytes())
	if document.User.Email != "empty@example.com" {
		t.Fatalf("unexpected user: %+v", document.User)
	}
	if document.DailyLogs == nil || document.SymptomTypes == nil || document.Cycles == nil {
		t.Fatalf("expected empty collections (not null) for an empty account: %+v", document)
	}
	if len(document.DailyLogs) != 0 || len(document.SymptomTypes) != 0 || len(document.Cycles) != 0 {
		t.Fatalf("expected zero-length collections, got %+v", document)
	}
}

func TestRunExportCommandArgumentErrorsUseStableUsage(t *testing.T) {
	t.Parallel()

	databasePath := createCLIUsersDatabase(t)
	config := db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath}

	for _, args := range [][]string{
		nil,
		{},
		{"--out", "x.json"},
		{"--bogus"},
		{"--id", "not-a-number"},
		{"a@example.com", "b@example.com"},
	} {
		var output bytes.Buffer
		err := runExportCommand(config, args, &output)
		if err == nil || !strings.Contains(err.Error(), "usage: ovumcy export") {
			t.Fatalf("expected usage error for %v, got %v", args, err)
		}
	}
}

func TestRunExportCommandEqualsFormOutAndUserSummaryShape(t *testing.T) {
	t.Parallel()

	databasePath := createCLIUsersDatabase(t)
	user := seedExportFixture(t, databasePath)

	outPath := filepath.Join(t.TempDir(), "export.json")
	var output bytes.Buffer
	err := runExportCommand(
		db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath},
		[]string{"--out=" + outPath, "export-owner@example.com"},
		&output,
	)
	if err != nil {
		t.Fatalf("runExportCommand(--out=path) returned error: %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("expected nothing on stdout with --out=path, got %q", output.String())
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read export file: %v", err)
	}

	// The user block must carry the account's identity fields the operator
	// can audit, not just the id/email pair.
	document := decodeExportDocument(t, raw)
	if document.User.Role != models.RoleOwner || !document.User.OnboardingCompleted {
		t.Fatalf("unexpected user summary: %+v", document.User)
	}
	if document.User.CreatedAt != user.CreatedAt.UTC().Format(time.RFC3339) {
		t.Fatalf("unexpected created_at: %q", document.User.CreatedAt)
	}
	if document.User.DisplayName != "Export Owner" {
		t.Fatalf("unexpected display_name: %q", document.User.DisplayName)
	}
	// Nil relations serialise as [] never null, and non-nil ones round-trip.
	for _, fragment := range []string{`"cycle_factor_keys": [`, `"symptom_ids": [`} {
		if !strings.Contains(string(raw), fragment) {
			t.Fatalf("payload missing %q:\n%s", fragment, raw)
		}
	}
}

func TestRunExportCommandArchivedSymptomRoundTrips(t *testing.T) {
	t.Parallel()

	databasePath := createCLIUsersDatabase(t)
	seedExportFixture(t, databasePath)

	database, err := db.OpenDatabase(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatalf("open sql db: %v", err)
	}
	archivedAt := time.Date(2026, time.March, 20, 12, 0, 0, 0, time.UTC)
	archived := models.SymptomType{UserID: 1, Name: "Old Ache", ArchivedAt: &archivedAt}
	if err := database.Create(&archived).Error; err != nil {
		t.Fatalf("create archived symptom: %v", err)
	}
	_ = sqlDB.Close()

	var output bytes.Buffer
	err = runExportCommand(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath}, []string{"export-owner@example.com"}, &output)
	if err != nil {
		t.Fatalf("runExportCommand returned error: %v", err)
	}
	document := decodeExportDocument(t, output.Bytes())
	if len(document.SymptomTypes) != 2 {
		t.Fatalf("expected 2 symptoms incl. archived, got %+v", document.SymptomTypes)
	}
	var found *exportSymptomType
	for index := range document.SymptomTypes {
		if document.SymptomTypes[index].Name == "Old Ache" {
			found = &document.SymptomTypes[index]
		}
	}
	if found == nil || found.ArchivedAt == nil || !found.ArchivedAt.Equal(archivedAt) {
		t.Fatalf("archived symptom did not round-trip: %+v", found)
	}
}

func TestRunExportCommandOutPathFailureIsReported(t *testing.T) {
	t.Parallel()

	databasePath := createCLIUsersDatabase(t)
	seedExportFixture(t, databasePath)

	// A directory cannot be opened O_TRUNC as a file, so the write must fail
	// with the command's own "write export" wording — not silently succeed.
	var output bytes.Buffer
	err := runExportCommand(
		db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath},
		[]string{"--out", t.TempDir(), "export-owner@example.com"},
		&output,
	)
	if err == nil || !strings.Contains(err.Error(), "write export") {
		t.Fatalf("expected write-export error for a directory --out, got %v", err)
	}
}

func TestRunExportCommandArgumentErrorsExtended(t *testing.T) {
	t.Parallel()

	databasePath := createCLIUsersDatabase(t)
	config := db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath}

	for _, args := range [][]string{
		{"--out"},                 // value form with nothing after it
		{"Owner <a@example.com>"}, // display-name form the CLI refuses up front
		{"--id=abc"},
		{"a@example.com", "--bogus"},
	} {
		var output bytes.Buffer
		err := runExportCommand(config, args, &output)
		if err == nil {
			t.Fatalf("expected error for %v, got none", args)
		}
		if !strings.Contains(err.Error(), "usage: ovumcy export") &&
			!strings.Contains(err.Error(), "invalid email address") {
			t.Fatalf("expected usage/invalid-email error for %v, got %v", args, err)
		}
	}
}
