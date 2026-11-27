package i18n

import (
	"slices"
	"sort"
	"testing"
)

func TestManagerSupportsGermanLocale(t *testing.T) {
	manager := newTestI18nManager(t, LangDE)

	supported := manager.SupportedLanguages()
	if !slices.Equal(supported, []string{LangDE, LangEN, LangES, LangFR, LangIT, LangRU}) {
		t.Fatalf("expected sorted supported languages [de en es fr it ru], got %#v", supported)
	}
	if manager.DefaultLanguage() != LangDE {
		t.Fatalf("expected default language %q, got %q", LangDE, manager.DefaultLanguage())
	}
	if got := manager.NormalizeLanguage("de-DE"); got != LangDE {
		t.Fatalf("expected de-DE to normalize to %q, got %q", LangDE, got)
	}
	if got := manager.DetectFromAcceptLanguage("de-DE,de;q=0.9,en;q=0.8"); got != LangDE {
		t.Fatalf("expected Accept-Language to detect %q, got %q", LangDE, got)
	}
}

func TestManagerFallsBackToDefaultForUnsupportedLanguage(t *testing.T) {
	manager := newTestI18nManager(t, LangEN)

	if got := manager.NormalizeLanguage("pt-BR"); got != LangEN {
		t.Fatalf("expected unsupported language to fall back to %q, got %q", LangEN, got)
	}
	if got := manager.DetectFromAcceptLanguage("pt-BR,pt;q=0.9"); got != LangEN {
		t.Fatalf("expected unsupported Accept-Language to fall back to %q, got %q", LangEN, got)
	}
}

// TestDetectFromAcceptLanguagePicksTheFirstSupportedToken pins the negotiation
// order production relies on: every token the browser sent is examined in the
// order it was sent, an unsupported or malformed one is skipped rather than
// ending the search, and only when none of them names a shipped locale does the
// answer become the default language.
//
// Each token is stripped of its RFC 4647 quality value before matching, so
// "fr-CH;q=0.8" names the same locale as "fr-CH". The half of the header the
// parser must not confuse with a language is the "q=" parameter itself: a
// parser that matched on the raw token would see "fr-ch;q=0.8" missing from the
// catalogue and fall through to the default, which is what makes this the
// behaviour worth pinning — the owner would get the operator's default in a
// language their browser explicitly asked for.
func TestDetectFromAcceptLanguagePicksTheFirstSupportedToken(t *testing.T) {
	manager := newTestI18nManager(t, LangDE)

	tests := []struct {
		name   string
		header string
		want   string
	}{
		{
			name:   "first supported token wins over a later one",
			header: "it-CH,ru;q=0.8",
			want:   LangIT,
		},
		{
			name:   "a quality value does not hide the locale",
			header: "fr-CH;q=0.8",
			want:   LangFR,
		},
		{
			name:   "a quality value with no space is stripped",
			header: "es;q=0.9",
			want:   LangES,
		},
		{
			name:   "an unsupported token is skipped, not decisive",
			header: "pt-BR,en;q=0.9,de;q=0.8",
			want:   LangEN,
		},
		{
			name:   "an empty token is skipped",
			header: " , ,ru",
			want:   LangRU,
		},
		{
			name:   "a wildcard does not swallow the header",
			header: "*,it;q=0.7",
			want:   LangIT,
		},
		{
			name:   "a region subtag does not prevent the match",
			header: "de-AT",
			want:   LangDE,
		},
		{
			name:   "surrounding whitespace is trimmed",
			header: "   ru-RU   ",
			want:   LangRU,
		},
		{
			name:   "nothing supported falls back to the default",
			header: "pt-BR,zz;q=0.9",
			want:   LangDE,
		},
		{
			name:   "an absent header falls back to the default",
			header: "",
			want:   LangDE,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := manager.DetectFromAcceptLanguage(testCase.header); got != testCase.want {
				t.Fatalf("DetectFromAcceptLanguage(%q) = %q, want %q", testCase.header, got, testCase.want)
			}
		})
	}
}

// TestDetectFromAcceptLanguageFallsBackToItsOwnDefault pins that the fallback is
// the manager's configured default and not a hard-coded language: an operator
// who boots the instance with DEFAULT_LANGUAGE=ru must get Russian, not English,
// from a browser asking for a language this build does not ship.
func TestDetectFromAcceptLanguageFallsBackToItsOwnDefault(t *testing.T) {
	manager := newTestI18nManager(t, LangRU)

	if got := manager.DetectFromAcceptLanguage("pt-BR"); got != LangRU {
		t.Fatalf("DetectFromAcceptLanguage(\"pt-BR\") = %q, want the configured default %q", got, LangRU)
	}
}

// TestIsSupportedLanguageSeparatesUnknownFromDefault pins the distinction
// NormalizeLanguage cannot make. A stored language code (users.interface_language)
// is only usable if this build actually ships its catalogue: normalization
// answers with the default for an unknown code, which a caller reading a stored
// value would take for a deliberate choice of that default.
func TestIsSupportedLanguageSeparatesUnknownFromDefault(t *testing.T) {
	manager := newTestI18nManager(t, LangEN)

	for _, supported := range []string{LangRU, "ru-RU", "RU", LangEN} {
		if !manager.IsSupportedLanguage(supported) {
			t.Fatalf("expected %q to be recognized as a shipped locale", supported)
		}
	}
	for _, unsupported := range []string{"", "   ", "zz", "pt-BR"} {
		if manager.IsSupportedLanguage(unsupported) {
			t.Fatalf("expected %q to be rejected as unsupported, while NormalizeLanguage answers %q",
				unsupported, manager.NormalizeLanguage(unsupported))
		}
	}
}

func TestRequiredLocalesCoversAllSupportedLanguages(t *testing.T) {
	want := []string{LangDE, LangEN, LangES, LangFR, LangIT, LangRU}

	got := slices.Clone(requiredLocales)
	sort.Strings(got)

	if !slices.Equal(got, want) {
		t.Fatalf("expected requiredLocales to cover exactly %#v, got %#v", want, got)
	}
}

func newTestI18nManager(t *testing.T, defaultLanguage string) *Manager {
	t.Helper()

	manager, err := NewManager(defaultLanguage)
	if err != nil {
		t.Fatalf("NewManager() unexpected error: %v", err)
	}
	return manager
}
