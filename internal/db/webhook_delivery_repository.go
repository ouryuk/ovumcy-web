package db

import (
	"context"
	"errors"
	"strings"

	"github.com/ovumcy/ovumcy-web/internal/models"
	"gorm.io/gorm"
)

// WebhookDeliveryRepository persists the owner-visible webhook delivery
// history (migration 040): one row per delivery ATTEMPT, kept as a RING.
//
// The ring is the reason this is a repository of its own rather than a method
// on the users repository: the bound is PER OWNER (the latest 20 rows for
// that user_id), so pruning one owner's history can never evict another's.
// The pruning happens in the INSERT path, which keeps the table bounded
// without a background job and without a sweeper that has to be scheduled.
//
// The rows carry no secret and no health specifics: the destination is the
// HOSTNAME only, and the failure is a bounded class. That is enforced by the
// caller (services) and re-checked here by trimming what it stores, so a
// future caller cannot widen the shape by accident.
type WebhookDeliveryRepository struct {
	database *gorm.DB
}

// WebhookDeliveryRingSize is how many rows per owner the table keeps. It is
// the same number the API and the UI read (the "last 20"), so the write side
// and the read side cannot disagree about what the history is.
const WebhookDeliveryRingSize = 20

// NewWebhookDeliveryRepository builds the repository over an open database.
func NewWebhookDeliveryRepository(database *gorm.DB) *WebhookDeliveryRepository {
	return &WebhookDeliveryRepository{database: database}
}

// Record inserts one delivery row and prunes the owner's history back to the
// ring size in the SAME transaction, so a crash between the two cannot leave
// an owner with an unbounded history.
//
// The prune keeps the NEWEST rows: the rows that fall outside the ring are
// those beyond the limit in occurred_at order, which for an owner whose
// attempts are timestamped by the same clock is exactly the oldest ones. The
// boundary is inclusive of the newest limit rows.
//
// The id is used as the tie-breaker so two rows sharing an occurred_at (the
// same millisecond, or an owner whose clock stands still) still prune
// deterministically: without it the surviving set would depend on the
// engine's row order, and two runs of the same history could keep different
// rows.
func (repo *WebhookDeliveryRepository) RecordWebhookDelivery(ctx context.Context, delivery models.WebhookDelivery) error {
	if delivery.UserID == 0 {
		return errors.New("webhook_deliveries: user id is required")
	}
	if delivery.OccurredAt.IsZero() {
		return errors.New("webhook_deliveries: occurred_at is required")
	}
	if strings.TrimSpace(delivery.ReminderKind) == "" {
		return errors.New("webhook_deliveries: reminder_kind is required")
	}
	if strings.TrimSpace(delivery.Outcome) == "" {
		return errors.New("webhook_deliveries: outcome is required")
	}

	row := models.WebhookDelivery{
		UserID:       delivery.UserID,
		OccurredAt:   delivery.OccurredAt.UTC(),
		ReminderKind: strings.TrimSpace(delivery.ReminderKind),
		Outcome:      strings.TrimSpace(delivery.Outcome),
		HTTPStatus:   delivery.HTTPStatus,
		ErrorClass:   strings.TrimSpace(delivery.ErrorClass),
		LatencyMS:    delivery.LatencyMS,
		// The host is stored as given. The REDACTION of the URL happens before
		// this point (services owns the one hostOnly rule); what this adds is
		// the last line of defence against a caller that passes a full URL:
		// a value carrying a scheme, a path or a query is not a hostname, and
		// storing it would put the token back into the table.
		EndpointHost: sanitizeWebhookEndpointHost(delivery.EndpointHost),
	}

	return repo.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		return pruneWebhookDeliveries(tx, row.UserID)
	})
}

// pruneWebhookDeliveries deletes the rows of one owner beyond the ring size,
// keeping the newest. It runs inside the caller's transaction, so the insert
// and the prune commit together.
func pruneWebhookDeliveries(tx *gorm.DB, userID uint) error {
	// The rows to keep are named first (the newest WebhookDeliveryRingSize of
	// this owner's rows), and everything else of this owner's is deleted by
	// id. Naming the survivors and deleting the rest is what keeps the delete
	// from racing the insert: the survivors are already committed inside this
	// transaction, so the DELETE cannot take one of them.
	survivors := make([]uint, 0, WebhookDeliveryRingSize)
	if err := tx.Model(&models.WebhookDelivery{}).
		Where("user_id = ?", userID).
		Order("occurred_at DESC, id DESC").
		Limit(WebhookDeliveryRingSize).
		Pluck("id", &survivors).Error; err != nil {
		return err
	}
	if len(survivors) == 0 {
		return nil
	}

	// Ids outside the survivor set. The NOT IN list is built from the
	// survivors' own ids, so it can never name a row of ANOTHER owner (the
	// user_id predicate below is what scopes the delete) and it never grows
	// beyond the ring size.
	placeholders := make([]string, 0, len(survivors))
	args := make([]any, 0, len(survivors)+1)
	for _, id := range survivors {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	args = append(args, userID)

	return tx.Exec(
		"DELETE FROM webhook_deliveries WHERE user_id = ? AND id NOT IN ("+strings.Join(placeholders, ", ")+")",
		args...,
	).Error
}

// ListRecentWebhookDeliveries returns the owner's most recent delivery rows, newest first, at
// most limit of them. A limit of zero or less means "the ring size", so a
// caller that does not care gets the same bounded window the write side
// keeps.
func (repo *WebhookDeliveryRepository) ListRecentWebhookDeliveries(ctx context.Context, userID uint, limit int) ([]models.WebhookDelivery, error) {
	if userID == 0 {
		return nil, errors.New("webhook_deliveries: user id is required")
	}
	if limit <= 0 {
		limit = WebhookDeliveryRingSize
	}

	rows := make([]models.WebhookDelivery, 0, limit)
	if err := repo.database.WithContext(ctx).
		Model(&models.WebhookDelivery{}).
		Where("user_id = ?", userID).
		Order("occurred_at DESC, id DESC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// sanitizeWebhookEndpointHost keeps only what a hostname is. A value that
// carries a scheme, a path, a query or a userinfo -- anything a URL would --
// is refused outright rather than trimmed, because a trimmed URL can still
// carry a token in its query string and a partial host is not worth storing.
//
// The rule is deliberately narrow: a bare hostname (letters, digits, dots and
// dashes, plus the colon and brackets an IPv6 literal needs) survives; every
// other character means the caller passed something that is not a host.
func sanitizeWebhookEndpointHost(host string) string {
	trimmed := strings.TrimSpace(host)
	if trimmed == "" {
		return ""
	}
	for _, character := range trimmed {
		switch {
		case character >= 'a' && character <= 'z':
		case character >= 'A' && character <= 'Z':
		case character >= '0' && character <= '9':
		case character == '.' || character == '-' || character == ':' || character == '[' || character == ']':
		default:
			return ""
		}
	}
	return trimmed
}
