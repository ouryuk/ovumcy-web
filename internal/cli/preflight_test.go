package cli

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/db"
	"github.com/ovumcy/ovumcy-web/internal/models"
	"github.com/ovumcy/ovumcy-web/internal/services"
)

// pinPreflightNow pins preflightNow's clock for the duration of one test and
// restores the real clock afterwards, mirroring pinPredictNow.
func pinPreflightNow(t *testing.T, pinned time.Time) {
	t.Helper()
	previous := preflightNow
	preflightNow = func() time.Time { return pinned }
	t.Cleanup(func() { preflightNow = previous })
}

// preflightFixture is what a preflight test needs to have seeded: the owner row
// (so assertions can compare before/after) and the implied database path. The
// helper is a struct rather than three return values because every test seeds
// the cycles and then varies only the webhook settings.
type preflightFixture struct {
	databasePath string
	user         models.User
}

// seedPreflightCycles seeds the SAME four-cycle, 28-day history
// seedPredictFixture uses — explicit cycle starts at 2026-01-01 / 01-29 /
// 02-26 / 03-25, five period days each, the last cycle left open — so a clock
// pinned at 2026-03-27 puts the owner on cycle day 3 with a projected next
// period start of 2026-04-22 and ovulation of 2026-04-07. Reusing the fixture
// shape keeps the two commands' expectations directly comparable.
func seedPreflightCycles(t *testing.T, databasePath string, userID uint) {
	t.Helper()

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
				UserID:     userID,
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
}

// updatePreflightUser applies a column change to the seeded owner between the
// fixture setup and the command run, so a test can arm a webhook, stamp a
// delivery or switch a toggle without re-seeding the whole account.
func updatePreflightUser(t *testing.T, databasePath string, userID uint, updates map[string]any) {
	t.Helper()

	database, err := db.OpenDatabase(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatalf("open sql db: %v", err)
	}
	defer func() { _ = sqlDB.Close() }()

	if err := database.Model(&models.User{}).Where("id = ?", userID).Updates(updates).Error; err != nil {
		t.Fatalf("update user: %v", err)
	}
}

// newPreflightFixture creates an owner with the standard cycle history and NO
// webhook configured, which is the state most findings tests start from.
func newPreflightFixture(t *testing.T, email string) preflightFixture {
	t.Helper()

	databasePath := createCLIUsersDatabase(t)
	user := createCLIUsersUser(t, databasePath, email, "Preflight Owner", models.RoleOwner, true, time.Date(2026, time.January, 1, 8, 0, 0, 0, time.UTC))
	seedPreflightCycles(t, databasePath, user.ID)
	return preflightFixture{databasePath: databasePath, user: user}
}

// readyPreflightFixture arms everything a ready owner needs: a stored endpoint
// (the ciphertext is never opened by this command, so any non-empty value
// works), delivery enabled, both kinds on, and a recent delivery stamp.
func (fixture preflightFixture) makeReady(t *testing.T, deliveredAt time.Time) preflightFixture {
	t.Helper()

	updatePreflightUser(t, fixture.databasePath, fixture.user.ID, map[string]any{
		"webhook_url":               "ciphertext-not-opened-here",
		"webhook_enabled":           true,
		"webhook_notify_period":     true,
		"webhook_notify_ovulation":  true,
		"reminder_lead_days":        3,
		"webhook_last_delivered_at": deliveredAt,
	})
	return fixture
}

// runPreflightJSON runs the command with --json and unmarshals the document. The
// command's error is returned alongside it, because a not-ready verdict is an
// error AND a document: the report is written to stdout before the verdict is
// returned, and a test needs to inspect both sides of that contract.
func (fixture preflightFixture) runPreflightJSON(t *testing.T, args []string) (PreflightDocument, error) {
	t.Helper()

	rendered, err := fixture.runPreflightRaw(t, append(args, "--json"))
	var document PreflightDocument
	if unmarshalErr := json.Unmarshal([]byte(rendered), &document); unmarshalErr != nil {
		t.Fatalf("output is not valid JSON: %v (%q)", unmarshalErr, rendered)
	}
	return document, err
}

// runPreflightRaw runs the command and returns its stdout, together with the
// command's own error so a test can assert the exit-code contract.
func (fixture preflightFixture) runPreflightRaw(t *testing.T, args []string) (string, error) {
	t.Helper()

	var output strings.Builder
	err := runPreflightCommand(db.Config{Driver: db.DriverSQLite, SQLitePath: fixture.databasePath}, args, &output)
	return output.String(), err
}

// findingCodes returns the ordered code list, for assertions that care about
// the ORDER of the findings as much as their presence.
func findingCodes(findings []PreflightFinding) []string {
	codes := make([]string, 0, len(findings))
	for _, finding := range findings {
		codes = append(codes, string(finding.Code))
	}
	return codes
}

func TestRunPreflightCommandReportsReadyOwner(t *testing.T) {
	fixture := newPreflightFixture(t, "ready-owner@example.com").
		makeReady(t, time.Date(2026, time.March, 20, 9, 0, 0, 0, time.UTC))
	pinPreflightNow(t, time.Date(2026, time.March, 27, 12, 0, 0, 0, time.UTC))

	document, err := fixture.runPreflightJSON(t, []string{"ready-owner@example.com"})
	if err != nil {
		t.Fatalf("expected a ready owner to return no error (exit 0), got %v", err)
	}
	if document.Schema != preflightSchemaVersion {
		t.Fatalf("expected schema %q, got %q", preflightSchemaVersion, document.Schema)
	}
	if document.User.ID != fixture.user.ID || document.User.Email != "ready-owner@example.com" {
		t.Fatalf("unexpected user: %+v", document.User)
	}
	if document.AsOf != "2026-03-27" {
		t.Fatalf("expected as_of 2026-03-27, got %q", document.AsOf)
	}
	if !document.Ready {
		t.Fatalf("expected ready=true, findings: %+v", document.Findings)
	}
	if len(document.Findings) != 0 {
		t.Fatalf("expected no findings, got %v", findingCodes(document.Findings))
	}

	// The nearer of the two projected dates is the ovulation (2026-04-07), so
	// that is what the report names as the next reminder.
	if document.Next.Kind != "ovulation-soon" {
		t.Fatalf("expected the next reminder to be ovulation-soon, got %q", document.Next.Kind)
	}
	if document.Next.ProjectedDate != "2026-04-07" {
		t.Fatalf("expected projected date 2026-04-07, got %q", document.Next.ProjectedDate)
	}
	if document.Next.DaysUntil == nil || *document.Next.DaysUntil != 11 {
		t.Fatalf("expected 11 days until the ovulation, got %v", document.Next.DaysUntil)
	}
	if document.Next.DueToday {
		t.Fatalf("expected the ovulation reminder not to be due 11 days out")
	}
	if document.Next.LeadDays != 3 {
		t.Fatalf("expected the clamped lead window 3, got %d", document.Next.LeadDays)
	}

	if !document.Webhook.Configured || !document.Webhook.Enabled {
		t.Fatalf("expected a configured, enabled webhook, got %+v", document.Webhook)
	}
	if !document.Webhook.DeliveryRecordExists || document.Webhook.LastDeliveredAt == "" {
		t.Fatalf("expected a recorded delivery, got %+v", document.Webhook)
	}
	if document.Webhook.LastDeliveryAgeDays == nil || *document.Webhook.LastDeliveryAgeDays != 7 {
		t.Fatalf("expected a 7-day-old delivery, got %v", document.Webhook.LastDeliveryAgeDays)
	}
}

func TestRunPreflightCommandInWindowIsDueToday(t *testing.T) {
	fixture := newPreflightFixture(t, "due-owner@example.com").
		makeReady(t, time.Date(2026, time.March, 20, 9, 0, 0, 0, time.UTC))
	// 2026-04-05 is two days before the projected ovulation of 2026-04-07,
	// inside the owner's 3-day lead window, so the notify pass's own decision
	// (DecideDueReminders, called unchanged) must say it is due today.
	pinPreflightNow(t, time.Date(2026, time.April, 5, 12, 0, 0, 0, time.UTC))

	document, err := fixture.runPreflightJSON(t, []string{"due-owner@example.com"})
	if err != nil {
		t.Fatalf("expected a ready owner to return no error, got %v", err)
	}
	if document.Next.Kind != "ovulation-soon" || document.Next.ProjectedDate != "2026-04-07" {
		t.Fatalf("expected the ovulation reminder, got %+v", document.Next)
	}
	if !document.Next.DueToday {
		t.Fatalf("expected due_today=true inside the lead window, got %+v", document.Next)
	}
}

func TestRunPreflightCommandIsReadOnly(t *testing.T) {
	fixture := newPreflightFixture(t, "readonly-owner@example.com").makeReady(t, time.Date(2026, time.March, 20, 9, 0, 0, 0, time.UTC))
	pinPreflightNow(t, time.Date(2026, time.April, 5, 12, 0, 0, 0, time.UTC))

	before := loadPreflightUserRow(t, fixture.databasePath, fixture.user.ID)
	// The reminder is DUE today, so a delivery pass would claim a watermark and
	// stamp a delivery mark. This command must move neither.
	if _, err := fixture.runPreflightJSON(t, []string{"readonly-owner@example.com"}); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	after := loadPreflightUserRow(t, fixture.databasePath, fixture.user.ID)

	if !before.WebhookLastDeliveredAt.Equal(*after.WebhookLastDeliveredAt) {
		t.Fatalf("the delivery mark moved: %v -> %v", before.WebhookLastDeliveredAt, after.WebhookLastDeliveredAt)
	}
	if (before.WebhookPeriodLastSentCycleStart == nil) != (after.WebhookPeriodLastSentCycleStart == nil) ||
		(before.WebhookOvulationLastSentCycleStart == nil) != (after.WebhookOvulationLastSentCycleStart == nil) {
		t.Fatalf("a watermark appeared: before %+v, after %+v", before, after)
	}
	if before.WebhookConfigVersion != after.WebhookConfigVersion {
		t.Fatalf("the revocation epoch moved: %d -> %d", before.WebhookConfigVersion, after.WebhookConfigVersion)
	}
	if before.WebhookURL != after.WebhookURL || before.WebhookEnabled != after.WebhookEnabled {
		t.Fatalf("the webhook settings moved: before %+v, after %+v", before, after)
	}
}

// loadPreflightUserRow reads the owner row back so a test can compare before and
// after a run. It deliberately selects every webhook-state column a delivery
// pass would touch.
func loadPreflightUserRow(t *testing.T, databasePath string, userID uint) models.User {
	t.Helper()

	database, err := db.OpenDatabase(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatalf("open sql db: %v", err)
	}
	defer func() { _ = sqlDB.Close() }()

	var user models.User
	if err := database.First(&user, userID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	return user
}

func TestRunPreflightCommandReportsNoWebhookAndDisabled(t *testing.T) {
	fixture := newPreflightFixture(t, "bare-owner@example.com")
	pinPreflightNow(t, time.Date(2026, time.March, 27, 12, 0, 0, 0, time.UTC))

	document, err := fixture.runPreflightJSON(t, []string{"bare-owner@example.com"})
	if err == nil {
		t.Fatalf("expected the not-ready verdict to return an error (exit 1)")
	}
	var notReady *preflightNotReadyError
	if !errors.As(err, &notReady) {
		t.Fatalf("expected a *preflightNotReadyError, got %T: %v", err, err)
	}
	if notReady.findings != len(document.Findings) {
		t.Fatalf("the error carries %d findings, the report %d", notReady.findings, len(document.Findings))
	}
	if document.Ready {
		t.Fatalf("expected ready=false")
	}

	// An owner with no endpoint, delivery off and both reminder kinds off gets all
	// three findings, in the documented repair order: the endpoint first, then the
	// master switch, then the per-kind opt-ins.
	want := []string{"no_webhook_configured", "webhook_disabled", "reminder_toggles_off"}
	got := findingCodes(document.Findings)
	if len(got) != len(want) {
		t.Fatalf("expected findings %v, got %v", want, got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("expected findings %v, got %v", want, got)
		}
	}
	for _, finding := range document.Findings {
		if finding.Fix == "" {
			t.Fatalf("finding %q carries no one-line fix", finding.Code)
		}
		if finding.Detail == "" {
			t.Fatalf("finding %q carries no detail", finding.Code)
		}
	}
	// A delivery record is not reported for an owner who never armed delivery:
	// that would misdescribe an account that simply asked for nothing.
	for _, code := range got {
		if code == "no_delivery_recorded" {
			t.Fatalf("a never-armed owner must not be reported as missing a delivery: %v", got)
		}
	}
}

func TestRunPreflightCommandReportsTogglesOff(t *testing.T) {
	fixture := newPreflightFixture(t, "muted-owner@example.com").
		makeReady(t, time.Date(2026, time.March, 20, 9, 0, 0, 0, time.UTC))
	updatePreflightUser(t, fixture.databasePath, fixture.user.ID, map[string]any{
		"webhook_notify_period":    false,
		"webhook_notify_ovulation": false,
	})
	pinPreflightNow(t, time.Date(2026, time.March, 27, 12, 0, 0, 0, time.UTC))

	document, err := fixture.runPreflightJSON(t, []string{"muted-owner@example.com"})
	if err == nil {
		t.Fatalf("expected a not-ready verdict")
	}
	codes := findingCodes(document.Findings)
	if len(codes) != 1 || codes[0] != "reminder_toggles_off" {
		t.Fatalf("expected only reminder_toggles_off, got %v", codes)
	}
	if document.Next.Kind != "" {
		t.Fatalf("expected no projected kind with both toggles off, got %q", document.Next.Kind)
	}
	if !strings.Contains(document.Next.Reason, "switched off") {
		t.Fatalf("expected the reason to name the toggles, got %q", document.Next.Reason)
	}
}

func TestRunPreflightCommandReportsNoDeliveryRecorded(t *testing.T) {
	fixture := newPreflightFixture(t, "silent-owner@example.com")
	updatePreflightUser(t, fixture.databasePath, fixture.user.ID, map[string]any{
		"webhook_url":              "ciphertext-not-opened-here",
		"webhook_enabled":          true,
		"webhook_notify_period":    true,
		"webhook_notify_ovulation": true,
		"reminder_lead_days":       3,
	})
	pinPreflightNow(t, time.Date(2026, time.March, 27, 12, 0, 0, 0, time.UTC))

	document, _ := fixture.runPreflightJSON(t, []string{"silent-owner@example.com"})
	codes := findingCodes(document.Findings)
	if len(codes) != 1 || codes[0] != "no_delivery_recorded" {
		t.Fatalf("expected only no_delivery_recorded, got %v", codes)
	}
}

func TestRunPreflightCommandReportsNoProjection(t *testing.T) {
	// An owner who completed onboarding (so last_period_start is set) but has
	// logged no cycle at all, with only the OVULATION kind switched on: the
	// first-cycle floor clears the ovulation date, and the period kind was
	// switched off, so neither opted-in kind yields a date while the whole-
	// projection gate is still open. That is the `no_projection` residue.
	databasePath := createCLIUsersDatabase(t)
	owner := createCLIUsersUser(t, databasePath, "onboarded-owner@example.com", "Onboarded Owner", models.RoleOwner, true, time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC))
	updatePreflightUser(t, databasePath, owner.ID, map[string]any{
		"last_period_start":         time.Date(2026, time.March, 20, 0, 0, 0, 0, time.UTC),
		"webhook_url":               "ciphertext-not-opened-here",
		"webhook_enabled":           true,
		"webhook_notify_period":     false,
		"webhook_notify_ovulation":  true,
		"reminder_lead_days":        3,
		"webhook_last_delivered_at": time.Date(2026, time.March, 20, 9, 0, 0, 0, time.UTC),
	})
	fixture := preflightFixture{databasePath: databasePath, user: owner}
	pinPreflightNow(t, time.Date(2026, time.March, 27, 12, 0, 0, 0, time.UTC))

	document, err := fixture.runPreflightJSON(t, []string{"onboarded-owner@example.com"})
	if err == nil {
		t.Fatalf("expected a not-ready verdict")
	}
	codes := findingCodes(document.Findings)
	if len(codes) != 1 || codes[0] != "no_projection" {
		t.Fatalf("expected only no_projection, got %v", codes)
	}
	if document.Next.Kind != "" {
		t.Fatalf("expected no projected kind, got %q", document.Next.Kind)
	}
	if document.Next.Reason != "nothing can be projected yet" {
		t.Fatalf("expected the not-yet-projectable reason, got %q", document.Next.Reason)
	}
}

func TestRunPreflightCommandClampsAFutureDeliveryStamp(t *testing.T) {
	// A stamp ahead of the clock says nothing honest about age, so the report
	// states an age of 0 rather than a negative one — and must NOT read a future
	// stamp as stale.
	fixture := newPreflightFixture(t, "future-owner@example.com").
		makeReady(t, time.Date(2026, time.April, 10, 9, 0, 0, 0, time.UTC))
	pinPreflightNow(t, time.Date(2026, time.March, 27, 12, 0, 0, 0, time.UTC))

	document, err := fixture.runPreflightJSON(t, []string{"future-owner@example.com"})
	if err != nil {
		t.Fatalf("expected a ready owner, got %v", err)
	}
	if document.Webhook.LastDeliveryAgeDays == nil || *document.Webhook.LastDeliveryAgeDays != 0 {
		t.Fatalf("expected a clamped age of 0, got %v", document.Webhook.LastDeliveryAgeDays)
	}
}

func TestRunPreflightCommandReportsStaleDelivery(t *testing.T) {
	// 90 days before the pinned clock is past preflightStaleDeliveryDays.
	fixture := newPreflightFixture(t, "stale-owner@example.com").
		makeReady(t, time.Date(2025, time.December, 27, 9, 0, 0, 0, time.UTC))
	pinPreflightNow(t, time.Date(2026, time.March, 27, 12, 0, 0, 0, time.UTC))

	document, _ := fixture.runPreflightJSON(t, []string{"stale-owner@example.com"})
	codes := findingCodes(document.Findings)
	if len(codes) != 1 || codes[0] != "stale_delivery" {
		t.Fatalf("expected only stale_delivery, got %v", codes)
	}
	if document.Webhook.LastDeliveryAgeDays == nil || *document.Webhook.LastDeliveryAgeDays != 90 {
		t.Fatalf("expected a 90-day-old delivery, got %v", document.Webhook.LastDeliveryAgeDays)
	}
	if !strings.Contains(document.Findings[0].Detail, "90 days ago") {
		t.Fatalf("expected the detail to state the age, got %q", document.Findings[0].Detail)
	}
}
func TestRunPreflightCommandReportsPregnancyPause(t *testing.T) {
	fixture := newPreflightFixture(t, "paused-owner@example.com").
		makeReady(t, time.Date(2026, time.March, 20, 9, 0, 0, 0, time.UTC))
	pinPreflightNow(t, time.Date(2026, time.March, 30, 12, 0, 0, 0, time.UTC))

	// A positive pregnancy test after the latest cycle start, with no cycle
	// start after it, activates the pause — the same rule the dashboard and the
	// notify pass follow. 03-30 is after the live cycle's period days
	// (03-25..03-29), so the daily_logs unique key is not hit.
	database, err := db.OpenDatabase(db.Config{Driver: db.DriverSQLite, SQLitePath: fixture.databasePath})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	logEntry := models.DailyLog{
		UserID:        fixture.user.ID,
		Date:          time.Date(2026, time.March, 30, 0, 0, 0, 0, time.UTC),
		PregnancyTest: models.PregnancyTestPositive,
	}
	if err := database.Create(&logEntry).Error; err != nil {
		t.Fatalf("create pregnancy log: %v", err)
	}
	if sqlDB, err := database.DB(); err == nil {
		_ = sqlDB.Close()
	}

	document, err := fixture.runPreflightJSON(t, []string{"paused-owner@example.com"})
	if err == nil {
		t.Fatalf("expected a not-ready verdict under a pregnancy pause")
	}
	codes := findingCodes(document.Findings)
	if len(codes) != 1 || codes[0] != "predictions_suppressed" {
		t.Fatalf("expected only predictions_suppressed, got %v", codes)
	}
	if !strings.Contains(document.Findings[0].Detail, "pregnancy_pause") {
		t.Fatalf("expected the suppression reason to name the pregnancy pause, got %q", document.Findings[0].Detail)
	}
	if document.Next.Kind != "" || document.Next.ProjectedDate != "" {
		t.Fatalf("expected no projection under a pregnancy pause, got %+v", document.Next)
	}
}

func TestRunPreflightCommandReportsNoCycleHistory(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	createCLIUsersUser(t, databasePath, "empty-owner@example.com", "Empty Owner", models.RoleOwner, true, time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC))
	fixture := preflightFixture{databasePath: databasePath}
	pinPreflightNow(t, time.Date(2026, time.March, 27, 12, 0, 0, 0, time.UTC))

	rendered, err := fixture.runPreflightRaw(t, []string{"empty-owner@example.com"})
	if err == nil {
		t.Fatalf("expected a not-ready verdict with no cycle history")
	}
	// The report is still written to stdout before the verdict error, so a
	// script can capture it — that is part of the exit-code contract.
	for _, want := range []string{
		"verdict: not ready",
		"none — both reminder kinds are switched off",
		"[no_cycle_history]",
		"[no_webhook_configured]",
		"fix: ",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("table missing %q:\n%s", want, rendered)
		}
	}
}

func TestRunPreflightCommandRendersHumanTable(t *testing.T) {
	fixture := newPreflightFixture(t, "table-owner@example.com").
		makeReady(t, time.Date(2026, time.March, 20, 9, 0, 0, 0, time.UTC))
	pinPreflightNow(t, time.Date(2026, time.March, 27, 12, 0, 0, 0, time.UTC))

	rendered, err := fixture.runPreflightRaw(t, []string{"table-owner@example.com", "--tz", "Europe/Madrid"})
	if err != nil {
		t.Fatalf("expected a ready owner, got %v", err)
	}
	for _, want := range []string{
		"ovumcy preflight — table-owner@example.com",
		"as of: 2026-03-27 (Europe/Madrid)",
		"verdict: ready — nothing is blocking the next reminder",
		"kind:      ovulation-soon",
		"projected: 2026-04-07 (in 11 day(s))",
		"due today: false (lead window 3 day(s))",
		"enabled:      true",
		"endpoint:     configured",
		"last delivery: 2026-03-20T09:00:00Z",
		"findings\n  none",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("table missing %q:\n%s", want, rendered)
		}
	}
}

func TestRunPreflightCommandAcceptsIDAndRejectsBadInput(t *testing.T) {
	fixture := newPreflightFixture(t, "id-owner@example.com").
		makeReady(t, time.Date(2026, time.March, 20, 9, 0, 0, 0, time.UTC))
	pinPreflightNow(t, time.Date(2026, time.March, 27, 12, 0, 0, 0, time.UTC))

	document, err := fixture.runPreflightJSON(t, []string{"--id", strconv.FormatUint(uint64(fixture.user.ID), 10)})
	if err != nil {
		t.Fatalf("expected no error for the --id form, got %v", err)
	}
	if document.User.ID != fixture.user.ID {
		t.Fatalf("expected the --id target, got %+v", document.User)
	}

	if _, err := parsePreflightArgs([]string{}); err == nil {
		t.Fatalf("expected usage error with no target")
	}
	if _, err := parsePreflightArgs([]string{"a@example.com", "--nope"}); err == nil {
		t.Fatalf("expected usage error for unknown flag")
	}
	if _, err := parsePreflightArgs([]string{"a@example.com", "--tz", "Not/AZone"}); err == nil {
		t.Fatalf("expected usage error for a bad timezone")
	}
	if _, err := parsePreflightArgs([]string{"a@example.com", "--id", "nope"}); err == nil {
		t.Fatalf("expected usage error for a non-numeric --id")
	}
	if _, err := parsePreflightArgs([]string{"a@example.com", "b@example.com"}); err == nil {
		t.Fatalf("expected usage error for a second positional")
	}
	if _, err := parsePreflightArgs([]string{"a@example.com", "--id"}); err == nil {
		t.Fatalf("expected usage error for a flag with no value")
	}
	if _, err := parsePreflightArgs([]string{"a@example.com", "--json=true"}); err == nil {
		t.Fatalf("expected usage error for --json with a value")
	}
	opts, err := parsePreflightArgs([]string{"--id=7", "--tz=UTC", "--json"})
	if err != nil || opts.userID != 7 || !opts.asJSON || opts.tz != "UTC" {
		t.Fatalf("expected the flag=value form to parse, got %+v (%v)", opts, err)
	}
	if opts, err := parsePreflightArgs([]string{"", "  "}); err == nil || opts.email != "" {
		t.Fatalf("expected a usage error for blank arguments only")
	}
}

func TestRunPreflightCommandRejectsUnknownOwner(t *testing.T) {
	fixture := newPreflightFixture(t, "known-owner@example.com")
	pinPreflightNow(t, time.Date(2026, time.March, 27, 12, 0, 0, 0, time.UTC))

	_, err := fixture.runPreflightRaw(t, []string{"nobody@example.com"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected a not-found error, got %v", err)
	}
}

// TestPreflightSuppressionFixCoversEveryPublishedReason walks the services
// layer's own published suppression vocabulary, so a signal added there without
// a one-line fix here fails this test rather than silently borrowing the default
// sentence.
func TestPreflightSuppressionFixCoversEveryPublishedReason(t *testing.T) {
	reasons := []services.SuppressionReason{
		services.SuppressionReasonUnpredictableCycle,
		services.SuppressionReasonPregnancyPause,
		services.SuppressionReasonCycleOverdue,
		services.SuppressionReasonAwaitingFirstCycle,
	}
	for _, reason := range reasons {
		verdict := services.PredictionSuppression{Reasons: []services.SuppressionReason{reason}}
		fix := preflightSuppressionFix(verdict)
		if fix == "" || strings.Contains(fix, "re-run preflight") {
			t.Fatalf("reason %q has no specific fix: %q", reason, fix)
		}
	}
}

// TestPreflightSuppressionFixFallsBackOnAnUnknownReason keeps the default arm
// alive: a signal this package has never heard of must still produce advice.
func TestPreflightSuppressionFixFallsBackOnAnUnknownReason(t *testing.T) {
	verdict := services.PredictionSuppression{Reasons: []services.SuppressionReason{"a_future_signal"}}
	if fix := preflightSuppressionFix(verdict); !strings.Contains(fix, "re-run preflight") {
		t.Fatalf("expected the default remedy, got %q", fix)
	}
	if fix := preflightSuppressionFix(services.PredictionSuppression{}); !strings.Contains(fix, "re-run preflight") {
		t.Fatalf("expected the default remedy for an empty verdict, got %q", fix)
	}
}

// TestPreflightSuppressionReasonRendersTheVocabulary pins the empty-reasons
// fallback and the joined rendering a script reads.
func TestPreflightSuppressionReasonRendersTheVocabulary(t *testing.T) {
	if got := preflightSuppressionReason(services.PredictionSuppression{}); got != "predictions are suppressed" {
		t.Fatalf("expected the bare fallback, got %q", got)
	}
	got := preflightSuppressionReason(services.PredictionSuppression{
		Reasons: []services.SuppressionReason{services.SuppressionReasonPregnancyPause, services.SuppressionReasonCycleOverdue},
	})
	if !strings.Contains(got, "pregnancy_pause") || !strings.Contains(got, "cycle_overdue") {
		t.Fatalf("expected both reasons named, got %q", got)
	}
}

// TestPreflightOptedIntoAKind pins the guard that keeps the no-projection
// finding from firing for an owner who asked for nothing.
func TestPreflightOptedIntoAKind(t *testing.T) {
	if preflightOptedIntoAKind(&models.User{}) {
		t.Fatalf("expected false when both kinds are off")
	}
	if !preflightOptedIntoAKind(&models.User{WebhookNotifyPeriod: true}) {
		t.Fatalf("expected true when the period kind is on")
	}
	if !preflightOptedIntoAKind(&models.User{WebhookNotifyOvulation: true}) {
		t.Fatalf("expected true when the ovulation kind is on")
	}
}
