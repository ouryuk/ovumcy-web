package services

import (
	"testing"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/models"
)

// `ovumcy trends` is the operator-facing phase-analytics document. Unlike the
// stats page it reports the completed-cycle count beside whatever it could
// measure, so a reader sees "2 completed cycles" instead of an empty answer —
// and therefore BOTH halves of the document (the symptoms and the moods) have
// to be counted over whatever cycles the owner has.
//
// The symptom half used to keep the stats page's pattern minimum
// (minimumPhaseInsightCycles = 3) while the mood half did not: the mood
// counting was split out of BuildPhaseMoodInsights for exactly that reason,
// and the symptom counting was not. The document therefore reported the mood
// averages of a two-cycle account and withheld its symptoms, which reads as
// "there were no symptoms in that phase" — a fact the owner knows to be false
// — rather than "there were too few cycles to count them".
//
// The two helpers below build the same history at two cycle counts so the
// difference between the stats page's gate and the trends document's is
// visible from the tests alone.

// trendsCycleLogs builds the logs of `cycleCount` completed cycles plus the
// start of the next one, so buildCompletedCyclePhaseContexts observes
// exactly `cycleCount` completed cycles. Every cycle carries one symptom
// occurrence and one mood reading on its second day — cycle day 2, inside the
// five-day period, so both land in the menstrual phase and the assertions
// below can name it.
func trendsCycleLogs(t *testing.T, cycleCount int) []models.DailyLog {
	t.Helper()

	starts := []string{"2026-01-01", "2026-01-29", "2026-02-26", "2026-03-26"}
	if cycleCount < 1 || cycleCount > len(starts)-1 {
		t.Fatalf("cycle count %d is out of the range this fixture supports", cycleCount)
	}

	logs := []models.DailyLog{}
	for _, start := range starts[:cycleCount+1] {
		startDay := mustParseStatsServiceDay(t, start)
		logs = append(logs, models.DailyLog{Date: startDay, IsPeriod: true, CycleStart: true})
		logs = append(logs, models.DailyLog{Date: startDay.AddDate(0, 0, 1), IsPeriod: true, Mood: 3, SymptomIDs: []uint{1}})
	}
	return logs
}

func trendsSymptoms() []models.SymptomType {
	return []models.SymptomType{{ID: 1, Name: "Headache", Icon: "H"}}
}

// TestBuildCyclePhaseTrendsReportsSymptomsBelowThePatternMinimum is the
// regression: a trends document for an account with fewer than three
// completed cycles must report the symptoms it can count, exactly as it
// reports the moods it can count.
func TestBuildCyclePhaseTrendsReportsSymptomsBelowThePatternMinimum(t *testing.T) {
	trends := BuildCyclePhaseTrends("UTC", trendsCycleLogs(t, 2), trendsSymptoms())

	if trends.CompletedCycles != 2 {
		t.Fatalf("expected 2 completed cycles, got %d", trends.CompletedCycles)
	}
	if !trends.HasMoodData {
		t.Fatal("expected mood data below the pattern minimum: the mood half has never been gated on it")
	}
	if !trends.HasSymptomData {
		t.Fatal("expected symptom data below the pattern minimum: the document reported moods for the same cycles it withheld symptoms for, which reads as 'no symptoms in that phase' rather than 'too few cycles to count them'")
	}

	menstrual := trends.SymptomInsights[0]
	if menstrual.Phase != "menstrual" {
		t.Fatalf("expected the menstrual phase first, got %q", menstrual.Phase)
	}
	if !menstrual.HasData || len(menstrual.Items) != 1 {
		t.Fatalf("expected the menstrual phase to carry its symptom item, got %#v", menstrual)
	}
	if menstrual.Items[0].Name != "Headache" || menstrual.Items[0].Count != 2 {
		t.Fatalf("expected two headache occurrences in the menstrual phase, got %#v", menstrual.Items[0])
	}
}

// TestBuildCyclePhaseTrendsAgreesWithTheStatsPageAboveThePatternMinimum
// pins the other half of the contract: from the pattern minimum upward the
// two surfaces must agree, because they share the counting. The trends
// document is not a second, looser implementation of the phase analytics.
func TestBuildCyclePhaseTrendsAgreesWithTheStatsPageAboveThePatternMinimum(t *testing.T) {
	logs := trendsCycleLogs(t, minimumPhaseInsightCycles)
	symptoms := trendsSymptoms()

	symptomByID := map[uint]models.SymptomType{}
	for _, symptom := range symptoms {
		symptomByID[symptom.ID] = symptom
	}

	statsInsights, statsOK := buildPhaseSymptomInsightsWithMap(logs, time.UTC, symptomByID)
	if !statsOK {
		t.Fatalf("expected the stats page's phase symptom insights at %d completed cycles", minimumPhaseInsightCycles)
	}

	trends := BuildCyclePhaseTrends("UTC", logs, symptoms)
	if !trends.HasSymptomData {
		t.Fatal("expected symptom data at the pattern minimum")
	}
	if len(trends.SymptomInsights) != len(statsInsights) {
		t.Fatalf("expected %d phase symptom insights, got %d", len(statsInsights), len(trends.SymptomInsights))
	}
	for index, insight := range trends.SymptomInsights {
		expected := statsInsights[index]
		if insight.Phase != expected.Phase || insight.TotalDays != expected.TotalDays || insight.HasData != expected.HasData {
			t.Fatalf("phase %q: trends and the stats page disagree: trends %#v, stats %#v", expected.Phase, insight, expected)
		}
		if len(insight.Items) != len(expected.Items) {
			t.Fatalf("phase %q: expected %d symptom items, got %d", expected.Phase, len(expected.Items), len(insight.Items))
		}
		for itemIndex, item := range insight.Items {
			if item.Name != expected.Items[itemIndex].Name || item.Count != expected.Items[itemIndex].Count {
				t.Fatalf("phase %q: symptom item %d: trends %#v, stats %#v", expected.Phase, itemIndex, item, expected.Items[itemIndex])
			}
		}
	}
}

// TestBuildCyclePhaseTrendsWithoutSymptomsReportsNoSymptomData keeps the two
// halves of the document honest about a second case: an owner with no
// symptoms at all has no symptom data to report, whatever the cycle count.
func TestBuildCyclePhaseTrendsWithoutSymptomsReportsNoSymptomData(t *testing.T) {
	trends := BuildCyclePhaseTrends("UTC", trendsCycleLogs(t, 2), nil)

	if trends.CompletedCycles != 2 {
		t.Fatalf("expected 2 completed cycles, got %d", trends.CompletedCycles)
	}
	if trends.HasSymptomData {
		t.Fatal("expected no symptom data for an account with no symptoms")
	}
	if len(trends.SymptomInsights) != 0 {
		t.Fatalf("expected no phase symptom insights, got %#v", trends.SymptomInsights)
	}
	if !trends.HasMoodData {
		t.Fatal("expected mood data for an account with no symptoms: the two halves are independent")
	}
}
