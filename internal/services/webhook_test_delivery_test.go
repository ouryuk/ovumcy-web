package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/models"
)

// The owner-initiated test action has three outcomes the settings page must be
// able to tell apart. These tests pin them at the service seam, where the
// transport can be stubbed and the secret URL is observable if it leaks:
//
//   - success: exactly one POST carrying a TEST payload, not a period or
//     ovulation reminder;
//   - missing configuration: no stored endpoint (or an empty decrypted value)
//     sends nothing and returns the not-configured sentinel;
//   - transport failure: the deliverer's error collapses to the stable
//     delivery-failed sentinel and the URL/token never appear in the error.

func TestSendTestDeliversOneNonReminderPayload(t *testing.T) {
	const secretURL = "https://ntfy.example.test/owner-secret-topic?token=tk_do_not_leak"
	deliverer := &stubDeliverer{}
	provider := newLocaleCopyProvider(t)
	service := NewWebhookNotifyService(nil, nil, stubDecryptor{}, deliverer, provider)

	record := models.WebhookNotifyRecord{
		ID:                7,
		WebhookURL:        secretURL,
		InterfaceLanguage: "en",
	}
	eventDate := time.Date(2026, time.March, 12, 9, 0, 0, 0, time.UTC)

	if err := service.SendTest(context.Background(), record, eventDate, time.UTC); err != nil {
		t.Fatalf("SendTest: %v", err)
	}

	deliveries := deliverer.deliveries()
	if len(deliveries) != 1 {
		t.Fatalf("expected exactly one test delivery, got %d", len(deliveries))
	}
	delivery := deliveries[0]
	if delivery.url != secretURL {
		t.Fatalf("test delivery used %q, want the stored endpoint", delivery.url)
	}

	payload := delivery.payload
	if payload.Type != WebhookTestType || payload.Type == DueReminderTypePeriod || payload.Type == DueReminderTypeOvulation {
		t.Fatalf("test payload type = %q, want the dedicated %q kind and never a reminder kind", payload.Type, WebhookTestType)
	}
	if payload.LeadDays != 0 {
		t.Fatalf("test payload lead days = %d, want 0 (a test is not a lead-window reminder)", payload.LeadDays)
	}
	if payload.EventDate != "2026-03-12" {
		t.Fatalf("test payload event date = %q, want the owner-local day", payload.EventDate)
	}

	english := provider.messages(t, "en")
	if payload.Title != english["webhook.test.title"] {
		t.Fatalf("test payload title = %q, want the catalogue entry %q", payload.Title, english["webhook.test.title"])
	}
	wantMessage := strings.Replace(english["webhook.test.message"], "%s", "2026-03-12", 1)
	if payload.Message != wantMessage {
		t.Fatalf("test payload message = %q, want %q", payload.Message, wantMessage)
	}
	if payload.Disclaimer != english["medical.disclaimer"] {
		t.Fatalf("test payload disclaimer = %q, want the catalogue entry %q", payload.Disclaimer, english["medical.disclaimer"])
	}
}

func TestSendTestWithoutStoredEndpointSendsNothing(t *testing.T) {
	for _, stored := range []string{"", "   "} {
		deliverer := &stubDeliverer{}
		service := NewWebhookNotifyService(nil, nil, stubDecryptor{}, deliverer, newLocaleCopyProvider(t))

		err := service.SendTest(context.Background(), models.WebhookNotifyRecord{ID: 8, WebhookURL: stored}, time.Now(), time.UTC)
		if !errors.Is(err, ErrWebhookTestNotConfigured) {
			t.Fatalf("SendTest with stored URL %q = %v, want ErrWebhookTestNotConfigured", stored, err)
		}
		if got := len(deliverer.deliveries()); got != 0 {
			t.Fatalf("stored URL %q produced %d delivery attempt(s), want none", stored, got)
		}
	}
}

func TestSendTestUnreadableEndpointSendsNothingAndHidesTheFailure(t *testing.T) {
	const secretURL = "https://ntfy.example.test/owner-secret-topic?token=tk_do_not_leak"
	deliverer := &stubDeliverer{}
	service := NewWebhookNotifyService(nil, nil, stubDecryptor{failFor: map[uint]bool{9: true}}, deliverer, newLocaleCopyProvider(t))

	err := service.SendTest(context.Background(), models.WebhookNotifyRecord{ID: 9, WebhookURL: secretURL}, time.Now(), time.UTC)
	if !errors.Is(err, ErrWebhookTestEndpointUnreadable) {
		t.Fatalf("SendTest over an unreadable ciphertext = %v, want ErrWebhookTestEndpointUnreadable", err)
	}
	if got := len(deliverer.deliveries()); got != 0 {
		t.Fatalf("an unreadable endpoint produced %d delivery attempt(s), want none", got)
	}
	assertWebhookTestErrorHidesSecret(t, err, secretURL)
}

func TestSendTestTransportFailureIsStableAndSecretFree(t *testing.T) {
	const secretURL = "https://ntfy.example.test/owner-secret-topic?token=tk_do_not_leak"
	deliverer := &stubDeliverer{failEvery: true}
	service := NewWebhookNotifyService(nil, nil, stubDecryptor{}, deliverer, newLocaleCopyProvider(t))

	err := service.SendTest(context.Background(), models.WebhookNotifyRecord{ID: 10, WebhookURL: secretURL}, time.Now(), time.UTC)
	if !errors.Is(err, ErrWebhookTestDeliveryFailed) {
		t.Fatalf("SendTest against a failing transport = %v, want ErrWebhookTestDeliveryFailed", err)
	}
	if got := len(deliverer.deliveries()); got != 1 {
		t.Fatalf("a failed test should still make exactly one attempt, got %d", got)
	}
	assertWebhookTestErrorHidesSecret(t, err, secretURL)
}

func assertWebhookTestErrorHidesSecret(t *testing.T, err error, secretURL string) {
	t.Helper()
	text := err.Error()
	for _, secret := range []string{secretURL, "owner-secret-topic", "tk_do_not_leak"} {
		if strings.Contains(text, secret) {
			t.Fatalf("test-delivery error leaked %q: %q", secret, text)
		}
	}
}
