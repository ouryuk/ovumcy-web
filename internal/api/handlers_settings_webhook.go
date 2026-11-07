package api

import (
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/ovumcy/ovumcy-web/internal/models"
	"github.com/ovumcy/ovumcy-web/internal/services"
)

var (
	webhookSettingsMutation = healthMutationKind{action: "settings.webhook_update", target: "webhook_settings"}
	webhookTestMutation     = healthMutationKind{action: "settings.webhook_test", target: "webhook"}
)

// UpdateWebhookSettings persists an owner's webhook notification settings
// (issue #124): the enable toggle, the two per-kind notify toggles, and the
// WRITE-ONLY endpoint URL. The URL is a secret (it can embed an ntfy/Gotify
// token), so this handler treats it like a password field:
//
//   - it never renders the stored URL back (the settings page shows only
//     status + host);
//   - a blank URL means "leave the stored endpoint unchanged"; a distinct
//     remove affordance clears it;
//   - all validation, the keep/replace/remove decision, and encryption live in
//     WebhookSettingsService — the transport layer only parses and forwards;
//   - nothing about the URL is logged (the mutation audit records action +
//     outcome only, never the value).
//
// It is OwnerOnly + CSRF-protected at the route/middleware layer and scoped to
// the authenticated session's user id. It does not bump auth_session_version:
// a notification-preference change is not a security-posture change.
func (handler *Handler) UpdateWebhookSettings(c fiber.Ctx) error {
	user, ok := currentUser(c)
	if !ok {
		return handler.failMutation(c, webhookSettingsMutation, unauthorizedErrorSpec())
	}

	input, err := parseWebhookSettingsInput(c)
	if err != nil {
		return handler.failEgressMutation(c, webhookSettingsMutation, user, settingsInvalidInputErrorSpec())
	}

	form := services.WebhookSettingsFormUpdate{
		Enabled:         input.Enabled,
		NotifyPeriod:    input.NotifyPeriod,
		NotifyOvulation: input.NotifyOvulation,
		URL:             input.URL,
		URLProvided:     input.URL != "",
		RemoveURL:       input.RemoveURL,
	}
	if err := handler.webhookSettingsSvc.SaveWebhookSettingsFromForm(c.Context(), user.ID, form); err != nil {
		return handler.failEgressMutation(c, webhookSettingsMutation, user, mapSettingsWebhookSaveError(err))
	}

	status := services.SettingsWebhookUpdatedStatus
	handler.logMutationSuccess(c, webhookSettingsMutation)

	if acceptsJSON(c) {
		return c.JSON(fiber.Map{
			"ok":               true,
			"status":           status,
			"webhook_enabled":  form.Enabled,
			"notify_period":    form.NotifyPeriod,
			"notify_ovulation": form.NotifyOvulation,
		})
	}
	// The save is the fifth mutation on the egress card and answers like the
	// other four. It used to return a bare status toast, which was right while
	// the form targeted its own island; the card now replaces itself, so a toast
	// alone would swap the whole section -- states, controls and payload lists --
	// out of the page. Rebuilding from a read after the write is also what stops
	// the freshly saved endpoint being described by the state it had before.
	return handler.respondEgressMutationHTML(c, user, status)
}

func parseWebhookSettingsInput(c fiber.Ctx) (webhookSettingsInput, error) {
	input := webhookSettingsInput{}
	if hasJSONBody(c) {
		if err := c.Bind().Body(&input); err != nil {
			return webhookSettingsInput{}, err
		}
		return input, nil
	}

	return webhookSettingsInput{
		Enabled:         services.ParseBoolLike(c.FormValue("webhook_enabled")),
		URL:             c.FormValue("webhook_url"),
		NotifyPeriod:    services.ParseBoolLike(c.FormValue("webhook_notify_period")),
		NotifyOvulation: services.ParseBoolLike(c.FormValue("webhook_notify_ovulation")),
		RemoveURL:       services.ParseBoolLike(c.FormValue("webhook_remove_url")),
	}, nil
}

// SendTestWebhook delivers one owner-initiated test notification to the
// CURRENTLY STORED endpoint. It is a POST for the same reason the save is: it
// reaches outside this instance, so it must be CSRF-protected and OwnerOnly at
// the route layer. It changes no settings and writes no watermark.
//
// The handler passes only the owner-scoped delivery projection — id, stored
// ciphertext, timezone, and interface language — never a health row. The
// service decrypts with that owner's AAD and reuses the notify pass's hardened
// deliverer, so this path cannot become a second, weaker egress client.
//
// Secret discipline: the endpoint is never rendered back and no error text is
// logged. A success answers the rebuilt egress card with a status key; a refusal
// answers the same card with localized copy, so the owner sees the test result
// where the button is and the page still shows the stored host (never the URL).
func (handler *Handler) SendTestWebhook(c fiber.Ctx) error {
	user, ok := currentUser(c)
	if !ok {
		return handler.failMutation(c, webhookTestMutation, unauthorizedErrorSpec())
	}

	record := models.WebhookNotifyRecord{
		ID:                user.ID,
		Timezone:          user.Timezone,
		InterfaceLanguage: user.InterfaceLanguage,
		WebhookURL:        user.WebhookURL,
	}
	if err := handler.webhookNotifySvc.SendTest(c.Context(), record, time.Now(), handler.requestLocation(c)); err != nil {
		return handler.failEgressMutation(c, webhookTestMutation, user, mapWebhookTestError(err))
	}

	handler.logMutationSuccess(c, webhookTestMutation)
	return handler.respondEgressMutation(c, user, services.SettingsWebhookTestedStatus)
}
