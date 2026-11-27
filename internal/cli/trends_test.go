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
		for periodDay := range 5 {
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

	// An omitted --tz is "use the owner's stored zone", not "UTC": tzSet has to
	// stay false so the command can tell the two apart.
	opts, err := parseTrendsArgs([]string{"owner@example.com", "--id=7", "--json"})
	if err != nil {
		t.Fatalf("parseTrendsArgs returned error: %v", err)
	}
	if opts.email != "owner@example.com" || opts.userID != 7 || opts.tzSet || !opts.asJSON {
		t.Fatalf("unexpected options: %+v", opts)
	}
	opts, err = parseTrendsArgs([]string{"owner@example.com", "--tz=Europe/Madrid"})
	if err != nil || !opts.tzSet || opts.tz != "Europe/Madrid" {
		t.Fatalf("expected an explicit --tz to be recorded, got %+v (%v)", opts, err)
	}
	// --id 0 is refused as a value error, never as a fall-through to the email.
	if _, err := parseTrendsArgs([]string{"owner@example.com", "--id", "0"}); err == nil || !strings.Contains(err.Error(), "positive account id") {
		t.Fatalf("expected the non-positive-id message for --id 0, got %v", err)
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

// seedTrendsShortHistoryFixture seeds one owner with THREE cycle starts —
// two completed cycles, one BELOW the pattern minimum of three — so the trends
// document has something to say while still being under the sample size the
// stats page would withhold a pattern at.
//
// Per cycle: five period days (a mood on each, and the Cramps symptom on the
// second one) and one day-10 mood. The symptom lands on a period day, so the
// menstrual phase carries both halves of the breakdown.
func seedTrendsShortHistoryFixture(t *testing.T, databasePath string) models.User {
	t.Helper()

	user := createCLIUsersUser(t, databasePath, "short-owner@example.com", "Short Owner", models.RoleOwner, true, time.Date(2026, time.January, 1, 8, 0, 0, 0, time.UTC))

	database, err := db.OpenDatabase(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatalf("open sql db: %v", err)
	}
	defer func() { _ = sqlDB.Close() }()

	cramps := models.SymptomType{UserID: user.ID, Name: "Cramps", Icon: "c"}
	if err := database.Create(&cramps).Error; err != nil {
		t.Fatalf("create cramps symptom: %v", err)
	}

	for _, start := range []time.Time{
		time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.January, 29, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.February, 26, 0, 0, 0, 0, time.UTC),
	} {
		for periodDay := range 5 {
			logEntry := models.DailyLog{
				UserID:     user.ID,
				Date:       start.AddDate(0, 0, periodDay),
				IsPeriod:   true,
				CycleStart: periodDay == 0,
				Flow:       models.FlowLight,
				Mood:       3,
			}
			if periodDay == 1 {
				logEntry.SymptomIDs = []uint{cramps.ID}
			}
			if err := database.Create(&logEntry).Error; err != nil {
				t.Fatalf("create period log %v: %v", logEntry.Date, err)
			}
		}
	}
	return user
}

// TestRunTrendsCommandReportsSymptomsAndMoodBelowThePatternMinimum is the
// operator-visible half of the fix: with TWO completed cycles the document
// must report the symptoms AND the mood of the phase they share.
//
// The symptom half used to be withheld below the pattern minimum while the
// mood half was reported, so the document claimed an average mood for a phase
// it refused to report symptoms for — and the operator could not tell an
// empty phase from a withheld one.
func TestRunTrendsCommandReportsSymptomsAndMoodBelowThePatternMinimum(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	user := seedTrendsShortHistoryFixture(t, databasePath)

	document, _ := runTrendsForOwner(t, databasePath, []string{"short-owner@example.com", "--json"})
	if document.User.ID != user.ID {
		t.Fatalf("unexpected user: %+v", document.User)
	}
	if document.CompletedCycles != 2 {
		t.Fatalf("expected 2 completed cycles, got %d", document.CompletedCycles)
	}

	menstrual := trendsPhaseByName(t, document, "menstrual")
	if !menstrual.HasSymptoms {
		t.Fatal("the symptoms were withheld below the pattern minimum while the mood was reported: the two halves of the breakdown must move together")
	}
	if !menstrual.HasMood {
		t.Fatal("expected the mood half to report below the pattern minimum: the trends surface does not gate on it")
	}
	if len(menstrual.Symptoms) != 1 || menstrual.Symptoms[0].Name != "Cramps" {
		t.Fatalf("unexpected menstrual symptoms: %+v", menstrual.Symptoms)
	}
	// One Cramps occurrence per completed cycle, over every logged menstrual
	// day (five per cycle).
	if menstrual.Symptoms[0].Count != 2 {
		t.Fatalf("expected cramps twice across two cycles, got %d", menstrual.Symptoms[0].Count)
	}
	if menstrual.TotalDays != 10 {
		t.Fatalf("expected 10 logged menstrual days across two cycles, got %d", menstrual.TotalDays)
	}
	if menstrual.MoodEntries != 10 {
		t.Fatalf("expected 10 menstrual mood entries across two cycles, got %d", menstrual.MoodEntries)
	}

	// The human table reads from the same document, so it must show both. It
	// is a different rendering of the same facts, so it is rendered WITHOUT
	// --json: the JSON document never carries the table's own wording.
	var table bytes.Buffer
	if err := runTrendsCommand(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath}, []string{"short-owner@example.com"}, &table); err != nil {
		t.Fatalf("runTrendsCommand returned error: %v", err)
	}
	rendered := table.String()
	if !strings.Contains(rendered, "Cramps×2") {
		t.Fatalf("rendered document missing the symptom count:\n%s", rendered)
	}
	if !strings.Contains(rendered, "avg mood 3.0") {
		t.Fatalf("rendered document missing the mood average:\n%s", rendered)
	}
}

// seedTrendsBelowPatternFixture seeds one owner with TWO completed cycles
// (three explicit cycle starts) so the account sits BELOW the stats page's
// pattern minimum of three completed cycles.
//
// The `ovumcy trends` document reports the completed-cycle count beside
// whatever it could measure, so both halves of the document have to be
// counted over the cycles the owner has. The mood half always was; the
// symptom half kept the stats page's minimum and was withheld, which reads
// as "there were no symptoms in that phase" — a fact the owner knows to be
// false — rather than "there were too few cycles to count them".
func seedTrendsBelowPatternFixture(t *testing.T, databasePath string) models.User {
	t.Helper()

	user := createCLIUsersUser(t, databasePath, "trends-below@example.com", "Trends Below", models.RoleOwner, true, time.Date(2026, time.January, 1, 8, 0, 0, 0, time.UTC))

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
	if err := database.Create(&cramps).Error; err != nil {
		t.Fatalf("create cramps symptom: %v", err)
	}

	// Three starts → two completed cycles; the third never finishes.
	for _, start := range []time.Time{
		time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.January, 29, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.February, 26, 0, 0, 0, 0, time.UTC),
	} {
		for periodDay := range 5 {
			logEntry := models.DailyLog{
				UserID:     user.ID,
				Date:       start.AddDate(0, 0, periodDay),
				IsPeriod:   true,
				CycleStart: periodDay == 0,
			}
			if periodDay == 1 {
				logEntry.Mood = 3
				logEntry.SymptomIDs = []uint{cramps.ID}
			}
			if err := database.Create(&logEntry).Error; err != nil {
				t.Fatalf("create period log %v: %v", logEntry.Date, err)
			}
		}
	}
	return user
}

// TestRunTrendsCommandReportsSymptomsBelowThePatternMinimum is the
// operator-visible half of the regression: the document an operator reads for
// a two-cycle account must carry the symptoms it can count, exactly as it
// carries the moods it can count.
func TestRunTrendsCommandReportsSymptomsBelowThePatternMinimum(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	seedTrendsBelowPatternFixture(t, databasePath)

	document, _ := runTrendsForOwner(t, databasePath, []string{"trends-below@example.com", "--json"})
	if document.CompletedCycles != 2 {
		t.Fatalf("expected 2 completed cycles, got %d", document.CompletedCycles)
	}

	menstrual := trendsPhaseByName(t, document, "menstrual")
	if !menstrual.HasMood || menstrual.MoodEntries != 2 {
		t.Fatalf("expected the mood half to be reported below the pattern minimum, got %+v", menstrual)
	}
	if !menstrual.HasSymptoms {
		t.Fatal("expected the symptom half to be reported below the pattern minimum: the document reported moods for the same cycles it withheld symptoms for, which reads as 'no symptoms in that phase' rather than 'too few cycles to count them'")
	}
	if len(menstrual.Symptoms) != 1 || menstrual.Symptoms[0].Name != "Cramps" || menstrual.Symptoms[0].Count != 2 {
		t.Fatalf("unexpected menstrual symptoms: %+v", menstrual.Symptoms)
	}
}
