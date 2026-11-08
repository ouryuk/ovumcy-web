package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/ovumcy/ovumcy-web/internal/services"
)

// fetchCycleHistory drives the real authenticated request path so the contract
// exercised here is the transport's, not a constructed DTO's.
func fetchCycleHistory(t *testing.T, app *fiber.App, authCookie string) []CycleHistoryEntryResponse {
	t.Helper()

	request := httptest.NewRequest(http.MethodGet, "/api/v1/stats/cycles", nil)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Language", "en")
	request.Header.Set("Cookie", joinCookieHeader(authCookie, timezoneCookieName+"=UTC"))
	request.Header.Set(timezoneHeaderName, "UTC")

	response := mustAppResponse(t, app, request)
	assertStatusCode(t, response, http.StatusOK)
	body := mustReadBodyString(t, response.Body)

	payload := []CycleHistoryEntryResponse{}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("decode cycle history payload: %v\n%s", err, body)
	}
	return payload
}

func TestCycleHistoryListsEachCompletedCycleInChronologicalOrder(t *testing.T) {
	app, database, _ := newOnboardingTestAppWithLocation(t, time.UTC)
	user, authCookie, today := newStatsOverviewOwner(t, app, database, "cycle-history@example.com")
	seedStatsOverviewCycleHistory(t, database, user, today, 62, 34, 6)

	payload := fetchCycleHistory(t, app, authCookie)

	// The start six days ago opens the running cycle. It must not appear beside
	// cycles whose end and length are known.
	if len(payload) != 2 {
		t.Fatalf("expected two completed cycles (the running cycle is open), got %d: %+v", len(payload), payload)
	}

	wantStarts := []string{
		services.AddCalendarDays(today, -62, time.UTC).Format("2006-01-02"),
		services.AddCalendarDays(today, -34, time.UTC).Format("2006-01-02"),
	}
	wantEnds := []string{
		services.AddCalendarDays(today, -35, time.UTC).Format("2006-01-02"),
		services.AddCalendarDays(today, -7, time.UTC).Format("2006-01-02"),
	}
	for index, entry := range payload {
		if entry.StartDate != wantStarts[index] {
			t.Fatalf("entry %d start_date = %q, want %q", index, entry.StartDate, wantStarts[index])
		}
		if entry.EndDate != wantEnds[index] {
			t.Fatalf("entry %d end_date = %q, want %q", index, entry.EndDate, wantEnds[index])
		}
		if entry.Length != 28 {
			t.Fatalf("entry %d length = %d, want 28", index, entry.Length)
		}
		if entry.OvulationDate == nil {
			t.Fatalf("entry %d has no ovulation_date; a 28-day cycle is long enough to estimate one", index)
		}
		if !entry.OvulationEstimated || entry.OvulationConfirmed {
			t.Fatalf("entry %d flags = confirmed:%v estimated:%v, want estimated only", index, entry.OvulationConfirmed, entry.OvulationEstimated)
		}
	}
}

func TestCycleHistoryReturnsAnEmptyListAnd200WithoutLogs(t *testing.T) {
	app, database := newOnboardingTestApp(t)
	user := createOnboardingTestUser(t, database, "cycle-history-empty@example.com", "StrongPass1", true)
	authCookie := loginAndExtractAuthCookie(t, app, user.Email, "StrongPass1")

	request := httptest.NewRequest(http.MethodGet, "/api/v1/stats/cycles", nil)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Language", "en")
	request.Header.Set("Cookie", joinCookieHeader(authCookie, timezoneCookieName+"=UTC"))
	request.Header.Set(timezoneHeaderName, "UTC")

	response := mustAppResponse(t, app, request)
	assertStatusCode(t, response, http.StatusOK)
	body := mustReadBodyString(t, response.Body)

	payload := []CycleHistoryEntryResponse{}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("decode empty cycle history: %v\n%s", err, body)
	}
	if payload == nil {
		t.Fatal("an owner with no logs received null, want []")
	}
	if len(payload) != 0 {
		t.Fatalf("expected no history, got %+v", payload)
	}
}

func TestCycleHistoryDoesNotLeakAnotherOwnersCycles(t *testing.T) {
	app, database, _ := newOnboardingTestAppWithLocation(t, time.UTC)
	first, firstCookie, today := newStatsOverviewOwner(t, app, database, "cycle-history-first@example.com")
	seedStatsOverviewCycleHistory(t, database, first, today, 62, 34, 6)

	second := createOnboardingTestUser(t, database, "cycle-history-second@example.com", "StrongPass1", true)
	secondCookie := loginAndExtractAuthCookie(t, app, second.Email, "StrongPass1")

	if payload := fetchCycleHistory(t, app, firstCookie); len(payload) != 2 {
		t.Fatalf("first owner's history has %d entries, wanted 2 — the fixture did not build the state it claims", len(payload))
	}
	if payload := fetchCycleHistory(t, app, secondCookie); len(payload) != 0 {
		t.Fatalf("second owner saw %+v — another owner's cycle history leaked", payload)
	}
}
