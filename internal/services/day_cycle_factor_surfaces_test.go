package services

// day_cycle_factor_surfaces_test.go — the day editor's cycle-factor chip is
// driven from markup: the template renders each key through the
// `cycleFactorLabel` and `cycleFactorIcon` helpers, and the stats surfaces
// render the same five keys through the same pair. A key that resolves to no
// label key or no icon does not fail — it renders as a bare word next to a dot,
// or as the raw key when the catalogue has no entry for it, and nothing notices.
//
// The tests below pin the two surfaces against the key list itself rather than
// against a copy of it: every supported key must be named, must translate in
// the catalogue, and must draw its own icon, and the helpers must accept the
// same spellings NormalizeDayCycleFactorKeys does (the log rows they render
// come from the import/export paths, which normalize on read but a row written
// before the normalization existed can carry padding or caps).

import (
	"strings"
	"testing"

	"github.com/ovumcy/ovumcy-web/internal/models"
)

// TestSupportedDayCycleFactorKeysCarriesEveryModelConstant keeps the list the
// day editor's chip list and the stats surfaces range over in step with the
// constants the validation accepts. A factor constant that never reached the
// list is a key the owner can never pick; a list entry with no constant behind
// it is a chip that persists a value nothing else understands.
func TestSupportedDayCycleFactorKeysCarriesEveryModelConstant(t *testing.T) {
	want := []string{
		models.CycleFactorStress,
		models.CycleFactorIllness,
		models.CycleFactorTravel,
		models.CycleFactorSleepDisruption,
		models.CycleFactorMedicationChange,
	}
	got := SupportedDayCycleFactorKeys()

	if len(got) != len(want) {
		t.Fatalf("supported cycle factors = %v, want %v", got, want)
	}
	for index, key := range want {
		if got[index] != key {
			t.Fatalf("supported cycle factor %d = %q, want %q", index, got[index], key)
		}
	}
}

// TestSupportedDayCycleFactorKeysHandsOutACopy is what makes the accessor safe
// to hand to a template scope or a caller that sorts it: the package's own list
// is what NormalizeDayCycleFactorKeys uses to order every stored row, so a
// caller editing the returned slice would reorder every day entry's factors.
func TestSupportedDayCycleFactorKeysHandsOutACopy(t *testing.T) {
	first := SupportedDayCycleFactorKeys()
	if len(first) == 0 {
		t.Fatal("expected a non-empty supported cycle factor list")
	}
	first[0] = "tampered"

	second := SupportedDayCycleFactorKeys()
	if second[0] != models.CycleFactorStress {
		t.Fatalf("editing the returned slice changed the package list: now starts with %q", second[0])
	}

	// The same edit must not reach NormalizeDayCycleFactorKeys' stable order,
	// which reads the package list directly.
	keys, allValid := NormalizeDayCycleFactorKeys([]string{models.CycleFactorStress, models.CycleFactorTravel})
	if !allValid || len(keys) != 2 || keys[0] != models.CycleFactorStress {
		t.Fatalf("normalization order changed after editing a returned slice: %v (valid=%v)", keys, allValid)
	}
}

// TestEverySupportedCycleFactorIsNamedAndDrawsItsOwnIcon is the completeness
// half of the chip contract: the label key a chip resolves to must exist, and
// no two factors may share an icon — two chips a reader cannot tell apart in
// the day editor defeats the point of showing them.
func TestEverySupportedCycleFactorIsNamedAndDrawsItsOwnIcon(t *testing.T) {
	seenKeys := map[string]string{}
	seenIcons := map[string]string{}

	for _, key := range SupportedDayCycleFactorKeys() {
		labelKey := DayCycleFactorTranslationKey(key)
		if labelKey == "" {
			t.Fatalf("supported cycle factor %q has no label key", key)
		}
		if !strings.HasPrefix(labelKey, "dashboard.cycle_factors.") {
			t.Fatalf("cycle factor %q resolves to %q, outside the dashboard.cycle_factors namespace", key, labelKey)
		}
		if previous, duplicate := seenKeys[labelKey]; duplicate {
			t.Fatalf("cycle factors %q and %q share the label key %q", previous, key, labelKey)
		}
		seenKeys[labelKey] = key

		icon := DayCycleFactorIcon(key)
		if icon == "" || icon == "•" {
			t.Fatalf("supported cycle factor %q falls back to the unknown-key icon %q", key, icon)
		}
		if previous, duplicate := seenIcons[icon]; duplicate {
			t.Fatalf("cycle factors %q and %q share the icon %q", previous, key, icon)
		}
		seenIcons[icon] = key
	}
}

// TestCycleFactorHelpersAcceptTheSpellingsNormalizationAccepts pins the two
// resolvers to the same tolerance NormalizeDayCycleFactorKeys has. A day row
// stored as " Travel " — a row written before the write path trimmed, or a raw
// CSV column value handed to the renderer — must draw the Travel chip, not a raw
// "travel" word beside a bullet, and "TRAVEL" must not be mistaken for a key
// the catalogue does not know.
func TestCycleFactorHelpersAcceptTheSpellingsNormalizationAccepts(t *testing.T) {
	for _, key := range SupportedDayCycleFactorKeys() {
		spellings := []string{
			key,
			strings.ToUpper(key),
			"  " + key + "  ",
			strings.ToUpper(" " + key + " "),
		}
		wantLabel := DayCycleFactorTranslationKey(key)
		wantIcon := DayCycleFactorIcon(key)

		for _, spelling := range spellings {
			if got := DayCycleFactorTranslationKey(spelling); got != wantLabel {
				t.Fatalf("label for %q = %q, want %q", spelling, got, wantLabel)
			}
			if got := DayCycleFactorIcon(spelling); got != wantIcon {
				t.Fatalf("icon for %q = %q, want %q", spelling, got, wantIcon)
			}
		}
	}
}

// TestUnknownCycleFactorFallsBackToTheBullet covers the arm the templates rely
// on for a value the catalogue has never heard of: an empty label key makes the
// `cycleFactorLabel` helper render the raw key, and the bullet is the "no icon"
// mark. A near-miss key must not be mapped onto a supported one by prefix, case
// or a stray separator — that would render one factor's name for a value the
// owner never picked.
func TestUnknownCycleFactorFallsBackToItselfAndToTheBullet(t *testing.T) {
	// An unknown key must not be mapped onto a supported one by prefix or case.
	for _, unknown := range []string{"", "   ", "stress_", "stressed", "alcohol", "sleep", "0"} {
		if key := DayCycleFactorTranslationKey(unknown); key != "" {
			t.Fatalf("unknown cycle factor %q resolved to label key %q, want none", unknown, key)
		}
		if icon := DayCycleFactorIcon(unknown); icon != "•" {
			t.Fatalf("unknown cycle factor %q resolved to icon %q, want the bullet", unknown, icon)
		}
	}
}

// TestNormalizeDayCycleFactorKeysReordersIntoTheAdvertisedOrder guards the one
// thing the day editor's option list depends on: whatever order the browser
// posts the checkboxes in, the stored slice follows SupportedDayCycleFactorKeys.
// The chip list above is rendered from the same order, so a stored row that
// followed the post order would render its chips in a different order on every
// save.
func TestNormalizeDayCycleFactorKeysReordersIntoTheAdvertisedOrder(t *testing.T) {
	advertised := SupportedDayCycleFactorKeys()

	// Post the advertised list backwards, with a duplicate and a blank entry
	// the browser leaves behind for an unchecked box with an empty value.
	posted := make([]string, 0, len(advertised)+2)
	for index := len(advertised) - 1; index >= 0; index-- {
		posted = append(posted, advertised[index])
	}
	posted = append(posted, advertised[0], "  ")

	stored, allValid := NormalizeDayCycleFactorKeys(posted)
	if !allValid {
		t.Fatalf("expected a blank entry to be skipped rather than reported invalid: %v", stored)
	}
	if len(stored) != len(advertised) {
		t.Fatalf("stored %d factors %v, want %d", len(stored), stored, len(advertised))
	}
	for index, key := range advertised {
		if stored[index] != key {
			t.Fatalf("stored factor %d = %q, want %q (the advertised order)", index, stored[index], key)
		}
	}
}
