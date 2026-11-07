package api

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/models"
	"github.com/ovumcy/ovumcy-web/internal/services"
)

func csvImportTestBody(t *testing.T, rows [][]string) string {
	t.Helper()

	var output bytes.Buffer
	writer := csv.NewWriter(&output)
	if err := writer.Write(services.ExportCSVHeaders()); err != nil {
		t.Fatalf("write csv header: %v", err)
	}
	for _, row := range rows {
		if err := writer.Write(row); err != nil {
			t.Fatalf("write csv row: %v", err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		t.Fatalf("flush csv: %v", err)
	}
	return output.String()
}

func csvImportTestRow(t *testing.T, date string, period string, notes string) []string {
	t.Helper()

	headers := services.ExportCSVHeaders()
	row := make([]string, len(headers))
	for i := range row {
		row[i] = "No"
	}
	for i, header := range headers {
		switch header {
		case "Date":
			row[i] = date
		case "Period":
			row[i] = period
		case "Flow":
			if period == "Yes" {
				row[i] = "Medium"
			}
		case "Notes":
			row[i] = notes
		}
	}
	return row
}

func newCSVImportRequest(ctx settingsSecurityTestContext, body string, withCSRF bool) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/imports/csv", strings.NewReader(body))
	request.Header.Set("Content-Type", "text/csv")
	request.Header.Set("Accept", "application/json")
	if withCSRF {
		request.Header.Set("Cookie", settingsCookieHeader(ctx.authCookie, ctx.csrfCookie))
		request.Header.Set("X-CSRF-Token", ctx.csrfToken)
		return request
	}
	request.Header.Set("Cookie", ctx.authCookie)
	return request
}

// TestImportCSVRejectsMissingCSRF pins the defense-in-depth invariant for the
// CSV restore too: it is state-mutating, so the global CSRF middleware must
// reject a token-less request before any row is written.
func TestImportCSVRejectsMissingCSRF(t *testing.T) {
	t.Parallel()

	ctx := newSettingsSecurityTestContext(t, "import-csv-csrf-missing@example.com")
	body := csvImportTestBody(t, [][]string{csvImportTestRow(t, "2026-07-01", "Yes", "")})

	response, err := ctx.app.Test(newCSVImportRequest(ctx, body, false), testConfigNoTimeout)
	if err != nil {
		t.Fatalf("csv import without csrf failed: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 without csrf, got %d", response.StatusCode)
	}
	if rows := countImportedDays(t, ctx); rows != 0 {
		t.Fatalf("expected the CSRF refusal to write no days, got %d", rows)
	}
}

// TestImportCSVSucceedsWithCSRF is the valid-token happy path: two rows restore
// two days and the additive counts come back on the same wire shape as the JSON
// restore.
func TestImportCSVSucceedsWithCSRF(t *testing.T) {
	t.Parallel()

	ctx := newSettingsSecurityTestContext(t, "import-csv-valid@example.com")
	body := csvImportTestBody(t, [][]string{
		csvImportTestRow(t, "2026-07-01", "Yes", "first"),
		csvImportTestRow(t, "2026-07-02", "No", "second"),
	})

	response, err := ctx.app.Test(newCSVImportRequest(ctx, body, true), testConfigNoTimeout)
	if err != nil {
		t.Fatalf("csv import with csrf failed: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.StatusCode)
	}
	var result struct {
		OK       bool `json:"ok"`
		Added    int  `json:"added"`
		Skipped  int  `json:"skipped"`
		Rejected int  `json:"rejected"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode import result: %v", err)
	}
	if !result.OK || result.Added != 2 || result.Skipped != 0 || result.Rejected != 0 {
		t.Fatalf("expected {ok:true added:2 skipped:0 rejected:0}, got %+v", result)
	}
	if rows := countImportedDays(t, ctx); rows != 2 {
		t.Fatalf("expected 2 persisted days, got %d", rows)
	}
}

// TestImportCSVIsAdditiveAndOwnerScoped pins the two boundaries that make the
// restore safe: an existing day for the session owner is skipped untouched, and
// another account's row for the same calendar day is neither read nor changed.
func TestImportCSVIsAdditiveAndOwnerScoped(t *testing.T) {
	t.Parallel()

	ctx := newSettingsSecurityTestContext(t, "import-csv-owner@example.com")
	other := createOnboardingTestUser(t, ctx.database, "import-csv-other@example.com", "StrongPass1", true)

	existing := models.DailyLog{
		UserID: ctx.user.ID, Date: time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC),
		IsPeriod: true, Flow: models.FlowLight, Notes: "owner original", CycleFactorKeys: []string{},
	}
	otherDay := models.DailyLog{
		UserID: other.ID, Date: time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC),
		IsPeriod: true, Flow: models.FlowHeavy, Notes: "other original", CycleFactorKeys: []string{},
	}
	if err := ctx.database.Create(&existing).Error; err != nil {
		t.Fatalf("create owner day: %v", err)
	}
	if err := ctx.database.Create(&otherDay).Error; err != nil {
		t.Fatalf("create other day: %v", err)
	}

	body := csvImportTestBody(t, [][]string{
		csvImportTestRow(t, "2026-09-01", "Yes", "incoming"),
		csvImportTestRow(t, "2026-09-02", "No", "new"),
	})
	response, err := ctx.app.Test(newCSVImportRequest(ctx, body, true), testConfigNoTimeout)
	if err != nil {
		t.Fatalf("csv import failed: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.StatusCode)
	}
	var result struct {
		Added    int `json:"added"`
		Skipped  int `json:"skipped"`
		Rejected int `json:"rejected"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode import result: %v", err)
	}
	if result.Added != 1 || result.Skipped != 1 || result.Rejected != 0 {
		t.Fatalf("expected {added:1 skipped:1 rejected:0}, got %+v", result)
	}

	var storedOwner models.DailyLog
	if err := ctx.database.Where("user_id = ? AND date = ?", ctx.user.ID, time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)).First(&storedOwner).Error; err != nil {
		t.Fatalf("reload owner day: %v", err)
	}
	if storedOwner.Notes != "owner original" || storedOwner.Flow != models.FlowLight {
		t.Fatalf("the owner's existing day was overwritten: %+v", storedOwner)
	}

	var storedOther models.DailyLog
	if err := ctx.database.Where("user_id = ? AND date = ?", other.ID, time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)).First(&storedOther).Error; err != nil {
		t.Fatalf("reload other day: %v", err)
	}
	if storedOther.Notes != "other original" || storedOther.Flow != models.FlowHeavy {
		t.Fatalf("another account's day was changed: %+v", storedOther)
	}
}

// TestImportCSVRejectsMalformedFile maps an unacceptable CSV shape onto the
// same stable 400 key the JSON restore uses.
func TestImportCSVRejectsMalformedFile(t *testing.T) {
	t.Parallel()

	ctx := newSettingsSecurityTestContext(t, "import-csv-malformed@example.com")
	body := strings.Join(services.ExportCSVHeaders()[:len(services.ExportCSVHeaders())-1], ",") + "\n"

	response, err := ctx.app.Test(newCSVImportRequest(ctx, body, true), testConfigNoTimeout)
	if err != nil {
		t.Fatalf("csv import malformed failed: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", response.StatusCode)
	}
	if key := errorKeyFromEnvelope(t, response.Body); key != "invalid import file" {
		t.Fatalf("expected the stable invalid import file key, got %q", key)
	}
}

// TestImportCSVSuccessIsAuditedAsHealthDataMutation pins the CSV handler to the
// same typed mutation path as the JSON restore, counts included.
func TestImportCSVSuccessIsAuditedAsHealthDataMutation(t *testing.T) {
	ctx := newSettingsSecurityTestContextWithOptions(t, "import-csv-audit@example.com", onboardingTestAppOptions{enableCSRF: true, auditLogEnabled: true})
	body := csvImportTestBody(t, [][]string{
		csvImportTestRow(t, "2026-07-01", "Yes", ""),
		csvImportTestRow(t, "2026-07-02", "No", ""),
	})

	response, logOutput := captureAuditedRequest(t, ctx.app, newCSVImportRequest(ctx, body, true))
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for an accepted restore, got %d", response.StatusCode)
	}

	line := securityEventLine(t, logOutput, "data.import", "success")
	if !strings.Contains(line, `domain="health_data"`) {
		t.Fatalf("expected the CSV restore to be tagged domain=\"health_data\", got %q", line)
	}
	if !strings.Contains(line, `target="day_entry"`) {
		t.Fatalf("expected the CSV restore to carry target=\"day_entry\", got %q", line)
	}
	for _, count := range []string{`added="2"`, `skipped="0"`, `rejected="0"`} {
		if !strings.Contains(line, count) {
			t.Fatalf("expected the CSV restore audit line to carry %s, got %q", count, line)
		}
	}
}
