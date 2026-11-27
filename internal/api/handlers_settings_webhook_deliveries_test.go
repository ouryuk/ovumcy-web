package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/db"
	"github.com/ovumcy/ovumcy-web/internal/models"
	"gorm.io/gorm"
)

// deliveryHistoryRequest builds the owner-history GET for one owner's cookie.
func deliveryHistoryRequest(cookie string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, webhookDeliveriesPath, nil)
	request.AddCookie(&http.Cookie{Name: "ovumcy_auth", Value: cookie})
	return request
}

// webhookDeliveriesPath is the route under test. It is spelled once so the
// request builder and the assertions cannot drift from the route it names.
const webhookDeliveriesPath = "/api/v1/users/current/webhook/deliveries"

// seedOwnerDeliveries writes delivery rows for one owner straight through the
// repository, so the read path can be asserted without a notify pass.
func seedOwnerDeliveries(t *testing.T, database *gorm.DB, userID uint, rows []models.WebhookDelivery) {
	t.Helper()
	repo := db.NewWebhookDeliveryRepository(database)
	for index := range rows {
		row := rows[index]
		row.UserID = userID
		if err := repo.RecordWebhookDelivery(t.Context(), row); err != nil {
			t.Fatalf("record delivery row %d: %v", index, err)
		}
	}
}

// TestWebhookDeliveriesEndpointIsOwnerOnly proves the delivery history is
// readable by its own owner and NOT by another account: the GET reads the
// session's user id, so a second owner's cookie reaches the route and receives
// their own (empty) history rather than the first owner's rows. There is no
// request shape that names an owner — the endpoint accepts no id — so the
// isolation is the session's, and this test pins the observable half of it.
func TestWebhookDeliveriesEndpointIsOwnerOnly(t *testing.T) {
	t.Parallel()

	app, database := newOnboardingTestApp(t)
	owner := createOnboardingTestUser(t, database, "deliveries-owner@example.com", "StrongPass1", true)
	other := createOnboardingTestUser(t, database, "deliveries-other@example.com", "StrongPass1", true)

	base := time.Date(2026, time.November, 27, 9, 0, 0, 0, time.UTC)
	seedOwnerDeliveries(t, database, owner.ID, []models.WebhookDelivery{
		{OccurredAt: base, ReminderKind: models.WebhookDeliveryKindPeriod, Outcome: models.WebhookDeliveryOutcomeOK, HTTPStatus: 200, LatencyMS: 84, EndpointHost: "ntfy.example.com"},
		{OccurredAt: base.Add(time.Hour), ReminderKind: models.WebhookDeliveryKindOvulation, Outcome: models.WebhookDeliveryOutcomeError, ErrorClass: models.WebhookDeliveryErrorTimeout, LatencyMS: 5000, EndpointHost: "ntfy.example.com"},
	})

	// The owner sees their own two rows, newest first.
	ownerResponse := mustAppResponse(t, app, deliveryHistoryRequest(issueAuthCookieForUser(t, owner)))
	if ownerResponse.StatusCode != http.StatusOK {
		t.Fatalf("owner GET status = %d, want 200", ownerResponse.StatusCode)
	}
	var ownerBody WebhookDeliveriesResponse
	if err := json.NewDecoder(ownerResponse.Body).Decode(&ownerBody); err != nil {
		t.Fatalf("decode owner response: %v", err)
	}
	if len(ownerBody.Deliveries) != 2 {
		t.Fatalf("owner deliveries = %d, want 2", len(ownerBody.Deliveries))
	}
	if ownerBody.Deliveries[0].Outcome != models.WebhookDeliveryOutcomeError {
		t.Errorf("expected the newest (failed) attempt first, got outcome %q", ownerBody.Deliveries[0].Outcome)
	}
	if ownerBody.Limit != 20 {
		t.Errorf("published limit = %d, want the ring size 20", ownerBody.Limit)
	}

	// The other owner reaches the same route and sees NONE of the first owner's
	// rows: the read is scoped to the session, not to a request argument.
	otherResponse := mustAppResponse(t, app, deliveryHistoryRequest(issueAuthCookieForUser(t, other)))
	if otherResponse.StatusCode != http.StatusOK {
		t.Fatalf("other owner GET status = %d, want 200", otherResponse.StatusCode)
	}
	var otherBody WebhookDeliveriesResponse
	if err := json.NewDecoder(otherResponse.Body).Decode(&otherBody); err != nil {
		t.Fatalf("decode other response: %v", err)
	}
	if len(otherBody.Deliveries) != 0 {
		t.Fatalf("other owner saw %d row(s); the history must be owner-scoped", len(otherBody.Deliveries))
	}
}

// TestWebhookDeliveriesEndpointRejectsAnonymous proves an unauthenticated
// request is refused at the gate rather than being handed an empty history,
// which would read as "this owner has no deliveries" to a caller who is not the
// owner at all.
func TestWebhookDeliveriesEndpointRejectsAnonymous(t *testing.T) {
	t.Parallel()

	app, _ := newOnboardingTestApp(t)
	request := httptest.NewRequest(http.MethodGet, webhookDeliveriesPath, nil)
	response := mustAppResponse(t, app, request)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous GET status = %d, want 401", response.StatusCode)
	}
}

// TestWebhookDeliveriesEndpointPublishesHostOnly proves the response NEVER
// carries the endpoint URL, its path, its query, or a token: the only
// destination detail is the host, exactly as the storage row keeps it.
func TestWebhookDeliveriesEndpointPublishesHostOnly(t *testing.T) {
	t.Parallel()

	app, database := newOnboardingTestApp(t)
	owner := createOnboardingTestUser(t, database, "deliveries-host@example.com", "StrongPass1", true)

	base := time.Date(2026, time.November, 27, 9, 0, 0, 0, time.UTC)
	// A caller that stored a bare host gets a bare host back. The repository
	// refuses a URL-shaped value outright, so the row can never hold one.
	seedOwnerDeliveries(t, database, owner.ID, []models.WebhookDelivery{
		{OccurredAt: base, ReminderKind: models.WebhookDeliveryKindTest, Outcome: models.WebhookDeliveryOutcomeOK, HTTPStatus: 200, EndpointHost: "ntfy.example.com"},
	})

	response := mustAppResponse(t, app, deliveryHistoryRequest(issueAuthCookieForUser(t, owner)))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", response.StatusCode)
	}
	var body WebhookDeliveriesResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Deliveries) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(body.Deliveries))
	}
	if got := body.Deliveries[0].EndpointHost; got != "ntfy.example.com" {
		t.Fatalf("endpoint_host = %q, want the host alone", got)
	}
	if body.Deliveries[0].ReminderKind != models.WebhookDeliveryKindTest {
		t.Errorf("reminder_kind = %q, want test", body.Deliveries[0].ReminderKind)
	}
}
