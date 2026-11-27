package api

import (
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/ovumcy/ovumcy-web/internal/models"
	"github.com/ovumcy/ovumcy-web/internal/services"
)

// GetWebhookDeliveries publishes the CURRENT owner's webhook delivery history:
// the newest attempts, newest first, at most the ring size of them.
//
// It is owner-only by construction — the handler reads the authenticated
// session's user id and never accepts an id from the request, so there is no
// shape of request that can ask for another owner's rows. There is not even an
// optional query parameter for one.
//
// What the response carries is deliberately thin: the time, the reminder kind,
// the outcome, the endpoint's status code, the bounded failure class and the
// latency. It carries NO url, no path, no query string, no token and no error
// text: the endpoint's HOST is the one piece of destination detail that is safe
// to show (it names where the reminder went without naming the secret that
// destination carries), and the failure CLASS says what went wrong without
// quoting a message that would embed the URL.
//
// An empty history is an empty array with 200, not a 404: an owner who has
// just configured an endpoint has no deliveries yet, and that is a state, not
// a missing thing.
func (handler *Handler) GetWebhookDeliveries(c fiber.Ctx) error {
	user, ok := currentUser(c)
	if !ok {
		return handler.respondMappedError(c, unauthorizedErrorSpec())
	}

	deliveries, err := handler.webhookDeliveryHist.List(
		c.Context(),
		user.ID,
		services.WebhookDeliveryRingSize,
	)
	if err != nil {
		return handler.respondMappedError(c, webhookDeliveriesLoadErrorSpec())
	}
	if deliveries == nil {
		deliveries = []models.WebhookDelivery{}
	}

	return c.JSON(newWebhookDeliveriesResponse(deliveries))
}

// WebhookDeliveryResponse is one row of the owner-visible delivery history, as
// the API publishes it.
//
// The field set is the redaction: there is no url, path, query or token field
// to leak, because a field that does not exist cannot be published by
// accident. EndpointHost is the destination host alone.
type WebhookDeliveryResponse struct {
	// OccurredAt is the attempt's time, in RFC3339 with the offset it was
	// recorded at.
	OccurredAt string `json:"occurred_at"`
	// ReminderKind is one of period, ovulation or test.
	ReminderKind string `json:"reminder_kind"`
	// Outcome is one of ok, refused or error.
	Outcome string `json:"outcome"`
	// HTTPStatus is the endpoint's own status code, or 0 when no response
	// arrived at all.
	HTTPStatus int `json:"http_status"`
	// ErrorClass is the bounded failure class, or "" when the attempt did not
	// fail.
	ErrorClass string `json:"error_class,omitempty"`
	// LatencyMS is the attempt's measured duration in milliseconds.
	LatencyMS int `json:"latency_ms"`
	// EndpointHost is the destination host alone.
	EndpointHost string `json:"endpoint_host,omitempty"`
}

// WebhookDeliveriesResponse is the owner's delivery history.
type WebhookDeliveriesResponse struct {
	// Deliveries holds the newest rows, newest first.
	Deliveries []WebhookDeliveryResponse `json:"deliveries"`
	// Limit is the ring size the response is capped at, so a client can tell
	// "there are no more" from "there are more than we publish".
	Limit int `json:"limit"`
}

// newWebhookDeliveriesResponse projects the domain rows onto the published
// shape.
//
// The projection is where the time is formatted: the domain row carries a
// UTC instant, and the API publishes it with the offset it was recorded at so
// a client can render it in the owner's own zone without guessing.
func newWebhookDeliveriesResponse(deliveries []models.WebhookDelivery) WebhookDeliveriesResponse {
	rows := make([]WebhookDeliveryResponse, 0, len(deliveries))
	for _, delivery := range deliveries {
		rows = append(rows, WebhookDeliveryResponse{
			OccurredAt:   delivery.OccurredAt.Format(time.RFC3339),
			ReminderKind: delivery.ReminderKind,
			Outcome:      delivery.Outcome,
			HTTPStatus:   delivery.HTTPStatus,
			ErrorClass:   delivery.ErrorClass,
			LatencyMS:    delivery.LatencyMS,
			EndpointHost: delivery.EndpointHost,
		})
	}
	return WebhookDeliveriesResponse{
		Deliveries: rows,
		Limit:      services.WebhookDeliveryRingSize,
	}
}
