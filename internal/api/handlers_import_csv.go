package api

import (
	"strconv"

	"github.com/gofiber/fiber/v3"
)

// ImportCSV restores an owner's prior CSV export. Like ImportJSON it is
// transport-only: the session owner is resolved here, the raw body is handed to
// the service, and the typed outcome is mapped back. Owner scoping, header and
// row validation, and the additive (skip-existing) contract all live in
// services.ImportService. CSRF is enforced by global middleware; OwnerOnly is
// declared explicitly on the route.
//
// It shares dayImportMutation with the JSON restore: both write the same
// health data through the same service path, so the audit line carries the same
// domain, target, and added/skipped/rejected counts regardless of format.
func (handler *Handler) ImportCSV(c fiber.Ctx) error {
	user, ok := currentUser(c)
	// codecov:ignore:start -- defensive: AuthRequired middleware guarantees a session user on this route, so this guard is unreachable via routing
	if !ok || user == nil {
		return handler.failMutation(c, dayImportMutation, unauthorizedErrorSpec())
	}
	// codecov:ignore:end

	result, err := handler.importService.ImportCSV(
		c.Context(),
		user.ID,
		c.Body(),
		handler.requestLocation(c),
	)
	if err != nil {
		return handler.failMutation(c, dayImportMutation, mapImportError(err))
	}

	handler.logMutationSuccess(
		c,
		dayImportMutation,
		securityEventField("added", strconv.Itoa(result.Added)),
		securityEventField("skipped", strconv.Itoa(result.Skipped)),
		securityEventField("rejected", strconv.Itoa(result.Rejected)),
	)
	return handler.respondImportSuccess(c, result)
}
