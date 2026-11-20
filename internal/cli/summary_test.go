package cli

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/db"
	"github.com/ovumcy/ovumcy-web/internal/models"
)

// seedSummaryFixture seeds one owner with a custom symptom pair and a set of
// daily logs chosen so every summary metric is pinned by a distinct
// expectation. The clock is pinned to 2026-03-07 by pinSummaryClock.
//
//	2026-03-01  mood 5, Cramps        (best day; window start when days=7)
//	2026-03-02  mood 2, Cramps+Headache
//	2026-03-03  no mood, Cramps
//	2026-03-04  mood 1, Headache      (worst day)
//	2026-03-05  mood 4
//	2026-03-08  no mood               (outside the 7-day window; breaks the
//	                                   30-day streak, which then resets to 1)
//
// 7-day window 2026-03-01..07: 5 logged / 7 days (71.4%), longest streak 5.
// 30-day window 2026-02-06..03-07: 6 logged / 30 days (20.0%), streak 5.
func seedSummaryFixture(t *testing.T, databasePath string) (models.User, uint, uint) {
	t.Helper()

	user := createCLIUsersUser(t, databasePath, "summary-owner@example.com", "Summary Owner", models.RoleOwner, true, time.Date(2026, time.March, 1, 8, 0, 0, 0, time.UTC))

	database, err := db.OpenDatabase(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatalf("open sql db: %v", err)
	}
	defer func() { _ = sqlDB.Close() }()

	cramps := models.SymptomType{UserID: user.ID, Name: "Cramps", Icon: "🩸", Color: "#FF4444"}
	headache := models.SymptomType{UserID: user.ID, Name: "Headache", Icon: "🤕", Color: "#8888FF"}
	if err := database.Create(&cramps).Error; err != nil {
		t.Fatalf("create cramps symptom: %v", err)
	}
	if err := database.Create(&headache).Error; err != nil {
		t.Fatalf("create headache symptom: %v", err)
	}

	type seedLog struct {
		day   int
		mood  int
		sympt []uint
	}
	seedLogs := []seedLog{
		{day: 1, mood: 5, sympt: []uint{cramps.ID}},
		{day: 2, mood: 2, sympt: []uint{cramps.ID, headache.ID}},
		{day: 3, mood: 0, sympt: []uint{cramps.ID}},
		{day: 4, mood: 1, sympt: []uint{headache.ID}},
		{day: 5, mood: 4},
		{day: 8, mood: 0},
	}
	for _, entry := range seedLogs {
		logEntry := models.DailyLog{
			UserID:     user.ID,
			Date:       time.Date(2026, time.March, entry.day, 0, 0, 0, 0, time.UTC),
			Mood:       entry.mood,
			SymptomIDs: entry.sympt,
		}
		if err := database.Create(&logEntry).Error; err != nil {
			t.Fatalf("create daily log: %v", err)
		}
	}
	return user, cramps.ID, headache.ID
}

// pinSummaryClock pins the summary window's "today" to 2026-03-07 and restores
// the real clock when the test finishes.
func pinSummaryClock(t *testing.T) {
	t.Helper()
	previous := summaryNow
	summaryNow = func() time.Time { return time.Date(2026, time.March, 7, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { summaryNow = previous })
}

func runSummaryForOwner(t *testing.T, databasePath string, args []string) (SummaryDocument, string) {
	t.Helper()
	pinSummaryClock(t)

	var output bytes.Buffer
	err := runSummaryCommand(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath}, args, &output)
	if err != nil {
		t.Fatalf("runSummaryCommand returned error: %v", err)
	}
	var document SummaryDocument
	if err := json.Unmarshal(output.Bytes(), &document); err != nil {
		t.Fatalf("output is not valid JSON: %v (%q)", err, output.String())
	}
	return document, output.String()
}

func TestRunSummaryCommandAggregatesSevenDayWindowAsJSON(t *testing.T) {

	databasePath := createCLIUsersDatabase(t)
	user, _, _ := seedSummaryFixture(t, databasePath)

	document, rendered := runSummaryForOwner(t, databasePath, []string{"summary-owner@example.com", "--days", "7", "--json"})
	if document.Schema != summarySchemaVersion {
		t.Fatalf("expected schema %q, got %q", summarySchemaVersion, document.Schema)
	}
	if document.User.ID != user.ID || document.User.Email != "summary-owner@example.com" {
		t.Fatalf("unexpected user: %+v", document.User)
	}
	if document.WindowDays != 7 || document.CalendarDays != 7 {
		t.Fatalf("unexpected window: %+v", document)
	}
	if document.From != "2026-03-01" || document.To != "2026-03-07" {
		t.Fatalf("unexpected window bounds: %s .. %s", document.From, document.To)
	}
	if document.LoggedDays != 5 {
		t.Fatalf("expected 5 logged days, got %d", document.LoggedDays)
	}
	if document.AdherencePercent < 71.4 || document.AdherencePercent > 71.5 {
		t.Fatalf("expected adherence ~71.4%%, got %f", document.AdherencePercent)
	}
	if document.LongestStreak != 5 {
		t.Fatalf("expected longest streak 5 (Mar 1–5), got %d", document.LongestStreak)
	}
	if document.BestDay == nil || document.BestDay.Date != "2026-03-01" || document.BestDay.Mood != 5 {
		t.Fatalf("unexpected best day: %+v", document.BestDay)
	}
	if document.WorstDay == nil || document.WorstDay.Date != "2026-03-04" || document.WorstDay.Mood != 1 {
		t.Fatalf("unexpected worst day: %+v", document.WorstDay)
	}
	if len(document.SymptomCounts) != 2 {
		t.Fatalf("expected 2 symptom tallies, got %+v", document.SymptomCounts)
	}
	if document.SymptomCounts[0].Name != "Cramps" || document.SymptomCounts[0].Count != 3 {
		t.Fatalf("expected Cramps x3 first, got %+v", document.SymptomCounts)
	}
	if document.SymptomCounts[1].Name != "Headache" || document.SymptomCounts[1].Count != 2 {
		t.Fatalf("expected Headache x2 second, got %+v", document.SymptomCounts)
	}
	if len(document.TopSymptoms) != 2 {
		t.Fatalf("expected top symptoms to mirror counts for a short list, got %+v", document.TopSymptoms)
	}
	if !strings.Contains(rendered, "\"logged_days\": 5") {
		t.Fatalf("expected indented JSON payload, got: %s", rendered)
	}
}

func TestRunSummaryCommandTopSymptomsCapAtThreeAndTiesBreakByName(t *testing.T) {
	t.Parallel()

	databasePath := createCLIUsersDatabase(t)
	user := createCLIUsersUser(t, databasePath, "caps@example.com", "Caps", models.RoleOwner, true, time.Date(2026, time.March, 1, 8, 0, 0, 0, time.UTC))

	database, err := db.OpenDatabase(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatalf("open sql db: %v", err)
	}
	var ids []uint
	for _, name := range []string{"Delta", "Alpha", "Charlie", "Bravo"} {
		symptom := models.SymptomType{UserID: user.ID, Name: name}
		if err := database.Create(&symptom).Error; err != nil {
			t.Fatalf("create symptom: %v", err)
		}
		ids = append(ids, symptom.ID)
	}
	logEntry := models.DailyLog{
		UserID:     user.ID,
		Date:       time.Date(2026, time.March, 3, 0, 0, 0, 0, time.UTC),
		SymptomIDs: ids,
	}
	if err := database.Create(&logEntry).Error; err != nil {
		t.Fatalf("create daily log: %v", err)
	}
	_ = sqlDB.Close()

	document, _ := runSummaryForOwner(t, databasePath, []string{"--id", strconv.FormatUint(uint64(user.ID), 10), "--json"})
	if len(document.SymptomCounts) != 3 || len(document.TopSymptoms) != 3 {
		t.Fatalf("expected tallies and top-both capped at 3, got %+v", document)
	}
	// All four counts tie at 1, so the tie must break alphabetically.
	if document.TopSymptoms[0].Name != "Alpha" || document.TopSymptoms[1].Name != "Bravo" || document.SymptomCounts[2].Name != "Charlie" {
		t.Fatalf("expected alphabetical tie-break [Alpha Bravo Charlie], got %+v", document.SymptomCounts)
	}
}

func TestRunSummaryCommandTableAndJSONShareStructs(t *testing.T) {
	t.Parallel()

	databasePath := createCLIUsersDatabase(t)
	seedSummaryFixture(t, databasePath)

	pinSummaryClock(t)
	var output bytes.Buffer
	err := runSummaryCommand(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath}, []string{"summary-owner@example.com"}, &output)
	if err != nil {
		t.Fatalf("runSummaryCommand returned error: %v", err)
	}
	rendered := output.String()
	for _, fragment := range []string{
		"Summary for summary-owner@example.com",
		"Window: 2026-02-06 .. 2026-03-07 (30 days)",
		"logged days:    5 / 30 (16.7%)",
		"longest streak: 5 day(s)",
		"1. Cramps",
		"2. Headache",
		"best day:  2026-03-01 (mood 5)",
		"worst day: 2026-03-04 (mood 1)",
	} {
		if !strings.Contains(rendered, fragment) {
			t.Fatalf("table output missing %q:\n%s", fragment, rendered)
		}
	}
	if strings.Contains(rendered, "\"schema\"") {
		t.Fatalf("table output must not be JSON: %s", rendered)
	}
}

func TestRunSummaryCommandEmptyAccountRendersEmptyCollections(t *testing.T) {
	t.Parallel()

	databasePath := createCLIUsersDatabase(t)
	createCLIUsersUser(t, databasePath, "empty@example.com", "Empty", models.RoleOwner, false, time.Date(2026, time.March, 2, 9, 0, 0, 0, time.UTC))

	pinSummaryClock(t)
	var output bytes.Buffer
	err := runSummaryCommand(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath}, []string{"empty@example.com"}, &output)
	if err != nil {
		t.Fatalf("runSummaryCommand returned error: %v", err)
	}
	if !strings.Contains(output.String(), "(none recorded in this window)") || !strings.Contains(output.String(), "(no mood logged in this window)") {
		t.Fatalf("expected friendly empty-table labels, got: %s", output.String())
	}

	document, _ := runSummaryForOwner(t, databasePath, []string{"empty@example.com", "--json"})
	if document.LoggedDays != 0 || document.AdherencePercent != 0 || document.LongestStreak != 0 {
		t.Fatalf("unexpected non-zero metrics for empty account: %+v", document)
	}
	if document.BestDay != nil || document.WorstDay != nil {
		t.Fatalf("expected null mood days for empty account: %+v", document)
	}
	if document.SymptomCounts == nil || document.TopSymptoms == nil {
		t.Fatalf("expected empty collections (not null): %+v", document)
	}
}

func TestRunSummaryCommandArgumentErrorsUseStableUsage(t *testing.T) {
	t.Parallel()

	databasePath := createCLIUsersDatabase(t)
	pinSummaryClock(t)
	config := db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath}

	for _, args := range [][]string{
		nil,
		{},
		{"--json"},
		{"--bogus"},
		{"--days", "14"},
		{"--days", "not-a-number"},
		{"--id", "not-a-number"},
		{"a@example.com", "b@example.com"},
		{"--json=true"},
	} {
		var output bytes.Buffer
		err := runSummaryCommand(config, args, &output)
		if err == nil || !strings.Contains(err.Error(), "usage: ovumcy summary") {
			t.Fatalf("expected usage error for %v, got %v", args, err)
		}
	}
}

func TestRunSummaryCommandUnknownUserReturnsAdvice(t *testing.T) {
	t.Parallel()

	databasePath := createCLIUsersDatabase(t)
	seedSummaryFixture(t, databasePath)

	var output bytes.Buffer
	err := runSummaryCommand(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath}, []string{"nobody@example.com"}, &output)
	if err == nil || !strings.Contains(err.Error(), "user nobody@example.com not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("expected no output on failure, got %q", output.String())
	}
}
