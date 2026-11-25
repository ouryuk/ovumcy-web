package services

import (
	"time"

	"github.com/ovumcy/ovumcy-web/internal/models"
)

// CyclePhaseTrends is the read-only phase analytics document behind
// `ovumcy trends`: how the owner's symptom occurrences and logged moods
// distribute across the four cycle phases (menstrual / follicular /
// ovulation / luteal), computed from the same completed-cycle phase
// contexts the stats page uses. Unlike the stats page it does not hide the
// cycle count when it is below the pattern threshold — an operator reading
// a trends document wants to see "2 completed cycles, and here is what those
// two cycles recorded" rather than an empty answer — so NEITHER half of the
// breakdown is gated by that threshold.
//
// "No pattern claimed" is the whole claim, and it applies to BOTH halves of
// the document: the symptoms and the moods are counted over whichever
// completed cycles the owner has, so one of the two being reported while the
// other is withheld would read as "there were no symptoms in that phase" —
// a fact the owner knows to be false — instead of "there were too few cycles
// to count them".
type CyclePhaseTrends struct {
	Timezone        string
	CompletedCycles int
	SymptomInsights []StatsPhaseSymptomInsight
	HasSymptomData  bool
	MoodInsights    []StatsPhaseMoodInsight
	HasMoodData     bool
}

// BuildCyclePhaseTrends aggregates the phase analytics for one owner's full
// log history. It is pure: everything it needs arrives as arguments, so the
// CLI can call it with what its repositories already serve — no schema
// change, no write, no secret.
//
// The two halves of the breakdown MUST move together. Gating the symptom half
// alone — which is what the pattern minimum did here, through
// buildPhaseSymptomInsightsWithMap — produced a document that reported a
// phase's average mood while refusing to report the symptoms logged on the
// very same days, and the operator reading it could not tell an empty phase
// from a withheld one. So both halves are computed from the SAME completed
// cycle contexts, under the same rules, with no sample-size gate: a phase
// with nothing in it is reported as empty rather than withheld.
func BuildCyclePhaseTrends(timezone string, logs []models.DailyLog, symptoms []models.SymptomType) CyclePhaseTrends {
	location := resolveOwnerLocation(timezone, time.UTC)

	symptomByID := make(map[uint]models.SymptomType, len(symptoms))
	for _, symptom := range symptoms {
		symptomByID[symptom.ID] = symptom
	}

	trends := CyclePhaseTrends{Timezone: timezone}
	if location == nil {
		location = time.UTC
	}

	contexts := buildCompletedCyclePhaseContexts(logs, location)
	trends.CompletedCycles = len(contexts)
	if len(contexts) == 0 {
		return trends
	}

	symptomInsights, hasSymptomData := buildPhaseSymptomInsightsFromCycles(logs, contexts, location, symptomByID)
	trends.SymptomInsights = symptomInsights
	trends.HasSymptomData = hasSymptomData

	moodInsights, hasMoodData := buildPhaseMoodInsightsFromCycles(logs, contexts, location)
	trends.MoodInsights = moodInsights
	trends.HasMoodData = hasMoodData

	return trends
}
