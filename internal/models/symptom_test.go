package models

import (
	"testing"
	"time"
)

func TestDefaultBuiltinSymptomsReturnsACopy(t *testing.T) {
	t.Parallel()

	first := DefaultBuiltinSymptoms()
	if len(first) == 0 {
		t.Fatal("DefaultBuiltinSymptoms() returned an empty slice")
	}

	first[0].Name = "Mutated"
	first[0].Color = "#000000"
	first[1] = BuiltinSymptom{Key: "mutated"}

	second := DefaultBuiltinSymptoms()
	if len(second) != len(defaultBuiltinSymptoms) {
		t.Fatalf("DefaultBuiltinSymptoms() length = %d, want %d", len(second), len(defaultBuiltinSymptoms))
	}
	for index := range defaultBuiltinSymptoms {
		if second[index] != defaultBuiltinSymptoms[index] {
			t.Fatalf("DefaultBuiltinSymptoms()[%d] = %#v, want %#v", index, second[index], defaultBuiltinSymptoms[index])
		}
	}
}

func TestDefaultBuiltinSymptomsIndependentSlices(t *testing.T) {
	t.Parallel()

	first := DefaultBuiltinSymptoms()
	second := DefaultBuiltinSymptoms()

	if len(first) == 0 || len(second) == 0 {
		t.Fatal("DefaultBuiltinSymptoms() returned an empty slice")
	}

	// Distinct backing arrays: appending to one cannot be observed by the other.
	first = append(first, BuiltinSymptom{Key: "extra"})
	if len(second) != len(defaultBuiltinSymptoms) {
		t.Fatalf("second length = %d, want %d after appending to first", len(second), len(defaultBuiltinSymptoms))
	}
}

func TestSymptomTypeIsActive(t *testing.T) {
	t.Parallel()

	archivedAt := time.Date(2026, time.November, 6, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		archivedAt *time.Time
		want       bool
	}{
		{name: "nil archived at is active", archivedAt: nil, want: true},
		{name: "set archived at is inactive", archivedAt: &archivedAt, want: false},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			symptom := SymptomType{ArchivedAt: testCase.archivedAt}
			if got := symptom.IsActive(); got != testCase.want {
				t.Fatalf("IsActive() = %v, want %v", got, testCase.want)
			}
		})
	}
}
