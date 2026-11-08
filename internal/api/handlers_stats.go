package api

import (
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/ovumcy/ovumcy-web/internal/services"
)

func (handler *Handler) GetStatsOverview(c fiber.Ctx) error {
	user, ok := currentUser(c)
	if !ok {
		return handler.respondMappedError(c, unauthorizedErrorSpec())
	}

	location := handler.requestLocation(c)
	now := time.Now().In(location)
	stats, logs, err := handler.statsService.BuildOverviewStats(c.Context(), user, now, location)
	if err != nil {
		return handler.respondMappedError(c, statsFetchErrorSpec())
	}

	// The same adapter /stats and the dashboard publish through: this endpoint
	// used to serialize the domain struct, so every date those pages withhold
	// left the instance as JSON (medical safety — suppression is the floor).
	// PublishedOverviewStats additionally resolves a confirmed ovulation day the
	// way the calendar's solid marker and the dashboard's line already do, so a
	// BBT shift that has superseded the model's projection is not the one thing
	// this endpoint still names the old day for.
	today := services.DateAtLocation(now, location)
	published, suppression, confirmed := services.PublishedOverviewStats(user, logs, stats, today, location)

	return c.JSON(newStatsOverviewResponse(published, suppression, confirmed, handler.medicalDisclaimer(c)))
}

// GetCycleHistory publishes the owner's individual completed cycles, oldest
// first. It is the per-cycle counterpart to GetStatsOverview: the aggregate
// endpoint answers the averages, this one answers "what were my last cycles,
// one by one".
//
// The builder reuses the overview's log read and cycle detection, so an empty
// history is an empty array with 200 — not a 404 and not a 500. The only
// fallible step is the repository read, which keeps the existing stats fetch
// error mapping.
func (handler *Handler) GetCycleHistory(c fiber.Ctx) error {
	user, ok := currentUser(c)
	if !ok {
		return handler.respondMappedError(c, unauthorizedErrorSpec())
	}

	location := handler.requestLocation(c)
	now := time.Now().In(location)
	entries, err := handler.statsService.BuildCycleHistory(c.Context(), user, now, location)
	if err != nil {
		return handler.respondMappedError(c, statsFetchErrorSpec())
	}

	return c.JSON(newCycleHistoryResponse(entries))
}
