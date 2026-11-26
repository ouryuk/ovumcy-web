package services

import (
	"testing"

	"github.com/ovumcy/ovumcy-web/internal/models"
)

// The three enumerated day fields — sex activity, cervical mucus and pregnancy
// test — are each read by a pair of functions: an IsValid* gate that lets a
// value through and a Normalize* step that rewrites what was accepted onto the
// stored constant. NormalizeDayEntryInput only ever shows the pair working
// together on an exact constant or rejecting a wrong one, so two things were
// unpinned: that the gate accepts the spellings the normaliser understands
// (mixed case, surrounding whitespace, the empty "not recorded" value), and
// that an accepted value always comes out as one of the constants the rest of
// the app compares against. A gate that admitted a spelling the normaliser
// folds to "none" would silently record "not recorded" for a measured value.

type dayTrackingEnumCase struct {
	name      string
	sent      string
	canonical string
}

func dayTrackingEnumCases(label, none string, values []string) []dayTrackingEnumCase {
	cases := []dayTrackingEnumCase{
		{name: label + " empty", sent: "", canonical: none},
		{name: label + " none constant", sent: none, canonical: none},
		{name: label + " none upper case padded", sent: "  " + upper(none) + " ", canonical: none},
	}
	for _, value := range values {
		cases = append(cases,
			dayTrackingEnumCase{name: label + " " + value + " constant", sent: value, canonical: value},
			dayTrackingEnumCase{name: label + " " + value + " mixed case", sent: mixedCase(value), canonical: value},
			dayTrackingEnumCase{name: label + " " + value + " tab padded", sent: "\t" + upper(value) + "\n", canonical: value},
		)
	}
	return cases
}

func upper(value string) string {
	out := []byte(value)
	for i := range out {
		if out[i] >= 'a' && out[i] <= 'z' {
			out[i] -= 'a' - 'A'
		}
	}
	return string(out)
}

func mixedCase(value string) string {
	out := []byte(value)
	for i := range out {
		if i%2 == 0 {
			if out[i] >= 'a' && out[i] <= 'z' {
				out[i] -= 'a' - 'A'
			}
		}
	}
	return string(out)
}

func TestDayTrackingEnumsAcceptAndCanonicaliseEverySpelling(t *testing.T) {
	suites := []struct {
		name   string
		valid  func(string) bool
		normal func(string) string
		cases  []dayTrackingEnumCase
	}{
		{
			name:   "sex activity",
			valid:  IsValidDaySexActivity,
			normal: NormalizeDaySexActivity,
			cases: dayTrackingEnumCases("sex activity", models.SexActivityNone,
				[]string{models.SexActivityProtected, models.SexActivityUnprotected}),
		},
		{
			name:   "cervical mucus",
			valid:  IsValidDayCervicalMucus,
			normal: NormalizeDayCervicalMucus,
			cases: dayTrackingEnumCases("cervical mucus", models.CervicalMucusNone,
				[]string{models.CervicalMucusDry, models.CervicalMucusMoist, models.CervicalMucusCreamy, models.CervicalMucusEggWhite}),
		},
		{
			name:   "pregnancy test",
			valid:  IsValidDayPregnancyTest,
			normal: NormalizeDayPregnancyTest,
			cases: dayTrackingEnumCases("pregnancy test", models.PregnancyTestNone,
				[]string{models.PregnancyTestNegative, models.PregnancyTestPositive}),
		},
	}

	for _, suite := range suites {
		t.Run(suite.name, func(t *testing.T) {
			for _, tc := range suite.cases {
				t.Run(tc.name, func(t *testing.T) {
					if !suite.valid(tc.sent) {
						t.Fatalf("value %q rejected, want it accepted", tc.sent)
					}
					if got := suite.normal(tc.sent); got != tc.canonical {
						t.Fatalf("normalised %q to %q, want the stored constant %q", tc.sent, got, tc.canonical)
					}
					// The stored constant is itself a valid, fixed point: what
					// the normaliser writes must survive a second read unchanged.
					if got := suite.normal(tc.canonical); got != tc.canonical {
						t.Fatalf("stored constant %q does not round trip: got %q", tc.canonical, got)
					}
				})
			}
		})
	}
}

// A value outside the enumeration is refused by the gate, and the normaliser
// — which import and merge paths call without the gate — answers "none" for it
// rather than storing an unknown string the templates cannot render.
func TestDayTrackingEnumsRejectAndNeutraliseUnknownValues(t *testing.T) {
	suites := []struct {
		name   string
		valid  func(string) bool
		normal func(string) string
		none   string
	}{
		{name: "sex activity", valid: IsValidDaySexActivity, normal: NormalizeDaySexActivity, none: models.SexActivityNone},
		{name: "cervical mucus", valid: IsValidDayCervicalMucus, normal: NormalizeDayCervicalMucus, none: models.CervicalMucusNone},
		{name: "pregnancy test", valid: IsValidDayPregnancyTest, normal: NormalizeDayPregnancyTest, none: models.PregnancyTestNone},
	}

	// Each unknown is a near miss on a real constant: a plural, an inner
	// mutation and a prefix of the word.
	unknowns := []string{"maybe-not", "maybe", "negatives", "positiv", "egg-white", "unprotect", "moisturised", "0", "NONE!"}

	for _, suite := range suites {
		t.Run(suite.name, func(t *testing.T) {
			for _, unknown := range unknowns {
				if suite.valid(unknown) {
					t.Fatalf("value %q accepted, want it rejected", unknown)
				}
				if got := suite.normal(unknown); got != suite.none {
					t.Fatalf("normalised unknown %q to %q, want the neutral %q", unknown, got, suite.none)
				}
			}
		})
	}
}
