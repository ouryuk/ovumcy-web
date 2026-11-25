package services

import (
	"context"
	"sort"
	"strings"
	"time"
)

// BriefingSymptomCount is one symptom's day-count over an appointment
// briefing's range. `Name` is the built-in symptom column key for catalogued
// symptoms (the same spelling the CSV export's flag names and the JSON export's
// `symptoms` object use) and the trimmed custom name for an owner's own
// symptom, so one uniform list serves both.
type BriefingSymptomCount struct {
	Name  string
	Count int
}

// BuildBriefingSummary answers the two range aggregates an appointment
// briefing needs — the export summary (entry count and covered calendar span)
// and the per-symptom day counts — from ONE read of the owner's logs.
//
// A symptom is counted once per day it was logged on, no matter how its
// entries were stored, and a day logged twice with the same symptom still
// counts that symptom once. The list is ordered by count descending and then
// by name so the answer is stable for a client rendering it.
func (service *ExportService) BuildBriefingSummary(ctx context.Context, userID uint, from *time.Time, to *time.Time, location *time.Location) (ExportSummary, []BriefingSymptomCount, error) {
	logs, symptomNames, err := service.LoadDataForRange(ctx, userID, from, to, location)
	if err != nil {
		return ExportSummary{}, nil, err
	}

	counts := make(map[string]int)
	for _, logEntry := range logs {
		seen := make(map[string]struct{})
		for _, symptomID := range logEntry.SymptomIDs {
			name, ok := symptomNames[symptomID]
			if !ok {
				continue
			}
			column := exportSymptomColumn(name)
			key := column
			if column == "other" {
				trimmed := strings.TrimSpace(name)
				if trimmed == "" {
					continue
				}
				key = trimmed
			}
			seen[key] = struct{}{}
		}
		for key := range seen {
			counts[key]++
		}
	}

	frequency := make([]BriefingSymptomCount, 0, len(counts))
	for name, count := range counts {
		frequency = append(frequency, BriefingSymptomCount{Name: name, Count: count})
	}
	sort.Slice(frequency, func(i, j int) bool {
		if frequency[i].Count != frequency[j].Count {
			return frequency[i].Count > frequency[j].Count
		}
		return frequency[i].Name < frequency[j].Name
	})

	return summarizeExportLogs(logs), frequency, nil
}

// FilterBriefingCycles keeps the completed cycles that overlap the inclusive
// calendar range [from, to]. A nil bound is open on that side; both bounds nil
// returns every entry (as a fresh slice, never the caller's backing array).
//
// A cycle is in range when any part of it — its start, its span, or its end —
// touches the range, because a clinician asking about "May" wants the cycle
// that runs through May even though it started in April. The comparison is
// done on calendar days in the request location, never on instants, matching
// the rest of the day-math in this package.
func FilterBriefingCycles(entries []CycleHistoryEntry, from *time.Time, to *time.Time, location *time.Location) []CycleHistoryEntry {
	if location == nil {
		location = time.UTC
	}

	filtered := make([]CycleHistoryEntry, 0, len(entries))
	for _, entry := range entries {
		if from != nil && CalendarDaysBetween(CalendarDay(*from, location), entry.End) < 0 {
			// The cycle ended before the range opened.
			continue
		}
		if to != nil && CalendarDaysBetween(entry.Start, CalendarDay(*to, location)) < 0 {
			// The cycle started after the range closed.
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}
