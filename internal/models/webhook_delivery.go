package models

import "time"

// Webhook delivery history (migration 040): one row per delivery ATTEMPT, so
// an owner can see what happened to their reminders instead of inferring it
// from a single "last delivered" mark.
//
// What the row deliberately does NOT carry is as load-bearing as what it does:
// the destination is the HOSTNAME only (never the path, the query string, or a
// token embedded in the userinfo -- ntfy and Gotify both put the secret
// there), and the failure is a bounded error CLASS, never the error text (a
// transport error message embeds the URL it was raised for).
//
// No health specifics are stored either: a row names the KIND of reminder
// (period, ovulation, test) and nothing about the cycle the reminder was
// about. The predicted date stays in the payload that left the instance.

// WebhookDeliveryKind names the reminder kind a delivery carried. The test
// kind is deliberately NOT one of the two reminder kinds: a test message must
// never be mistaken for a period or ovulation reminder by anything routing on
// this value.
const (
	// WebhookDeliveryKindPeriod is a period-soon reminder.
	WebhookDeliveryKindPeriod = "period"
	// WebhookDeliveryKindOvulation is an ovulation-soon reminder.
	WebhookDeliveryKindOvulation = "ovulation"
	// WebhookDeliveryKindTest is an owner-initiated test message.
	WebhookDeliveryKindTest = "test"
)

// WebhookDeliveryOutcome is the three-valued result of one delivery attempt.
const (
	// WebhookDeliveryOutcomeOK means the endpoint answered 2xx.
	WebhookDeliveryOutcomeOK = "ok"
	// WebhookDeliveryOutcomeRefused means the endpoint answered, and its answer
	// was not a 2xx (a 4xx or 5xx). The endpoint REACHED the request and
	// declined it -- a different fact from a transport failure, which never got
	// that far.
	WebhookDeliveryOutcomeRefused = "refused"
	// WebhookDeliveryOutcomeError means the request never completed: a timeout,
	// a refused dial, a DNS failure, a refused redirect, a private-address
	// refusal, or a non-2xx status the delivery client could not read.
	WebhookDeliveryOutcomeError = "error"
)

// WebhookDeliveryErrorClass is the BOUNDED set of failure kinds a delivery row
// may carry. It is a closed enum on purpose: the row is a UI surface, and an
// unbounded free-text reason would let a transport error's message -- which
// embeds the URL -- into the owner's page.
//
// The empty string means "no failure was classified", which is the value an
// OK or a plain HTTP refusal carries.
const (
	// WebhookDeliveryErrorTimeout means the delivery exceeded its budget.
	WebhookDeliveryErrorTimeout = "timeout"
	// WebhookDeliveryErrorDNS means the hostname could not be resolved.
	WebhookDeliveryErrorDNS = "dns"
	// WebhookDeliveryErrorTLS means the TLS handshake failed.
	WebhookDeliveryErrorTLS = "tls"
	// WebhookDeliveryErrorRefusedRedirect means the endpoint answered with a
	// redirect, which this client refuses to follow.
	WebhookDeliveryErrorRefusedRedirect = "refused_redirect"
	// WebhookDeliveryErrorPrivateAddress means the target resolved to a
	// private, loopback, link-local or otherwise non-public address and the
	// private-address gate refused it.
	WebhookDeliveryErrorPrivateAddress = "private_address"
	// WebhookDeliveryErrorHTTP means the endpoint answered with a non-2xx
	// status (a refusal rather than a transport failure).
	WebhookDeliveryErrorHTTP = "http_error"
	// WebhookDeliveryErrorTransport means the request failed for any other
	// transport reason (a refused dial, a reset connection, a malformed
	// response).
	WebhookDeliveryErrorTransport = "transport"
)

// WebhookDelivery is one recorded delivery attempt.
//
// The row is owner-scoped (UserID) and carries the moment the attempt was
// made (OccurredAt), the kind of reminder it carried (ReminderKind), how it
// ended (Outcome), the endpoint's status code when there was one
// (HTTPStatus), the failure class when there was one (ErrorClass), how long it
// took (LatencyMS) and the destination HOST only (EndpointHost).
//
// HTTPStatus is 0 when there was no response at all -- a timeout, a refused
// dial, a DNS failure -- which is what distinguishes "the endpoint said no"
// from "the endpoint was never reached".
type WebhookDelivery struct {
	ID         uint      `gorm:"column:id;primaryKey"`
	UserID     uint      `gorm:"column:user_id;not null"`
	OccurredAt time.Time `gorm:"column:occurred_at;not null"`
	// ReminderKind is one of the WebhookDeliveryKind* values.
	ReminderKind string `gorm:"column:reminder_kind;not null"`
	// Outcome is one of the WebhookDeliveryOutcome* values.
	Outcome string `gorm:"column:outcome;not null"`
	// HTTPStatus is the endpoint's own status code, or 0 when there was no
	// response.
	HTTPStatus int `gorm:"column:http_status;not null"`
	// ErrorClass is one of the WebhookDeliveryError* values, or "" when the
	// attempt did not fail.
	ErrorClass string `gorm:"column:error_class;not null"`
	// LatencyMS is the measured duration of the attempt in milliseconds.
	LatencyMS int `gorm:"column:latency_ms;not null"`
	// EndpointHost is the destination HOSTNAME ONLY.
	EndpointHost string `gorm:"column:endpoint_host;not null"`
}

// TableName pins the table to webhook_deliveries (GORM would derive
// webhook_deliveries anyway, but the pin keeps the name from ever drifting
// with the struct).
func (WebhookDelivery) TableName() string {
	return "webhook_deliveries"
}
