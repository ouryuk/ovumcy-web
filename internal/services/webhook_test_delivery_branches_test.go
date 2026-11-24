package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/models"
	"github.com/ovumcy/ovumcy-web/internal/testenv"
)

// plaintextDecryptor stands in for the SECRET_KEY decryptor with a fixed
// answer, so the branches AFTER a successful decrypt can be driven: the
// ciphertext-to-empty case (an endpoint that decrypts to nothing) and the
// owner-timezone case (any plaintext works).
type plaintextDecryptor struct {
	plaintext string
}

func (stub plaintextDecryptor) DecryptWebhookURL(uint, string) (string, error) {
	return stub.plaintext, nil
}

// TestSendTestEndpointThatDecryptsToNothingIsNotConfigured covers the second
// "not configured" arm of SendTest: a ciphertext IS stored and DOES decrypt,
// but the plaintext is empty or whitespace. Nothing must be sent, and the
// owner gets the same stable ErrWebhookTestNotConfigured as with no stored
// endpoint — never ErrWebhookTestDeliveryFailed, because no request was made.
func TestSendTestEndpointThatDecryptsToNothingIsNotConfigured(t *testing.T) {
	for _, plaintext := range []string{"", "   ", "\t\n"} {
		deliverer := &stubDeliverer{}
		service := NewWebhookNotifyService(nil, nil, plaintextDecryptor{plaintext: plaintext}, deliverer, newLocaleCopyProvider(t))

		err := service.SendTest(context.Background(), models.WebhookNotifyRecord{ID: 11, WebhookURL: "ciphertext"}, time.Now(), time.UTC)
		if !errors.Is(err, ErrWebhookTestNotConfigured) {
			t.Fatalf("plaintext %q: SendTest = %v, want ErrWebhookTestNotConfigured", plaintext, err)
		}
		if got := len(deliverer.deliveries()); got != 0 {
			t.Fatalf("plaintext %q produced %d delivery attempt(s), want none", plaintext, got)
		}
	}
}

// TestSendTestDatesThePayloadOnTheOwnersPersistedTimeZone pins that the test
// delivery resolves "today" on the owner's persisted timezone (the shared
// resolveOwnerLocation seam the request-free passes use), not on the caller's
// fallback location: at 01:30 UTC on 2026-03-12 it is still 2026-03-11 in
// America/New_York, and the owner's test message must carry THEIR day.
func TestSendTestDatesThePayloadOnTheOwnersPersistedTimeZone(t *testing.T) {
	ownerZone := testenv.RequireTimeZone(t, "America/New_York")
	now := time.Date(2026, time.March, 12, 1, 30, 0, 0, time.UTC)
	if got := now.In(ownerZone).Format("2006-01-02"); got != "2026-03-11" {
		t.Fatalf("fixture: 01:30 UTC should still be 2026-03-11 in New York, got %s", got)
	}

	deliverer := &stubDeliverer{}
	service := NewWebhookNotifyService(nil, nil, plaintextDecryptor{plaintext: "https://ntfy.example.test/topic"}, deliverer, newLocaleCopyProvider(t))
	record := models.WebhookNotifyRecord{ID: 12, WebhookURL: "ciphertext", InterfaceLanguage: "en", Timezone: "America/New_York"}

	if err := service.SendTest(context.Background(), record, now, time.UTC); err != nil {
		t.Fatalf("SendTest: %v", err)
	}
	deliveries := deliverer.deliveries()
	if len(deliveries) != 1 {
		t.Fatalf("expected one delivery, got %d", len(deliveries))
	}
	if got := deliveries[0].payload.EventDate; got != "2026-03-11" {
		t.Fatalf("event date = %q, want the owner-local day 2026-03-11 (fallback UTC would say 2026-03-12)", got)
	}
	if !strings.Contains(deliveries[0].payload.Message, "2026-03-11") {
		t.Fatalf("message = %q, want the owner-local date interpolated", deliveries[0].payload.Message)
	}
}

// TestSendTestFallsBackToTheCallerLocationForAnUnusableTimeZone covers the
// other side of the seam: an empty timezone and the rejected "Local" token both
// fall back to the injected location rather than failing or pinning the owner
// to the server's zone.
func TestSendTestFallsBackToTheCallerLocationForAnUnusableTimeZone(t *testing.T) {
	now := time.Date(2026, time.March, 12, 1, 30, 0, 0, time.UTC)
	fallback := testenv.RequireTimeZone(t, "Asia/Tokyo")

	for _, timezone := range []string{"", "   ", "Local", "Not/AZone"} {
		deliverer := &stubDeliverer{}
		service := NewWebhookNotifyService(nil, nil, plaintextDecryptor{plaintext: "https://ntfy.example.test/topic"}, deliverer, newLocaleCopyProvider(t))
		record := models.WebhookNotifyRecord{ID: 13, WebhookURL: "ciphertext", InterfaceLanguage: "en", Timezone: timezone}

		if err := service.SendTest(context.Background(), record, now, fallback); err != nil {
			t.Fatalf("timezone %q: SendTest: %v", timezone, err)
		}
		deliveries := deliverer.deliveries()
		if len(deliveries) != 1 {
			t.Fatalf("timezone %q: expected one delivery, got %d", timezone, len(deliveries))
		}
		// 01:30 UTC is 10:30 in Tokyo: the same calendar day, but resolved
		// THROUGH the fallback zone rather than rejected.
		if got := deliveries[0].payload.EventDate; got != "2026-03-12" {
			t.Fatalf("timezone %q: event date = %q, want 2026-03-12 via the fallback location", timezone, got)
		}
	}
}

// TestBuildTestPayloadWithoutAMessageSendsTheHeadlineAlone pins the test
// payload's missing-template arm: a provider without the test message entry
// must yield an EMPTY message, not a formatted residue like "%!(string=...)".
// The reminder copy has the same guard (see TestReminderCopyWithoutASentence);
// this is the test-delivery twin.
func TestBuildTestPayloadWithoutAMessageSendsTheHeadlineAlone(t *testing.T) {
	service := NewWebhookNotifyService(nil, nil, nil, nil, titleOnlyCopyProvider{})
	eventDate := time.Date(2026, time.March, 12, 0, 0, 0, 0, time.UTC)

	payload := service.buildTestPayload("en", eventDate)
	if payload.Title != "Reminder" {
		t.Fatalf("title = %q, want the headline the provider does carry", payload.Title)
	}
	if payload.Message != "" {
		t.Fatalf("message = %q, want it empty rather than formatted from a missing template", payload.Message)
	}
	if strings.Contains(payload.Message, "%!") {
		t.Fatalf("message carries formatting-verb residue: %q", payload.Message)
	}
	if payload.Type != WebhookTestType || payload.EventDate != "2026-03-12" || payload.LeadDays != 0 {
		t.Fatalf("unexpected payload shape: %#v", payload)
	}
}
