package services

import (
	"testing"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/models"
)

// The `ovumcy trends` document (`services.BuildCyclePhaseTrends`) is the ONE
// phase-analytics surface that does not gate on the pattern minimum
// (minimumPhaseInsightCycles): the operator reading it wants to see what the
// owner's completed cycles actually recorded, not an empty answer because the
// sample was small.
//
// That made the two halves of the breakdown — symptom counts and mood
// averages — two INDEPENDENT decisions, and only the mood half was ungated.
// The symptom half still rode the pattern minimum, so an owner with two
// completed cycles got a document that reported each phase's average mood
// while refusing to report the symptoms logged on the very same days. An
// operator could not tell an EMPTY phase from a WITHHELD one, which is the
// whole thing the trends surface exists to avoid.
//
// The tests below pin the two halves together at the boundary the pattern
// minimum would have split them at.

const (
	// cycletrendsSymptomID is the one symptom id the fixtures log.
	cycletrendsSymptomID = uint(7)
	// cycletrendsOtherSymptomID is a SECOND symptom id the fixtures log, so a
	// test can tell "the symptom half counted" from "the symptom half reported
	// the one symptom it happened to know about".
	cycletrendsOtherSymptomID = uint(9)
)

// cycletrendsDay parses a "2006-01-02" date in UTC and fatals on error.
func cycletrendsDay(t *testing.T, raw string) time.Time {
	t.Helper()
	day, err := time.ParseInLocation("2006-01-02", raw, time.UTC)
	if err != nil {
		t.Fatalf("cycletrendsDay: parse %q: %v", raw, err)
	}
	return day
}

// cycletrendsLogs builds one owner's history from the given cycle starts.
// Each cycle carries five period days starting on its start date, a logged
// mood on every period day, and one logged symptom on the second period day.
//
// The symptom lands on period day 2, which is inside the MENSTRUAL phase of
// every cycle, so the menstrual phase is the one carrying both halves of the
// breakdown — a symptom count and a mood average over the same days.
func cycletrendsLogs(t *testing.T, starts ...string) []models.DailyLog {
	t.Helper()

	logs := make([]models.DailyLog, 0, len(starts)*5)
	for _, start := range starts {
		startDay := cycletrendsDay(t, start)
		for offset := range 5 {
			day := startDay.AddDate(0, 0, offset)
			entry := models.DailyLog{
				Date:       day,
				IsPeriod:   true,
				CycleStart: offset == 0,
				Mood:       3,
			}
			if offset == 1 {
				entry.SymptomIDs = []uint{cycletrendsSymptomID, cycletrendsOtherSymptomID}
			}
			logs = append(logs, entry)
		}
	}
	return logs
}

// cycletrendsSymptoms is the symptom vocabulary the fixture logs against.
func cycletrendsSymptoms() []models.SymptomType {
	return []models.SymptomType{
		{ID: cycletrendsSymptomID, Name: "Cramps", Icon: "cramps"},
		{ID: cycletrendsOtherSymptomID, Name: "Headache", Icon: "headache"},
	}
}

// cycletrendsInsightByPhase finds the insight for one phase.
func cycletrendsInsightByPhase(t *testing.T, insights []StatsPhaseSymptomInsight, phase string) StatsPhaseSymptomInsight {
	t.Helper()
	for _, insight := range insights {
		if insight.Phase == phase {
			return insight
		}
	}
	t.Fatalf("phase %q missing from the symptom insights: %+v", phase, insights)
	return StatsPhaseSymptomInsight{}
}

// cycletrendsMoodByPhase finds the mood insight for one phase.
func cycletrendsMoodByPhase(t *testing.T, insights []StatsPhaseMoodInsight, phase string) StatsPhaseMoodInsight {
	t.Helper()
	for _, insight := range insights {
		if insight.Phase == phase {
			return insight
		}
	}
	t.Fatalf("phase %q missing from the mood insights: %+v", phase, insights)
	return StatsPhaseMoodInsight{}
}

// TestBuildCyclePhaseTrendsReportsBothHalvesBelowThePatternMinimum pins the
// fix: at TWO completed cycles — one below the pattern minimum of three — the
// symptom half and the mood half must BOTH report. Before the fix the mood
// half reported and the symptom half was withheld by the pattern minimum, so
// the document claimed a phase's average mood while refusing the symptoms
// logged on the same days.
func TestBuildCyclePhaseTrendsReportsBothHalvesBelowThePatternMinimum(t *testing.T) {
	t.Parallel()

	// Three starts → two completed cycles: one below the pattern minimum.
	logs := cycletrendsLogs(t, "2026-01-01", "2026-01-29", "2026-02-26")

	trends := BuildCyclePhaseTrends("UTC", logs, cycletrendsSymptoms())

	if trends.CompletedCycles != 2 {
		t.Fatalf("expected 2 completed cycles, got %d", trends.CompletedCycles)
	}
	if trends.CompletedCycles >= minimumPhaseInsightCycles {
		t.Fatalf("this fixture must sit BELOW the pattern minimum, but it has %d completed cycles", trends.CompletedCycles)
	}

	if !trends.HasMoodData {
		t.Fatal("the mood half was withheld below the pattern minimum: the trends surface does not gate on it")
	}
	if !trends.HasSymptomData {
		t.Fatal("the symptom half was withheld below the pattern minimum while the mood half was reported: the two halves of one breakdown must move together")
	}

	menstrual := cycletrendsInsightByPhase(t, trends.SymptomInsights, "menstrual")
	if !menstrual.HasData {
		t.Fatal("the menstrual phase reported no symptom data although the fixture logged a symptom on every cycle's second period day")
	}
	if menstrual.TotalDays != 10 {
		t.Fatalf("expected 10 menstrual days across two cycles, got %d", menstrual.TotalDays)
	}
	if len(menstrual.Items) != 2 {
		t.Fatalf("expected both fixture symptoms to be counted, got %+v", menstrual.Items)
	}
	for _, item := range menstrual.Items {
		if item.Count != 2 {
			t.Fatalf("expected symptom %q logged once per cycle (2), got %d", item.Name, item.Count)
		}
		if item.TotalDays != 10 {
			t.Fatalf("expected symptom %q measured over 10 menstrual days, got %d", item.Name, item.TotalDays)
		}
	}

	// The mood half must have reported the SAME phase over the SAME days —
	// otherwise the document still describes two different histories.
	mood := cycletrendsMoodByPhase(t, trends.MoodInsights, "menstrual")
	if !mood.HasData {
		t.Fatal("the menstrual phase reported no mood data although the fixture logged a mood on every period day")
	}
	if mood.EntryCount != 10 {
		t.Fatalf("expected 10 menstrual mood entries across two cycles, got %d", mood.EntryCount)
	}
}

// TestBuildCyclePhaseTrendsReportsBothHalvesAtThePatternMinimum is the
// boundary's other side: at exactly the pattern minimum both halves report,
// and they report the SAME numbers they report below it. A gate that moved
// with the minimum would show up here as a difference between the two
// boundaries.
func TestBuildCyclePhaseTrendsReportsBothHalvesAtThePatternMinimum(t *testing.T) {
	t.Parallel()

	// Four starts → three completed cycles: exactly the pattern minimum.
	logs := cycletrendsLogs(t, "2026-01-01", "2026-01-29", "2026-02-26", "2026-03-26")

	trends := BuildCyclePhaseTrends("UTC", logs, cycletrendsSymptoms())

	if trends.CompletedCycles != minimumPhaseInsightCycles {
		t.Fatalf("expected %d completed cycles, got %d", minimumPhaseInsightCycles, trends.CompletedCycles)
	}
	if !trends.HasSymptomData || !trends.HasMoodData {
		t.Fatalf("expected both halves to report at the pattern minimum, got symptom=%v mood=%v", trends.HasSymptomData, trends.HasMoodData)
	}

	menstrual := cycletrendsInsightByPhase(t, trends.SymptomInsights, "menstrual")
	if menstrual.TotalDays != 15 {
		t.Fatalf("expected 15 menstrual days across three cycles, got %d", menstrual.TotalDays)
	}
	for _, item := range menstrual.Items {
		if item.Count != 3 {
			t.Fatalf("expected symptom %q logged once per cycle (3), got %d", item.Name, item.Count)
		}
	}
}

// TestBuildCyclePhaseTrendsHalvesAgreeOnWhichPhasesCarryData is the
// invariant behind both tests above, stated on its own: the set of phases the
// symptom half reports and the set the mood half reports must be the SAME.
//
// The two halves read the same completed-cycle contexts, so a phase can only
// carry data in one of them by accident — a symptom counted on a day the mood
// half skipped, or a mood logged on a day the symptom half skipped. Such a
// split is what made the pre-fix document self-contradictory: "average mood
// 3.0 over 10 days" beside "no symptoms recorded".
//
// The day COUNTS are deliberately not compared here: the symptom half counts
// every logged day in the phase while the mood half counts only the days that
// carry a valid mood, so the two differ by design on a day that has symptoms
// but no mood.
func TestBuildCyclePhaseTrendsHalvesAgreeOnWhichPhasesCarryData(t *testing.T) {
	t.Parallel()

	for _, starts := range [][]string{
		{"2026-01-01", "2026-01-29"},
		{"2026-01-01", "2026-01-29", "2026-02-26"},
		{"2026-01-01", "2026-01-29", "2026-02-26", "2026-03-26"},
	} {
		logs := cycletrendsLogs(t, starts...)
		trends := BuildCyclePhaseTrends("UTC", logs, cycletrendsSymptoms())

		if len(trends.SymptomInsights) != len(trends.MoodInsights) {
			t.Fatalf("starts %v: the two halves reported %d symptom insights and %d mood insights", starts, len(trends.SymptomInsights), len(trends.MoodInsights))
		}
		for index := range trends.SymptomInsights {
			symptomInsight := trends.SymptomInsights[index]
			moodInsight := trends.MoodInsights[index]
			if symptomInsight.Phase != moodInsight.Phase {
				t.Fatalf("starts %v: the halves disagree on the phase order: %q vs %q", starts, symptomInsight.Phase, moodInsight.Phase)
			}
			if symptomInsight.HasData != moodInsight.HasData {
				t.Fatalf("starts %v: phase %q carries data in one half and not the other (symptom=%v mood=%v)", starts, symptomInsight.Phase, symptomInsight.HasData, moodInsight.HasData)
			}
			// Every phase both halves name must be one of the four the
			// breakdown is defined over — a phase name invented by one half
			// would leave the other half's entry dangling.
			switch symptomInsight.Phase {
			case "menstrual", "follicular", "ovulation", "luteal":
			default:
				t.Fatalf("starts %v: unexpected phase %q in the breakdown", starts, symptomInsight.Phase)
			}
		}
	}
}

// TestBuildCyclePhaseTrendsWithoutSymptomsReportsNoSymptomData pins the
// remaining edge: an owner whose vocabulary holds no symptoms at all (or one
// the fixture logged but the vocabulary does not know) gets no symptom data,
// and — importantly — the mood half still reports. The symptom half refuses
// only when it has nothing to count, never because of a cycle count.
func TestBuildCyclePhaseTrendsWithoutSymptomsReportsNoSymptomData(t *testing.T) {
	t.Parallel()

	logs := cycletrendsLogs(t, "2026-01-01", "2026-01-29", "2026-02-26")

	trends := BuildCyclePhaseTrends("UTC", logs, nil)

	if trends.HasSymptomData {
		t.Fatal("the symptom half reported data for an owner with no symptom vocabulary")
	}
	if len(trends.SymptomInsights) != 0 {
		t.Fatalf("expected no symptom insights for an owner with no symptom vocabulary, got %+v", trends.SymptomInsights)
	}
	if !trends.HasMoodData {
		t.Fatal("the mood half was withheld although the fixture logged moods: only the symptom half may refuse, and only for want of symptoms")
	}
}

// TestBuildCyclePhaseTrendsUnknownSymptomIDsAreNotCounted pins that the
// symptom half counts only ids the owner's vocabulary knows — the rule
// uniqueKnownSymptomIDs exists for, and the one the stats page shares. An
// unknown id must not become a symptom row with an empty name.
func TestBuildCyclePhaseTrendsUnknownSymptomIDsAreNotCounted(t *testing.T) {
	t.Parallel()

	logs := cycletrendsLogs(t, "2026-01-01", "2026-01-29", "2026-02-26")
	// The fixture logs ids 7 and 9; the owner only knows id 7.
	trends := BuildCyclePhaseTrends("UTC", logs, []models.SymptomType{
		{ID: cycletrendsSymptomID, Name: "Cramps", Icon: "cramps"},
	})

	menstrual := cycletrendsInsightByPhase(t, trends.SymptomInsights, "menstrual")
	if len(menstrual.Items) != 1 {
		t.Fatalf("expected only the known symptom to be counted, got %+v", menstrual.Items)
	}
	if menstrual.Items[0].Name != "Cramps" {
		t.Fatalf("expected the known symptom by name, got %q", menstrual.Items[0].Name)
	}
}
