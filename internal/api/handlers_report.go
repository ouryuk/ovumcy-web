package api

import (
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/ovumcy/ovumcy-web/internal/services"
)

// appointmentBriefingEgress tags the briefing read as audited health-data
// egress, under the same data.export action the three file downloads use so
// one operator filter keeps covering every format. The briefing is a read-only
// JSON answer, but it still carries tracked cycle and symptom data out of the
// instance in-band, and the audit stream should count that disclosure like the
// others. No export payload ever enters the log line — only the format detail.
var appointmentBriefingEgress = healthEgressKind{
	action: dataExportAction,
	target: exportEgressTarget,
	detail: securityEventField("export_format", "briefing"),
}

// GetReport publishes the appointment briefing: GET /api/v1/report?from=&to=
// summarises what the owner recorded over a date range — logged days, the
// completed cycles that overlap the range, and per-symptom day counts — as one
// JSON answer an owner can print or hand to a clinician.
//
// It reuses the read-only services behind the exports and the cycle history:
// the range aggregates come from one ExportService read and the cycles are the
// cycle-history entries filtered to the range, so the briefing can never
// disagree with /api/v1/stats/cycles or /api/v1/exports/summary about the same
// window. An unparseable or reversed range is a 400 before any data is read
// (and before any egress is logged as successful), and a range with no logs is
// a 200 with has_data=false and empty lists, never a 404.
func (handler *Handler) GetReport(c fiber.Ctx) error {
	user, from, to, spec := handler.exportUserAndRange(c, appointmentBriefingEgress)
	if spec != nil {
		return handler.respondMappedError(c, *spec)
	}

	location := handler.requestLocation(c)

	summary, frequency, err := handler.exportService.BuildBriefingSummary(c.Context(), user.ID, from, to, location)
	if err != nil {
		return handler.failEgress(c, appointmentBriefingEgress, exportFetchLogsErrorSpec())
	}

	entries, err := handler.statsService.BuildCycleHistory(c.Context(), user, time.Now().In(location), location)
	if err != nil {
		return handler.failEgress(c, appointmentBriefingEgress, statsFetchErrorSpec())
	}
	cycles := newCycleHistoryResponse(services.FilterBriefingCycles(entries, from, to, location))

	handler.logEgressSuccess(c, appointmentBriefingEgress)
	return c.JSON(newAppointmentBriefingResponse(summary, cycles, frequency, handler.medicalDisclaimer(c)))
}
