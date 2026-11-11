package models

import (
	"testing"
	"time"
)

// TestDefaultBuiltinSymptomsReturnsCopy guards the copy-on-read contract: the
// exported slice is handed out by value and mutated freely by callers, so a
// later call must still observe the pristine built-ins.
func TestDefaultBuiltinSymptomsReturnsCopy(t *testing.T) {
	first := DefaultBuiltinSymptoms()
	if len(first) == 0 {
		t.Fatal("DefaultBuiltinSymptoms returned an empty slice")
	}

	originalKey := first[0].Key
	originalName := first[0].Name
	first[0].Key = "mutated"
	first[0].Name = "Mutated"
	first = append(first, BuiltinSymptom{Key: "extra"})

	second := DefaultBuiltinSymptoms()
	if len(second) != len(defaultBuiltinSymptoms) {
		t.Fatalf("later call length = %d, want %d (slice append leaked into the package default)", len(second), len(defaultBuiltinSymptoms))
	}
	if second[0].Key != originalKey || second[0].Name != originalName {
		t.Fatalf("later call first entry = {%q, %q}, want {%q, %q} (mutation leaked through the returned slice)", second[0].Key, second[0].Name, originalKey, originalName)
	}
}

// TestDefaultBuiltinSymptomsEntries asserts every built-in carries the fields
// the symptom picker and i18n layer rely on, and that keys are unique so a key
// can identify exactly one entry.
func TestDefaultBuiltinSymptomsEntries(t *testing.T) {
	symptoms := DefaultBuiltinSymptoms()
	if len(symptoms) == 0 {
		t.Fatal("DefaultBuiltinSymptoms returned an empty slice")
	}

	cases := []struct {
		name string
		get  func(BuiltinSymptom) string
	}{
		{name: "Key", get: func(s BuiltinSymptom) string { return s.Key }},
		{name: "Name", get: func(s BuiltinSymptom) string { return s.Name }},
		{name: "TranslationKey", get: func(s BuiltinSymptom) string { return s.TranslationKey }},
		{name: "Icon", get: func(s BuiltinSymptom) string { return s.Icon }},
		{name: "Color", get: func(s BuiltinSymptom) string { return s.Color }},
	}

	for _, tc := range cases {
		t.Run(tc.name+" non-empty", func(t *testing.T) {
			for _, symptom := range symptoms {
				if tc.get(symptom) == "" {
					t.Errorf("built-in symptom %q has empty %s", symptom.Key, tc.name)
				}
			}
		})
	}

	t.Run("Keys unique", func(t *testing.T) {
		seen := make(map[string]struct{}, len(symptoms))
		for _, symptom := range symptoms {
			if _, ok := seen[symptom.Key]; ok {
				t.Errorf("duplicate built-in symptom key %q", symptom.Key)
			}
			seen[symptom.Key] = struct{}{}
		}
	})
}

// TestSymptomTypeIsActive checks the archived-at sentinel that keeps archived
// symptoms out of the active picker while preserving their history.
func TestSymptomTypeIsActive(t *testing.T) {
	archivedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	cases := []struct {
		name     string
		archived *time.Time
		want     bool
	}{
		{name: "nil ArchivedAt is active", archived: nil, want: true},
		{name: "set ArchivedAt is inactive", archived: &archivedAt, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			symptom := SymptomType{ArchivedAt: tc.archived}
			if got := symptom.IsActive(); got != tc.want {
				t.Fatalf("IsActive() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestTableNameMappings pins every model whose table name GORM would otherwise
// pluralize incorrectly; these strings are referenced by migrations and
// repositories, so a rename would silently break lookups.
func TestTableNameMappings(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{name: "AppState", got: AppState{}.TableName(), want: "app_state"},
		{name: "OIDCIdentity", got: OIDCIdentity{}.TableName(), want: "oidc_identities"},
		{name: "OIDCLogoutState", got: OIDCLogoutState{}.TableName(), want: "oidc_logout_states"},
		{name: "RegisterPickupToken", got: RegisterPickupToken{}.TableName(), want: "register_pickup_tokens"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("TableName() = %q, want %q", tc.got, tc.want)
			}
		})
	}
}

// TestCycleFactorConstants pins the wire/storage values; they are written into
// daily-log JSON and read back by reports, so the literal strings are contract.
func TestCycleFactorConstants(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{name: "CycleFactorStress", got: CycleFactorStress, want: "stress"},
		{name: "CycleFactorIllness", got: CycleFactorIllness, want: "illness"},
		{name: "CycleFactorTravel", got: CycleFactorTravel, want: "travel"},
		{name: "CycleFactorSleepDisruption", got: CycleFactorSleepDisruption, want: "sleep_disruption"},
		{name: "CycleFactorMedicationChange", got: CycleFactorMedicationChange, want: "medication_change"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("%s = %q, want %q", tc.name, tc.got, tc.want)
			}
		})
	}
}

// TestDailyLogBeforeSave exercises the GORM hook's pure normalisation contract
// (it never reads the passed *gorm.DB, so a nil handle is sufficient): a nil
// CycleFactorKeys slice becomes an allocated empty slice so JSON serialisation
// writes [] rather than null, and a non-zero Date is truncated to a UTC
// midnight while a zero Date is left untouched for the caller to fill.
func TestDailyLogBeforeSave(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("load Europe/Berlin: %v", err)
	}

	cases := []struct {
		name        string
		date        time.Time
		factors     []string
		wantDate    time.Time
		wantFactors []string
	}{
		{
			name:        "nil factors become empty non-nil slice",
			date:        time.Date(2026, 5, 17, 14, 35, 12, 987, time.UTC),
			factors:     nil,
			wantDate:    time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC),
			wantFactors: []string{},
		},
		{
			name:        "existing factors are preserved",
			date:        time.Date(2026, 5, 17, 14, 35, 12, 0, time.UTC),
			factors:     []string{CycleFactorStress, CycleFactorTravel},
			wantDate:    time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC),
			wantFactors: []string{CycleFactorStress, CycleFactorTravel},
		},
		{
			name:        "non-UTC timestamp truncates in its own day",
			date:        time.Date(2026, 5, 17, 23, 59, 59, 0, berlin),
			factors:     []string{},
			wantDate:    time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC),
			wantFactors: []string{},
		},
		{
			name:        "zero date is left untouched",
			date:        time.Time{},
			factors:     nil,
			wantDate:    time.Time{},
			wantFactors: []string{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := DailyLog{Date: tc.date, CycleFactorKeys: tc.factors}
			if err := entry.BeforeSave(nil); err != nil {
				t.Fatalf("BeforeSave returned error: %v", err)
			}
			if !entry.Date.Equal(tc.wantDate) {
				t.Fatalf("Date = %s, want %s", entry.Date.Format(time.RFC3339Nano), tc.wantDate.Format(time.RFC3339Nano))
			}
			if entry.Date.Location() != tc.wantDate.Location() {
				t.Fatalf("Date location = %s, want %s", entry.Date.Location(), tc.wantDate.Location())
			}
			if entry.CycleFactorKeys == nil {
				t.Fatal("CycleFactorKeys is nil, want non-nil slice")
			}
			if len(entry.CycleFactorKeys) != len(tc.wantFactors) {
				t.Fatalf("CycleFactorKeys = %v, want %v", entry.CycleFactorKeys, tc.wantFactors)
			}
			for i := range tc.wantFactors {
				if entry.CycleFactorKeys[i] != tc.wantFactors[i] {
					t.Fatalf("CycleFactorKeys = %v, want %v", entry.CycleFactorKeys, tc.wantFactors)
				}
			}
		})
	}
}
