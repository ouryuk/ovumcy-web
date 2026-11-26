package templates

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
	"time"
)

// statsCycleHistoryMessages is the slice of the catalogue the stats page reads
// when the rest of it is in its empty state. The cycle-history values are
// deliberately shouty so a rendered key (the fallback) cannot pass for copy.
var statsCycleHistoryMessages = map[string]string{
	"stats.title":                     "Stats",
	"stats.subtitle":                  "stats subtitle",
	"stats.empty_title":               "empty title",
	"stats.empty_body_zero":           "zero body",
	"stats.empty_progress_hint":       "progress hint",
	"stats.completed_cycles_progress": "%d completed",
	"stats.cycle_history_title":       "Cycle history",
	"stats.cycle_history_subtitle":    "history subtitle",
	"stats.cycle_history_empty":       "NO COMPLETED CYCLE",
	"stats.cycle_history_start":       "COL_START",
	"stats.cycle_history_end":         "COL_END",
	"stats.cycle_history_length":      "COL_LENGTH",
	"stats.cycle_history_ovulation":   "COL_OVULATION",
	"stats.cycle_history_source":      "COL_SOURCE",
	"stats.cycle_history_none":        "NOT PLACED",
	"stats.cycle_history_source_none": "SOURCE_NONE",
	"stats.cycle_history_confirmed":   "CONFIRMED",
	"stats.cycle_history_estimated":   "ESTIMATED",
	"stats.cycle_history_export":      "CSV_LINK",
	"common.days_short":               "d",
	"medical.disclaimer":              "disclaimer",
}

// renderStatsCycleHistory renders internal/templates/stats.html the way the app
// does — "content" off the embedded file, with the page's helper names — for a
// dot carrying only what the cycle-history section reads. The rest of the page
// is put in its empty state so this test is about the section and not about
// every aggregate beside it.
func renderStatsCycleHistory(t *testing.T, data map[string]any, isOwner bool) string {
	t.Helper()

	data["Messages"] = statsCycleHistoryMessages
	data["Lang"] = "en"
	data["IsOwner"] = isOwner
	data["CompletedCycleCount"] = 0
	data["HasInsights"] = false

	noop := func(string) string { return "" }
	parsed, err := template.New("layout").Funcs(template.FuncMap{
		"t": func(messages map[string]string, key string) string {
			if value, ok := messages[key]; ok {
				return value
			}
			return key
		},
		"tn":                  func(map[string]string, string, string, int) string { return "" },
		"formatLocalizedDate": func(_ string, value time.Time, _ string) string { return value.Format("2006-01-02") },
		"formatFloat":         func(float64) string { return "" },
		"toJSON":              func(any) string { return "{}" },
		"phaseLabel":          noop,
		"phaseIcon":           func(string) string { return "phase" },
		"cycleFactorLabel":    noop,
		"cycleFactorIcon":     func(string) string { return "factor" },
		"symptomLabel":        noop,
	}).ParseFS(Files, "stats.html", "components/icons.html")
	if err != nil {
		t.Fatalf("parse stats.html: %v", err)
	}

	var output bytes.Buffer
	if err := parsed.ExecuteTemplate(&output, "content", data); err != nil {
		t.Fatalf("render stats.html: %v", err)
	}
	return output.String()
}

// stripStatsHTML collapses the whitespace html/template leaves between
// actions so a cell's text can be compared as one string.
func stripStatsHTML(markup string) string {
	return strings.Join(strings.Fields(markup), " ")
}

// statsCycleHistoryRow mirrors services.CycleHistoryEntry field for field. The
// templates package cannot import the service (the app imports both), and the
// section reads the entry by name.
type statsCycleHistoryRow struct {
	Start              time.Time
	End                time.Time
	Length             int
	OvulationDate      time.Time
	OvulationConfirmed bool
	OvulationEstimated bool
}

func statsCycleHistoryDay(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		t.Fatalf("parse day: %v", err)
	}
	return parsed
}

// TestStatsTemplateRendersOneCycleHistoryRowPerEntry walks the three states a
// row can be in — estimated, confirmed, not placed — and requires each to show
// the day, the length unit and its own source copy.
func TestStatsTemplateRendersOneCycleHistoryRowPerEntry(t *testing.T) {
	day := func(value string) time.Time { return statsCycleHistoryDay(t, value) }
	output := stripStatsHTML(renderStatsCycleHistory(t, map[string]any{
		"CycleHistory": []statsCycleHistoryRow{
			{Start: day("2026-01-01"), End: day("2026-01-28"), Length: 28, OvulationDate: day("2026-01-14"), OvulationEstimated: true},
			{Start: day("2026-01-29"), End: day("2026-02-26"), Length: 29, OvulationDate: day("2026-01-15"), OvulationConfirmed: true},
			{Start: day("2026-02-27"), End: day("2026-03-10"), Length: 12},
		},
		"HasCycleHistory": true,
	}, true))

	for _, want := range []string{
		"data-stats-cycle-history",
		`data-cycle-history-export download`,
		"<th scope=\"col\">COL_START</th>",
		"<th scope=\"col\">COL_END</th>",
		"<th scope=\"col\">COL_LENGTH</th>",
		"<th scope=\"col\">COL_OVULATION</th>",
		"<th scope=\"col\">COL_SOURCE</th>",
		`data-cycle-history-start="2026-01-01" data-cycle-history-length="28"`,
		"<td>2026-01-01</td> <td>2026-01-28</td> <td>28 d</td> <td data-cycle-history-ovulation>2026-01-14</td> <td data-cycle-history-source=\"estimated\">ESTIMATED</td>",
		"<td>2026-01-29</td> <td>2026-02-26</td> <td>29 d</td> <td data-cycle-history-ovulation>2026-01-15</td> <td data-cycle-history-source=\"confirmed\">CONFIRMED</td>",
		"<td>2026-02-27</td> <td>2026-03-10</td> <td>12 d</td> <td data-cycle-history-ovulation>NOT PLACED</td> <td data-cycle-history-source=\"none\">SOURCE_NONE</td>",
		`data-cycle-history-source="estimated"`,
		`data-cycle-history-source="confirmed"`,
		`data-cycle-history-source="none"`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("rendered cycle history lacks %q:\n%s", want, output)
		}
	}
	if got := strings.Count(output, "data-cycle-history-row "); got != 3 {
		t.Fatalf("expected three cycle rows, rendered %d:\n%s", got, output)
	}
	if strings.Contains(output, "data-cycle-history-empty") {
		t.Fatalf("the empty state rendered beside cycle rows:\n%s", output)
	}
	if strings.Contains(output, "common.days_short") || strings.Contains(output, "stats.cycle_history_") {
		t.Fatalf("a translation key rendered instead of its message:\n%s", output)
	}
}

func TestStatsTemplateRendersTheCycleHistoryEmptyState(t *testing.T) {
	output := stripStatsHTML(renderStatsCycleHistory(t, map[string]any{
		"CycleHistory":    []statsCycleHistoryRow{},
		"HasCycleHistory": false,
	}, true))

	if !strings.Contains(output, "data-cycle-history-empty") || !strings.Contains(output, "NO COMPLETED CYCLE") {
		t.Fatalf("an owner without completed cycles got no empty state:\n%s", output)
	}
	for _, absent := range []string{"data-cycle-history-row", "data-cycle-history-table", "data-cycle-history-export"} {
		if strings.Contains(output, absent) {
			t.Fatalf("the empty state rendered %s:\n%s", absent, output)
		}
	}

	// A missing history is what the page data never carries today, but a dot
	// without the key must read the same rather than fail to render.
	nilOutput := stripStatsHTML(renderStatsCycleHistory(t, map[string]any{}, true))
	if !strings.Contains(nilOutput, "NO COMPLETED CYCLE") || strings.Contains(nilOutput, "data-cycle-history-row") {
		t.Fatalf("a missing cycle history did not fall back to the empty state:\n%s", nilOutput)
	}
}

// TestStatsTemplateHidesTheCycleHistoryFromANonOwner pins the guard the other
// per-owner sections sit behind: the list is the owner's own record, so a
// viewer who is not the owner gets neither rows nor empty state.
func TestStatsTemplateHidesTheCycleHistoryFromANonOwner(t *testing.T) {
	output := stripStatsHTML(renderStatsCycleHistory(t, map[string]any{
		"CycleHistory":    []statsCycleHistoryRow{{Start: statsCycleHistoryDay(t, "2026-01-01"), Length: 28}},
		"HasCycleHistory": true,
	}, false))

	for _, absent := range []string{"data-stats-cycle-history", "data-cycle-history-row", "data-cycle-history-empty", "CSV_LINK"} {
		if strings.Contains(output, absent) {
			t.Fatalf("the per-cycle list rendered %s for a viewer who is not the owner:\n%s", absent, output)
		}
	}
}
