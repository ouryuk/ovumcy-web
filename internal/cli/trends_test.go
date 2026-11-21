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

// seedTrendsFixture seeds one owner with four 28-day cycles (explicit cycle
// starts 2026-01-01 / 01-29 / 02-26 / 03-25, five period days each) so exactly
// three cycles are COMPLETED (the fourth has no following start). Per cycle:
//
//	day  2 (menstrual):  mood 3, Cramps
//	day 10 (follicular): mood 4
//	day 22 (luteal):     mood 2, Headache
//
// The last completed cycle ends 2026-03-24, so pin nothing: phase analytics
// reads the full history and never looks at "today".
func seedTrendsFixture(t *testing.T, databasePath string) (models.User, uint, uint) {
	t.Helper()

	user := createCLIUsersUser(t, databasePath, "trends-owner@example.com", "Trends Owner", models.RoleOwner, true, time.Date(2026, time.January, 1, 8, 0, 0, 0, time.UTC))

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

	cycleStarts := []time.Time{
		time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.January, 29, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.February, 26, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.March, 25, 0, 0, 0, 0, time.UTC),
	}
	for _, start := range cycleStarts {
		for periodDay := 0; periodDay < 5; periodDay++ {
			logEntry := models.DailyLog{
				UserID:     user.ID,
				Date:       start.AddDate(0, 0, periodDay),
				IsPeriod:   true,
				CycleStart: periodDay == 0,
				Flow:       models.FlowLight,
			}
			if periodDay == 1 {
				logEntry.Mood = 3
				logEntry.SymptomIDs = []uint{cramps.ID}
			}
			if err := database.Create(&logEntry).Error; err != nil {
				t.Fatalf("create period log %v: %v", logEntry.Date, err)
			}
		}
		for _, extra := range []struct {
			offset int
			mood   int
			sympt  []uint
		}{
			{offset: 9, mood: 4},
			{offset: 21, mood: 2, sympt: []uint{headache.ID}},
		} {
			logEntry := models.DailyLog{
				UserID:     user.ID,
				Date:       start.AddDate(0, 0, extra.offset),
				Mood:       extra.mood,
				SymptomIDs: extra.sympt,
			}
			if err := database.Create(&logEntry).Error; err != nil {
				t.Fatalf("create extra log %v: %v", logEntry.Date, err)
			}
		}
	}
	return user, cramps.ID, headache.ID
}

func runTrendsForOwner(t *testing.T, databasePath string, args []string) (TrendsDocument, string) {
	t.Helper()

	var output bytes.Buffer
	err := runTrendsCommand(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath}, args, &output)
	if err != nil {
		t.Fatalf("runTrendsCommand returned error: %v", err)
	}
	var document TrendsDocument
	if err := json.Unmarshal(output.Bytes(), &document); err != nil {
		t.Fatalf("output is not valid JSON: %v (%q)", err, output.String())
	}
	return document, output.String()
}

func trendsPhaseByName(t *testing.T, document TrendsDocument, phase string) trendsPhase {
	t.Helper()
	for _, entry := range document.Phases {
		if entry.Phase == phase {
			return entry
		}
	}
	t.Fatalf("phase %q missing from document: %+v", phase, document.Phases)
	return trendsPhase{}
}

func TestRunTrendsCommandCorrelatesSymptomsWithPhasesAsJSON(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	user, _, _ := seedTrendsFixture(t, databasePath)

	document, rendered := runTrendsForOwner(t, databasePath, []string{"trends-owner@example.com", "--json"})
	if document.Schema != trendsSchemaVersion {
		t.Fatalf("expected schema %q, got %q", trendsSchemaVersion, document.Schema)
	}
	if document.User.ID != user.ID || document.User.Email != "trends-owner@example.com" {
		t.Fatalf("unexpected user: %+v", document.User)
	}
	// Four seeded starts → three completed cycles; the fourth never finishes.
	if document.CompletedCycles != 3 {
		t.Fatalf("expected 3 completed cycles, got %d", document.CompletedCycles)
	}
	if document.Timezone != "UTC" {
		t.Fatalf("expected default UTC timezone, got %q", document.Timezone)
	}
	if len(document.Phases) != 4 {
		t.Fatalf("expected 4 phases, got %d", len(document.Phases))
	}
	if document.Phases[0].Phase != "menstrual" || document.Phases[3].Phase != "luteal" {
		t.Fatalf("unexpected phase order: %+v", document.Phases)
	}

	// Menstrual: every period day of the three completed cycles is logged
	// (5 × 3 = 15 days); Cramps hit day 2 of each cycle → 3/15 = 20%.
	menstrual := trendsPhaseByName(t, document, "menstrual")
	if menstrual.TotalDays != 15 {
		t.Fatalf("expected menstrual total days 15, got %d", menstrual.TotalDays)
	}
	if len(menstrual.Symptoms) != 1 || menstrual.Symptoms[0].Name != "Cramps" || menstrual.Symptoms[0].Count != 3 {
		t.Fatalf("unexpected menstrual symptoms: %+v", menstrual.Symptoms)
	}
	if menstrual.Symptoms[0].Percentage < 19.99 || menstrual.Symptoms[0].Percentage > 20.01 {
		t.Fatalf("expected cramps at 20%% of menstrual days, got %.2f", menstrual.Symptoms[0].Percentage)
	}
	if !menstrual.HasMood || menstrual.MoodEntries != 3 || menstrual.AverageMood != 3 {
		t.Fatalf("unexpected menstrual mood insight: %+v", menstrual)
	}

	// Follicular: only the three day-10 logs, no symptoms, mood 4 each.
	follicular := trendsPhaseByName(t, document, "follicular")
	if follicular.TotalDays != 3 || follicular.HasSymptoms {
		t.Fatalf("unexpected follicular phase: %+v", follicular)
	}
	if !follicular.HasMood || follicular.MoodEntries != 3 || follicular.AverageMood != 4 {
		t.Fatalf("unexpected follicular mood insight: %+v", follicular)
	}

	// Ovulation: a single-day phase nobody logged → empty but present.
	ovulation := trendsPhaseByName(t, document, "ovulation")
	if ovulation.TotalDays != 0 || ovulation.HasSymptoms || ovulation.HasMood {
		t.Fatalf("expected empty ovulation phase, got %+v", ovulation)
	}

	// Luteal: the three day-22 logs, Headache on every one → 100%.
	luteal := trendsPhaseByName(t, document, "luteal")
	if luteal.TotalDays != 3 {
		t.Fatalf("expected luteal total days 3, got %d", luteal.TotalDays)
	}
	if len(luteal.Symptoms) != 1 || luteal.Symptoms[0].Name != "Headache" || luteal.Symptoms[0].Count != 3 {
		t.Fatalf("unexpected luteal symptoms: %+v", luteal.Symptoms)
	}
	if luteal.Symptoms[0].Percentage != 100 {
		t.Fatalf("expected headache on every luteal day, got %.2f%%", luteal.Symptoms[0].Percentage)
	}
	if !luteal.HasMood || luteal.AverageMood != 2 {
		t.Fatalf("unexpected luteal mood insight: %+v", luteal)
	}

	// Symptom IDs must not leak into the document — it is a rendered view,
	// not an export; names and icons are the vocabulary.
	if strings.Contains(rendered, `"symptom_id"`) {
		t.Fatalf("trends document leaked symptom IDs: %s", rendered)
	}
}

func TestRunTrendsCommandRendersHumanTable(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	seedTrendsFixture(t, databasePath)

	var output bytes.Buffer
	err := runTrendsCommand(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath}, []string{"trends-owner@example.com"}, &output)
	if err != nil {
		t.Fatalf("runTrendsCommand returned error: %v", err)
	}
	rendered := output.String()
	for _, expected := range []string{"completed cycles: 3", "menstrual  15d", "Cramps×3", "luteal  3d", "Headache×3", "avg mood 2.0"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered table missing %q:\n%s", expected, rendered)
		}
	}
}

func TestRunTrendsCommandAcceptsIDAndTimezone(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	user, _, _ := seedTrendsFixture(t, databasePath)

	document, _ := runTrendsForOwner(t, databasePath, []string{"--id", strconv.FormatUint(uint64(user.ID), 10), "--tz", "America/Chicago", "--json"})
	if document.User.ID != user.ID || document.Timezone != "America/Chicago" {
		t.Fatalf("unexpected document head: %+v", document.User)
	}
	// Chicago is UTC−6 in March/January: the day boundaries shift, but the
	// seeded logs sit at UTC midnight — far enough from an edge that the
	// same three cycles are detected in either location.
	if document.CompletedCycles != 3 {
		t.Fatalf("expected 3 completed cycles in America/Chicago, got %d", document.CompletedCycles)
	}
}

func TestParseTrendsArgsRejectsBadInput(t *testing.T) {
	cases := [][]string{
		{},
		{"--json"},
		{"a@example.com", "b@example.com"},
		{"a@example.com", "--days", "7"},
		{"a@example.com", "--tz"},
		{"a@example.com", "--tz", "Not/AZone"},
		{"a@example.com", "--id", "zero"},
		{"a@example.com", "--json=true"},
	}
	for _, args := range cases {
		if _, err := parseTrendsArgs(args); err == nil || err.Error() != trendsUsage {
			t.Fatalf("parseTrendsArgs(%q) = %v, expected usage error", args, err)
		}
	}

	opts, err := parseTrendsArgs([]string{"owner@example.com", "--id=7", "--json"})
	if err != nil {
		t.Fatalf("parseTrendsArgs returned error: %v", err)
	}
	if opts.email != "owner@example.com" || opts.userID != 7 || opts.tz != "UTC" || !opts.asJSON {
		t.Fatalf("unexpected options: %+v", opts)
	}
}

func TestRunTrendsCommandWithoutCompletedCycles(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	createCLIUsersUser(t, databasePath, "new-owner@example.com", "New Owner", models.RoleOwner, true, time.Date(2026, time.March, 1, 8, 0, 0, 0, time.UTC))

	document, rendered := runTrendsForOwner(t, databasePath, []string{"new-owner@example.com", "--json"})
	if document.CompletedCycles != 0 {
		t.Fatalf("expected 0 completed cycles, got %d", document.CompletedCycles)
	}
	for _, phase := range document.Phases {
		if phase.TotalDays != 0 || phase.HasSymptoms || phase.HasMood {
			t.Fatalf("expected empty phases, got %+v", phase)
		}
	}
	if !strings.Contains(rendered, `"completed_cycles": 0`) {
		t.Fatalf("JSON missing zero cycle count: %s", rendered)
	}

	var table bytes.Buffer
	err := runTrendsCommand(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath}, []string{"new-owner@example.com"}, &table)
	if err != nil {
		t.Fatalf("runTrendsCommand returned error: %v", err)
	}
	if !strings.Contains(table.String(), "no completed cycles") {
		t.Fatalf("table missing the no-cycles note:\n%s", table.String())
	}
}
