package services

import (
	"context"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/models"
)

// CycleHistoryEntry is one COMPLETED cycle from the owner's recorded history.
//
// A completed cycle is an observed cycle start followed by a second observed
// start: the open, running cycle is deliberately not an entry, because its end
// and length are not known yet. Entries are derived from the same log window
// and the same day reading path StatsService.BuildOverviewStats uses, so the
// history can never describe a different set of cycles than the aggregate
// overview counts.
type CycleHistoryEntry struct {
	Start time.Time
	End   time.Time
	// Length is the calendar-day span from Start to the next observed start,
	// exactly the length buildCycles and the trend series use.
	Length int
	// OvulationDate is either the day inferred from a confirmed BBT shift or
	// the model's estimate from this cycle's own length. Zero when the cycle is
	// too short for PredictCycleWindow to place one.
	OvulationDate time.Time
	// OvulationConfirmed is true when OvulationDate comes from the shared
	// "3-over-6" thermal detector over the owner's recorded BBT readings.
	OvulationConfirmed bool
	// OvulationEstimated is true when OvulationDate is the length-based model
	// estimate. The two flags are mutually exclusive.
	OvulationEstimated bool
}

// BuildCycleHistory returns the owner's completed cycles in chronological
// order (oldest first), derived from the same overview range and day-reading
// path as GET /api/v1/stats/overview.
//
// It deliberately adds no second cycle-detection algorithm: observed starts
// and per-cycle boundaries come from ObservedCycleStarts/buildCycles, and the
// BBT experiment reuses inferBBTOvulationDate, the detector the calendar,
// dashboard and stats chart already share.
//
// No logs (or a single open cycle) is an empty history, never an error. The
// repository read is the only fallible step and its error is passed through.
func (service *StatsService) BuildCycleHistory(ctx context.Context, user *models.User, now time.Time, location *time.Location) ([]CycleHistoryEntry, error) {
	if location == nil {
		location = time.UTC
	}
	if user == nil {
		return []CycleHistoryEntry{}, nil
	}

	// BuildOverviewStats is the day-reading path the aggregate overview uses:
	// it fetches the same two-year window and applies the owner's baseline, so
	// the history cannot disagree with the overview about which cycles exist.
	stats, logs, err := service.BuildOverviewStats(ctx, user, now, location)
	if err != nil {
		return nil, err
	}
	return buildCycleHistoryFromLogs(user, logs, stats, now, location), nil
}

func buildCycleHistoryFromLogs(user *models.User, logs []models.DailyLog, stats CycleStats, now time.Time, location *time.Location) []CycleHistoryEntry {
	if location == nil {
		location = time.UTC
	}

	today := DateAtLocation(now, location)
	sorted := sortDailyLogs(filterLogsNotAfter(logs, today))
	starts := ObservedCycleStarts(sorted)
	if len(starts) < 2 {
		return []CycleHistoryEntry{}
	}

	cycles := buildCycles(starts, sorted)
	entries := make([]CycleHistoryEntry, 0, len(starts)-1)
	for index := 0; index+1 < len(starts) && index < len(cycles); index++ {
		start := CalendarDay(starts[index], location)
		nextStart := CalendarDay(starts[index+1], location)
		length := CalendarDaysBetween(start, nextStart)
		if length <= 0 {
			continue
		}

		entry := CycleHistoryEntry{
			Start:  start,
			End:    CalendarDay(cycles[index].End, location),
			Length: length,
		}

		ovulationDate := time.Time{}
		if user != nil && user.TrackBBT {
			// The detected shift belongs to this completed cycle only, so the
			// series ends at the next observed start — the same exclusive bound
			// the luteal-phase inference uses for completed cycles.
			ovulationDate = inferBBTOvulationDate(sorted, start, nextStart, location)
			if !ovulationDate.IsZero() {
				entry.OvulationConfirmed = true
			}
		}
		if ovulationDate.IsZero() {
			window := PredictCycleWindow(start, length, stats.LutealPhase)
			if window.Calculable && !window.OvulationDate.IsZero() {
				ovulationDate = window.OvulationDate
				entry.OvulationEstimated = true
			}
		}
		if !ovulationDate.IsZero() {
			entry.OvulationDate = CalendarDay(ovulationDate, location)
		}

		entries = append(entries, entry)
	}
	return entries
}
