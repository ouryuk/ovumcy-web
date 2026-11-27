package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/models"
)

// stubDeliveryRecorder is an in-memory WebhookDeliveryRecorder. It records what
// the history asked it to store and can be told to fail the write, so a test can
// prove that a failed write leaves the delivery's outcome untouched.
type stubDeliveryRecorder struct {
	rows     []models.WebhookDelivery
	listErr  error
	writeErr error
}

func (stub *stubDeliveryRecorder) RecordWebhookDelivery(_ context.Context, delivery models.WebhookDelivery) error {
	if stub.writeErr != nil {
		return stub.writeErr
	}
	stub.rows = append(stub.rows, delivery)
	return nil
}

func (stub *stubDeliveryRecorder) ListRecentWebhookDeliveries(_ context.Context, userID uint, limit int) ([]models.WebhookDelivery, error) {
	if stub.listErr != nil {
		return nil, stub.listErr
	}
	kept := make([]models.WebhookDelivery, 0, len(stub.rows))
	for index := len(stub.rows) - 1; index >= 0; index-- {
		if stub.rows[index].UserID != userID {
			continue
		}
		kept = append(kept, stub.rows[index])
		if limit > 0 && len(kept) >= limit {
			break
		}
	}
	return kept, nil
}

// outcomeDeliverer is a deliverer that implements the OPTIONAL outcome
// extension, reporting exactly the facts a test wants to see recorded.
type outcomeDeliverer struct {
	outcome WebhookDeliveryOutcome
	err     error
	calls   int
}

func (deliverer *outcomeDeliverer) Deliver(_ context.Context, _ string, _ WebhookPayload) error {
	deliverer.calls++
	return deliverer.err
}

func (deliverer *outcomeDeliverer) DeliverWithOutcome(_ context.Context, _ string, _ WebhookPayload) (WebhookDeliveryOutcome, error) {
	deliverer.calls++
	return deliverer.outcome, deliverer.err
}

// TestWebhookDeliveryHistoryRecordsBothOutcomes proves the recorder stores a
// row for a delivered attempt AND for a failed one, carrying the status code,
// the bounded failure class, and the host — and never the URL.
func TestWebhookDeliveryHistoryRecordsBothOutcomes(t *testing.T) {
	recorder := &stubDeliveryRecorder{}
	history := NewWebhookDeliveryHistory(recorder)
	ctx := context.Background()
	at := time.Date(2026, time.November, 27, 9, 0, 0, 0, time.UTC)

	history.RecordDeliveryOutcome(ctx, 7, models.WebhookDeliveryKindPeriod,
		WebhookDeliveryOutcome{HTTPStatus: 200, LatencyMS: 84, Host: "ntfy.example.com"}, true, at)
	history.RecordDeliveryOutcome(ctx, 7, models.WebhookDeliveryKindOvulation,
		WebhookDeliveryOutcome{HTTPStatus: 0, ErrorClass: models.WebhookDeliveryErrorTimeout, LatencyMS: 5000, Host: "ntfy.example.com"}, false, at.Add(time.Hour))
	history.RecordDeliveryOutcome(ctx, 7, models.WebhookDeliveryKindTest,
		WebhookDeliveryOutcome{HTTPStatus: 503, ErrorClass: models.WebhookDeliveryErrorHTTP, LatencyMS: 12, Host: "ntfy.example.com"}, false, at.Add(2*time.Hour))

	if len(recorder.rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(recorder.rows))
	}

	delivered := recorder.rows[0]
	if delivered.Outcome != models.WebhookDeliveryOutcomeOK {
		t.Errorf("expected the delivered attempt recorded as ok, got %q", delivered.Outcome)
	}
	if delivered.HTTPStatus != 200 || delivered.ErrorClass != "" {
		t.Errorf("expected status 200 and no class, got %d/%q", delivered.HTTPStatus, delivered.ErrorClass)
	}

	failed := recorder.rows[1]
	if failed.Outcome != models.WebhookDeliveryOutcomeError {
		t.Errorf("expected a transport failure recorded as error, got %q", failed.Outcome)
	}
	if failed.HTTPStatus != 0 || failed.ErrorClass != models.WebhookDeliveryErrorTimeout {
		t.Errorf("expected status 0 and timeout class, got %d/%q", failed.HTTPStatus, failed.ErrorClass)
	}

	refused := recorder.rows[2]
	if refused.Outcome != models.WebhookDeliveryOutcomeRefused {
		t.Errorf("expected a non-2xx answer recorded as refused, got %q", refused.Outcome)
	}
	if refused.HTTPStatus != 503 {
		t.Errorf("expected the endpoint's status recorded, got %d", refused.HTTPStatus)
	}
}

// TestWebhookDeliveryHistoryNeverStoresASecret proves the history reduces a
// destination to its host even when it is handed a full URL. The row must not
// carry a scheme, a path, a query string, or a token.
func TestWebhookDeliveryHistoryNeverStoresASecret(t *testing.T) {
	recorder := &stubDeliveryRecorder{}
	history := NewWebhookDeliveryHistory(recorder)
	ctx := context.Background()
	at := time.Date(2026, time.November, 27, 9, 0, 0, 0, time.UTC)

	history.RecordDeliveryOutcome(ctx, 7, models.WebhookDeliveryKindPeriod,
		WebhookDeliveryOutcome{HTTPStatus: 200, Host: "https://user:secret@ntfy.example.com/topic?token=abc123"}, true, at)

	if len(recorder.rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(recorder.rows))
	}
	if got := recorder.rows[0].EndpointHost; got != "ntfy.example.com" {
		t.Fatalf("expected the host alone to be stored, got %q", got)
	}
}

// TestWebhookDeliveryHistoryWriteFailureIsBestEffort proves a failed write never
// surfaces to the caller: the delivery's outcome is the transport's business,
// not the history's.
func TestWebhookDeliveryHistoryWriteFailureIsBestEffort(t *testing.T) {
	recorder := &stubDeliveryRecorder{writeErr: errors.New("disk full")}
	history := NewWebhookDeliveryHistory(recorder)
	ctx := context.Background()

	// RecordDeliveryOutcome returns nothing; the bar is that it does not panic
	// and that a subsequent read reports the failure honestly.
	history.RecordDeliveryOutcome(ctx, 7, models.WebhookDeliveryKindPeriod,
		WebhookDeliveryOutcome{HTTPStatus: 200, Host: "ntfy.example.com"}, true, time.Now())

	if len(recorder.rows) != 0 {
		t.Fatalf("expected the failed write to store nothing, got %d rows", len(recorder.rows))
	}
}

// TestWebhookDeliveryHistorySkipsAnUnwiredRecorder proves a nil recorder is a
// no-op rather than a panic: a partially wired instance must still deliver.
func TestWebhookDeliveryHistorySkipsAnUnwiredRecorder(t *testing.T) {
	history := NewWebhookDeliveryHistory(nil)
	history.RecordDeliveryOutcome(context.Background(), 7, models.WebhookDeliveryKindPeriod,
		WebhookDeliveryOutcome{HTTPStatus: 200}, true, time.Now())

	if _, err := history.List(context.Background(), 7, 0); err == nil {
		t.Fatal("expected an unwired history to report the read as unavailable")
	}
}

// TestWebhookNotifyServiceRecordsEachDelivery proves the notify service wires a
// delivery attempt through to the history: a success and a failure both leave a
// row, and each names the reminder kind that was sent.
func TestWebhookNotifyServiceRecordsEachDelivery(t *testing.T) {
	recorder := &stubDeliveryRecorder{}
	now := time.Date(2026, time.November, 27, 9, 0, 0, 0, time.UTC)

	deliverer := &outcomeDeliverer{outcome: WebhookDeliveryOutcome{HTTPStatus: 200, Host: "ntfy.example.com"}}
	service := NewWebhookNotifyService(
		nil,
		nil,
		stubDecryptor{},
		deliverer,
		newLocaleCopyProvider(t),
	).WithDeliveryHistory(NewWebhookDeliveryHistory(recorder))

	// A test-send is recorded under the test kind.
	if err := service.SendTest(context.Background(), models.WebhookNotifyRecord{
		ID:                7,
		WebhookURL:        "https://ntfy.example.com/topic",
		InterfaceLanguage: "en",
		Timezone:          "UTC",
	}, now, time.UTC); err != nil {
		t.Fatalf("SendTest: %v", err)
	}
	if len(recorder.rows) != 1 {
		t.Fatalf("expected the test send recorded, got %d rows", len(recorder.rows))
	}
	if recorder.rows[0].ReminderKind != models.WebhookDeliveryKindTest {
		t.Errorf("expected a test-kind row, got %q", recorder.rows[0].ReminderKind)
	}
	if recorder.rows[0].Outcome != models.WebhookDeliveryOutcomeOK {
		t.Errorf("expected the test send recorded as ok, got %q", recorder.rows[0].Outcome)
	}
	if deliverer.calls != 1 {
		t.Errorf("expected exactly one delivery attempt, got %d", deliverer.calls)
	}
}

// TestWebhookNotifyServiceRecordsAFailedTestSend proves a failed attempt is
// recorded too, with the bounded failure class, and that the failure still
// surfaces to the caller exactly as it did before the history existed.
func TestWebhookNotifyServiceRecordsAFailedTestSend(t *testing.T) {
	recorder := &stubDeliveryRecorder{}
	now := time.Date(2026, time.November, 27, 9, 0, 0, 0, time.UTC)

	deliverer := &outcomeDeliverer{
		outcome: WebhookDeliveryOutcome{HTTPStatus: 0, ErrorClass: models.WebhookDeliveryErrorTimeout, Host: "ntfy.example.com"},
		err:     errors.New("stub timeout"),
	}
	service := NewWebhookNotifyService(
		nil,
		nil,
		stubDecryptor{},
		deliverer,
		newLocaleCopyProvider(t),
	).WithDeliveryHistory(NewWebhookDeliveryHistory(recorder))

	err := service.SendTest(context.Background(), models.WebhookNotifyRecord{
		ID:                7,
		WebhookURL:        "https://ntfy.example.com/topic",
		InterfaceLanguage: "en",
		Timezone:          "UTC",
	}, now, time.UTC)
	if !errors.Is(err, ErrWebhookTestDeliveryFailed) {
		t.Fatalf("expected the delivery-failed sentinel, got %v", err)
	}

	if len(recorder.rows) != 1 {
		t.Fatalf("expected the failed attempt recorded, got %d rows", len(recorder.rows))
	}
	if recorder.rows[0].Outcome != models.WebhookDeliveryOutcomeError {
		t.Errorf("expected an error outcome, got %q", recorder.rows[0].Outcome)
	}
	if recorder.rows[0].ErrorClass != models.WebhookDeliveryErrorTimeout {
		t.Errorf("expected the timeout class, got %q", recorder.rows[0].ErrorClass)
	}
}
