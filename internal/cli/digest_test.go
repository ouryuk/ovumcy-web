package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/db"
	"github.com/ovumcy/ovumcy-web/internal/models"
	"github.com/ovumcy/ovumcy-web/internal/services"
)

// openDigestRepositories opens a repository set over the test database, the
// same construction the command uses, so the delivery tests exercise the real
// UserRepository rather than a fake.
func openDigestRepositories(t *testing.T, databasePath string) (*db.Repositories, *services.CalendarFeedRestoreFence) {
	t.Helper()

	database, err := db.OpenDatabase(sqliteConfig(databasePath))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatalf("open sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	return buildRepositories(database, calendarFeedFencePath())
}

// digestHostOf is the expected redaction of a test endpoint: the host, taken
// the same way the production report takes it.
func digestHostOf(t *testing.T, endpoint string) string {
	t.Helper()

	parsed, err := url.Parse(endpoint)
	if err != nil {
		t.Fatalf("parse test endpoint %q: %v", endpoint, err)
	}
	return parsed.Hostname()
}

// pinDigestNow pins digestNow's clock for the duration of one test and restores
// the real clock afterwards, exactly as pinPredictNow does for predict.
func pinDigestNow(t *testing.T, pinned time.Time) {
	t.Helper()
	previous := digestNow
	digestNow = func() time.Time { return pinned }
	t.Cleanup(func() { digestNow = previous })
}

// seedDigestFixture seeds one owner with three completed 28-day cycles and a
// live fourth cycle plus the symptoms and moods the three shipped commands
// report — the same shape seedTrendsFixture uses, anchored so that a clock
// pinned at 2026-03-27 lands on day 3 of the live cycle. It returns the owner
// and the symptom rows so a test can assert the tallies by name.
func seedDigestFixture(t *testing.T, databasePath string) (models.User, models.SymptomType, models.SymptomType) {
	t.Helper()

	user := createCLIUsersUser(t, databasePath, "digest-owner@example.com", "Digest Owner", models.RoleOwner, true, time.Date(2026, time.January, 1, 8, 0, 0, 0, time.UTC))

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
	return user, cramps, headache
}

// runDigestRaw runs the command and returns its raw stdout.
func runDigestRaw(t *testing.T, databasePath string, args ...string) string {
	t.Helper()

	var output bytes.Buffer
	err := runDigestCommand(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath}, testWebhookSecretKey, false, args, &output)
	if err != nil {
		t.Fatalf("runDigestCommand returned error: %v (output %q)", err, output.String())
	}
	return output.String()
}

// TestRunDigestCommandRendersSpanishMarkdownGolden is the golden test: one
// pinned week, the full Spanish digest, byte for byte. It is the contract the
// changelog fragment describes — the digest composes summary + trends +
// predict, and a change to any of the three shows up here.
func TestRunDigestCommandRendersSpanishMarkdownGolden(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	seedDigestFixture(t, databasePath)
	pinDigestNow(t, time.Date(2026, time.March, 27, 12, 0, 0, 0, time.UTC))

	rendered := runDigestRaw(t, databasePath, "digest-owner@example.com", "--week")

	want := `# Resumen semanal de Ovumcy

**Cuenta:** digest-owner@example.com  
**Generado:** 2026-03-27  
**Ventana:** 2026-03-21 .. 2026-03-27 (7 días)  
**Zona horaria:** UTC

## Constancia de registro

- Días con registro: 3 de 7 (42.9%)
- Racha más larga: 3 día(s)

## Síntomas más frecuentes

1. Cramps — 1 vez(veces)

## Estado de ánimo

- Mejor día: 2026-03-26 (ánimo 3)
- Día más difícil: 2026-03-26 (ánimo 3)

## Patrones por fase

Ciclos completados: 3

### Menstrual

- 🩸 Cramps — 3 vez(veces) (20.0% de los días)
- Ánimo medio: 3.0 (3 registros)

### Folicular

- Ánimo medio: 4.0 (3 registros)

### Ovulación

### Lútea

- 🤕 Headache — 3 vez(veces) (100.0% de los días)
- Ánimo medio: 2.0 (3 registros)

## Próximo ciclo

- Próximo periodo: 2026-04-22
- Ovulación estimada: 2026-04-07
- Ventana fértil: 2026-04-02 .. 2026-04-07
- Ciclo medio: 28 días (mediana 28, 3 ciclos completados)

---

` + predictDisclaimer + `
`

	if rendered != want {
		t.Fatalf("digest markdown drifted.\n--- got ---\n%s\n--- want ---\n%s", rendered, want)
	}
}

// TestRunDigestCommandCarriesTheThreeShippedDocumentsAsJSON proves the JSON
// rendering is a VIEW: the embedded summary, trends and predict documents are
// the shipped ones, carrying their own schema tags, so a script reading
// digest.summary is reading exactly what `ovumcy summary --json` emits.
func TestRunDigestCommandCarriesTheThreeShippedDocumentsAsJSON(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	user, _, _ := seedDigestFixture(t, databasePath)
	pinDigestNow(t, time.Date(2026, time.March, 27, 12, 0, 0, 0, time.UTC))

	rendered := runDigestRaw(t, databasePath, "digest-owner@example.com", "--week", "--format", "json")

	var document DigestDocument
	if err := json.Unmarshal([]byte(rendered), &document); err != nil {
		t.Fatalf("output is not valid JSON: %v (%q)", err, rendered)
	}
	if document.Schema != digestSchemaVersion {
		t.Fatalf("expected schema %q, got %q", digestSchemaVersion, document.Schema)
	}
	if document.User.ID != user.ID || document.User.Email != "digest-owner@example.com" {
		t.Fatalf("unexpected user: %+v", document.User)
	}
	if document.Language != "es" {
		t.Fatalf("expected the digest rendered in Spanish, got %q", document.Language)
	}
	if document.AsOf != "2026-03-27" || document.WindowDays != digestWeekDays {
		t.Fatalf("unexpected window: as_of %q days %d", document.AsOf, document.WindowDays)
	}
	if document.From != "2026-03-21" || document.To != "2026-03-27" {
		t.Fatalf("expected the window to END on as_of, got %q..%q", document.From, document.To)
	}

	// The three embedded documents keep their own shipped schema tags.
	if document.Summary.Schema != summarySchemaVersion {
		t.Fatalf("expected embedded summary schema %q, got %q", summarySchemaVersion, document.Summary.Schema)
	}
	if document.Trends.Schema != trendsSchemaVersion {
		t.Fatalf("expected embedded trends schema %q, got %q", trendsSchemaVersion, document.Trends.Schema)
	}
	if document.Predict.Schema != predictSchemaVersion {
		t.Fatalf("expected embedded predict schema %q, got %q", predictSchemaVersion, document.Predict.Schema)
	}
	if document.Predict.NextPeriodStart != "2026-04-22" {
		t.Fatalf("expected the projection to travel unchanged, got %q", document.Predict.NextPeriodStart)
	}
	if document.Disclaimer != predictDisclaimer {
		t.Fatalf("expected the projection disclaimer, got %q", document.Disclaimer)
	}
	if document.Delivery != nil {
		t.Fatalf("expected no delivery without --ntfy, got %+v", document.Delivery)
	}
}

// TestRunDigestCommandWindowEndsOnTheOwnersCalendarDay is the timezone boundary
// test: the same instant is two different calendar days in Kiritimati (UTC+14)
// and Pago Pago (UTC-11), and the digest must land its window on the OWNER's
// day. A digest generated at 2026-03-27T23:30Z is already 2026-03-28 for an
// owner in UTC+14, so their week ends a day later than a UTC owner's would.
func TestRunDigestCommandWindowEndsOnTheOwnersCalendarDay(t *testing.T) {
	instant := time.Date(2026, time.March, 27, 23, 30, 0, 0, time.UTC)

	for _, testCase := range []struct {
		name     string
		timezone string
		wantAsOf string
		wantFrom string
	}{
		{name: "east of UTC rolls into the next day", timezone: "Pacific/Kiritimati", wantAsOf: "2026-03-28", wantFrom: "2026-03-22"},
		{name: "west of UTC is still the previous day", timezone: "Pacific/Pago_Pago", wantAsOf: "2026-03-27", wantFrom: "2026-03-21"},
		{name: "UTC is the reference", timezone: "UTC", wantAsOf: "2026-03-27", wantFrom: "2026-03-21"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			databasePath := createCLIUsersDatabase(t)
			seedDigestFixture(t, databasePath)
			pinDigestNow(t, instant)

			rendered := runDigestRaw(t, databasePath, "digest-owner@example.com", "--week", "--tz", testCase.timezone, "--format", "json")

			var document DigestDocument
			if err := json.Unmarshal([]byte(rendered), &document); err != nil {
				t.Fatalf("output is not valid JSON: %v (%q)", err, rendered)
			}
			if document.Timezone != testCase.timezone {
				t.Fatalf("expected timezone %q, got %q", testCase.timezone, document.Timezone)
			}
			if document.AsOf != testCase.wantAsOf {
				t.Fatalf("expected as_of %q in %s, got %q", testCase.wantAsOf, testCase.timezone, document.AsOf)
			}
			if document.From != testCase.wantFrom || document.To != testCase.wantAsOf {
				t.Fatalf("expected window %q..%q, got %q..%q", testCase.wantFrom, testCase.wantAsOf, document.From, document.To)
			}
		})
	}
}

// TestRunDigestCommandWindowSurvivesASkippedLocalMidnight is the DST regression:
// America/Santiago springs forward at midnight on 2026-09-06, so that day has no
// local midnight at all. The window is stepped from a UTC anchor, so the day is
// not silently lost — the failure mode AddDate-from-a-location-anchor produces,
// where the step resolves BACKWARD to 2026-09-05T23:00 and the window ends a day
// early.
func TestRunDigestCommandWindowSurvivesASkippedLocalMidnight(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	seedDigestFixture(t, databasePath)
	// 14:00Z on 2026-09-06 is 11:00 local in Santiago — comfortably inside the day.
	pinDigestNow(t, time.Date(2026, time.September, 6, 14, 0, 0, 0, time.UTC))

	rendered := runDigestRaw(t, databasePath, "digest-owner@example.com", "--week", "--tz", "America/Santiago", "--format", "json")

	var document DigestDocument
	if err := json.Unmarshal([]byte(rendered), &document); err != nil {
		t.Fatalf("output is not valid JSON: %v (%q)", err, rendered)
	}
	if document.AsOf != "2026-09-06" {
		t.Fatalf("expected as_of 2026-09-06, got %q", document.AsOf)
	}
	// A seven-day window ending on the 6th starts on the 31st of August.
	if document.To != "2026-09-06" || document.From != "2026-08-31" {
		t.Fatalf("expected window 2026-08-31..2026-09-06, got %q..%q", document.From, document.To)
	}
	if document.WindowDays != digestWeekDays {
		t.Fatalf("expected %d window days, got %d", digestWeekDays, document.WindowDays)
	}
}

// TestRunDigestCommandPrefersTheOwnersPersistedTimezone proves the default: with
// no --tz the digest reads users.timezone, which is the whole point of a
// shareable digest agreeing with the calendar the owner actually reads.
func TestRunDigestCommandPrefersTheOwnersPersistedTimezone(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	user, _, _ := seedDigestFixture(t, databasePath)
	pinDigestNow(t, time.Date(2026, time.March, 27, 23, 30, 0, 0, time.UTC))

	database, err := db.OpenDatabase(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatalf("open sql db: %v", err)
	}
	if err := database.Model(&models.User{}).Where("id = ?", user.ID).Update("timezone", "Pacific/Kiritimati").Error; err != nil {
		t.Fatalf("update timezone: %v", err)
	}
	_ = sqlDB.Close()

	rendered := runDigestRaw(t, databasePath, "digest-owner@example.com", "--week", "--format", "json")

	var document DigestDocument
	if err := json.Unmarshal([]byte(rendered), &document); err != nil {
		t.Fatalf("output is not valid JSON: %v (%q)", err, rendered)
	}
	if document.Timezone != "Pacific/Kiritimati" {
		t.Fatalf("expected the owner's persisted timezone, got %q", document.Timezone)
	}
	if document.AsOf != "2026-03-28" {
		t.Fatalf("expected the owner's calendar day 2026-03-28, got %q", document.AsOf)
	}
}

// TestParseDigestArgs covers the flag contract: both `--flag value` and
// `--flag=value` forms, --week setting the seven-day window, --days overriding
// it, and every rejection path returning the single stable usage string.
func TestParseDigestArgs(t *testing.T) {
	t.Run("defaults to a 30-day markdown digest with no delivery", func(t *testing.T) {
		opts, err := parseDigestArgs([]string{"owner@example.com"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if opts.email != "owner@example.com" || opts.days != 30 || opts.format != "markdown" || opts.ntfy || opts.week {
			t.Fatalf("unexpected defaults: %+v", opts)
		}
	})

	t.Run("--week selects seven days", func(t *testing.T) {
		opts, err := parseDigestArgs([]string{"owner@example.com", "--week"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !opts.week || opts.days != digestWeekDays {
			t.Fatalf("expected a seven-day week window, got %+v", opts)
		}
	})

	t.Run("--days overrides --week", func(t *testing.T) {
		opts, err := parseDigestArgs([]string{"owner@example.com", "--week", "--days", "14"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if opts.week || opts.days != 14 {
			t.Fatalf("expected --days to win, got %+v", opts)
		}
	})

	t.Run("accepts --flag=value in every form", func(t *testing.T) {
		opts, err := parseDigestArgs([]string{"--id=42", "--days=7", "--format=json", "--tz=Europe/Madrid", "--ntfy"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if opts.userID != 42 || opts.days != 7 || opts.format != "json" || opts.tz != "Europe/Madrid" || !opts.ntfy {
			t.Fatalf("unexpected options: %+v", opts)
		}
	})

	t.Run("--id alone is accepted, as in summary", func(t *testing.T) {
		opts, err := parseDigestArgs([]string{"--id", "7"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if opts.userID != 7 || opts.email != "" {
			t.Fatalf("unexpected options: %+v", opts)
		}
	})

	for _, testCase := range []struct {
		name string
		args []string
	}{
		{name: "no target at all", args: nil},
		{name: "second positional", args: []string{"a@example.com", "b@example.com"}},
		{name: "unknown flag", args: []string{"owner@example.com", "--verbose"}},
		{name: "unknown flag with value", args: []string{"owner@example.com", "--verbose=1"}},
		{name: "days out of range low", args: []string{"owner@example.com", "--days", "0"}},
		{name: "days out of range high", args: []string{"owner@example.com", "--days", "91"}},
		{name: "days not a number", args: []string{"owner@example.com", "--days", "week"}},
		{name: "format outside the set", args: []string{"owner@example.com", "--format", "csv"}},
		{name: "tz that does not resolve", args: []string{"owner@example.com", "--tz", "Mars/Olympus"}},
		{name: "boolean flag given a value", args: []string{"owner@example.com", "--week=7"}},
		{name: "bare flag with no value", args: []string{"owner@example.com", "--format"}},
		{name: "malformed id", args: []string{"--id", "not-a-number"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := parseDigestArgs(testCase.args)
			if err == nil {
				t.Fatalf("expected an error for %#v", testCase.args)
			}
			if err.Error() != digestUsage {
				t.Fatalf("expected the stable usage string, got %q", err.Error())
			}
		})
	}
}

// TestRunDigestCommandNTFYDeliversTheMarkdownThroughTheStoredEndpoint proves the
// opt-in delivery path: --ntfy hands the SAME Markdown the operator sees to the
// delivery seam, the document reports the attempt, and the destination is named
// by HOST only — never the URL, its path or its token.
func TestRunDigestCommandNTFYDeliversTheMarkdownThroughTheStoredEndpoint(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	user, _, _ := seedDigestFixture(t, databasePath)
	pinDigestNow(t, time.Date(2026, time.March, 27, 12, 0, 0, 0, time.UTC))

	var deliveredMarkdown string
	var deliveredTo uint
	previousDeliver := deliverDigest
	deliverDigest = func(_ db.Config, _ string, _ bool, userID uint, markdown string) *digestDeliveryDoc {
		deliveredTo = userID
		deliveredMarkdown = markdown
		return &digestDeliveryDoc{Attempted: true, Host: "notify.example.com", Delivered: true}
	}
	t.Cleanup(func() { deliverDigest = previousDeliver })

	rendered := runDigestRaw(t, databasePath, "digest-owner@example.com", "--week", "--ntfy")

	if deliveredTo != user.ID {
		t.Fatalf("expected delivery to owner %d, got %d", user.ID, deliveredTo)
	}
	// The delivered body is the same Spanish Markdown the operator sees.
	if !strings.HasPrefix(deliveredMarkdown, "# Resumen semanal de Ovumcy") {
		t.Fatalf("expected the Markdown digest to be delivered, got %q", deliveredMarkdown)
	}
	if !strings.Contains(rendered, "## Entrega") || !strings.Contains(rendered, "Enviado a notify.example.com.") {
		t.Fatalf("expected the rendering to report the delivery, got %q", rendered)
	}
	assertNoSecretLeak(t, rendered)
}

// TestRunDigestCommandNTFYFailureStillPrintsTheDigestAndFails proves the failure
// contract: an endpoint that refuses the digest does not cost the operator the
// content — the digest still prints — but the command exits non-zero so a cron
// job sees that delivery did not happen.
func TestRunDigestCommandNTFYFailureStillPrintsTheDigestAndFails(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	seedDigestFixture(t, databasePath)
	pinDigestNow(t, time.Date(2026, time.March, 27, 12, 0, 0, 0, time.UTC))

	previousDeliver := deliverDigest
	deliverDigest = func(_ db.Config, _ string, _ bool, _ uint, _ string) *digestDeliveryDoc {
		return &digestDeliveryDoc{Attempted: true, Host: "notify.example.com", Delivered: false, Error: ErrDigestDeliveryFailed.Error()}
	}
	t.Cleanup(func() { deliverDigest = previousDeliver })

	var output bytes.Buffer
	err := runDigestCommand(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath}, testWebhookSecretKey, false, []string{"digest-owner@example.com", "--week", "--ntfy"}, &output)
	if err == nil {
		t.Fatalf("expected the command to fail when delivery fails")
	}
	if !strings.Contains(output.String(), "# Resumen semanal de Ovumcy") {
		t.Fatalf("expected the digest to still print on delivery failure, got %q", output.String())
	}
	if !strings.Contains(output.String(), "No se pudo enviar a notify.example.com") {
		t.Fatalf("expected the failure to be reported by host, got %q", output.String())
	}
	assertNoSecretLeak(t, output.String())
}

// TestRunDigestCommandReportsAnOwnerWithNoHistory proves the two empty-state
// sentences: an owner with no logs gets an explicit "no symptoms"/"no mood"/"no
// cycle history" digest rather than a zeroed one that reads like real data.
func TestRunDigestCommandReportsAnOwnerWithNoHistory(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	createCLIUsersUser(t, databasePath, "fresh-owner@example.com", "Fresh Owner", models.RoleOwner, true, time.Date(2026, time.March, 1, 8, 0, 0, 0, time.UTC))
	pinDigestNow(t, time.Date(2026, time.March, 27, 12, 0, 0, 0, time.UTC))

	rendered := runDigestRaw(t, databasePath, "fresh-owner@example.com", "--week")

	for _, want := range []string{
		"_Sin síntomas registrados en esta ventana._",
		"_Sin estado de ánimo registrado en esta ventana._",
		"_Sin ciclos completados: todavía no se pueden calcular los patrones por fase._",
		"_Sin historial de ciclos: todavía no se puede proyectar la próxima ventana._",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected %q in an empty digest, got %q", want, rendered)
		}
	}
}

// TestRunDigestCommandRejectsAnUnknownAccount proves the shared operator
// vocabulary still advises the operator, exactly as summary and predict do.
func TestRunDigestCommandRejectsAnUnknownAccount(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	seedDigestFixture(t, databasePath)

	var output bytes.Buffer
	err := runDigestCommand(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath}, testWebhookSecretKey, false, []string{"nobody@example.com"}, &output)
	if err == nil {
		t.Fatalf("expected an unknown account to fail")
	}
	if !strings.Contains(err.Error(), "users list") {
		t.Fatalf("expected the shared operator advice, got %q", err.Error())
	}
}

// TestResolveDigestTimezone covers the fallback rule: an explicit override wins,
// the stored value is the default, and a stored value that no longer resolves
// falls back to UTC rather than making an owner's digest unreadable.
func TestResolveDigestTimezone(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		override string
		stored   string
		want     string
	}{
		{name: "override wins", override: "Europe/Madrid", stored: "Pacific/Kiritimati", want: "Europe/Madrid"},
		{name: "stored is the default", override: "", stored: "Pacific/Kiritimati", want: "Pacific/Kiritimati"},
		{name: "retired zone falls back", override: "", stored: "Mars/Olympus", want: "UTC"},
		{name: "nothing stored", override: "", stored: "", want: "UTC"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := resolveDigestTimezone(testCase.override, testCase.stored); got != testCase.want {
				t.Fatalf("expected %q, got %q", testCase.want, got)
			}
		})
	}
}

// storeDigestWebhook persists a real encrypted endpoint for the owner through
// the shipped `webhook set` path, so the delivery tests exercise the same
// ciphertext-at-rest the production notifier decrypts.
func storeDigestWebhook(t *testing.T, databasePath string, email string, endpoint string) {
	t.Helper()

	var output bytes.Buffer
	err := runWebhookCommand(
		sqliteConfig(databasePath),
		testWebhookSecretKey,
		[]string{"set", email, "--enabled=true", "--notify-period=true", "--notify-ovulation=true", "--url-stdin"},
		strings.NewReader(endpoint+"\n"),
		&output,
	)
	if err != nil {
		t.Fatalf("runWebhookCommand(set) returned error: %v", err)
	}
}

// TestDigestNotifierDeliversToTheStoredEndpoint drives the REAL notifier — the
// shipped decrypt seam, the shipped URL guard and the shipped hardened
// deliverer — against a local endpoint, so --ntfy's production path is covered
// rather than only its stub. It also proves the payload carries the digest in
// the message body and the mandatory disclaimer.
func TestDigestNotifierDeliversToTheStoredEndpoint(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	user, _, _ := seedDigestFixture(t, databasePath)

	var received services.WebhookPayload
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	storeDigestWebhook(t, databasePath, "digest-owner@example.com", server.URL+"/digest-topic")

	repositories, _ := openDigestRepositories(t, databasePath)
	notifier := NewDigestNotifier(repositories.Users, testWebhookSecretKey, false)

	host, err := notifier.Deliver(context.Background(), user.ID, "# Resumen semanal de Ovumcy\n")
	if err != nil {
		t.Fatalf("Deliver returned error: %v", err)
	}
	// The report names the HOST, never the URL or its path.
	if host != digestHostOf(t, server.URL) {
		t.Fatalf("expected the endpoint host %q, got %q", digestHostOf(t, server.URL), host)
	}
	if received.Message != "# Resumen semanal de Ovumcy\n" {
		t.Fatalf("expected the digest as the message body, got %q", received.Message)
	}
	// The envelope is resolved from the catalogue at the digest's language, not a
	// literal: the Spanish medical disclaimer and the Spanish digest title.
	if received.Title != "Resumen semanal de Ovumcy" {
		t.Fatalf("expected the Spanish catalogue title, got %q", received.Title)
	}
	if !strings.Contains(received.Disclaimer, "no consejo médico") {
		t.Fatalf("expected the Spanish medical disclaimer from the catalogue, got %q", received.Disclaimer)
	}
	// The mandatory disclaimer is never the empty miss-rendering.
	if strings.TrimSpace(received.Disclaimer) == "" {
		t.Fatalf("the delivered payload carried no disclaimer")
	}
}

// TestDigestNotifierReportsEachDeliveryFailure covers the sentinel vocabulary:
// no stored endpoint, an unusable stored value, and an endpoint that refuses
// the POST. None of them may echo the URL.
func TestDigestNotifierReportsEachDeliveryFailure(t *testing.T) {
	t.Run("no endpoint stored", func(t *testing.T) {
		databasePath := createCLIUsersDatabase(t)
		user, _, _ := seedDigestFixture(t, databasePath)

		repositories, _ := openDigestRepositories(t, databasePath)
		notifier := NewDigestNotifier(repositories.Users, testWebhookSecretKey, false)

		host, err := notifier.Deliver(context.Background(), user.ID, "# digest\n")
		if !errors.Is(err, ErrDigestDeliveryNotConfigured) {
			t.Fatalf("expected ErrDigestDeliveryNotConfigured, got %v", err)
		}
		if host != "" {
			t.Fatalf("expected no host, got %q", host)
		}
	})

	t.Run("stored ciphertext does not decrypt", func(t *testing.T) {
		databasePath := createCLIUsersDatabase(t)
		user, _, _ := seedDigestFixture(t, databasePath)
		storeDigestWebhook(t, databasePath, "digest-owner@example.com", "https://ntfy.example/topic-abc")

		// A different SECRET_KEY cannot open the stored ciphertext.
		repositories, _ := openDigestRepositories(t, databasePath)
		notifier := NewDigestNotifier(repositories.Users, "ffffffffffffffffffffffffffffffff", false)

		if _, err := notifier.Deliver(context.Background(), user.ID, "# digest\n"); !errors.Is(err, ErrDigestDeliveryUnavailable) {
			t.Fatalf("expected ErrDigestDeliveryUnavailable, got %v", err)
		}
	})

	t.Run("endpoint refuses the POST", func(t *testing.T) {
		databasePath := createCLIUsersDatabase(t)
		user, _, _ := seedDigestFixture(t, databasePath)

		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(server.Close)
		storeDigestWebhook(t, databasePath, "digest-owner@example.com", server.URL+"/digest-topic")

		repositories, _ := openDigestRepositories(t, databasePath)
		notifier := NewDigestNotifier(repositories.Users, testWebhookSecretKey, false)

		host, err := notifier.Deliver(context.Background(), user.ID, "# digest\n")
		if !errors.Is(err, ErrDigestDeliveryFailed) {
			t.Fatalf("expected ErrDigestDeliveryFailed, got %v", err)
		}
		// A refused delivery still names the host it tried, for the report.
		if host != digestHostOf(t, server.URL) {
			t.Fatalf("expected the endpoint host, got %q", host)
		}
	})
}

// TestRunDigestDeliveryReportsAnAttempt proves the production --ntfy wrapper
// records the attempt and reports the real outcome through the document.
func TestRunDigestDeliveryReportsAnAttempt(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	user, _, _ := seedDigestFixture(t, databasePath)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	storeDigestWebhook(t, databasePath, "digest-owner@example.com", server.URL+"/digest-topic")

	report := runDigestDelivery(sqliteConfig(databasePath), testWebhookSecretKey, false, user.ID, "# digest\n")
	if report == nil || !report.Attempted {
		t.Fatalf("expected an attempted delivery, got %+v", report)
	}
	if !report.Delivered || report.Error != "" {
		t.Fatalf("expected a successful delivery, got %+v", report)
	}
	if report.Host != digestHostOf(t, server.URL) {
		t.Fatalf("expected the endpoint host, got %q", report.Host)
	}
}

// TestRunDigestCommandThroughThePublicEntryPoint proves the os.Stdout wrapper
// runs the same command and renders the digest to stdout.
func TestRunDigestCommandThroughThePublicEntryPoint(t *testing.T) {
	databasePath := createCLIUsersDatabase(t)
	seedDigestFixture(t, databasePath)
	pinDigestNow(t, time.Date(2026, time.March, 27, 12, 0, 0, 0, time.UTC))

	// RunDigestCommand writes to os.Stdout; capturing it is the point of the
	// wrapper, so the assertion is that it succeeds without error.
	if err := RunDigestCommand(sqliteConfig(databasePath), testWebhookSecretKey, false, []string{"digest-owner@example.com", "--week"}); err != nil {
		t.Fatalf("RunDigestCommand returned error: %v", err)
	}
}

// TestDigestHostOnly covers the redaction helper directly, including the URL
// that cannot be parsed.
func TestDigestHostOnly(t *testing.T) {
	for _, testCase := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "https endpoint keeps only the host", raw: "https://ntfy.example/topic-abc?token=tk_secret", want: "ntfy.example"},
		{name: "port is dropped", raw: "http://127.0.0.1:9999/topic", want: "127.0.0.1"},
		{name: "unparseable value yields nothing", raw: "://nonsense", want: ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := digestHostOnly(testCase.raw); got != testCase.want {
				t.Fatalf("expected %q, got %q", testCase.want, got)
			}
		})
	}
}
