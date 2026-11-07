package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/ovumcy/ovumcy-web/internal/services"
)

// The settings-page test action is an OWNER-INITIATED probe of the stored
// destination. These tests exercise it through the real HTTP stack, the real
// encrypted settings save, and the real hardened deliverer — only the endpoint
// itself is a local httptest server. What they pin:
//
//   - one POST, carrying the dedicated TEST payload rather than a period or
//     ovulation reminder, to the URL that is actually stored;
//   - no settings or delivery watermark are changed by the probe;
//   - missing configuration and transport failure answer the card with
//     localized copy, and CSRF is enforced before any outbound request;
//   - the stored URL and its embedded token never reach the response body.

const (
	webhookTestSecretToken = "tk_do_not_leak"
	webhookTestSecretPath  = "owner-secret-topic"
)

type capturedWebhookRequest struct {
	method string
	path   string
	query  string
	body   []byte
}

// newWebhookCaptureServer returns a local endpoint that records the first
// request it receives. It is deliberately a real HTTP server: the test then
// proves the stored URL reaches the SAME transport the notify pass uses, not a
// handler-side shortcut.
func newWebhookCaptureServer(t *testing.T) (*httptest.Server, func() []capturedWebhookRequest) {
	t.Helper()

	var mu sync.Mutex
	var requests []capturedWebhookRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, capturedWebhookRequest{
			method: r.Method,
			path:   r.URL.Path,
			query:  r.URL.RawQuery,
			body:   body,
		})
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	return server, func() []capturedWebhookRequest {
		mu.Lock()
		defer mu.Unlock()
		out := make([]capturedWebhookRequest, len(requests))
		copy(out, requests)
		return out
	}
}

func saveWebhookTestEndpoint(t *testing.T, ctx settingsSecurityTestContext, endpoint string, enabled bool) {
	t.Helper()

	form := url.Values{"webhook_url": {endpoint}}
	if enabled {
		form.Set("webhook_enabled", "true")
	}
	response := settingsFormRequestWithCSRF(t, ctx, http.MethodPost, "/api/v1/users/current/webhook", form, nil)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("save webhook test endpoint: expected 303, got %d", response.StatusCode)
	}
}

func postWebhookTest(t *testing.T, ctx settingsSecurityTestContext, headers map[string]string) *http.Response {
	t.Helper()

	response := settingsFormRequestWithCSRF(t, ctx, http.MethodPost, "/api/v1/users/current/webhook/test", url.Values{}, headers)
	return response
}

func TestWebhookTestActionSendsOneTestNotificationToTheStoredEndpoint(t *testing.T) {
	server, requests := newWebhookCaptureServer(t)
	ctx := newSettingsSecurityTestContext(t, "webhook-test-success@example.com")
	endpoint := server.URL + "/" + webhookTestSecretPath + "?token=" + webhookTestSecretToken

	// Saved with reminder delivery OFF on purpose: a probe verifies the stored
	// destination before the owner arms it, so it must not depend on the toggle.
	saveWebhookTestEndpoint(t, ctx, endpoint, false)
	storedBefore := reloadUserForWebhook(t, ctx, ctx.user.ID)

	response := postWebhookTest(t, ctx, map[string]string{"HX-Request": "true"})
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected the rebuilt card on a successful test, got %d", response.StatusCode)
	}
	body := mustReadBodyString(t, response.Body)
	if !strings.Contains(body, "settings.success.webhook_tested") {
		t.Fatalf("expected the success status in the rebuilt card, body: %s", body)
	}
	for _, forbidden := range []string{endpoint, webhookTestSecretToken, webhookTestSecretPath} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("test response exposed the secret endpoint fragment %q", forbidden)
		}
	}

	captured := requests()
	if len(captured) != 1 {
		t.Fatalf("expected exactly one outbound test request, got %d", len(captured))
	}
	got := captured[0]
	if got.method != http.MethodPost {
		t.Fatalf("test notification method = %s, want POST", got.method)
	}
	if got.path != "/"+webhookTestSecretPath {
		t.Fatalf("test notification path = %q, want the stored path", got.path)
	}
	if got.query != "token="+webhookTestSecretToken {
		t.Fatalf("test notification query = %q, want the stored token query", got.query)
	}

	var payload services.WebhookPayload
	if err := json.Unmarshal(got.body, &payload); err != nil {
		t.Fatalf("decode test payload: %v (body %q)", err, string(got.body))
	}
	if payload.Type != services.WebhookTestType {
		t.Fatalf("test payload type = %q, want %q", payload.Type, services.WebhookTestType)
	}
	if payload.Type == services.DueReminderTypePeriod || payload.Type == services.DueReminderTypeOvulation {
		t.Fatalf("test payload claimed reminder type %q", payload.Type)
	}
	if payload.LeadDays != 0 {
		t.Fatalf("test payload lead days = %d, want 0", payload.LeadDays)
	}
	if payload.Title == "" || payload.Message == "" || payload.Disclaimer == "" {
		t.Fatalf("test payload is incomplete: %+v", payload)
	}

	storedAfter := reloadUserForWebhook(t, ctx, ctx.user.ID)
	if storedAfter.WebhookEnabled != storedBefore.WebhookEnabled ||
		storedAfter.WebhookURL != storedBefore.WebhookURL ||
		storedAfter.WebhookNotifyPeriod != storedBefore.WebhookNotifyPeriod ||
		storedAfter.WebhookNotifyOvulation != storedBefore.WebhookNotifyOvulation {
		t.Fatalf("a test notification changed webhook settings: before=%+v after=%+v", storedBefore, storedAfter)
	}
	if storedAfter.WebhookPeriodLastSentCycleStart != nil || storedAfter.WebhookOvulationLastSentCycleStart != nil {
		t.Fatal("a test notification advanced a reminder watermark")
	}
}

func TestWebhookTestActionWithoutAnEndpointAnswersWithLocalizedCopy(t *testing.T) {
	ctx := newSettingsSecurityTestContext(t, "webhook-test-missing@example.com")

	response := postWebhookTest(t, ctx, map[string]string{"HX-Request": "true"})
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected the rebuilt card on a missing endpoint, got %d", response.StatusCode)
	}
	body := mustReadBodyString(t, response.Body)
	if !strings.Contains(body, "settings.error.webhook_test_not_configured") {
		t.Fatalf("expected the not-configured copy in the card, body: %s", body)
	}
	if strings.Contains(body, "settings.success.webhook_tested") {
		t.Fatal("a missing endpoint was reported as a successful test")
	}
}

func TestWebhookTestActionTransportFailureIsSecretFree(t *testing.T) {
	server, requests := newWebhookCaptureServer(t)
	ctx := newSettingsSecurityTestContext(t, "webhook-test-transport@example.com")
	endpoint := server.URL + "/" + webhookTestSecretPath + "?token=" + webhookTestSecretToken
	saveWebhookTestEndpoint(t, ctx, endpoint, true)

	// Close the endpoint AFTER the save so the stored URL is a genuinely
	// unreachable destination, and the failure comes from the real transport.
	server.Close()

	response := postWebhookTest(t, ctx, map[string]string{"HX-Request": "true"})
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected the rebuilt card on a transport failure, got %d", response.StatusCode)
	}
	body := mustReadBodyString(t, response.Body)
	if !strings.Contains(body, "settings.error.webhook_test_failed") {
		t.Fatalf("expected the delivery-failed copy in the card, body: %s", body)
	}
	for _, forbidden := range []string{endpoint, webhookTestSecretToken, webhookTestSecretPath} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("transport-failure response exposed the secret endpoint fragment %q", forbidden)
		}
	}
	if got := len(requests()); got != 0 {
		// The server was closed, so nothing could be recorded; this guards a
		// future test helper change from silently moving the closure earlier.
		t.Fatalf("closed capture server recorded %d request(s)", got)
	}
}

func TestWebhookTestActionRefusesWithoutCSRFBeforeSending(t *testing.T) {
	server, requests := newWebhookCaptureServer(t)
	ctx := newSettingsSecurityTestContext(t, "webhook-test-csrf@example.com")
	endpoint := server.URL + "/" + webhookTestSecretPath + "?token=" + webhookTestSecretToken
	saveWebhookTestEndpoint(t, ctx, endpoint, true)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/users/current/webhook/test", nil)
	request.Header.Set("Cookie", ctx.authCookie)
	response, err := ctx.app.Test(request, testConfigNoTimeout)
	if err != nil {
		t.Fatalf("CSRF-less test request failed: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 without a CSRF token, got %d", response.StatusCode)
	}
	if got := len(requests()); got != 0 {
		t.Fatalf("a CSRF-refused test still made %d outbound request(s)", got)
	}
}
