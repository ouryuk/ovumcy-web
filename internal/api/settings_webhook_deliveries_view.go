package api

import (
	"time"

	"github.com/ovumcy/ovumcy-web/internal/models"
	"github.com/ovumcy/ovumcy-web/internal/services"
)

// The owner-visible webhook delivery history, projected for the Settings page.
//
// It rides the same optional-block convention as the egress ledger: the api
// layer projects NOTHING from it for a session that is not this row's owner, so
// a non-owner session receives an ABSENT key rather than a false one.
//
// Every i18n key below is a LITERAL at its own call site, for the reason the
// egress view models give: the reachability barrier resolves computed key
// families only through a registered enumerator, so an assembled key would put
// the catalogue's reachability behind a second declaration that can drift from
// this file.

// settingsWebhookDeliveryRow is one delivery attempt as the list renders it.
type settingsWebhookDeliveryRow struct {
	// OccurredISO is the attempt's time as a machine-readable attribute value.
	OccurredISO string
	// OccurredText is the attempt's time as the page shows it.
	OccurredText string
	// KindKey names the sentence for the attempt's reminder kind.
	KindKey string
	// OutcomeKey names the sentence for the attempt's outcome.
	OutcomeKey string
	// OutcomeClass is the CSS class the badge wears.
	OutcomeClass string
	// DetailKey names the sentence for the attempt's status/error detail. It
	// is empty when the attempt succeeded, because "HTTP 200" is not news.
	DetailKey string
	// DetailText carries the one free value the detail sentence needs — the
	// status code, or the latency. It never carries a URL.
	DetailText string
}

// settingsWebhookDeliveriesView is the whole delivery-history block.
type settingsWebhookDeliveriesView struct {
	// Rows are the newest attempts, newest first.
	Rows []settingsWebhookDeliveryRow
	// HasRows is false while the history is empty.
	HasRows bool
	// FailureNoticeKey is the sentence of the failure alert, and is empty when
	// there is no alert to show.
	FailureNoticeKey string
	// FailureNoticeText carries the one free value the alert sentence needs:
	// the date the newest failed attempt happened. It is a localized date and
	// never a URL.
	FailureNoticeText string
	// ShowFailureNotice is false when there is no alert to show.
	ShowFailureNotice bool
}

// buildSettingsWebhookDeliveriesView projects the domain rows onto the
// template's view.
//
// The failure alert is the point of the whole list: an owner whose reminders
// stopped arriving needs to be told SO, and told since when, without having to
// read a table and draw the conclusion themselves. It appears only when the
// NEWEST attempt failed — a failed attempt followed by a successful one is a
// retry that worked, and announcing it as an outage would be wrong.
func buildSettingsWebhookDeliveriesView(
	language string,
	location *time.Location,
	deliveries []models.WebhookDelivery,
) settingsWebhookDeliveriesView {
	view := settingsWebhookDeliveriesView{
		Rows:    make([]settingsWebhookDeliveryRow, 0, len(deliveries)),
		HasRows: len(deliveries) > 0,
	}

	for _, delivery := range deliveries {
		row := settingsWebhookDeliveryRow{
			OccurredISO:  delivery.OccurredAt.Format(time.RFC3339),
			OccurredText: services.LocalizedDateDisplay(language, delivery.OccurredAt.In(location)),
			KindKey:      settingsWebhookDeliveryKindKey(delivery.ReminderKind),
			OutcomeKey:   settingsWebhookDeliveryOutcomeKey(delivery.Outcome),
			OutcomeClass: settingsWebhookDeliveryOutcomeClass(delivery.Outcome),
		}
		row.DetailKey, row.DetailText = settingsWebhookDeliveryDetail(delivery, language, location)
		view.Rows = append(view.Rows, row)
	}

	if len(deliveries) > 0 && deliveries[0].Outcome != models.WebhookDeliveryOutcomeOK {
		view.ShowFailureNotice = true
		view.FailureNoticeKey = "settings.webhook.deliveries.failure_notice"
		view.FailureNoticeText = services.LocalizedDateDisplay(language, deliveries[0].OccurredAt.In(location))
	}

	return view
}

// settingsWebhookDeliveryKindKey names the sentence for a reminder kind.
func settingsWebhookDeliveryKindKey(kind string) string {
	switch kind {
	case models.WebhookDeliveryKindPeriod:
		return "settings.webhook.deliveries.kind.period"
	case models.WebhookDeliveryKindOvulation:
		return "settings.webhook.deliveries.kind.ovulation"
	case models.WebhookDeliveryKindTest:
		return "settings.webhook.deliveries.kind.test"
	}
	// codecov:ignore:start -- unreachable: the storage layer bounds the kind.
	return ""
	// codecov:ignore:end
}

// settingsWebhookDeliveryOutcomeKey names the sentence for an outcome.
func settingsWebhookDeliveryOutcomeKey(outcome string) string {
	switch outcome {
	case models.WebhookDeliveryOutcomeOK:
		return "settings.webhook.deliveries.outcome.ok"
	case models.WebhookDeliveryOutcomeRefused:
		return "settings.webhook.deliveries.outcome.refused"
	case models.WebhookDeliveryOutcomeError:
		return "settings.webhook.deliveries.outcome.error"
	}
	// codecov:ignore:start -- unreachable: the storage layer bounds the outcome.
	return ""
	// codecov:ignore:end
}

// settingsWebhookDeliveryOutcomeClass names the CSS class an outcome's badge
// wears, so the markup carries no conditional styling logic.
//
// The classes are the ones the stylesheet already ships for this purpose:
// status-ok for a delivery that landed, status-error for one that did not
// reach its endpoint, and status-notice for one the endpoint declined (which
// is a fact about the destination, not a fault in this instance).
func settingsWebhookDeliveryOutcomeClass(outcome string) string {
	switch outcome {
	case models.WebhookDeliveryOutcomeOK:
		return "status-ok"
	case models.WebhookDeliveryOutcomeRefused:
		return "status-notice"
	case models.WebhookDeliveryOutcomeError:
		return "status-error"
	}
	// codecov:ignore:start -- unreachable: the storage layer bounds the outcome.
	return "journal-muted"
	// codecov:ignore:end
}

// settingsWebhookDeliveryDetail names the sentence that describes WHAT the
// attempt's failure was, and the one value that sentence needs.
//
// A successful attempt has no detail sentence: "HTTP 200" is not news, and a
// badge already says the attempt worked.
//
// A failed attempt carries either the endpoint's status code (it answered and
// declined) or the bounded failure class (it never answered). The class is
// rendered as a WORD, through a key per class, so the page never shows a
// machine token like "refused_redirect" to an owner.
func settingsWebhookDeliveryDetail(delivery models.WebhookDelivery, language string, location *time.Location) (string, string) {
	if delivery.Outcome == models.WebhookDeliveryOutcomeOK {
		return "", ""
	}
	if delivery.HTTPStatus > 0 {
		return "settings.webhook.deliveries.detail.http_status", ""
	}
	switch delivery.ErrorClass {
	case models.WebhookDeliveryErrorTimeout:
		return "settings.webhook.deliveries.detail.timeout", ""
	case models.WebhookDeliveryErrorDNS:
		return "settings.webhook.deliveries.detail.dns", ""
	case models.WebhookDeliveryErrorTLS:
		return "settings.webhook.deliveries.detail.tls", ""
	case models.WebhookDeliveryErrorRefusedRedirect:
		return "settings.webhook.deliveries.detail.refused_redirect", ""
	case models.WebhookDeliveryErrorPrivateAddress:
		return "settings.webhook.deliveries.detail.private_address", ""
	case models.WebhookDeliveryErrorHTTP:
		return "settings.webhook.deliveries.detail.http_error", ""
	case models.WebhookDeliveryErrorTransport:
		return "settings.webhook.deliveries.detail.transport", ""
	}
	// codecov:ignore:start -- unreachable: the storage layer bounds the class.
	return "", ""
	// codecov:ignore:end
}
