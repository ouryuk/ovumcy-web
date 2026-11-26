package api

import "github.com/ovumcy/ovumcy-web/internal/services"

// AppointmentBriefingResponse is the published shape of
// GET /api/v1/report — the JSON appointment briefing an owner can hand a
// clinician: what was recorded over a range, which completed cycles ran
// through it, and which symptoms were logged on how many days.
type AppointmentBriefingResponse struct {
	// DateFrom/DateTo are the covered calendar span of the logged entries
	// themselves (YYYY-MM-DD), not merely the requested bounds — an empty
	// range carries empty strings, mirroring the export summary.
	DateFrom string `json:"date_from"`
	DateTo   string `json:"date_to"`
	// TotalEntries is the number of logged days in the range; HasData is
	// false when the range holds no entries at all.
	TotalEntries int  `json:"total_entries"`
	HasData      bool `json:"has_data"`
	// CompletedCycles lists every completed cycle overlapping the range,
	// oldest first, in the exact shape GET /api/v1/stats/cycles publishes.
	// Always non-nil.
	CompletedCycles []CycleHistoryEntryResponse `json:"completed_cycles"`
	// SymptomFrequency counts, per symptom, on how many distinct logged days
	// it was recorded, ordered by count descending then name. Always non-nil.
	SymptomFrequency []BriefingSymptomCountResponse `json:"symptom_frequency"`
	Disclaimer       string                         `json:"disclaimer"`
}

// BriefingSymptomCountResponse is one row of symptom_frequency.
type BriefingSymptomCountResponse struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func newBriefingSymptomFrequency(counts []services.BriefingSymptomCount) []BriefingSymptomCountResponse {
	response := make([]BriefingSymptomCountResponse, 0, len(counts))
	for _, count := range counts {
		response = append(response, BriefingSymptomCountResponse{Name: count.Name, Count: count.Count})
	}
	return response
}

// newAppointmentBriefingResponse assembles the wire briefing. Both list
// fields are always non-nil slices: an empty answer is an empty list, not a
// missing member, so a client can iterate without a nil check.
func newAppointmentBriefingResponse(summary services.ExportSummary, cycles []CycleHistoryEntryResponse, frequency []services.BriefingSymptomCount, disclaimer string) AppointmentBriefingResponse {
	if cycles == nil {
		cycles = []CycleHistoryEntryResponse{}
	}
	return AppointmentBriefingResponse{
		DateFrom:         summary.DateFrom,
		DateTo:           summary.DateTo,
		TotalEntries:     summary.TotalEntries,
		HasData:          summary.HasData,
		CompletedCycles:  cycles,
		SymptomFrequency: newBriefingSymptomFrequency(frequency),
		Disclaimer:       disclaimer,
	}
}
