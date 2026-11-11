package services

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/db"
	"github.com/ovumcy/ovumcy-web/internal/models"
)

// writeImportCSVTestFile encodes a header and rows with the same writer the
// export endpoint uses, so quoting and separators match the real download.
func writeImportCSVTestFile(t *testing.T, header []string, rows [][]string) []byte {
	t.Helper()

	var output bytes.Buffer
	writer := csv.NewWriter(&output)
	if err := writer.Write(header); err != nil {
		t.Fatalf("write csv header: %v", err)
	}
	for _, row := range rows {
		if err := writer.Write(row); err != nil {
			t.Fatalf("write csv row: %v", err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		t.Fatalf("flush csv: %v", err)
	}
	return output.Bytes()
}

func exportCSVRowsToTestFile(t *testing.T, rows []ExportCSVRow) []byte {
	t.Helper()

	records := make([][]string, 0, len(rows))
	for _, row := range rows {
		records = append(records, row.Columns())
	}
	return writeImportCSVTestFile(t, ExportCSVHeaders(), records)
}

// TestImportServiceCSVRoundTripPreservesEntries is the fidelity guarantee for
// the CSV direction: an owner's exported CSV, restored into a fresh account and
// exported again, must produce the same rows. It exercises built-in symptom
// flags, custom-symptom find-or-create, human-readable labels, BBT precision,
// and the CSV-injection apostrophe that must not become part of the note.
func TestImportServiceCSVRoundTripPreservesEntries(t *testing.T) {
	dayService, database := newDayServiceIntegration(t)
	repositories := db.NewRepositories(database)
	symptomService := NewSymptomService(repositories.Symptoms)
	exportService := NewExportService(dayService, symptomService)

	source := createDayServiceTestUser(t, database, "csv-import-source@example.com")
	if _, err := symptomService.CreateSymptomForUser(context.Background(), source.ID, "My custom", "", ""); err != nil {
		t.Fatalf("create custom symptom: %v", err)
	}
	crampsID := symptomIDByName(t, symptomService, source.ID, "Cramps")
	moodSwingsID := symptomIDByName(t, symptomService, source.ID, "Mood swings")
	customID := symptomIDByName(t, symptomService, source.ID, "My custom")

	logs := []models.DailyLog{
		{
			UserID: source.ID, Date: time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC),
			IsPeriod: true, CycleStart: true, IsUncertain: true, Flow: models.FlowHeavy, Mood: 3,
			SexActivity: models.SexActivityProtected, BBT: new(36.7), CervicalMucus: models.CervicalMucusEggWhite,
			PregnancyTest: models.PregnancyTestNegative, CycleFactorKeys: []string{models.CycleFactorStress, models.CycleFactorSleepDisruption},
			SymptomIDs: []uint{crampsID, moodSwingsID, customID}, Notes: "  =cmd|' /C calc'!A0",
		},
		{
			UserID: source.ID, Date: time.Date(2026, time.March, 5, 0, 0, 0, 0, time.UTC),
			Mood: 5, BBT: new(36.9), PregnancyTest: models.PregnancyTestPositive,
			SymptomIDs: []uint{moodSwingsID}, CycleFactorKeys: []string{},
		},
	}
	if err := database.Create(&logs).Error; err != nil {
		t.Fatalf("create source logs: %v", err)
	}

	sourceRows, err := exportService.BuildCSVRows(context.Background(), source.ID, nil, nil, time.UTC)
	if err != nil {
		t.Fatalf("export source: %v", err)
	}

	target := createDayServiceTestUser(t, database, "csv-import-target@example.com")
	importService := newImportServiceIntegration(t, database, symptomService)
	result, err := importService.ImportCSV(context.Background(), target.ID, exportCSVRowsToTestFile(t, sourceRows), time.UTC)
	if err != nil {
		t.Fatalf("import csv: %v", err)
	}
	if result.Added != 2 || result.Skipped != 0 || result.Rejected != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}

	targetRows, err := exportService.BuildCSVRows(context.Background(), target.ID, nil, nil, time.UTC)
	if err != nil {
		t.Fatalf("export target: %v", err)
	}
	if !reflect.DeepEqual(sourceRows, targetRows) {
		t.Fatalf("csv round-trip mismatch:\n source=%+v\n target=%+v", sourceRows, targetRows)
	}
}

// TestImportServiceCSVSkipsExistingDays pins the additive contract for the CSV
// path: a day the account already has is counted as skipped and its stored
// values are left exactly as they were.
func TestImportServiceCSVSkipsExistingDays(t *testing.T) {
	dayService, database := newDayServiceIntegration(t)
	repositories := db.NewRepositories(database)
	symptomService := NewSymptomService(repositories.Symptoms)

	user := createDayServiceTestUser(t, database, "csv-import-skip@example.com")
	existing := models.DailyLog{
		UserID: user.ID, Date: time.Date(2026, time.April, 10, 0, 0, 0, 0, time.UTC),
		IsPeriod: true, Flow: models.FlowLight, Notes: "original", CycleFactorKeys: []string{},
	}
	if err := database.Create(&existing).Error; err != nil {
		t.Fatalf("create existing: %v", err)
	}

	file := writeImportCSVTestFile(t, ExportCSVHeaders(), [][]string{
		importCSVRowForTest(t, "2026-04-10", "Yes", "Heavy", "incoming"),
		importCSVRowForTest(t, "2026-04-11", "No", "None", "new day"),
	})

	importService := newImportServiceIntegration(t, database, symptomService)
	result, err := importService.ImportCSV(context.Background(), user.ID, file, time.UTC)
	if err != nil {
		t.Fatalf("import csv: %v", err)
	}
	if result.Added != 1 || result.Skipped != 1 || result.Rejected != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}

	stored, err := dayService.FetchLogByDate(context.Background(), user.ID, time.Date(2026, time.April, 10, 0, 0, 0, 0, time.UTC), time.UTC)
	if err != nil {
		t.Fatalf("reload existing: %v", err)
	}
	if stored.Notes != "original" || stored.Flow != models.FlowLight {
		t.Fatalf("existing day was overwritten: %+v", stored)
	}
}

// importCSVRowForTest builds a full export-shaped row, letting a case override
// only the cells it cares about. The remaining cells are neutral values.
func importCSVRowForTest(t *testing.T, date string, period string, flow string, notes string) []string {
	t.Helper()

	row := make([]string, len(ExportCSVHeaders()))
	for i := range row {
		row[i] = "No"
	}
	set := func(header string, value string) {
		for i, name := range ExportCSVHeaders() {
			if name == header {
				row[i] = value
				return
			}
		}
		t.Fatalf("unknown export header %q", header)
	}
	set("Date", date)
	set("Period", period)
	set("Flow", flow)
	set("Notes", notes)
	return row
}

// TestImportServiceCSVAcceptsReorderedAndPaddedHeader proves the header is
// matched by name, not by position: a spreadsheet that reorders columns, and
// one that carries the prose spacing from the docs, still restores correctly.
func TestImportServiceCSVAcceptsReorderedAndPaddedHeader(t *testing.T) {
	dayService, database := newDayServiceIntegration(t)
	repositories := db.NewRepositories(database)
	symptomService := NewSymptomService(repositories.Symptoms)
	user := createDayServiceTestUser(t, database, "csv-import-header@example.com")

	headers := ExportCSVHeaders()
	reordered := make([]string, 0, len(headers))
	padded := make([]string, 0, len(headers))
	for i := len(headers) - 1; i >= 0; i-- {
		reordered = append(reordered, headers[i])
	}
	for _, header := range headers {
		padded = append(padded, " "+strings.ToUpper(header)+" ")
	}

	reorderedRow := importCSVRowForTest(t, "2026-06-02", "Yes", "Medium", "reordered")
	for i, j := 0, len(reorderedRow)-1; i < j; i, j = i+1, j-1 {
		reorderedRow[i], reorderedRow[j] = reorderedRow[j], reorderedRow[i]
	}

	importService := newImportServiceIntegration(t, database, symptomService)
	result, err := importService.ImportCSV(context.Background(), user.ID, writeImportCSVTestFile(t, reordered, [][]string{reorderedRow}), time.UTC)
	if err != nil {
		t.Fatalf("import reordered header: %v", err)
	}
	if result.Added != 1 || result.Skipped != 0 || result.Rejected != 0 {
		t.Fatalf("unexpected result for reordered header: %+v", result)
	}

	result, err = importService.ImportCSV(context.Background(), user.ID, writeImportCSVTestFile(t, padded, [][]string{importCSVRowForTest(t, "2026-06-03", "No", "None", "padded")}), time.UTC)
	if err != nil {
		t.Fatalf("import padded header: %v", err)
	}
	if result.Added != 1 || result.Skipped != 0 || result.Rejected != 0 {
		t.Fatalf("unexpected result for padded header: %+v", result)
	}

	stored, err := dayService.FetchLogByDate(context.Background(), user.ID, time.Date(2026, time.June, 2, 0, 0, 0, 0, time.UTC), time.UTC)
	if err != nil {
		t.Fatalf("reload reordered import: %v", err)
	}
	if stored.Flow != models.FlowMedium || stored.Notes != "reordered" {
		t.Fatalf("reordered header mapped the wrong cells: %+v", stored)
	}
}

// TestImportServiceCSVRejectsMalformedShape pins the 400 path for every header
// and record-shape problem, and the positive anchor that a header-only file is
// a valid empty restore rather than a malformed one.
func TestImportServiceCSVRejectsMalformedShape(t *testing.T) {
	_, database := newDayServiceIntegration(t)
	repositories := db.NewRepositories(database)
	symptomService := NewSymptomService(repositories.Symptoms)
	user := createDayServiceTestUser(t, database, "csv-import-shape@example.com")
	importService := newImportServiceIntegration(t, database, symptomService)

	headers := ExportCSVHeaders()
	missingColumn := headers[:len(headers)-1]
	unknownColumn := append([]string{}, headers...)
	unknownColumn[1] = "Mystery"
	duplicateColumn := append([]string{}, headers...)
	duplicateColumn[1] = "Date"

	cases := []struct {
		name      string
		file      []byte
		wantValid bool
	}{
		{name: "empty file", file: nil},
		{name: "header only is valid", file: writeImportCSVTestFile(t, headers, nil), wantValid: true},
		{name: "missing column", file: writeImportCSVTestFile(t, missingColumn, nil)},
		{name: "unknown column", file: writeImportCSVTestFile(t, unknownColumn, nil)},
		{name: "duplicate column", file: writeImportCSVTestFile(t, duplicateColumn, nil)},
		{name: "unclosed quote", file: []byte(strings.Join(headers, ",") + "\n2026-06-01,\"unclosed\n")},
		{name: "short row", file: []byte(strings.Join(headers, ",") + "\n2026-06-01,Yes\n")},
		{name: "long row", file: []byte(strings.Join(headers, ",") + "\n" + strings.Join(make([]string, len(headers)+1), ",") + "\n")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := importService.ImportCSV(context.Background(), user.ID, tc.file, time.UTC)
			if tc.wantValid {
				if err != nil {
					t.Fatalf("expected the header-only file to be valid, got %v", err)
				}
				if result != (ImportResult{}) {
					t.Fatalf("expected an empty restore, got %+v", result)
				}
				return
			}
			if !errors.Is(err, ErrImportMalformed) {
				t.Fatalf("expected ErrImportMalformed, got %v", err)
			}
		})
	}
}

// TestImportServiceCSVRejectsTooLargeFile pins the row cap to the same
// ErrImportTooLarge the JSON restore uses.
func TestImportServiceCSVRejectsTooLargeFile(t *testing.T) {
	_, database := newDayServiceIntegration(t)
	repositories := db.NewRepositories(database)
	symptomService := NewSymptomService(repositories.Symptoms)
	user := createDayServiceTestUser(t, database, "csv-import-large@example.com")
	importService := newImportServiceIntegration(t, database, symptomService)

	rows := make([][]string, 0, MaxImportEntries+1)
	for i := 0; i < MaxImportEntries+1; i++ {
		rows = append(rows, importCSVRowForTest(t, "2026-01-01", "No", "None", ""))
	}

	_, err := importService.ImportCSV(context.Background(), user.ID, writeImportCSVTestFile(t, ExportCSVHeaders(), rows), time.UTC)
	if !errors.Is(err, ErrImportTooLarge) {
		t.Fatalf("expected ErrImportTooLarge, got %v", err)
	}
}

// TestImportServiceCSVRejectsInvalidAndDuplicateDates pins row-level rejection:
// the bad rows are counted, and every good row still imports.
func TestImportServiceCSVRejectsInvalidAndDuplicateDates(t *testing.T) {
	dayService, database := newDayServiceIntegration(t)
	repositories := db.NewRepositories(database)
	symptomService := NewSymptomService(repositories.Symptoms)
	user := createDayServiceTestUser(t, database, "csv-import-dates@example.com")
	importService := newImportServiceIntegration(t, database, symptomService)

	file := writeImportCSVTestFile(t, ExportCSVHeaders(), [][]string{
		importCSVRowForTest(t, "not-a-date", "No", "None", ""),
		importCSVRowForTest(t, "2026-07-01", "No", "None", "first"),
		importCSVRowForTest(t, "2026-07-01", "No", "None", "duplicate"),
		importCSVRowForTest(t, "", "No", "None", ""),
	})

	result, err := importService.ImportCSV(context.Background(), user.ID, file, time.UTC)
	if err != nil {
		t.Fatalf("import csv: %v", err)
	}
	if result.Added != 1 || result.Skipped != 0 || result.Rejected != 3 {
		t.Fatalf("unexpected result: %+v", result)
	}

	stored, err := dayService.FetchLogByDate(context.Background(), user.ID, time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC), time.UTC)
	if err != nil {
		t.Fatalf("reload imported day: %v", err)
	}
	if stored.Notes != "first" {
		t.Fatalf("expected the first occurrence to win, got %q", stored.Notes)
	}
}

// TestImportServiceCSVNormalizesLabelsAndGarbage pins the server-side
// normalization of every CSV-specific cell: labels map back to enums, an
// unknown value falls to its neutral default, an unparseable BBT becomes "not
// measured", unknown cycle factors are dropped, and built-in plus custom
// symptoms resolve through the owner's own catalog.
func TestImportServiceCSVNormalizesLabelsAndGarbage(t *testing.T) {
	dayService, database := newDayServiceIntegration(t)
	repositories := db.NewRepositories(database)
	symptomService := NewSymptomService(repositories.Symptoms)
	user := createDayServiceTestUser(t, database, "csv-import-normalize@example.com")
	importService := newImportServiceIntegration(t, database, symptomService)

	row := importCSVRowForTest(t, "2026-08-04", "YES", "Bogus", "  =cmd")
	setImportCSVTestCell(t, row, "Mood rating", "99")
	setImportCSVTestCell(t, row, "Sex activity", "Unprotected")
	setImportCSVTestCell(t, row, "BBT (C)", "not-a-temperature")
	setImportCSVTestCell(t, row, "Cervical mucus", "Egg white")
	setImportCSVTestCell(t, row, "Pregnancy test", "Positive")
	setImportCSVTestCell(t, row, "Cycle factors", "Stress; Sleep disruption; Made up")
	setImportCSVTestCell(t, row, "Other", "Custom one; ")
	setImportCSVTestCell(t, row, "Cramps", "yes")

	result, err := importService.ImportCSV(context.Background(), user.ID, writeImportCSVTestFile(t, ExportCSVHeaders(), [][]string{row}), time.UTC)
	if err != nil {
		t.Fatalf("import csv: %v", err)
	}
	if result.Added != 1 || result.Skipped != 0 || result.Rejected != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}

	stored, err := dayService.FetchLogByDate(context.Background(), user.ID, time.Date(2026, time.August, 4, 0, 0, 0, 0, time.UTC), time.UTC)
	if err != nil {
		t.Fatalf("reload imported day: %v", err)
	}
	if !stored.IsPeriod {
		t.Fatalf("expected period=true from a case-insensitive Yes")
	}
	if stored.Flow != models.FlowNone {
		t.Fatalf("expected an unknown flow to become none, got %q", stored.Flow)
	}
	if stored.Mood != 0 {
		t.Fatalf("expected an out-of-range mood to become 0, got %d", stored.Mood)
	}
	if stored.SexActivity != models.SexActivityUnprotected {
		t.Fatalf("expected the sex activity label to map back, got %q", stored.SexActivity)
	}
	if stored.BBT != nil {
		t.Fatalf("expected an unparseable BBT to be unmeasured, got %v", *stored.BBT)
	}
	if stored.CervicalMucus != models.CervicalMucusEggWhite {
		t.Fatalf("expected Egg white to map back to eggwhite, got %q", stored.CervicalMucus)
	}
	if stored.PregnancyTest != models.PregnancyTestPositive {
		t.Fatalf("expected the pregnancy label to map back, got %q", stored.PregnancyTest)
	}
	if !reflect.DeepEqual(stored.CycleFactorKeys, []string{models.CycleFactorStress, models.CycleFactorSleepDisruption}) {
		t.Fatalf("expected labels to map back and unknown factors to drop, got %v", stored.CycleFactorKeys)
	}
	if stored.Notes != "  =cmd" {
		t.Fatalf("expected the CSV-injection apostrophe to be removed and the note kept, got %q", stored.Notes)
	}

	crampsID := symptomIDByName(t, symptomService, user.ID, "Cramps")
	customID := symptomIDByName(t, symptomService, user.ID, "Custom one")
	want := []uint{crampsID, customID}
	sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
	if !reflect.DeepEqual(stored.SymptomIDs, want) {
		t.Fatalf("expected cramps and the custom symptom, got %v (want %v)", stored.SymptomIDs, want)
	}
}

func setImportCSVTestCell(t *testing.T, row []string, header string, value string) {
	t.Helper()

	for i, name := range ExportCSVHeaders() {
		if name == header {
			row[i] = value
			return
		}
	}
	t.Fatalf("unknown export header %q", header)
}
