package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/models"
)

func cycleHistoryLog(t *testing.T, raw string, ownerID uint, period bool, cycleStart bool) models.DailyLog {
	t.Helper()
	return models.DailyLog{
		UserID:     ownerID,
		Date:       mustParseStatsServiceDay(t, raw),
		IsPeriod:   period,
		CycleStart: cycleStart,
		Flow:       models.FlowMedium,
	}
}

func cycleHistoryBBTLog(t *testing.T, raw string, ownerID uint, bbt float64) models.DailyLog {
	t.Helper()
	return models.DailyLog{
		UserID: ownerID,
		Date:   mustParseStatsServiceDay(t, raw),
		BBT:    &bbt,
	}
}

func cycleHistoryOwner(ownerID uint, trackBBT bool) *models.User {
	return &models.User{
		ID:           ownerID,
		Role:         models.RoleOwner,
		CycleLength:  28,
		PeriodLength: 5,
		TrackBBT:     trackBBT,
	}
}

func TestBuildCycleHistoryListsOneEntryPerCompletedCycleInChronologicalOrder(t *testing.T) {
	ownerID := uint(7)
	logs := []models.DailyLog{
		cycleHistoryLog(t, "2026-01-01", ownerID, true, true),
		cycleHistoryLog(t, "2026-01-29", ownerID, true, true),
		cycleHistoryLog(t, "2026-02-26", ownerID, true, true),
		cycleHistoryLog(t, "2026-03-26", ownerID, true, true),
	}
	service := NewStatsService(&stubStatsDayReader{logsForRange: logs}, &stubStatsSymptomReader{})
	now := mustParseStatsServiceDay(t, "2026-04-01")

	entries, err := service.BuildCycleHistory(context.Background(), cycleHistoryOwner(ownerID, false), now, time.UTC)
	if err != nil {
		t.Fatalf("BuildCycleHistory() unexpected error: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 completed cycles (the running March cycle is open), got %d: %+v", len(entries), entries)
	}

	wantStarts := []string{"2026-01-01", "2026-01-29", "2026-02-26"}
	wantEnds := []string{"2026-01-28", "2026-02-25", "2026-03-25"}
	for index, entry := range entries {
		if got := entry.Start.Format("2006-01-02"); got != wantStarts[index] {
			t.Fatalf("entry %d start = %s, want %s", index, got, wantStarts[index])
		}
		if got := entry.End.Format("2006-01-02"); got != wantEnds[index] {
			t.Fatalf("entry %d end = %s, want %s", index, got, wantEnds[index])
		}
		if entry.Length != 28 {
			t.Fatalf("entry %d length = %d, want 28", index, entry.Length)
		}
	}
}

func TestBuildCycleHistoryLengthMatchesCycleDetectionForTheSameLogs(t *testing.T) {
	ownerID := uint(9)
	logs := []models.DailyLog{
		cycleHistoryLog(t, "2025-11-03", ownerID, true, true),
		cycleHistoryLog(t, "2025-12-02", ownerID, true, true), // 29 days
		cycleHistoryLog(t, "2026-01-01", ownerID, true, true), // 30 days
		cycleHistoryLog(t, "2026-01-29", ownerID, true, true), // 28 days
	}
	service := NewStatsService(&stubStatsDayReader{logsForRange: logs}, &stubStatsSymptomReader{})
	now := mustParseStatsServiceDay(t, "2026-02-10")

	entries, err := service.BuildCycleHistory(context.Background(), cycleHistoryOwner(ownerID, false), now, time.UTC)
	if err != nil {
		t.Fatalf("BuildCycleHistory() unexpected error: %v", err)
	}

	sorted := sortDailyLogs(logs)
	starts := ObservedCycleStarts(sorted)
	detected := buildCycles(starts, sorted)
	if len(entries) != len(detected)-1 {
		t.Fatalf("history has %d entries, cycle detection has %d completed cycles", len(entries), len(detected)-1)
	}
	for index, entry := range entries {
		wantLength := CalendarDaysBetween(starts[index], starts[index+1])
		if entry.Length != wantLength {
			t.Fatalf("entry %d length = %d, cycle detection says %d", index, entry.Length, wantLength)
		}
		wantEnd := CalendarDay(detected[index].End, time.UTC)
		if !entry.End.Equal(wantEnd) {
			t.Fatalf("entry %d end = %s, cycle detection says %s", index, entry.End.Format("2006-01-02"), wantEnd.Format("2006-01-02"))
		}
	}
}

func TestBuildCycleHistoryReturnsAnEmptySliceWhenThereAreNoLogs(t *testing.T) {
	service := NewStatsService(&stubStatsDayReader{}, &stubStatsSymptomReader{})
	now := mustParseStatsServiceDay(t, "2026-04-01")

	entries, err := service.BuildCycleHistory(context.Background(), cycleHistoryOwner(3, false), now, time.UTC)
	if err != nil {
		t.Fatalf("BuildCycleHistory() unexpected error: %v", err)
	}
	if entries == nil {
		t.Fatal("an owner with no logs got a nil history, want an empty slice")
	}
	if len(entries) != 0 {
		t.Fatalf("expected no entries, got %+v", entries)
	}
}

func TestBuildCycleHistoryScopesTheReadToTheSessionOwner(t *testing.T) {
	logsByOwner := map[uint][]models.DailyLog{
		11: {
			cycleHistoryLog(t, "2026-01-01", 11, true, true),
			cycleHistoryLog(t, "2026-01-29", 11, true, true),
		},
		22: nil,
	}
	dayReader := &stubStatsDayReader{logsByOwner: logsByOwner}
	service := NewStatsService(dayReader, &stubStatsSymptomReader{})
	now := mustParseStatsServiceDay(t, "2026-02-10")

	first, err := service.BuildCycleHistory(context.Background(), cycleHistoryOwner(11, false), now, time.UTC)
	if err != nil {
		t.Fatalf("owner 11 history: %v", err)
	}
	if len(first) != 1 || first[0].Start.Format("2006-01-02") != "2026-01-01" {
		t.Fatalf("owner 11 history = %+v, want the first owner's completed cycle", first)
	}

	second, err := service.BuildCycleHistory(context.Background(), cycleHistoryOwner(22, false), now, time.UTC)
	if err != nil {
		t.Fatalf("owner 22 history: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("owner 22 saw %+v — another owner's cycles leaked", second)
	}
	if dayReader.gotRangeOwner != 22 {
		t.Fatalf("the second read used owner id %d, want 22", dayReader.gotRangeOwner)
	}
}

func TestBuildCycleHistoryMarksLengthBasedOvulationAsEstimated(t *testing.T) {
	ownerID := uint(5)
	logs := []models.DailyLog{
		cycleHistoryLog(t, "2026-01-01", ownerID, true, true),
		cycleHistoryLog(t, "2026-01-29", ownerID, true, true),
	}
	service := NewStatsService(&stubStatsDayReader{logsForRange: logs}, &stubStatsSymptomReader{})
	now := mustParseStatsServiceDay(t, "2026-02-10")

	entries, err := service.BuildCycleHistory(context.Background(), cycleHistoryOwner(ownerID, false), now, time.UTC)
	if err != nil {
		t.Fatalf("BuildCycleHistory() unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one completed cycle, got %+v", entries)
	}
	entry := entries[0]
	if !entry.OvulationEstimated || entry.OvulationConfirmed {
		t.Fatalf("ovulation flags = estimated:%v confirmed:%v, want estimated only", entry.OvulationEstimated, entry.OvulationConfirmed)
	}
	// 28-day cycle with the default 14-day luteal phase: ovulation is cycle day
	// 14, 13 calendar days after the start.
	if got := entry.OvulationDate.Format("2006-01-02"); got != "2026-01-14" {
		t.Fatalf("estimated ovulation = %s, want 2026-01-14", got)
	}
}

func TestBuildCycleHistoryMarksBTBShiftAsConfirmed(t *testing.T) {
	ownerID := uint(6)
	logs := []models.DailyLog{
		cycleHistoryLog(t, "2026-01-01", ownerID, true, true),
		cycleHistoryLog(t, "2026-01-29", ownerID, true, true),
	}
	for day := 1; day <= 6; day++ {
		logs = append(logs, cycleHistoryBBTLog(t, mustParseStatsServiceDay(t, "2026-01-01").AddDate(0, 0, day-1).Format("2006-01-02"), ownerID, 36.00))
	}
	for day := 7; day <= 9; day++ {
		logs = append(logs, cycleHistoryBBTLog(t, mustParseStatsServiceDay(t, "2026-01-01").AddDate(0, 0, day-1).Format("2006-01-02"), ownerID, 36.30))
	}
	service := NewStatsService(&stubStatsDayReader{logsForRange: logs}, &stubStatsSymptomReader{})
	now := mustParseStatsServiceDay(t, "2026-02-10")

	entries, err := service.BuildCycleHistory(context.Background(), cycleHistoryOwner(ownerID, true), now, time.UTC)
	if err != nil {
		t.Fatalf("BuildCycleHistory() unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one completed cycle, got %+v", entries)
	}
	entry := entries[0]
	if !entry.OvulationConfirmed || entry.OvulationEstimated {
		t.Fatalf("ovulation flags = estimated:%v confirmed:%v, want confirmed only", entry.OvulationEstimated, entry.OvulationConfirmed)
	}
	// The first elevated day is cycle day 7, so the inferred ovulation day is
	// the day before it.
	if got := entry.OvulationDate.Format("2006-01-02"); got != "2026-01-06" {
		t.Fatalf("confirmed ovulation = %s, want 2026-01-06", got)
	}
}

func TestBuildCycleHistoryPassesThroughRepositoryErrors(t *testing.T) {
	service := NewStatsService(&stubStatsDayReader{rangeErr: errors.New("database down")}, &stubStatsSymptomReader{})
	now := mustParseStatsServiceDay(t, "2026-04-01")

	entries, err := service.BuildCycleHistory(context.Background(), cycleHistoryOwner(3, false), now, time.UTC)
	if err == nil {
		t.Fatal("expected the repository error to be returned")
	}
	if entries != nil {
		t.Fatalf("error path returned entries %+v, want nil", entries)
	}
}
