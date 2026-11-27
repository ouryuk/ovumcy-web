package services

import (
	"context"
	"errors"
	"log"
	"net/url"
	"strings"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/models"
)

// The owner-visible webhook delivery history (migration 040).
//
// Why it exists: an owner whose reminders stopped arriving had exactly one
// piece of evidence — a single "last accepted delivery" mark — which says
// WHEN the last one arrived and nothing about what happened to the ones after
// it. "Did it go out?" was unanswerable, and so was "why did it stop?", which
// is the question an owner actually asks when their ntfy server went quiet.
//
// What the history is careful about:
//
//   - It records the DESTINATION HOST ONLY. The URL's path, its query string
//     and any token embedded in its userinfo (ntfy and Gotify both put the
//     secret there) never reach a row, a log, or a page. The hostOnly rule
//     below is the package's single redaction rule, so one hardening reaches
//     every surface at once.
//   - It records a bounded FAILURE CLASS, never the error's text: a transport
//     error message embeds the URL it was raised for.
//   - It records the reminder KIND and nothing about the cycle: no predicted
//     date, no lead days, no symptoms. Those stay in the payload that left the
//     instance, not in this table.
//   - It is BEST EFFORT. A failed write is logged and dropped: a storage
//     fault in the history must never turn a delivered reminder into a failed
//     one, nor the other way round.

// WebhookDeliveryRingSize is how many rows the history keeps per owner. It is
// the package's own copy of the storage bound, and the number the API and the
// UI read — so the write side and the read side cannot disagree about what
// the history is.
const WebhookDeliveryRingSize = 20

// WebhookDeliveryRecorder is the narrow persistence surface the delivery
// history needs. It is an interface so the notify service can be unit-tested
// with a stub and never reach for a database.
//
// Two operations, and the second one is owner-scoped by contract: a reader
// that could be asked for another owner's rows would be an IDOR.
type WebhookDeliveryRecorder interface {
	// RecordWebhookDelivery stores one delivery row and prunes the owner's
	// history back to the ring size.
	RecordWebhookDelivery(ctx context.Context, delivery models.WebhookDelivery) error
	// ListRecentWebhookDeliveries returns the owner's newest rows, newest
	// first, at most limit of them.
	ListRecentWebhookDeliveries(ctx context.Context, userID uint, limit int) ([]models.WebhookDelivery, error)
}

// WebhookDeliveryRecord is the transport-free description of one delivery
// attempt, as the recorder writes it. It exists so the notify service does not
// have to know the column names and so the mapping from a delivery attempt to
// a row happens in exactly one place.
type WebhookDeliveryRecord struct {
	// UserID is the owner the delivery was made for.
	UserID uint
	// OccurredAt is the moment the attempt was made.
	OccurredAt time.Time
	// ReminderKind is one of the models.WebhookDeliveryKind* values.
	ReminderKind string
	// Outcome is one of the models.WebhookDeliveryOutcome* values.
	Outcome string
	// HTTPStatus is the endpoint's own status code, or 0 when there was no
	// response at all.
	HTTPStatus int
	// ErrorClass is one of the models.WebhookDeliveryError* values, or "" when
	// the attempt did not fail.
	ErrorClass string
	// LatencyMS is the measured duration of the attempt in milliseconds.
	LatencyMS int
	// EndpointHost is the destination HOST ONLY. It is redacted here even if
	// the caller passes a full URL: the redaction belongs to the package, not
	// to the discipline of whoever calls it.
	EndpointHost string
}

// WebhookDeliveryHistory writes and reads the owner-visible delivery history.
//
// It holds only the recorder seam, so the notify service can be assembled with
// a stub and the recording path can be exercised without a database.
type WebhookDeliveryHistory struct {
	recorder WebhookDeliveryRecorder
}

// NewWebhookDeliveryHistory builds the history over a recorder seam. A nil
// recorder is allowed and disables the history: a partially wired instance
// must not panic on the first delivery attempt, and a missing history is
// better than a failed reminder.
func NewWebhookDeliveryHistory(recorder WebhookDeliveryRecorder) *WebhookDeliveryHistory {
	return &WebhookDeliveryHistory{recorder: recorder}
}

// Record stores one delivery attempt, best effort.
//
// A failed write is logged (owner id only — the row's own contents are not
// safe to log wholesale, and the failure carries none of them) and DROPPED.
// The delivery itself already happened, or already failed, and neither fact
// changes because this instance could not write a history row about it. That
// is the whole meaning of "best effort" here: the history is evidence about a
// delivery, never a part of it.
//
// The occurrence time is supplied by the caller, not read from a clock here:
// this package holds no clock on purpose, and the notify pass's injected
// clock is the same one the decision layer decided against.
func (history *WebhookDeliveryHistory) Record(ctx context.Context, record WebhookDeliveryRecord) {
	if history == nil || history.recorder == nil {
		return
	}
	if record.UserID == 0 {
		return
	}

	row := models.WebhookDelivery{
		UserID:       record.UserID,
		OccurredAt:   record.OccurredAt.UTC(),
		ReminderKind: strings.TrimSpace(record.ReminderKind),
		Outcome:      strings.TrimSpace(record.Outcome),
		HTTPStatus:   record.HTTPStatus,
		ErrorClass:   strings.TrimSpace(record.ErrorClass),
		LatencyMS:    record.LatencyMS,
		EndpointHost: bareHost(record.EndpointHost),
	}
	if row.OccurredAt.IsZero() {
		return
	}
	if row.ReminderKind == "" || row.Outcome == "" {
		return
	}

	if err := history.recorder.RecordWebhookDelivery(ctx, row); err != nil {
		// Owner id only, and the error's own text is not logged: a storage
		// error can quote the row it could not write.
		log.Printf("webhook delivery history: write failed, owner id=%d", record.UserID)
	}
}

// List returns the owner's newest delivery rows, newest first, at most limit
// of them. A limit of zero or less means the ring size.
//
// It is a plain read: unlike the write, an unreadable history is reported to
// the caller, because a page that shows an empty list where the history
// exists is a wrong answer rather than a missing convenience.
func (history *WebhookDeliveryHistory) List(ctx context.Context, userID uint, limit int) ([]models.WebhookDelivery, error) {
	if history == nil || history.recorder == nil {
		return nil, errors.New("webhook delivery history is not wired")
	}
	return history.recorder.ListRecentWebhookDeliveries(ctx, userID, limit)
}

// RecordDeliveryOutcome turns a delivery attempt's outcome into the history
// row that describes it, and writes it.
//
// It is the ONE place where an attempt becomes a row, so the mapping from an
// attempt to what the owner sees cannot drift between the reminder path and
// the test-send path.
//
// The outcome value is derived, not chosen by the caller: a success is an OK,
// a non-2xx answer is a REFUSAL (the endpoint was reached and declined), and
// anything else is an ERROR (the request never completed). That distinction is
// the one an owner acts on differently, so it is not left to a caller to get
// right.
func (history *WebhookDeliveryHistory) RecordDeliveryOutcome(
	ctx context.Context,
	userID uint,
	kind string,
	outcome WebhookDeliveryOutcome,
	delivered bool,
	occurredAt time.Time,
) {
	history.Record(ctx, WebhookDeliveryRecord{
		UserID:       userID,
		OccurredAt:   occurredAt,
		ReminderKind: kind,
		Outcome:      deliveryOutcomeValue(outcome, delivered),
		HTTPStatus:   outcome.HTTPStatus,
		ErrorClass:   outcome.ErrorClass,
		LatencyMS:    outcome.LatencyMS,
		EndpointHost: outcome.Host,
	})
}

// bareHost reduces a destination to the HOSTNAME it names, and nothing else.
//
// It accepts EITHER shape a caller may hold: a bare hostname (the delivery
// client's outcome already carries one) or a full URL (a caller that only had
// the endpoint string). Either way the result is a hostname — no scheme, no
// port, no path, no query, no userinfo — so a token embedded in the URL's
// query string or its userinfo cannot reach the row even if a future caller
// passes the URL straight through.
//
// A value that is neither a hostname nor a URL that parses to one is refused
// outright (""), rather than trimmed: a trimmed URL can still carry a token,
// and a partial host is worse than no host.
func bareHost(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}

	// A bare hostname has no scheme separator. url.Parse would read
	// "ntfy.example.com" as a PATH and yield an empty hostname, so the
	// hostname-looking shape is recognized first and kept as it is.
	if !strings.Contains(trimmed, "//") && !strings.Contains(trimmed, "/") && !strings.Contains(trimmed, "?") && !strings.Contains(trimmed, "#") && !strings.Contains(trimmed, "@") {
		if isBareHostname(trimmed) {
			return trimmed
		}
		return ""
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return ""
	}
	hostname := parsed.Hostname()
	if hostname == "" || !isBareHostname(hostname) {
		return ""
	}
	return hostname
}

// isBareHostname reports whether a value consists only of the characters a
// hostname is made of: letters, digits, dots and dashes, plus the colon and
// brackets an IPv6 literal needs.
//
// The check is a CHARACTER check, not a format check, and it is deliberately
// so: it refuses every character that could carry a secret (a slash, a
// question mark, an at sign, an equals sign), and it says nothing about
// whether the name resolves or is routable.
func isBareHostname(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z':
		case character >= 'A' && character <= 'Z':
		case character >= '0' && character <= '9':
		case character == '.' || character == '-' || character == ':' || character == '[' || character == ']':
		default:
			return false
		}
	}
	return true
}

// deliveryOutcomeValue maps an attempt to the three-valued outcome the
// history stores.
//
// A delivered attempt is an OK. An attempt that got an answer which was not a
// 2xx is a REFUSAL — the endpoint was reached and declined, which is a
// different fact from a request that never completed. Everything else is an
// ERROR.
//
// The failure class is kept only where it means something: an OK carries none,
// and a REFUSAL carries the http_error class (which says the endpoint
// answered and said no), while an ERROR carries whatever class the transport
// produced.
func deliveryOutcomeValue(outcome WebhookDeliveryOutcome, delivered bool) string {
	if delivered {
		return models.WebhookDeliveryOutcomeOK
	}
	if outcome.HTTPStatus > 0 {
		return models.WebhookDeliveryOutcomeRefused
	}
	return models.WebhookDeliveryOutcomeError
}
