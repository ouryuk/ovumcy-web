package cli

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/db"
	"github.com/ovumcy/ovumcy-web/internal/models"
)

// pinPredictNow pins predictNow's clock for the duration of one test and
// restores the real clock afterwards.
func pinPredictNow(t *testing.T, pinned time.Time) {
	t.Helper()
	previous := predictNow
	predictNow = func() time.Time { return pinned }
	t.Cleanup(func() { predictNow = previous })
}

// seedPredictFixture seeds one owner with three completed 28-day cycles and a
// live fourth cycle, all anchored to explicit cycle starts (2026-01-01 /
// 01-29 / 02-26 / 03-25, five period days each, the last cycle left open so
// the owner is on day 3 when the clock is pinned at 2026-03-27).
func seedPredictFixture(t *testing.T, databasePath string) models.User {
	t.Helper()

	user := createCLIUsersUser(t, databasePath, "predict-owner@example.com", "Predict Owner", models.RoleOwner, true, time.Date(2026, time.January, 1, 8, 0, 0, 0, time.UTC))

	database, err := db.OpenDatabase(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatalf("open sql db: %v", err)
	}
	defer func() { _ = sqlDB.Close() }()

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
			if err := database.Create(&logEntry).Error; err != nil {
				t.Fatalf("create period log %v: %v", logEntry.Date, err)
			}
		}
	}
	return user
}

func runPredictForOwner(t *testing.T, databasePath string, args []string) (PredictDocument, string) {
	t.Helper()

	rendered := runPredictRaw(t, databasePath, args)
	var document PredictDocument
	if err := json.Unmarshal([]byte(rendered), &document); err != nil {
		t.Fatalf("output is not valid JSON: %v (%q)", err, rendered)
	}
	return document, rendered
}

// runPredictRaw runs the command and returns its raw stdout, for the table
// rendering tests that must not unmarshal.
func runPredictRaw(t *testing.T, databasePath string, args []string) string {
	t.Helper()

	var output strings.Builder
	err := runPredictCommand(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath}, args, &output)
	if err != nil {
		t.Fatalf("runPredictCommand returned error: %v", err)
	}
	return output.String()
}

func TestRunPredictCommandProjectsNextCycleWindowAsJSON(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	user := seedPredictFixture(t, databasePath)
	pinPredictNow(t, time.Date(2026, time.March, 27, 12, 0, 0, 0, time.UTC))

	document, rendered := runPredictForOwner(t, databasePath, []string{"predict-owner@example.com", "--json"})
	if document.Schema != predictSchemaVersion {
		t.Fatalf("expected schema %q, got %q", predictSchemaVersion, document.Schema)
	}
	if document.User.ID != user.ID || document.User.Email != "predict-owner@example.com" {
		t.Fatalf("unexpected user: %+v", document.User)
	}
	if document.AsOf != "2026-03-27" {
		t.Fatalf("expected as_of 2026-03-27, got %q", document.AsOf)
	}
	if document.LastPeriodStart != "2026-03-25" {
		t.Fatalf("expected last_period_start 2026-03-25, got %q", document.LastPeriodStart)
	}
	if document.CurrentCycleDay != 3 {
		t.Fatalf("expected cycle day 3, got %d", document.CurrentCycleDay)
	}
	if document.NextPeriodStart != "2026-04-22" {
		t.Fatalf("expected next_period_start 2026-04-22, got %q", document.NextPeriodStart)
	}
	if document.OvulationDate != "2026-04-07" {
		t.Fatalf("expected ovulation 2026-04-07 (day 14 of 28), got %q", document.OvulationDate)
	}
	if document.FertilityWindowStart != "2026-04-02" || document.FertilityWindowEnd != "2026-04-07" {
		t.Fatalf("expected fertile window 2026-04-02..2026-04-07, got %q..%q", document.FertilityWindowStart, document.FertilityWindowEnd)
	}
	if !document.OvulationExact {
		t.Fatalf("expected ovulation_exact for an unclamped 14-day luteal phase")
	}
	if document.CycleBasis.CompletedCycles != 3 || document.CycleBasis.MedianCycleLength != 28 || document.CycleBasis.PredictedCycleLength != 28 {
		t.Fatalf("unexpected cycle basis: %+v", document.CycleBasis)
	}
	if document.CycleBasis.PredictedPeriodLength != 5 {
		t.Fatalf("expected predicted period length 5, got %d", document.CycleBasis.PredictedPeriodLength)
	}
	if document.Disclaimer != predictDisclaimer || !strings.Contains(rendered, "projections from past cycles") {
		t.Fatalf("expected the disclaimer to travel with the document, got %q", document.Disclaimer)
	}
	if document.PregnancyPaused {
		t.Fatalf("expected pregnancy_paused false for a plain cycle history")
	}
}

func TestRunPredictCommandRendersHumanTable(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	seedPredictFixture(t, databasePath)
	pinPredictNow(t, time.Date(2026, time.March, 27, 12, 0, 0, 0, time.UTC))

	rendered := runPredictRaw(t, databasePath, []string{"predict-owner@example.com", "--tz", "Europe/Madrid"})
	for _, want := range []string{
		"ovumcy predict — predict-owner@example.com",
		"as of: 2026-03-27 (Europe/Madrid)",
		"cycle day 3",
		"next period start: 2026-04-22",
		"ovulation (projected): 2026-04-07 (exact: true)",
		"fertile window (projected): 2026-04-02 to 2026-04-07",
		"projections from past cycles",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("table missing %q:\n%s", want, rendered)
		}
	}
}

func TestRunPredictCommandAcceptsIDAndRejectsBadInput(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	user := seedPredictFixture(t, databasePath)
	pinPredictNow(t, time.Date(2026, time.March, 27, 12, 0, 0, 0, time.UTC))

	document, _ := runPredictForOwner(t, databasePath, []string{"--id", strconv.FormatUint(uint64(user.ID), 10), "--json"})
	if document.User.ID != user.ID {
		t.Fatalf("expected the --id target, got %+v", document.User)
	}
	if document.NextPeriodStart != "2026-04-22" {
		t.Fatalf("expected next_period_start 2026-04-22, got %q", document.NextPeriodStart)
	}

	if _, err := parsePredictArgs([]string{}); err == nil {
		t.Fatalf("expected usage error with no target")
	}
	if _, err := parsePredictArgs([]string{"a@example.com", "--nope"}); err == nil {
		t.Fatalf("expected usage error for unknown flag")
	}
	if _, err := parsePredictArgs([]string{"a@example.com", "--tz", "Not/AZone"}); err == nil {
		t.Fatalf("expected usage error for a bad timezone")
	}
	if _, err := parsePredictArgs([]string{"a@example.com", "--id", "nope"}); err == nil {
		t.Fatalf("expected usage error for a non-numeric --id")
	}
	if _, err := parsePredictArgs([]string{"a@example.com", "b@example.com"}); err == nil {
		t.Fatalf("expected usage error for a second positional")
	}
	opts, err := parsePredictArgs([]string{"--id=7", "--tz=UTC", "--json"})
	if err != nil || opts.userID != 7 || !opts.asJSON || opts.tzSet {
		t.Fatalf("expected the flag=value form to parse, got %+v (%v)", opts, err)
	}
}

func TestRunPredictCommandWithoutCycleHistory(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	createCLIUsersUser(t, databasePath, "empty-owner@example.com", "Empty Owner", models.RoleOwner, true, time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC))
	pinPredictNow(t, time.Date(2026, time.March, 27, 12, 0, 0, 0, time.UTC))

	rendered := runPredictRaw(t, databasePath, []string{"empty-owner@example.com"})
	if !strings.Contains(rendered, "no cycle history: nothing can be projected yet") {
		t.Fatalf("expected the no-history line, got:\n%s", rendered)
	}
	document, _ := runPredictForOwner(t, databasePath, []string{"empty-owner@example.com", "--json"})
	if document.LastPeriodStart != "" || document.NextPeriodStart != "" {
		t.Fatalf("expected empty projections with no history, got %q / %q", document.LastPeriodStart, document.NextPeriodStart)
	}
}

func TestRunPredictCommandSuppressesUnderPregnancyPause(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	user := seedPredictFixture(t, databasePath)
	pinPredictNow(t, time.Date(2026, time.March, 30, 12, 0, 0, 0, time.UTC))

	// A positive pregnancy test after the latest cycle start with no cycle
	// start after it activates the pause — the same rule the dashboard and
	// the feed follow. The clock moves to 2026-03-30 so the test day is both
	// within "today" (the derivation bounds the timeline there) and not a
	// seeded log day (the live cycle's period days are 03-25..03-29), so the
	// daily_logs unique key is not hit.
	database, err := db.OpenDatabase(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	logEntry := models.DailyLog{
		UserID:        user.ID,
		Date:          time.Date(2026, time.March, 30, 0, 0, 0, 0, time.UTC),
		PregnancyTest: models.PregnancyTestPositive,
	}
	if err := database.Create(&logEntry).Error; err != nil {
		t.Fatalf("create pregnancy log: %v", err)
	}
	if sqlDB, err := database.DB(); err == nil {
		_ = sqlDB.Close()
	}

	document, rendered := runPredictForOwner(t, databasePath, []string{"predict-owner@example.com", "--json"})
	if !document.PregnancyPaused {
		t.Fatalf("expected pregnancy_paused true")
	}
	if !strings.Contains(rendered, `"pregnancy_paused": true`) {
		t.Fatalf("expected pregnancy_paused true in JSON, got:\n%s", rendered)
	}
}
