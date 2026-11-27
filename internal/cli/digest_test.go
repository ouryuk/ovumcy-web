package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/db"
	"github.com/ovumcy/ovumcy-web/internal/models"
)

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
