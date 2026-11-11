package api

import (
	"github.com/ovumcy/ovumcy-web/internal/services"
)

// CycleHistoryEntryResponse is the published shape of one completed cycle from
// GET /api/v1/stats/cycles.
//
// Dates are calendar days (YYYY-MM-DD), never RFC 3339 instants: a cycle is a
// day range, not a moment, and serializing the domain time.Time values would
// publish a timezone the owner never chose. An ovulation day is null when the
// recorded cycle is too short for either the BBT detector or the model to place
// one, so the field set stays constant across entries.
//
// ovulation_confirmed and ovulation_estimated are mutually exclusive and both
// false when ovulation_date is null. The confirmation comes from the shared
// thermal detector over the owner's own BBT readings; the estimate is the
// model's projection from the recorded cycle length and the owner's luteal
// phase. Keeping the two flags separate lets a client show "estimated" without
// parsing prose, and mirrors the overview's confirmed/exact distinction.
type CycleHistoryEntryResponse struct {
	StartDate          string  `json:"start_date"`
	EndDate            string  `json:"end_date"`
	Length             int     `json:"length"`
	OvulationDate      *string `json:"ovulation_date"`
	OvulationConfirmed bool    `json:"ovulation_confirmed"`
	OvulationEstimated bool    `json:"ovulation_estimated"`
}

// newCycleHistoryResponse maps the service entries onto the wire. It always
// returns a non-nil slice: an owner with no completed cycles must receive []
// rather than null, so an empty history is a list with no items, not a missing
// answer.
func newCycleHistoryResponse(entries []services.CycleHistoryEntry) []CycleHistoryEntryResponse {
	response := make([]CycleHistoryEntryResponse, 0, len(entries))
	for _, entry := range entries {
		response = append(response, CycleHistoryEntryResponse{
			StartDate:          entry.Start.Format(statsOverviewDateLayout),
			EndDate:            entry.End.Format(statsOverviewDateLayout),
			Length:             entry.Length,
			OvulationDate:      statsOverviewDate(entry.OvulationDate),
			OvulationConfirmed: entry.OvulationConfirmed,
			OvulationEstimated: entry.OvulationEstimated,
		})
	}
	return response
}
