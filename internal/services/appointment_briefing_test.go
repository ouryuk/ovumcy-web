package services

import (
	"context"
	"errors"
	"github.com/ovumcy/ovumcy-web/internal/models"
	"testing"
	"time"
)

// briefingDay parses a calendar day for the briefing tests.
func briefingDay(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.ParseInLocation("2006-01-02", value, time.UTC)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return parsed
}

func TestBuildBriefingSummaryCountsDistinctLoggedDaysPerSymptom(t *testing.T) {
	days := &stubExportDayReader{logs: []models.DailyLog{
		// Cramps on two days, custom on one, an unknown id ignored, and a
		// built-in repeated inside one day's entry list counted once.
		{Date: briefingDay(t, "2026-05-01"), SymptomIDs: []uint{1, 2}},
		{Date: briefingDay(t, "2026-05-02"), SymptomIDs: []uint{1, 1, 99}},
		{Date: briefingDay(t, "2026-05-03"), SymptomIDs: []uint{}},
	}}
	symptoms := &stubExportSymptomReader{symptoms: []models.SymptomType{
		{ID: 1, Name: "Cramps"},
		{ID: 2, Name: "Custom Symptom"},
	}}
	service := NewExportService(days, symptoms)

	summary, frequency, err := service.BuildBriefingSummary(context.Background(), 1, nil, nil, time.UTC)
	if err != nil {
		t.Fatalf("BuildBriefingSummary() unexpected error: %v", err)
	}
	if !summary.HasData || summary.TotalEntries != 3 || summary.DateFrom != "2026-05-01" || summary.DateTo != "2026-05-03" {
		t.Fatalf("summary = %+v, want 3 entries over 2026-05-01..2026-05-03", summary)
	}

	want := []BriefingSymptomCount{{Name: "cramps", Count: 2}, {Name: "Custom Symptom", Count: 1}}
	if len(frequency) != len(want) {
		t.Fatalf("frequency = %+v, want %+v", frequency, want)
	}
	for index, entry := range want {
		if frequency[index] != entry {
			t.Fatalf("frequency[%d] = %+v, want %+v", index, frequency[index], entry)
		}
	}
}

func TestBuildBriefingSummaryOrdersByCountThenName(t *testing.T) {
	days := &stubExportDayReader{logs: []models.DailyLog{
		{Date: briefingDay(t, "2026-05-01"), SymptomIDs: []uint{1, 2, 3}},
		{Date: briefingDay(t, "2026-05-02"), SymptomIDs: []uint{2, 3}},
		{Date: briefingDay(t, "2026-05-03"), SymptomIDs: []uint{3}},
	}}
	symptoms := &stubExportSymptomReader{symptoms: []models.SymptomType{
		{ID: 1, Name: "Acne"},
		{ID: 2, Name: "Zeta Custom"},
		{ID: 3, Name: "Cramps"},
	}}
	service := NewExportService(days, symptoms)

	_, frequency, err := service.BuildBriefingSummary(context.Background(), 1, nil, nil, time.UTC)
	if err != nil {
		t.Fatalf("BuildBriefingSummary() unexpected error: %v", err)
	}

	want := []BriefingSymptomCount{
		{Name: "cramps", Count: 3},
		{Name: "Zeta Custom", Count: 2},
		{Name: "acne", Count: 1},
	}
	for index, entry := range want {
		if frequency[index] != entry {
			t.Fatalf("frequency[%d] = %+v, want %+v", index, frequency[index], entry)
		}
	}
}

func TestBuildBriefingSummaryPropagatesTheReadFailure(t *testing.T) {
	readFailed := errors.New("read failed")
	days := &stubExportDayReader{err: readFailed}
	symptoms := &stubExportSymptomReader{}
	service := NewExportService(days, symptoms)

	if _, _, err := service.BuildBriefingSummary(context.Background(), 1, nil, nil, time.UTC); !errors.Is(err, readFailed) {
		t.Fatalf("BuildBriefingSummary() error = %v, want the read failure", err)
	}
}

func TestFilterBriefingCyclesKeepsCyclesOverlappingTheRange(t *testing.T) {
	entries := []CycleHistoryEntry{
		{Start: briefingDay(t, "2026-04-01"), End: briefingDay(t, "2026-04-28")}, // ends before the range
		{Start: briefingDay(t, "2026-04-29"), End: briefingDay(t, "2026-05-26")}, // straddles `from`
		{Start: briefingDay(t, "2026-05-27"), End: briefingDay(t, "2026-06-23")}, // straddles `to`
		{Start: briefingDay(t, "2026-06-24"), End: briefingDay(t, "2026-07-21")}, // starts after the range
		{Start: briefingDay(t, "2026-07-01"), End: briefingDay(t, "2026-07-28")}, // inside the range
	}

	from := briefingDay(t, "2026-05-01")
	to := briefingDay(t, "2026-06-30")
	got := FilterBriefingCycles(entries, &from, &to, time.UTC)

	if len(got) != 3 {
		t.Fatalf("filtered = %d entries, want the three cycles touching May or June: %+v", len(got), got)
	}
	if got[0].Start.Format("2006-01-02") != "2026-04-29" || got[2].Start.Format("2006-01-02") != "2026-07-01" {
		t.Fatalf("filtered kept the wrong cycles: %+v", got)
	}
}

func TestFilterBriefingCyclesWithNoBoundsKeepsEverything(t *testing.T) {
	entries := []CycleHistoryEntry{
		{Start: briefingDay(t, "2026-01-01"), End: briefingDay(t, "2026-01-28")},
		{Start: briefingDay(t, "2026-01-29"), End: briefingDay(t, "2026-02-25")},
	}
	got := FilterBriefingCycles(entries, nil, nil, time.UTC)
	if len(got) != 2 {
		t.Fatalf("unbounded filter = %d entries, want all of them", len(got))
	}

	if empty := FilterBriefingCycles(nil, nil, nil, time.UTC); len(empty) != 0 || empty == nil {
		t.Fatalf("filter over no entries = %+v, want an empty non-nil slice", empty)
	}
}
