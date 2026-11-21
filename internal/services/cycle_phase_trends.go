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
// a trends document wants to see "2 completed cycles, no pattern claimed"
// rather than an empty answer.
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

	symptomInsights, hasSymptomData := buildPhaseSymptomInsightsWithMap(logs, location, symptomByID)
	trends.SymptomInsights = symptomInsights
	trends.HasSymptomData = hasSymptomData

	moodInsights, hasMoodData := buildPhaseMoodInsightsFromCycles(logs, contexts, location)
	trends.MoodInsights = moodInsights
	trends.HasMoodData = hasMoodData

	return trends
}
