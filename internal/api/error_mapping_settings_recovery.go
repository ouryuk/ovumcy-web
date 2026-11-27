package api

import (
	"errors"

	"github.com/gofiber/fiber/v3"
	"github.com/ovumcy/ovumcy-web/internal/services"
)

// mapRecoveryCodeRegenerationError narrows only the generation failure; the
// persistence failure and an unrecognized error share the default, since after
// generation the regeneration can only have failed on the way to storage.
func mapRecoveryCodeRegenerationError(err error) APIErrorSpec {
	switch {
	case errors.Is(err, services.ErrRecoveryCodeGenerate):
		return globalErrorSpec(fiber.StatusInternalServerError, APIErrorCategoryInternal, "failed to create recovery code")
	default:
		return globalErrorSpec(fiber.StatusInternalServerError, APIErrorCategoryInternal, "failed to update recovery code")
	}
}

func settingsLoadErrorSpec() APIErrorSpec {
	return globalErrorSpec(fiber.StatusInternalServerError, APIErrorCategoryInternal, "failed to load settings")
}

// webhookDeliveriesLoadErrorSpec maps a failed read of the owner-visible
// webhook delivery history.
//
// The failure is INTERNAL and not a 404: the rows may well be there, and the
// owner cannot fix a storage fault. The key names the load so the page can
// distinguish "the history is empty" from "the history could not be read".
func webhookDeliveriesLoadErrorSpec() APIErrorSpec {
	return globalErrorSpec(fiber.StatusInternalServerError, APIErrorCategoryInternal, "failed to load webhook deliveries")
}
