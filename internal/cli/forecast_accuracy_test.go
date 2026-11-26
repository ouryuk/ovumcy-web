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

// pinForecastAccuracyNow pins forecastAccuracyNow's clock for the duration of
// one test and restores the real clock afterwards.
func pinForecastAccuracyNow(t *testing.T, pinned time.Time) {
	t.Helper()
	previous := forecastAccuracyNow
	forecastAccuracyNow = func() time.Time { return pinned }
	t.Cleanup(func() { forecastAccuracyNow = previous })
}

// seedForecastAccuracyFixture seeds one owner with completed cycles anchored
// to explicit cycle starts (five period days each), so the shared cycle
// detection sees exactly the starts the test names.
func seedForecastAccuracyFixture(t *testing.T, databasePath string, cycleStarts []time.Time) models.User {
	t.Helper()

	user := createCLIUsersUser(t, databasePath, "accuracy-owner@example.com", "Accuracy Owner", models.RoleOwner, true, time.Date(2026, time.January, 1, 8, 0, 0, 0, time.UTC))

	database, err := db.OpenDatabase(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatalf("open sql db: %v", err)
	}
	defer func() { _ = sqlDB.Close() }()

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

func runForecastAccuracyForOwner(t *testing.T, databasePath string, args []string) (ForecastAccuracyDocument, string) {
	t.Helper()

	rendered := runForecastAccuracyRaw(t, databasePath, args)
	var document ForecastAccuracyDocument
	if err := json.Unmarshal([]byte(rendered), &document); err != nil {
		t.Fatalf("output is not valid JSON: %v (%q)", err, rendered)
	}
	return document, rendered
}

// runForecastAccuracyRaw runs the command and returns its raw stdout, for the
// table rendering tests that must not unmarshal.
func runForecastAccuracyRaw(t *testing.T, databasePath string, args []string) string {
	t.Helper()

	var output strings.Builder
	err := runForecastAccuracyCommand(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath}, args, &output)
	if err != nil {
		t.Fatalf("runForecastAccuracyCommand returned error: %v", err)
	}
	return output.String()
}

// accuracyFixtureStarts is a run of cycles whose lengths the median model
// predicts with KNOWN errors: 28 / 28 / 27 / 28 days. Every projection is
// last-start + median-of-recent-lengths, so the per-cycle errors are exactly
// 0, 0, -1, 0.
func accuracyFixtureStarts() []time.Time {
	return []time.Time{
		time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.January, 29, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.February, 26, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.March, 25, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.April, 22, 0, 0, 0, 0, time.UTC),
	}
}

func TestRunForecastAccuracyCommandScoresKnownErrorsAsJSON(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	user := seedForecastAccuracyFixture(t, databasePath, accuracyFixtureStarts())
	pinForecastAccuracyNow(t, time.Date(2026, time.May, 1, 12, 0, 0, 0, time.UTC))

	document, _ := runForecastAccuracyForOwner(t, databasePath, []string{"accuracy-owner@example.com", "--json"})
	if document.Schema != forecastAccuracySchemaVersion {
		t.Fatalf("expected schema %q, got %q", forecastAccuracySchemaVersion, document.Schema)
	}
	if document.User.ID != user.ID || document.User.Email != "accuracy-owner@example.com" {
		t.Fatalf("unexpected user: %+v", document.User)
	}
	if document.Completed != 4 || document.Evaluated != 4 || document.Skipped != 0 {
		t.Fatalf("expected 4 completed and evaluated cycles, got completed=%d evaluated=%d skipped=%d", document.Completed, document.Evaluated, document.Skipped)
	}
	if len(document.Cycles) != 4 {
		t.Fatalf("expected 4 per-cycle rows, got %d", len(document.Cycles))
	}

	expectedPredicted := []string{"2026-01-29", "2026-02-26", "2026-03-26", "2026-04-22"}
	expectedActual := []string{"2026-01-29", "2026-02-26", "2026-03-25", "2026-04-22"}
	expectedErrors := []int{0, 0, -1, 0}
	for index, row := range document.Cycles {
		if row.ActualStart != expectedActual[index] || row.PredictedStart != expectedPredicted[index] {
			t.Fatalf("row %d: expected predicted %s vs actual %s, got %s vs %s", index, expectedPredicted[index], expectedActual[index], row.PredictedStart, row.ActualStart)
		}
		if row.ErrorDays != expectedErrors[index] {
			t.Fatalf("row %d: expected error %+d, got %+d", index, expectedErrors[index], row.ErrorDays)
		}
		if row.Skipped {
			t.Fatalf("row %d: expected an evaluated row, got skipped (%s)", index, row.Reason)
		}
		// The model's recent length range collapses to a single day on this
		// regular history, so only the exact hits land inside the window.
		if row.WithinWindow != (row.ErrorDays == 0) {
			t.Fatalf("row %d: expected within_window %t, got %t", index, row.ErrorDays == 0, row.WithinWindow)
		}
	}
	if document.Cycles[2].Basis.CompletedCycles != 2 || document.Cycles[2].Basis.MedianCycleLength != 28 || document.Cycles[2].Basis.PredictedCycleLength != 28 {
		t.Fatalf("row 2: unexpected basis: %+v", document.Cycles[2].Basis)
	}

	summary := document.Summary
	if summary.PredictionsTotal != 4 {
		t.Fatalf("expected predictions_total 4, got %d", summary.PredictionsTotal)
	}
	if summary.MeanAbsoluteError != 0.3 {
		t.Fatalf("expected mean absolute error 0.3 (0.25 rounded), got %v", summary.MeanAbsoluteError)
	}
	if summary.MedianAbsoluteError != 0 {
		t.Fatalf("expected median absolute error 0, got %v", summary.MedianAbsoluteError)
	}
	if summary.WorstErrorDays != 1 || summary.WorstActualStart != "2026-03-25" || summary.WorstPredictedStart != "2026-03-26" {
		t.Fatalf("unexpected worst cycle: %+v", summary)
	}
	if summary.WithinWindow != 3 || summary.WithinWindowPercent != 75 {
		t.Fatalf("expected 3/4 within window (75%%), got %d (%v%%)", summary.WithinWindow, summary.WithinWindowPercent)
	}
}

func TestRunForecastAccuracyCommandRendersHumanTable(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	seedForecastAccuracyFixture(t, databasePath, accuracyFixtureStarts())
	pinForecastAccuracyNow(t, time.Date(2026, time.May, 1, 12, 0, 0, 0, time.UTC))

	rendered := runForecastAccuracyRaw(t, databasePath, []string{"accuracy-owner@example.com"})
	for _, want := range []string{
		"ovumcy forecast-accuracy — accuracy-owner@example.com",
		"evaluated 4 of 4 completed cycles (0 skipped)",
		"2026-01-29  predicted 2026-01-29  error +0d  in window: true",
		"2026-03-25  predicted 2026-03-26  error -1d  in window: false",
		"summary: mean abs error 0.3d, median 0.0d, worst 1d (actual 2026-03-25, predicted 2026-03-26)",
		"within predicted window: 3/4 (75.0%)",
		"does not guarantee future predictions",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("table missing %q:\n%s", want, rendered)
		}
	}
}

func TestRunForecastAccuracyCommandWithoutAnyHistory(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	createCLIUsersUser(t, databasePath, "empty-owner@example.com", "Empty Owner", models.RoleOwner, true, time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC))
	pinForecastAccuracyNow(t, time.Date(2026, time.March, 27, 12, 0, 0, 0, time.UTC))

	rendered := runForecastAccuracyRaw(t, databasePath, []string{"empty-owner@example.com"})
	if !strings.Contains(rendered, "no cycle history: accuracy cannot be measured yet") {
		t.Fatalf("expected the no-history line, got:\n%s", rendered)
	}
	document, _ := runForecastAccuracyForOwner(t, databasePath, []string{"empty-owner@example.com", "--json"})
	if document.Completed != 0 || document.Evaluated != 0 || len(document.Cycles) != 0 {
		t.Fatalf("expected an empty scorecard, got completed=%d evaluated=%d rows=%d", document.Completed, document.Evaluated, len(document.Cycles))
	}
	if document.Summary.PredictionsTotal != 0 || document.Summary.MeanAbsoluteError != 0 {
		t.Fatalf("expected a zero summary, got %+v", document.Summary)
	}
}

func TestRunForecastAccuracyCommandWithOneCycleIsInsufficient(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	// Two observed starts make ONE completed cycle — a single prediction
	// target with a single training cycle, but the FIRST completed cycle has
	// nothing before it to train on, so nothing is evaluable.
	seedForecastAccuracyFixture(t, databasePath, accuracyFixtureStarts()[:2])
	pinForecastAccuracyNow(t, time.Date(2026, time.February, 15, 12, 0, 0, 0, time.UTC))

	rendered := runForecastAccuracyRaw(t, databasePath, []string{"accuracy-owner@example.com"})
	if !strings.Contains(rendered, "accuracy cannot be measured yet") {
		t.Fatalf("expected the insufficient-data line, got:\n%s", rendered)
	}
	document, _ := runForecastAccuracyForOwner(t, databasePath, []string{"accuracy-owner@example.com", "--json"})
	if document.Completed != 1 {
		t.Fatalf("expected 1 completed cycle, got %d", document.Completed)
	}
	if document.Evaluated != 0 || len(document.Cycles) != 0 {
		t.Fatalf("expected no evaluable cycles, got evaluated=%d rows=%d", document.Evaluated, len(document.Cycles))
	}
}

func TestRunForecastAccuracyCommandSkipsPregnancyPausedCycles(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	user := seedForecastAccuracyFixture(t, databasePath, accuracyFixtureStarts())
	pinForecastAccuracyNow(t, time.Date(2026, time.May, 1, 12, 0, 0, 0, time.UTC))

	// A positive test between the fourth and fifth starts, with no cycle
	// start after it: the model would have paused and projected nothing for
	// the fifth cycle, so that cycle is reported skipped, not scored 0.
	database, err := db.OpenDatabase(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	logEntry := models.DailyLog{
		UserID:        user.ID,
		Date:          time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC),
		PregnancyTest: models.PregnancyTestPositive,
	}
	if err := database.Create(&logEntry).Error; err != nil {
		t.Fatalf("create pregnancy log: %v", err)
	}
	if sqlDB, err := database.DB(); err == nil {
		_ = sqlDB.Close()
	}

	document, rendered := runForecastAccuracyForOwner(t, databasePath, []string{"accuracy-owner@example.com", "--json"})
	if document.Completed != 4 || document.Evaluated != 3 || document.Skipped != 1 {
		t.Fatalf("expected 4 completed / 3 evaluated / 1 skipped, got %+v", document)
	}
	last := document.Cycles[len(document.Cycles)-1]
	if !last.Skipped || last.Reason != forecastAccuracySkipPregnancyPause || last.PredictedStart != "" {
		t.Fatalf("expected the fifth cycle skipped for the pause, got %+v", last)
	}
	if !strings.Contains(rendered, "skipped (pregnancy pause active)") {
		t.Fatalf("expected the skip reason in the table, got:\n%s", rendered)
	}
	if document.Summary.PredictionsTotal != 3 {
		t.Fatalf("expected the summary to count only evaluated rows, got %d", document.Summary.PredictionsTotal)
	}
}

func TestRunForecastAccuracyCommandAcceptsIDAndRejectsBadInput(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	user := seedForecastAccuracyFixture(t, databasePath, accuracyFixtureStarts())
	pinForecastAccuracyNow(t, time.Date(2026, time.May, 1, 12, 0, 0, 0, time.UTC))

	document, _ := runForecastAccuracyForOwner(t, databasePath, []string{"--id", strconv.FormatUint(uint64(user.ID), 10), "--json"})
	if document.User.ID != user.ID {
		t.Fatalf("expected the --id target, got %+v", document.User)
	}

	if _, err := parseForecastAccuracyArgs([]string{}); err == nil {
		t.Fatalf("expected usage error with no target")
	}
	if _, err := parseForecastAccuracyArgs([]string{"a@example.com", "--nope"}); err == nil {
		t.Fatalf("expected usage error for unknown flag")
	}
	if _, err := parseForecastAccuracyArgs([]string{"a@example.com", "--tz", "Not/AZone"}); err == nil {
		t.Fatalf("expected usage error for a bad timezone")
	}
	if _, err := parseForecastAccuracyArgs([]string{"a@example.com", "--id", "nope"}); err == nil {
		t.Fatalf("expected usage error for a non-numeric --id")
	}
	if _, err := parseForecastAccuracyArgs([]string{"a@example.com", "b@example.com"}); err == nil {
		t.Fatalf("expected usage error for a second positional")
	}
	opts, err := parseForecastAccuracyArgs([]string{"--id=7", "--tz=UTC", "--json"})
	if err != nil || opts.userID != 7 || !opts.asJSON || opts.tz != "UTC" {
		t.Fatalf("expected the flag=value form to parse, got %+v (%v)", opts, err)
	}
}

func TestForecastAccuracyMedianFloat(t *testing.T) {
	if got := medianFloat(nil); got != 0 {
		t.Fatalf("expected 0 for no values, got %v", got)
	}
	if got := medianFloat([]int{3}); got != 3 {
		t.Fatalf("expected 3, got %v", got)
	}
	if got := medianFloat([]int{4, 1, 2}); got != 2 {
		t.Fatalf("expected 2, got %v", got)
	}
	if got := medianFloat([]int{0, 1}); got != 0.5 {
		t.Fatalf("expected 0.5, got %v", got)
	}
}
