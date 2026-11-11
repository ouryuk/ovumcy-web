package services

import "testing"

// The two text-sanitization policies in this package guard two ways an owner's
// own text can hurt them once it leaves the database: the CSV export writes
// the text into a file the owner opens in a spreadsheet, and the plain-text
// label policies write it into a place a browser renders as markup.
//
// Both policies are reached only through their callers — sanitizeCSVTextCell
// through the export service's finished columns, containsInvalidPlainTextLabelRune
// through the symptom-name and profile-display-name validators — so the
// existing coverage proves the pair works together and says nothing about
// either policy on its own. The export suite does drive all seven dangerous
// prefixes through a finished CSV column, but the helper it drives them
// through is never asked on its own terms: what it answers for a value no
// export column carries (an empty cell, an all-space cell, a value whose
// equals sign sits in the middle) was therefore unspecified, and the label
// guard's own arms — the control rune and the markup delimiter — had no
// direct assertion at all.

// TestSanitizeCSVTextCell covers the CSV formula-injection guard: a cell whose
// first non-space character a spreadsheet would read as the start of a formula
// is prefixed with an apostrophe, so the owner's own export cannot execute
// anything in the spreadsheet they open it with.
func TestSanitizeCSVTextCell(t *testing.T) {
	t.Run("empty value is unchanged", func(t *testing.T) {
		if got := sanitizeCSVTextCell(""); got != "" {
			t.Fatalf("got %q, want the empty string", got)
		}
	})

	// Every character the guard treats as dangerous, plus the space-padded
	// disguise: a spreadsheet skips leading whitespace before it reads the
	// first byte, so " =cmd" is as live as "=cmd".
	dangerous := []struct {
		name  string
		value string
	}{
		{name: "equals", value: "=cmd|' /C calc'!A0"},
		{name: "plus", value: "+1+1"},
		{name: "minus", value: "-1+1"},
		{name: "at", value: "@SUM(A1:A9)"},
		{name: "tab", value: "\tcmd"},
		{name: "carriage return", value: "\rcmd"},
		{name: "line feed", value: "\ncmd"},
		{name: "space padded equals", value: " =cmd"},
		{name: "space padded at", value: " @SUM(A1)"},
	}
	for _, tc := range dangerous {
		t.Run("dangerous prefix "+tc.name, func(t *testing.T) {
			// The apostrophe goes in front of the ORIGINAL value, so the
			// cell reads as one leading apostrophe followed by exactly the
			// text the owner wrote.
			want := "'" + tc.value
			if got := sanitizeCSVTextCell(tc.value); got != want {
				t.Fatalf("sanitizeCSVTextCell(%q) = %q, want %q", tc.value, got, want)
			}
		})
	}

	// The positive control: the guard must not quote every cell, or the
	// export stops being readable. An equals sign in the middle of a value is
	// not a formula, and a space followed by an ordinary character is not one
	// either.
	harmless := []struct {
		name  string
		value string
	}{
		{name: "ordinary text", value: "cramps in the evening"},
		{name: "equals in the middle", value: "a=b"},
		{name: "equals at the end", value: "a="},
		{name: "space then an ordinary character", value: " cramps"},
	}
	for _, tc := range harmless {
		t.Run("unchanged "+tc.name, func(t *testing.T) {
			if got := sanitizeCSVTextCell(tc.value); got != tc.value {
				t.Fatalf("sanitizeCSVTextCell(%q) = %q, want it unchanged", tc.value, got)
			}
		})
	}
}

// TestHasDangerousCSVPrefix covers the predicate the guard reads: it decides
// on the first non-space character, and an all-space value has no such
// character at all.
func TestHasDangerousCSVPrefix(t *testing.T) {
	t.Run("empty value is not dangerous", func(t *testing.T) {
		if hasDangerousCSVPrefix("") {
			t.Fatal("expected the empty value to be safe")
		}
	})

	t.Run("all-space value is not dangerous", func(t *testing.T) {
		if hasDangerousCSVPrefix("   ") {
			t.Fatal("expected an all-space value to be safe")
		}
	})

	dangerous := []struct {
		name  string
		value string
	}{
		{name: "equals", value: "=cmd"},
		{name: "plus", value: "+cmd"},
		{name: "minus", value: "-cmd"},
		{name: "at", value: "@cmd"},
		{name: "tab", value: "\tcmd"},
		{name: "carriage return", value: "\rcmd"},
		{name: "line feed", value: "\ncmd"},
		{name: "space padded equals", value: " =cmd"},
		{name: "space padded at", value: " @cmd"},
	}
	for _, tc := range dangerous {
		t.Run("dangerous prefix "+tc.name, func(t *testing.T) {
			if !hasDangerousCSVPrefix(tc.value) {
				t.Fatalf("expected %q to be treated as dangerous", tc.value)
			}
		})
	}

	harmless := []struct {
		name  string
		value string
	}{
		{name: "ordinary text", value: "cramps"},
		{name: "equals in the middle", value: "a=b"},
		{name: "space then an ordinary character", value: " cramps"},
	}
	for _, tc := range harmless {
		t.Run("safe "+tc.name, func(t *testing.T) {
			if hasDangerousCSVPrefix(tc.value) {
				t.Fatalf("expected %q to be treated as safe", tc.value)
			}
		})
	}
}

// TestContainsInvalidPlainTextLabelRune covers the plain-text label guard: a
// label is written into a place a browser renders, so it may not carry a
// control rune (which can rewrite the line it sits on) nor a markup delimiter.
func TestContainsInvalidPlainTextLabelRune(t *testing.T) {
	t.Run("empty label is accepted", func(t *testing.T) {
		if containsInvalidPlainTextLabelRune("") {
			t.Fatal("expected the empty label to be accepted")
		}
	})

	accepted := []struct {
		name  string
		value string
	}{
		{name: "ordinary text", value: "Headache"},
		{name: "accented characters", value: "Café"},
		{name: "another accented word", value: "naïve"},
		{name: "emoji", value: "🌙"},
	}
	for _, tc := range accepted {
		t.Run("accepted "+tc.name, func(t *testing.T) {
			if containsInvalidPlainTextLabelRune(tc.value) {
				t.Fatalf("expected %q to be accepted", tc.value)
			}
		})
	}

	rejected := []struct {
		name  string
		value string
	}{
		{name: "nul byte", value: "x\x00y"},
		{name: "newline", value: "line\nbreak"},
		{name: "tab", value: "tab\there"},
		{name: "carriage return", value: "carriage\rreturn"},
		{name: "less than", value: "<b>"},
		{name: "greater than", value: "a > b"},
		{name: "markup", value: "<script>alert(1)</script>"},
	}
	for _, tc := range rejected {
		t.Run("rejected "+tc.name, func(t *testing.T) {
			if !containsInvalidPlainTextLabelRune(tc.value) {
				t.Fatalf("expected %q to be refused", tc.value)
			}
		})
	}
}
