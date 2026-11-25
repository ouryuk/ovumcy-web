package i18n

import (
	"sort"
	"testing"
)

// The day editor's cycle-factor chips take their text from this catalogue: the
// template resolves each key through `services.DayCycleFactorTranslationKey`,
// which answers with `dashboard.cycle_factors.<key>` and a bare `translateMessage`
// lookup for the reader's language. `translateMessage` falls back to the KEY
// itself when the catalogue has no entry, so a factor missing from one locale
// does not fail and does not render a blank — it renders the literal
// "dashboard.cycle_factors.stress" inside a translated page. The locale parity
// sweep only guarantees every catalogue carries the SAME key set; it says
// nothing about whether the set a chip needs is in it.
//
// The keys are spelled out rather than imported: `internal/i18n` sits below
// `internal/services` in the import direction, and a test reaching up into the
// service package to read the list would invert that for a five-element slice
// that changes about once a release. `TestSupportedDayCycleFactorKeysCarriesEveryModelConstant`
// in `internal/services` pins the list itself against the model constants, so
// the two halves cannot drift apart silently — a new factor needs an entry in
// both places or one of the two suites fails by name.
var everyDayCycleFactorTranslationKey = []string{
	"dashboard.cycle_factors.stress",
	"dashboard.cycle_factors.illness",
	"dashboard.cycle_factors.travel",
	"dashboard.cycle_factors.sleep_disruption",
	"dashboard.cycle_factors.medication_change",
}

// TestEveryCycleFactorChipHasCopyInEveryLocale is the human half of the chip
// contract: the key exists, and what it holds is a phrase a reader can act on
// rather than the key echoed back or an empty string.
func TestEveryCycleFactorChipHasCopyInEveryLocale(t *testing.T) {
	locales := mustLoadAllLocaleMessages(t)

	if len(locales) < len(requiredLocales) {
		t.Fatalf("loaded %d locale(s), fewer than the %d this build requires; this check would pass by simply not reading the catalogues", len(locales), len(requiredLocales))
	}

	languages := make([]string, 0, len(locales))
	for language := range locales {
		languages = append(languages, language)
	}
	sort.Strings(languages)

	for _, language := range languages {
		messages := locales[language]
		for _, key := range everyDayCycleFactorTranslationKey {
			text, ok := messages[key]
			if !ok {
				t.Errorf("%s has no copy for the cycle-factor chip %q; the day editor would render the key itself", language, key)
				continue
			}
			if text == "" {
				t.Errorf("%s carries an empty cycle-factor chip %q", language, key)
			}
			if text == key {
				t.Errorf("%s echoes the cycle-factor chip key %q back as its copy", language, key)
			}
		}
	}
}
