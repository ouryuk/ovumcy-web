package api

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/ovumcy/ovumcy-web/internal/services"
	"golang.org/x/net/html"
)

func TestBuildCycleHistoryCSVWritesOneRowPerCycleWithStableColumns(t *testing.T) {
	t.Parallel()

	estimated := "2026-01-14"
	confirmed := "2026-01-15"
	document, err := buildCycleHistoryCSV([]CycleHistoryEntryResponse{
		{StartDate: "2026-01-01", EndDate: "2026-01-28", Length: 28, OvulationDate: &estimated, OvulationEstimated: true},
		{StartDate: "2026-01-29", EndDate: "2026-02-26", Length: 29, OvulationDate: &confirmed, OvulationConfirmed: true},
		{StartDate: "2026-02-27", EndDate: "2026-03-10", Length: 12},
	})
	if err != nil {
		t.Fatalf("buildCycleHistoryCSV: %v", err)
	}

	want := strings.Join([]string{
		"start_date,end_date,length_days,ovulation_date,ovulation_source",
		"2026-01-01,2026-01-28,28,2026-01-14,estimated",
		"2026-01-29,2026-02-26,29,2026-01-15,confirmed",
		"2026-02-27,2026-03-10,12,,none",
		"",
	}, "\n")
	if string(document) != want {
		t.Fatalf("cycle history CSV =\n%s\nwant\n%s", string(document), want)
	}
}

func TestBuildCycleHistoryCSVQuotesSeparatorQuoteAndNewline(t *testing.T) {
	t.Parallel()

	day := "2026-01-14"
	hostile := "2026-01-01, \"day one\"\nnext line"
	document, err := buildCycleHistoryCSV([]CycleHistoryEntryResponse{
		{StartDate: hostile, EndDate: "2026-01-28", Length: 28, OvulationDate: &day, OvulationEstimated: true},
	})
	if err != nil {
		t.Fatalf("buildCycleHistoryCSV: %v", err)
	}

	records, err := csv.NewReader(bytes.NewReader(document)).ReadAll()
	if err != nil {
		t.Fatalf("the CSV a reader gets back does not parse: %v\n%s", err, document)
	}
	if len(records) != 2 {
		t.Fatalf("expected a header and exactly one cycle, got %d records: %q", len(records), records)
	}
	for _, record := range records {
		if len(record) != len(cycleHistoryCSVHeaders()) {
			t.Fatalf("record %q has %d cells, want %d — a value broke the column layout", record, len(record), len(cycleHistoryCSVHeaders()))
		}
	}
	if records[1][0] != hostile {
		t.Fatalf("round-tripped start_date = %q, want the original %q", records[1][0], hostile)
	}
}

func TestBuildCycleHistoryCSVIsEmptyHistoryAsHeaderOnly(t *testing.T) {
	t.Parallel()

	document, err := buildCycleHistoryCSV(newCycleHistoryResponse(nil))
	if err != nil {
		t.Fatalf("buildCycleHistoryCSV: %v", err)
	}
	if want := strings.Join(cycleHistoryCSVHeaders(), ",") + "\n"; string(document) != want {
		t.Fatalf("empty history CSV = %q, want the header alone %q", string(document), want)
	}
}

// TestCycleHistoryCSVRowPrefersConfirmedOverEstimated pins the precedence the
// table also renders: a confirmed thermal shift is never shown as an estimate.
func TestCycleHistoryCSVRowPrefersConfirmedOverEstimated(t *testing.T) {
	t.Parallel()

	// The row mapping reads only the DTO: a confirmed flag wins over an
	// estimated one, and the flags are mutually exclusive on the wire anyway.
	row := cycleHistoryCSVRow(CycleHistoryEntryResponse{OvulationConfirmed: true, OvulationEstimated: true})
	if row[len(row)-1] != cycleHistoryCSVStatusConfirmed {
		t.Fatalf("ovulation_source with both flags = %q, want %q", row[len(row)-1], cycleHistoryCSVStatusConfirmed)
	}
}

// fetchCycleHistoryCSV drives the real authenticated CSV path and returns the
// response plus the parsed records, so the assertions below read what a
// spreadsheet would.
func fetchCycleHistoryCSV(t *testing.T, app *fiber.App, authCookie string) (*http.Response, string, [][]string) {
	t.Helper()

	request := httptest.NewRequest(http.MethodGet, "/api/v1/stats/cycles?format=csv", nil)
	request.Header.Set("Accept-Language", "en")
	request.Header.Set("Cookie", joinCookieHeader(authCookie, timezoneCookieName+"=UTC"))
	request.Header.Set(timezoneHeaderName, "UTC")

	response := mustAppResponse(t, app, request)
	assertStatusCode(t, response, http.StatusOK)
	body := mustReadBodyString(t, response.Body)
	records, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("the CSV download does not parse: %v\n%s", err, body)
	}
	return response, body, records
}

func TestCycleHistoryCSVMatchesTheJSONEntriesAndIsADownload(t *testing.T) {
	app, database, _ := newOnboardingTestAppWithLocation(t, time.UTC)
	user, authCookie, today := newStatsOverviewOwner(t, app, database, "cycle-history-csv@example.com")
	seedStatsOverviewCycleHistory(t, database, user, today, 62, 34, 6)

	payload := fetchCycleHistory(t, app, authCookie)
	response, body, records := fetchCycleHistoryCSV(t, app, authCookie)

	if got := response.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/csv") {
		t.Fatalf("Content-Type = %q, want text/csv", got)
	}
	disposition := response.Header.Get("Content-Disposition")
	if !strings.HasPrefix(disposition, "attachment; filename=ovumcy-cycle-history-") || !strings.HasSuffix(disposition, ".csv") {
		t.Fatalf("Content-Disposition = %q, want an ovumcy-cycle-history attachment", disposition)
	}
	if strings.Contains(body, "{") {
		t.Fatal("the csv variant answered JSON")
	}

	if len(records) != len(payload)+1 {
		t.Fatalf("CSV has %d records, want a header and %d cycles", len(records), len(payload))
	}
	if got := strings.Join(records[0], ","); got != strings.Join(cycleHistoryCSVHeaders(), ",") {
		t.Fatalf("CSV header = %q, want %q", got, cycleHistoryCSVHeaders())
	}
	for index, entry := range payload {
		row := records[index+1]
		want := cycleHistoryCSVRow(entry)
		if strings.Join(row, "|") != strings.Join(want, "|") {
			t.Fatalf("cycle %d CSV row = %q, want the JSON entry's %q", index, row, want)
		}
		if row[0] != services.AddCalendarDays(today, []int{-62, -34}[index], time.UTC).Format("2006-01-02") {
			t.Fatalf("cycle %d start_date = %q, want the seeded start", index, row[0])
		}
		if row[4] != cycleHistoryCSVStatusEstimated {
			t.Fatalf("cycle %d ovulation_source = %q, want estimated — the fixture records no BBT shift", index, row[4])
		}
	}
}

func TestCycleHistoryCSVIsHeaderOnlyWithoutCompletedCycles(t *testing.T) {
	app, database := newOnboardingTestApp(t)
	user := createOnboardingTestUser(t, database, "cycle-history-csv-empty@example.com", "StrongPass1", true)
	authCookie := loginAndExtractAuthCookie(t, app, user.Email, "StrongPass1")

	_, _, records := fetchCycleHistoryCSV(t, app, authCookie)
	if len(records) != 1 || strings.Join(records[0], ",") != strings.Join(cycleHistoryCSVHeaders(), ",") {
		t.Fatalf("an owner with no completed cycles got %q, want the header row alone", records)
	}
}

func TestCycleHistoryCSVDoesNotLeakAnotherOwnersCycles(t *testing.T) {
	app, database, _ := newOnboardingTestAppWithLocation(t, time.UTC)
	first, _, today := newStatsOverviewOwner(t, app, database, "cycle-history-csv-first@example.com")
	seedStatsOverviewCycleHistory(t, database, first, today, 62, 34, 6)

	second := createOnboardingTestUser(t, database, "cycle-history-csv-second@example.com", "StrongPass1", true)
	secondCookie := loginAndExtractAuthCookie(t, app, second.Email, "StrongPass1")

	_, _, records := fetchCycleHistoryCSV(t, app, secondCookie)
	if len(records) != 1 {
		t.Fatalf("second owner's CSV = %q — another owner's cycle history leaked", records)
	}
}

func TestCycleHistoryCSVRequiresASession(t *testing.T) {
	t.Parallel()

	app, _ := newOnboardingTestApp(t)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/stats/cycles?format=csv", nil)
	response := mustAppResponse(t, app, request)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous CSV request got %d, want 401", response.StatusCode)
	}
}

func TestCycleHistoryCSVIsAuditedAsHealthEgress(t *testing.T) {
	app, database := newOnboardingTestAppWithOptions(t, onboardingTestAppOptions{auditLogEnabled: true})
	user := createOnboardingTestUser(t, database, "cycle-history-csv-audit@example.com", "StrongPass1", true)
	authCookie := loginAndExtractAuthCookie(t, app, user.Email, "StrongPass1")

	request := httptest.NewRequest(http.MethodGet, "/api/v1/stats/cycles?format=csv", nil)
	request.Header.Set("Accept-Language", "en")
	request.Header.Set("Cookie", joinCookieHeader(authCookie, timezoneCookieName+"=UTC"))
	request.Header.Set(timezoneHeaderName, "UTC")

	response, logOutput := captureAuditedRequest(t, app, request)
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("CSV download got %d, want 200", response.StatusCode)
	}

	line := assertHealthEgressAudited(t, logOutput, dataExportAction, "success", exportEgressTarget,
		securityEventField("export_format", "stats_cycles"))
	if strings.Contains(line, "export_format=\"csv\"") {
		t.Fatalf("the cycle-history download is attributed to the account export's format: %q", line)
	}
}

func TestStatsPageRendersTheCycleHistoryTheAPIPublishes(t *testing.T) {
	app, database, _ := newOnboardingTestAppWithLocation(t, time.UTC)
	user, authCookie, today := newStatsOverviewOwner(t, app, database, "cycle-history-page@example.com")
	seedStatsOverviewCycleHistory(t, database, user, today, 62, 34, 6)

	payload := fetchCycleHistory(t, app, authCookie)
	document := fetchStatsPageDocument(t, app, authCookie)

	section := findHTMLNodeWithAttr(document, "data-stats-cycle-history")
	if section == nil {
		t.Fatal("the stats page renders no cycle history section")
	}
	rows := htmlFindElements(section, func(node *html.Node) bool { return htmlHasAttr(node, "data-cycle-history-row") })
	if len(rows) != len(payload) || len(rows) != 2 {
		t.Fatalf("the table has %d rows while the API publishes %d cycles", len(rows), len(payload))
	}
	if findHTMLNodeWithAttr(section, "data-cycle-history-empty") != nil {
		t.Fatal("the table shows the empty state beside rendered cycles")
	}
	if findHTMLNodeWithAttr(section, "data-cycle-history-table") == nil {
		t.Fatal("the cycle history is not rendered as a table")
	}
	export := findHTMLNodeWithAttr(section, "data-cycle-history-export")
	if export == nil || htmlAttr(export, "href") != "/api/v1/stats/cycles?format=csv" {
		t.Fatal("the section does not link to the CSV download of the same entries")
	}

	for index, row := range rows {
		entry := payload[index]
		if got := htmlAttr(row, "data-cycle-history-start"); got != entry.StartDate {
			t.Fatalf("row %d start = %q while the API publishes %q", index, got, entry.StartDate)
		}
		if got := htmlAttr(row, "data-cycle-history-length"); got != fmt.Sprintf("%d", entry.Length) {
			t.Fatalf("row %d length = %q while the API publishes %d", index, got, entry.Length)
		}
		wantSource := cycleHistoryCSVStatusEstimated
		if entry.OvulationConfirmed {
			wantSource = cycleHistoryCSVStatusConfirmed
		}
		if got := htmlAttr(row, "data-cycle-history-source"); got != wantSource {
			t.Fatalf("row %d source = %q, want %q", index, got, wantSource)
		}
		ovulation := findHTMLNodeWithAttr(row, "data-cycle-history-ovulation")
		if ovulation == nil {
			t.Fatalf("row %d has no ovulation cell", index)
		}
		if entry.OvulationDate == nil {
			if !strings.Contains(htmlNodeText(ovulation), "Could not be placed") {
				t.Fatalf("row %d ovulation cell = %q while the API publishes none", index, htmlNodeText(ovulation))
			}
		} else if !strings.Contains(htmlNodeText(row), "Estimated") {
			t.Fatalf("row %d does not say its ovulation day is estimated: %q", index, htmlNodeText(row))
		}
	}
}

func TestStatsPageShowsTheCycleHistoryEmptyStateWithoutCompletedCycles(t *testing.T) {
	app, database := newOnboardingTestApp(t)
	user := createOnboardingTestUser(t, database, "cycle-history-page-empty@example.com", "StrongPass1", true)
	authCookie := loginAndExtractAuthCookie(t, app, user.Email, "StrongPass1")

	document := fetchStatsPageDocument(t, app, authCookie)

	section := findHTMLNodeWithAttr(document, "data-stats-cycle-history")
	if section == nil {
		t.Fatal("the stats page drops the cycle history section when there is nothing to list")
	}
	empty := findHTMLNodeWithAttr(section, "data-cycle-history-empty")
	if empty == nil {
		t.Fatal("an owner with no completed cycles sees no empty state")
	}
	if text := htmlDocumentText(empty); !strings.Contains(text, "No completed cycle yet") {
		t.Fatalf("empty state = %q, want the localized empty copy", text)
	}
	if findHTMLNodeWithAttr(section, "data-cycle-history-row") != nil {
		t.Fatal("the empty state renders beside a cycle row")
	}
	if findHTMLNodeWithAttr(section, "data-cycle-history-export") != nil {
		t.Fatal("the page offers a CSV download of a history that does not exist")
	}
}

// TestStatsPageCycleHistorySourceFlagMatchesTheAPIPerRow walks a history with
// cycles of different lengths and requires each row's source flag to be the
// one the API publishes for the same cycle, whatever that outcome is.
func TestStatsPageCycleHistorySourceFlagMatchesTheAPIPerRow(t *testing.T) {
	app, database, _ := newOnboardingTestAppWithLocation(t, time.UTC)
	user, authCookie, today := newStatsOverviewOwner(t, app, database, "cycle-history-page-flags@example.com")
	// A 28-day cycle, an 8-day one and a 20-day one, so the rows do not all
	// carry the same outcome by construction of the fixture.
	seedStatsOverviewCycleHistory(t, database, user, today, 62, 34, 26, 6)

	payload := fetchCycleHistory(t, app, authCookie)
	document := fetchStatsPageDocument(t, app, authCookie)
	section := findHTMLNodeWithAttr(document, "data-stats-cycle-history")
	if section == nil {
		t.Fatal("the stats page renders no cycle history section")
	}
	rows := htmlFindElements(section, func(node *html.Node) bool { return htmlHasAttr(node, "data-cycle-history-row") })
	if len(rows) != len(payload) || len(rows) == 0 {
		t.Fatalf("the table has %d rows while the API publishes %d cycles", len(rows), len(payload))
	}

	for index, row := range rows {
		entry := payload[index]
		want := "estimated"
		switch {
		case entry.OvulationConfirmed:
			want = "confirmed"
		case entry.OvulationDate == nil:
			want = "none"
		}
		if got := htmlAttr(row, "data-cycle-history-source"); got != want {
			t.Fatalf("row %d source = %q, want %q", index, got, want)
		}
		if got := htmlAttr(row, "data-cycle-history-start"); got != entry.StartDate {
			t.Fatalf("row %d start = %q while the API publishes %q", index, got, entry.StartDate)
		}
	}
}
