package models

import (
	"reflect"
	"testing"
	"time"
)

func TestDailyLogBeforeSave(t *testing.T) {
	t.Parallel()

	zone := time.FixedZone("test-zone", 5*60*60)

	tests := []struct {
		name            string
		date            time.Time
		cycleFactorKeys []string
		wantDate        time.Time
	}{
		{
			name:            "zero date is left untouched",
			date:            time.Time{},
			cycleFactorKeys: []string{"stress"},
			wantDate:        time.Time{},
		},
		{
			name:            "utc midnight keeps its date",
			date:            time.Date(2026, time.November, 6, 0, 0, 0, 0, time.UTC),
			cycleFactorKeys: []string{"stress"},
			wantDate:        time.Date(2026, time.November, 6, 0, 0, 0, 0, time.UTC),
		},
		{
			name:            "a daytime utc timestamp truncates to midnight",
			date:            time.Date(2026, time.November, 6, 14, 37, 41, 123, time.UTC),
			cycleFactorKeys: []string{"stress"},
			wantDate:        time.Date(2026, time.November, 6, 0, 0, 0, 0, time.UTC),
		},
		{
			name:            "a non-utc timestamp truncates its local date to utc midnight",
			date:            time.Date(2026, time.November, 6, 23, 30, 0, 0, zone),
			cycleFactorKeys: []string{"stress"},
			wantDate:        time.Date(2026, time.November, 6, 0, 0, 0, 0, time.UTC),
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			logEntry := DailyLog{
				Date:            testCase.date,
				CycleFactorKeys: testCase.cycleFactorKeys,
			}

			if err := logEntry.BeforeSave(nil); err != nil {
				t.Fatalf("BeforeSave() error = %v, want nil", err)
			}

			if !logEntry.Date.Equal(testCase.wantDate) {
				t.Fatalf("Date = %v, want %v", logEntry.Date, testCase.wantDate)
			}
			if logEntry.Date.Location() != testCase.wantDate.Location() {
				t.Fatalf("Date location = %v, want %v", logEntry.Date.Location(), testCase.wantDate.Location())
			}
		})
	}
}

func TestCycleFactorConstants(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		got  string
		want string
	}{
		{name: "stress", got: CycleFactorStress, want: "stress"},
		{name: "illness", got: CycleFactorIllness, want: "illness"},
		{name: "travel", got: CycleFactorTravel, want: "travel"},
		{name: "sleep disruption", got: CycleFactorSleepDisruption, want: "sleep_disruption"},
		{name: "medication change", got: CycleFactorMedicationChange, want: "medication_change"},
	}

	for _, testCase := range cases {
		if testCase.got != testCase.want {
			t.Fatalf("%s = %q, want %q", testCase.name, testCase.got, testCase.want)
		}
	}
}

func TestDailyLogBeforeSaveInitialisesNilCycleFactorKeys(t *testing.T) {
	t.Parallel()

	logEntry := DailyLog{}

	if err := logEntry.BeforeSave(nil); err != nil {
		t.Fatalf("BeforeSave() error = %v, want nil", err)
	}

	if logEntry.CycleFactorKeys == nil {
		t.Fatal("CycleFactorKeys = nil, want a non-nil empty slice")
	}
	if len(logEntry.CycleFactorKeys) != 0 {
		t.Fatalf("CycleFactorKeys = %#v, want an empty slice", logEntry.CycleFactorKeys)
	}
}

func TestDailyLogBeforeSaveKeepsCycleFactorKeysOrder(t *testing.T) {
	t.Parallel()

	keys := []string{CycleFactorStress, CycleFactorIllness, CycleFactorTravel, CycleFactorSleepDisruption, CycleFactorMedicationChange}
	logEntry := DailyLog{UserID: 1, Date: time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC), CycleFactorKeys: keys}

	if err := logEntry.BeforeSave(nil); err != nil {
		t.Fatalf("BeforeSave() returned error: %v", err)
	}

	if !reflect.DeepEqual(logEntry.CycleFactorKeys, keys) {
		t.Fatalf("cycle factor keys = %#v, want %#v", logEntry.CycleFactorKeys, keys)
	}
}

func TestDailyLogBeforeSavePreservesNonNilCycleFactorKeys(t *testing.T) {
	t.Parallel()

	logEntry := DailyLog{CycleFactorKeys: []string{"stress", "travel"}}

	if err := logEntry.BeforeSave(nil); err != nil {
		t.Fatalf("BeforeSave() error = %v, want nil", err)
	}

	want := []string{"stress", "travel"}
	if len(logEntry.CycleFactorKeys) != len(want) {
		t.Fatalf("CycleFactorKeys = %#v, want %#v", logEntry.CycleFactorKeys, want)
	}
	for index := range want {
		if logEntry.CycleFactorKeys[index] != want[index] {
			t.Fatalf("CycleFactorKeys = %#v, want %#v", logEntry.CycleFactorKeys, want)
		}
	}
}
