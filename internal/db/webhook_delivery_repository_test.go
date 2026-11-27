package db

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/models"
)

// TestWebhookDeliveryRepositoryKeepsTheNewestTwenty is the ring's own bar: it
// writes more rows than the ring holds and asserts BOTH halves — that the
// history is bounded at exactly the ring size, and that the rows that survived
// are the NEWEST ones. A prune that kept the first twenty would pass a
// count-only assertion and still be useless: the owner would be reading last
// month's attempts while this week's fell off the end.
func TestWebhookDeliveryRepositoryKeepsTheNewestTwenty(t *testing.T) {
	database := openSQLiteForMigrationBootstrapTest(t, filepath.Join(t.TempDir(), "deliveries.db"))
	userID := createDailyLogTestUser(t, database, "delivery-ring@example.com")
	repo := NewWebhookDeliveryRepository(database)
	ctx := context.Background()

	const inserted = WebhookDeliveryRingSize + 5
	base := time.Date(2026, time.November, 1, 8, 0, 0, 0, time.UTC)
	for index := 0; index < inserted; index++ {
		row := models.WebhookDelivery{
			UserID:       userID,
			OccurredAt:   base.Add(time.Duration(index) * time.Hour),
			ReminderKind: models.WebhookDeliveryKindPeriod,
			Outcome:      models.WebhookDeliveryOutcomeOK,
			HTTPStatus:   200,
			EndpointHost: "ntfy.example.com",
		}
		requireNoErr(t, repo.RecordWebhookDelivery(ctx, row), fmt.Sprintf("record row %d", index))
	}

	rows, err := repo.ListRecentWebhookDeliveries(ctx, userID, 0)
	requireNoErr(t, err, "list recent")

	if len(rows) != WebhookDeliveryRingSize {
		t.Fatalf("expected the ring to hold %d rows, got %d", WebhookDeliveryRingSize, len(rows))
	}

	// Newest first, and exactly the newest window: the oldest surviving row is
	// the one `inserted - WebhookDeliveryRingSize` hours after the base.
	wantOldest := base.Add(time.Duration(inserted-WebhookDeliveryRingSize) * time.Hour)
	gotOldest := rows[len(rows)-1].OccurredAt.UTC()
	if !gotOldest.Equal(wantOldest) {
		t.Fatalf("expected the oldest surviving row at %s, got %s", wantOldest, gotOldest)
	}
	if !rows[0].OccurredAt.UTC().Equal(base.Add(time.Duration(inserted-1) * time.Hour)) {
		t.Fatalf("expected the newest row first, got %s", rows[0].OccurredAt.UTC())
	}
	for index := 1; index < len(rows); index++ {
		if rows[index].OccurredAt.UTC().After(rows[index-1].OccurredAt.UTC()) {
			t.Fatalf("rows are not newest-first at index %d: %s after %s", index, rows[index].OccurredAt, rows[index-1].OccurredAt)
		}
	}
}

// TestWebhookDeliveryRepositoryRingIsPerOwner proves the bound is the OWNER's,
// not the table's: an owner whose history is nearly full must not be able to
// evict another owner's rows by delivering more reminders. A global ring would
// let the busiest account on an instance erase everyone else's evidence.
func TestWebhookDeliveryRepositoryRingIsPerOwner(t *testing.T) {
	database := openSQLiteForMigrationBootstrapTest(t, filepath.Join(t.TempDir(), "deliveries-owners.db"))
	busyUserID := createDailyLogTestUser(t, database, "busy-owner@example.com")
	quietUserID := createDailyLogTestUser(t, database, "quiet-owner@example.com")
	repo := NewWebhookDeliveryRepository(database)
	ctx := context.Background()

	base := time.Date(2026, time.November, 1, 8, 0, 0, 0, time.UTC)
	quiet := models.WebhookDelivery{
		UserID:       quietUserID,
		OccurredAt:   base,
		ReminderKind: models.WebhookDeliveryKindOvulation,
		Outcome:      models.WebhookDeliveryOutcomeError,
		ErrorClass:   models.WebhookDeliveryErrorTimeout,
		EndpointHost: "quiet.example.com",
	}
	requireNoErr(t, repo.RecordWebhookDelivery(ctx, quiet), "record quiet owner row")

	for index := 0; index < WebhookDeliveryRingSize*2; index++ {
		row := models.WebhookDelivery{
			UserID:       busyUserID,
			OccurredAt:   base.Add(time.Duration(index+1) * time.Minute),
			ReminderKind: models.WebhookDeliveryKindPeriod,
			Outcome:      models.WebhookDeliveryOutcomeOK,
			HTTPStatus:   200,
			EndpointHost: "busy.example.com",
		}
		requireNoErr(t, repo.RecordWebhookDelivery(ctx, row), fmt.Sprintf("record busy row %d", index))
	}

	quietRows, err := repo.ListRecentWebhookDeliveries(ctx, quietUserID, 0)
	requireNoErr(t, err, "list quiet owner")
	if len(quietRows) != 1 {
		t.Fatalf("expected the quiet owner's single row to survive, got %d", len(quietRows))
	}

	busyRows, err := repo.ListRecentWebhookDeliveries(ctx, busyUserID, 0)
	requireNoErr(t, err, "list busy owner")
	if len(busyRows) != WebhookDeliveryRingSize {
		t.Fatalf("expected the busy owner's history bounded at %d, got %d", WebhookDeliveryRingSize, len(busyRows))
	}
}

// TestWebhookDeliveryRepositoryStoresOnlyTheHost proves the table cannot hold
// a secret even if a caller hands one over. The stored value is the host alone:
// a full URL — with a token in its query string, a token in its userinfo, or a
// token in its path — is refused outright rather than trimmed, because a
// trimmed URL is still a leak.
func TestWebhookDeliveryRepositoryStoresOnlyTheHost(t *testing.T) {
	database := openSQLiteForMigrationBootstrapTest(t, filepath.Join(t.TempDir(), "deliveries-host.db"))
	userID := createDailyLogTestUser(t, database, "delivery-host@example.com")
	repo := NewWebhookDeliveryRepository(database)
	ctx := context.Background()

	cases := []struct {
		name     string
		host     string
		wantHost string
	}{
		{name: "bare hostname", host: "ntfy.example.com", wantHost: "ntfy.example.com"},
		{name: "host with port literal", host: "127.0.0.1:8080", wantHost: "127.0.0.1:8080"},
		{name: "full url is refused", host: "https://ntfy.example.com/topic?token=secret", wantHost: ""},
		{name: "userinfo token is refused", host: "https://user:secret@ntfy.example.com/topic", wantHost: ""},
		{name: "path is refused", host: "ntfy.example.com/topic", wantHost: ""},
	}

	base := time.Date(2026, time.November, 1, 8, 0, 0, 0, time.UTC)
	for index, testCase := range cases {
		row := models.WebhookDelivery{
			UserID:       userID,
			OccurredAt:   base.Add(time.Duration(index) * time.Minute),
			ReminderKind: models.WebhookDeliveryKindPeriod,
			Outcome:      models.WebhookDeliveryOutcomeOK,
			HTTPStatus:   200,
			EndpointHost: testCase.host,
		}
		requireNoErr(t, repo.RecordWebhookDelivery(ctx, row), "record "+testCase.name)
	}

	rows, err := repo.ListRecentWebhookDeliveries(ctx, userID, 0)
	requireNoErr(t, err, "list recent")
	if len(rows) != len(cases) {
		t.Fatalf("expected %d rows, got %d", len(cases), len(rows))
	}
	// Rows come back newest-first, so reverse the expectation order.
	for index, testCase := range cases {
		got := rows[len(cases)-1-index].EndpointHost
		if got != testCase.wantHost {
			t.Errorf("%s: expected stored host %q, got %q", testCase.name, testCase.wantHost, got)
		}
		if testCase.wantHost == "" && got != "" {
			t.Errorf("%s: a URL-shaped value must not be stored, got %q", testCase.name, got)
		}
	}
}

// TestWebhookDeliveryRepositoryRejectsAnIncompleteRow pins the three required
// fields. A row without an owner, a moment, a kind or an outcome would render
// as a blank line on the owner's page, and the prune would have to guess which
// owner's ring it belongs to.
func TestWebhookDeliveryRepositoryRejectsAnIncompleteRow(t *testing.T) {
	database := openSQLiteForMigrationBootstrapTest(t, filepath.Join(t.TempDir(), "deliveries-validation.db"))
	repo := NewWebhookDeliveryRepository(database)
	ctx := context.Background()

	valid := models.WebhookDelivery{
		UserID:       1,
		OccurredAt:   time.Date(2026, time.November, 1, 8, 0, 0, 0, time.UTC),
		ReminderKind: models.WebhookDeliveryKindPeriod,
		Outcome:      models.WebhookDeliveryOutcomeOK,
	}

	cases := []struct {
		name   string
		mutate func(*models.WebhookDelivery)
	}{
		{name: "missing owner", mutate: func(row *models.WebhookDelivery) { row.UserID = 0 }},
		{name: "missing time", mutate: func(row *models.WebhookDelivery) { row.OccurredAt = time.Time{} }},
		{name: "missing kind", mutate: func(row *models.WebhookDelivery) { row.ReminderKind = "  " }},
		{name: "missing outcome", mutate: func(row *models.WebhookDelivery) { row.Outcome = "" }},
	}

	for _, testCase := range cases {
		row := valid
		testCase.mutate(&row)
		if err := repo.RecordWebhookDelivery(ctx, row); err == nil {
			t.Errorf("%s: expected RecordWebhookDelivery to reject the row", testCase.name)
		}
	}

	// The valid row still writes, so the rejections above are not a repository
	// that refuses everything.
	requireNoErr(t, repo.RecordWebhookDelivery(ctx, valid), "record valid row")
}

// TestWebhookDeliveryRepositoryListIsOwnerScoped proves the read cannot be
// asked for another owner's rows by id. The list is filtered by user_id, so a
// row written for owner A never appears in owner B's history — the property the
// owner-only API and the CLI both rest on.
func TestWebhookDeliveryRepositoryListIsOwnerScoped(t *testing.T) {
	database := openSQLiteForMigrationBootstrapTest(t, filepath.Join(t.TempDir(), "deliveries-scope.db"))
	ownerA := createDailyLogTestUser(t, database, "scope-a@example.com")
	ownerB := createDailyLogTestUser(t, database, "scope-b@example.com")
	repo := NewWebhookDeliveryRepository(database)
	ctx := context.Background()

	at := time.Date(2026, time.November, 1, 8, 0, 0, 0, time.UTC)
	requireNoErr(t, repo.RecordWebhookDelivery(ctx, models.WebhookDelivery{
		UserID: ownerA, OccurredAt: at, ReminderKind: models.WebhookDeliveryKindPeriod,
		Outcome: models.WebhookDeliveryOutcomeOK, EndpointHost: "a.example.com",
	}), "record owner A row")

	rowsB, err := repo.ListRecentWebhookDeliveries(ctx, ownerB, 0)
	requireNoErr(t, err, "list owner B")
	if len(rowsB) != 0 {
		t.Fatalf("expected owner B's history to be empty, got %d rows", len(rowsB))
	}

	rowsA, err := repo.ListRecentWebhookDeliveries(ctx, ownerA, 0)
	requireNoErr(t, err, "list owner A")
	if len(rowsA) != 1 {
		t.Fatalf("expected owner A's row, got %d rows", len(rowsA))
	}
}
