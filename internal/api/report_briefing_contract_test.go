package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/ovumcy/ovumcy-web/internal/models"
	"github.com/ovumcy/ovumcy-web/internal/services"
)

// fetchReport drives the real authenticated request path so the contract
// exercised here is the transport's, not a constructed DTO's.
func fetchReport(t *testing.T, app *fiber.App, authCookie string, query string) (int, string, AppointmentBriefingResponse) {
	t.Helper()

	request := httptest.NewRequest(http.MethodGet, "/api/v1/report"+query, nil)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Language", "en")
	request.Header.Set("Cookie", joinCookieHeader(authCookie, timezoneCookieName+"=UTC"))
	request.Header.Set(timezoneHeaderName, "UTC")

	response := mustAppResponse(t, app, request)
	status := response.StatusCode
	body := mustReadBodyString(t, response.Body)

	var payload AppointmentBriefingResponse
	if status == http.StatusOK {
		if err := json.Unmarshal([]byte(body), &payload); err != nil {
			t.Fatalf("decode briefing payload: %v\n%s", err, body)
		}
	}
	return status, body, payload
}

func TestAppointmentBriefingSummarisesLogsCyclesAndSymptoms(t *testing.T) {
	app, database, _ := newOnboardingTestAppWithLocation(t, time.UTC)
	user, authCookie, today := newStatsOverviewOwner(t, app, database, "briefing@example.com")

	// Two completed cycles (starts 62 and 34 days ago, running cycle open 6
	// days ago) plus a symptom-heavy day inside the first of them.
	seedStatsOverviewCycleHistory(t, database, user, today, 62, 34, 6)

	symptoms := []models.SymptomType{
		{UserID: user.ID, Name: "Cramps", Icon: "A", Color: "#111111"},
		{UserID: user.ID, Name: "Custom Symptom", Icon: "B", Color: "#222222"},
	}
	if err := database.Create(&symptoms).Error; err != nil {
		t.Fatalf("create symptoms: %v", err)
	}
	seedStatsOverviewLog(t, database, models.DailyLog{
		UserID:     user.ID,
		Date:       services.AddCalendarDays(today, -60, time.UTC),
		IsPeriod:   true,
		Flow:       models.FlowLight,
		SymptomIDs: []uint{symptoms[0].ID, symptoms[0].ID, symptoms[1].ID},
	})

	// A range that covers the whole recorded history.
	_, _, payload := fetchReport(t, app, authCookie, "?from="+services.AddCalendarDays(today, -90, time.UTC).Format("2006-01-02")+"&to="+today.Format("2006-01-02"))

	if !payload.HasData || payload.TotalEntries != 4 {
		t.Fatalf("total_entries = %d has_data = %v, want 4 logged days", payload.TotalEntries, payload.HasData)
	}
	if len(payload.CompletedCycles) != 2 {
		t.Fatalf("completed_cycles = %d entries, want the two completed cycles", len(payload.CompletedCycles))
	}
	for _, cycle := range payload.CompletedCycles {
		if cycle.Length != 28 {
			t.Fatalf("cycle %+v length = %d, want 28", cycle, cycle.Length)
		}
	}
	if payload.CompletedCycles[0].StartDate == payload.CompletedCycles[1].StartDate {
		t.Fatalf("cycles are not in chronological order: %+v", payload.CompletedCycles)
	}
	if payload.DateFrom == "" || payload.DateTo == "" {
		t.Fatalf("date_from/date_to = %q/%q, want the covered span of the logged entries", payload.DateFrom, payload.DateTo)
	}

	// cramps on one day (the doubled id collapses), custom on the same day.
	if len(payload.SymptomFrequency) != 2 {
		t.Fatalf("symptom_frequency = %+v, want two rows", payload.SymptomFrequency)
	}
	if payload.SymptomFrequency[0].Name != "cramps" || payload.SymptomFrequency[0].Count != 1 {
		t.Fatalf("frequency[0] = %+v, want cramps on 1 day", payload.SymptomFrequency[0])
	}
	if payload.SymptomFrequency[1].Name != "Custom Symptom" || payload.SymptomFrequency[1].Count != 1 {
		t.Fatalf("frequency[1] = %+v, want Custom Symptom on 1 day", payload.SymptomFrequency[1])
	}
	if payload.Disclaimer == "" {
		t.Fatal("briefing carried no medical disclaimer")
	}
	if payload.CompletedCycles == nil || payload.SymptomFrequency == nil {
		t.Fatal("list fields must never be null on the wire")
	}
}

// A narrower range excludes the cycle that ended before it opened and the one
// that started after it closed, but keeps a cycle the range runs through —
// a clinician asking about a window wants the straddling cycles too.
func TestAppointmentBriefingFiltersCyclesToTheRequestedRange(t *testing.T) {
	app, database, _ := newOnboardingTestAppWithLocation(t, time.UTC)
	user, authCookie, today := newStatsOverviewOwner(t, app, database, "briefing-range@example.com")
	seedStatsOverviewCycleHistory(t, database, user, today, 62, 34, 6)

	// from/to bounding only the most recent completed cycle.
	from := services.AddCalendarDays(today, -33, time.UTC).Format("2006-01-02")
	to := services.AddCalendarDays(today, -5, time.UTC).Format("2006-01-02")
	_, _, payload := fetchReport(t, app, authCookie, "?from="+from+"&to="+to)

	if len(payload.CompletedCycles) != 1 {
		t.Fatalf("completed_cycles = %d entries, want only the cycle the range runs through: %+v", len(payload.CompletedCycles), payload.CompletedCycles)
	}
	wantStart := services.AddCalendarDays(today, -34, time.UTC).Format("2006-01-02")
	if payload.CompletedCycles[0].StartDate != wantStart {
		t.Fatalf("cycle start = %q, want %q", payload.CompletedCycles[0].StartDate, wantStart)
	}
}

func TestAppointmentBriefingReturnsAnEmptyAnswerWith200ForAnEmptyRange(t *testing.T) {
	app, database := newOnboardingTestApp(t)
	user := createOnboardingTestUser(t, database, "briefing-empty@example.com", "StrongPass1", true)
	authCookie := loginAndExtractAuthCookie(t, app, user.Email, "StrongPass1")

	status, _, payload := fetchReport(t, app, authCookie, "?from=2026-01-01&to=2026-01-31")

	if status != http.StatusOK {
		t.Fatalf("empty range answered %d, want 200", status)
	}
	if payload.HasData || payload.TotalEntries != 0 || payload.DateFrom != "" || payload.DateTo != "" {
		t.Fatalf("empty range payload = %+v, want has_data=false and no covered span", payload)
	}
	if payload.CompletedCycles == nil || payload.SymptomFrequency == nil {
		t.Fatal("empty range must carry [] lists, not null")
	}
	if len(payload.CompletedCycles) != 0 || len(payload.SymptomFrequency) != 0 {
		t.Fatalf("empty range carried data: %+v", payload)
	}
}

func TestAppointmentBriefingDoesNotLeakAnotherOwnersData(t *testing.T) {
	app, database, _ := newOnboardingTestAppWithLocation(t, time.UTC)
	first, firstCookie, today := newStatsOverviewOwner(t, app, database, "briefing-first@example.com")
	seedStatsOverviewCycleHistory(t, database, first, today, 62, 34, 6)

	second := createOnboardingTestUser(t, database, "briefing-second@example.com", "StrongPass1", true)
	secondCookie := loginAndExtractAuthCookie(t, app, second.Email, "StrongPass1")

	_, _, first := fetchReport(t, app, firstCookie, "")
	if !first.HasData {
		t.Fatal("first owner's briefing is empty — the fixture did not build the state it claims")
	}
	status, _, secondBriefing := fetchReport(t, app, secondCookie, "")
	if status != http.StatusOK {
		t.Fatalf("second owner answered %d, want 200", status)
	}
	if secondBriefing.HasData || len(secondBriefing.CompletedCycles) != 0 {
		t.Fatalf("second owner saw another owner's briefing: %+v", secondBriefing)
	}
}

func TestAppointmentBriefingRefusesAnInvalidRangeWith400(t *testing.T) {
	app, database := newOnboardingTestApp(t)
	user := createOnboardingTestUser(t, database, "briefing-badrange@example.com", "StrongPass1", true)
	authCookie := loginAndExtractAuthCookie(t, app, user.Email, "StrongPass1")

	for name, query := range map[string]string{
		"reversed":      "?from=2026-05-10&to=2026-05-01",
		"bad from":      "?from=not-a-date",
		"bad to":        "?to=2026-13-40",
		"trailing junk": "?from=2026/05/01&to=2026-05-31",
	} {
		t.Run(name, func(t *testing.T) {
			status, _, _ := fetchReport(t, app, authCookie, query)
			if status != http.StatusBadRequest {
				t.Fatalf("query %q answered %d, want 400", query, status)
			}
		})
	}
}
