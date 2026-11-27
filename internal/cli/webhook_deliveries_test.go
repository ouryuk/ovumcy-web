package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/db"
	"github.com/ovumcy/ovumcy-web/internal/models"
)

// seedWebhookDeliveries writes delivery rows straight through the repository so
// the `show` output can be asserted without running a real notify pass. The
// rows are newest-first by occurred_at, matching what the repository returns.
func seedWebhookDeliveries(t *testing.T, databasePath string, userID uint, rows []models.WebhookDelivery) {
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

	repo := db.NewWebhookDeliveryRepository(database)
	for index := range rows {
		row := rows[index]
		row.UserID = userID
		if err := repo.RecordWebhookDelivery(t.Context(), row); err != nil {
			t.Fatalf("record delivery row %d: %v", index, err)
		}
	}
}

// TestRunWebhookCommandShowAppendsTheLatestThreeOutcomes proves `webhook show`
// appends the latest delivery outcomes beneath the existing status block: the
// three most recent rows appear, an older fourth does NOT, and each failure is
// reduced to its bounded class or status code. It also proves the appended
// lines leak nothing: the endpoint's URL, path, query and token never appear,
// only the status block's host.
func TestRunWebhookCommandShowAppendsTheLatestThreeOutcomes(t *testing.T) {
	t.Parallel()

	databasePath := createCLIWebhookDatabase(t)
	owner := createCLIWebhookOwner(t, databasePath, "owner@example.com")

	var armOut bytes.Buffer
	if err := runWebhookCommand(
		sqliteConfig(databasePath),
		testWebhookSecretKey,
		[]string{"set", "owner@example.com", "--enabled=true", "--url-stdin"},
		strings.NewReader(testWebhookURLWithToken+"\n"),
		&armOut,
	); err != nil {
		t.Fatalf("arm webhook: %v", err)
	}

	base := time.Date(2026, time.November, 27, 9, 0, 0, 0, time.UTC)
	seedWebhookDeliveries(t, databasePath, owner.ID, []models.WebhookDelivery{
		// Oldest first; the show output reverses to newest first.
		{OccurredAt: base, ReminderKind: models.WebhookDeliveryKindPeriod, Outcome: models.WebhookDeliveryOutcomeOK, HTTPStatus: 200, EndpointHost: "ntfy.example"},
		{OccurredAt: base.Add(time.Hour), ReminderKind: models.WebhookDeliveryKindOvulation, Outcome: models.WebhookDeliveryOutcomeError, HTTPStatus: 0, ErrorClass: models.WebhookDeliveryErrorTimeout, EndpointHost: "ntfy.example"},
		{OccurredAt: base.Add(2 * time.Hour), ReminderKind: models.WebhookDeliveryKindTest, Outcome: models.WebhookDeliveryOutcomeRefused, HTTPStatus: 503, ErrorClass: models.WebhookDeliveryErrorHTTP, EndpointHost: "ntfy.example"},
		{OccurredAt: base.Add(3 * time.Hour), ReminderKind: models.WebhookDeliveryKindPeriod, Outcome: models.WebhookDeliveryOutcomeOK, HTTPStatus: 200, EndpointHost: "ntfy.example"},
	})

	var output bytes.Buffer
	if err := runWebhookCommand(
		sqliteConfig(databasePath),
		testWebhookSecretKey,
		[]string{"show", "owner@example.com"},
		strings.NewReader(""),
		&output,
	); err != nil {
		t.Fatalf("runWebhookCommand(show): %v", err)
	}

	rendered := output.String()
	// The status block still leads, unchanged.
	if !strings.Contains(rendered, "webhook status for owner@example.com") {
		t.Fatalf("expected status header, got %q", rendered)
	}
	if !strings.Contains(rendered, "recent deliveries:") {
		t.Fatalf("expected the delivery history section, got %q", rendered)
	}

	// The three NEWEST rows appear — the ok, the refused (with its status code),
	// and the timeout (with its class) — and the oldest ok does not.
	if got := strings.Count(rendered, "2026-11-27 12:00"); got != 1 {
		t.Errorf("expected the newest row's time once, got %d occurrences in %q", got, rendered)
	}
	if !strings.Contains(rendered, "http_status=503") {
		t.Errorf("expected the refused row's status code, got %q", rendered)
	}
	if !strings.Contains(rendered, "error_class=timeout") {
		t.Errorf("expected the timeout row's class, got %q", rendered)
	}
	// Bounded at three: the oldest row (09:00) must be outside the window.
	if strings.Contains(rendered, "2026-11-27 09:00") {
		t.Errorf("expected the fourth-newest row to be excluded, got %q", rendered)
	}

	// The history lines add no secret to the output.
	assertNoSecretLeak(t, rendered)
}

// TestRunWebhookCommandShowWithoutDeliveriesPrintsNoHistory proves `show` on an
// owner who has never attempted a delivery prints the status block and no
// empty "recent deliveries:" heading — an empty section would read as a
// failure rather than as "nothing has happened yet".
func TestRunWebhookCommandShowWithoutDeliveriesPrintsNoHistory(t *testing.T) {
	t.Parallel()

	databasePath := createCLIWebhookDatabase(t)
	createCLIWebhookOwner(t, databasePath, "owner@example.com")

	var output bytes.Buffer
	if err := runWebhookCommand(
		sqliteConfig(databasePath),
		testWebhookSecretKey,
		[]string{"show", "owner@example.com"},
		strings.NewReader(""),
		&output,
	); err != nil {
		t.Fatalf("runWebhookCommand(show): %v", err)
	}

	rendered := output.String()
	if !strings.Contains(rendered, "webhook status for owner@example.com") {
		t.Fatalf("expected status header, got %q", rendered)
	}
	if strings.Contains(rendered, "recent deliveries:") {
		t.Fatalf("expected no delivery section when there is no history, got %q", rendered)
	}
}
