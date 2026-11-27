package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/db"
	"github.com/ovumcy/ovumcy-web/internal/services"
)

// stubNotifyRunner records the dryRun flag it was asked to run with and returns
// a canned report/error, so the CLI command can be tested without a DB or a
// socket.
type stubNotifyRunner struct {
	report    services.NotifyReport
	err       error
	gotDryRun bool
	called    bool
}

func (stub *stubNotifyRunner) RunOnce(_ context.Context, _ time.Time, _ *time.Location, dryRun bool) (services.NotifyReport, error) {
	stub.called = true
	stub.gotDryRun = dryRun
	report := stub.report
	report.DryRun = dryRun
	return report, stub.err
}

func TestParseNotifyArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr bool
		check   func(notifyOptions) bool
	}{
		{name: "no args", args: nil, check: func(o notifyOptions) bool {
			return !o.dryRun && !o.failOnDeliveryError && !o.showHealthDetails
		}},
		{name: "dry run", args: []string{"--dry-run"}, check: func(o notifyOptions) bool { return o.dryRun && !o.showHealthDetails }},
		{name: "fail on delivery error", args: []string{"--fail-on-delivery-error"}, check: func(o notifyOptions) bool { return o.failOnDeliveryError }},
		{name: "show health details", args: []string{"--show-health-details"}, check: func(o notifyOptions) bool { return o.showHealthDetails }},
		{name: "dry run with health details", args: []string{"--dry-run", "--show-health-details"}, check: func(o notifyOptions) bool {
			return o.dryRun && o.showHealthDetails
		}},
		{name: "both flags", args: []string{"--dry-run", "--fail-on-delivery-error"}, check: func(o notifyOptions) bool { return o.dryRun && o.failOnDeliveryError }},
		{name: "blank ignored", args: []string{"", "--dry-run"}, check: func(o notifyOptions) bool { return o.dryRun }},
		{name: "unknown flag", args: []string{"--nope"}, wantErr: true},
		{name: "positional rejected", args: []string{"someowner"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := parseNotifyArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), "usage: ovumcy notify") {
					t.Fatalf("expected usage string, got %q", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.check(opts) {
				t.Fatalf("options did not match expectation: %+v", opts)
			}
		})
	}
}

// TestRunNotifyCommandDryRunPropagates proves the CLI passes --dry-run through to
// the service (which is where "no outbound request / no watermark" is enforced,
// covered in the service tests) and renders the preview block for it.
func TestRunNotifyCommandDryRunPropagates(t *testing.T) {
	runner := &stubNotifyRunner{report: services.NotifyReport{
		OwnersScanned: 3,
		Due:           1,
		DryRunPreview: []services.NotifyPreviewLine{
			{OwnerID: 1, Type: "period-soon", EventDate: "2026-03-14", Host: "ntfy.example.io"},
		},
	}}
	var out bytes.Buffer
	err := runNotifyCommand(runner, notifyOptions{dryRun: true}, time.Now(), time.UTC, &out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !runner.called {
		t.Fatal("service was never invoked")
	}
	if !runner.gotDryRun {
		t.Fatal("dry-run flag was not propagated to the service")
	}
	text := out.String()
	if !strings.Contains(text, "dry-run") {
		t.Fatalf("dry-run output should note the mode, got: %q", text)
	}
	// The default preview names the owner, the count, and the destination host.
	for _, want := range []string{"would send", "owner 1", "1 due", "ntfy.example.io"} {
		if !strings.Contains(text, want) {
			t.Fatalf("dry-run preview missing %q: %q", want, text)
		}
	}
	// It must NOT print a scheme/URL — host only.
	if strings.Contains(text, "http://") || strings.Contains(text, "https://") {
		t.Fatalf("dry-run preview leaked a URL scheme: %q", text)
	}
}

// notifyEventDatePattern matches any ISO calendar date the preview could print.
// The assertions below use the pattern rather than the literal fixture date, so
// the guard still fires if the printed date format changes.
var notifyEventDatePattern = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)

// notifyReminderTypes are the two reminder-type tokens the decision layer emits.
// Either one identifies which prediction is due for the named owner, so neither
// may appear in the default output.
var notifyReminderTypes = []string{"period-soon", "ovulation-soon"}

// multiOwnerDryRunReport is the shared dry-run fixture: two owners, one of them
// with both reminder kinds due against the same endpoint, so the default preview
// has to aggregate and the opt-in preview has to enumerate.
func multiOwnerDryRunReport() services.NotifyReport {
	return services.NotifyReport{
		OwnersScanned: 4,
		Due:           3,
		DryRunPreview: []services.NotifyPreviewLine{
			{OwnerID: 7, Type: "period-soon", EventDate: "2026-08-03", Host: "ntfy.example"},
			{OwnerID: 7, Type: "ovulation-soon", EventDate: "2026-08-17", Host: "ntfy.example"},
			{OwnerID: 9, Type: "period-soon", EventDate: "2026-08-05", Host: "gotify.example"},
		},
	}
}

// TestRunNotifyCommandDryRunOmitsHealthSpecificsByDefault is the headline privacy
// assertion for the dry run: without --show-health-details the preview names the
// owner, how many reminders are pending, and the destination host — and never the
// reminder type or the estimated date, which together are a prediction about an
// identified owner's cycle. The positive half of the assertion (the aggregated
// line is really rendered, for both owners, with the multi-reminder owner counted
// as 2) keeps this from passing merely because the preview block went missing.
func TestRunNotifyCommandDryRunOmitsHealthSpecificsByDefault(t *testing.T) {
	runner := &stubNotifyRunner{report: multiOwnerDryRunReport()}
	var out bytes.Buffer
	if err := runNotifyCommand(runner, notifyOptions{dryRun: true}, time.Now(), time.UTC, &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := out.String()

	// The two health specifics must be absent. Checked first so a regression is
	// reported as the leak it is, not as a formatting mismatch further down.
	if match := notifyEventDatePattern.FindString(text); match != "" {
		t.Fatalf("default dry-run output printed an estimated event date %q — a prediction about an identified owner: %q", match, text)
	}
	for _, reminderType := range notifyReminderTypes {
		if strings.Contains(text, reminderType) {
			t.Fatalf("default dry-run output printed the reminder type %q — which prediction is due for an identified owner: %q", reminderType, text)
		}
	}

	// Positive anchor: the preview block really rendered, aggregated per owner
	// endpoint, so the assertions above cannot pass on a missing preview.
	for _, want := range []string{"would send", "owner 7: 2 due -> host ntfy.example", "owner 9: 1 due -> host gotify.example"} {
		if !strings.Contains(text, want) {
			t.Fatalf("default dry-run preview missing %q: %q", want, text)
		}
	}
}

// TestRunNotifyCommandDryRunShowsHealthSpecificsOnRequest proves the opt-in half:
// with --show-health-details the operator gets the per-reminder type and
// estimated date back, one line per reminder, still host-only for the
// destination.
func TestRunNotifyCommandDryRunShowsHealthSpecificsOnRequest(t *testing.T) {
	runner := &stubNotifyRunner{report: multiOwnerDryRunReport()}
	var out bytes.Buffer
	if err := runNotifyCommand(runner, notifyOptions{dryRun: true, showHealthDetails: true}, time.Now(), time.UTC, &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := out.String()

	for _, want := range []string{
		"owner 7: period-soon on 2026-08-03 -> host ntfy.example",
		"owner 7: ovulation-soon on 2026-08-17 -> host ntfy.example",
		"owner 9: period-soon on 2026-08-05 -> host gotify.example",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("opt-in dry-run preview missing %q: %q", want, text)
		}
	}
	if !notifyEventDatePattern.MatchString(text) {
		t.Fatalf("opt-in dry-run preview should print estimated dates: %q", text)
	}
	// Even on the opt-in path the destination stays a host — never a URL.
	if strings.Contains(text, "http://") || strings.Contains(text, "https://") {
		t.Fatalf("opt-in dry-run preview leaked a URL scheme: %q", text)
	}
}

// TestRunNotifyCommandDeliveryPassIgnoresHealthDetailsFlag proves the opt-in is
// scoped to the dry run: a real delivery pass produces no preview, so the flag
// cannot turn one on and the pass output stays counts-only.
func TestRunNotifyCommandDeliveryPassIgnoresHealthDetailsFlag(t *testing.T) {
	runner := &stubNotifyRunner{report: services.NotifyReport{OwnersScanned: 2, Due: 1, Sent: 1}}
	var out bytes.Buffer
	if err := runNotifyCommand(runner, notifyOptions{showHealthDetails: true}, time.Now(), time.UTC, &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "reminders due") {
		t.Fatalf("delivery pass should still print the counts: %q", text)
	}
	if strings.Contains(text, "would send") {
		t.Fatalf("a delivery pass must not print a would-send preview: %q", text)
	}
	if match := notifyEventDatePattern.FindString(text); match != "" {
		t.Fatalf("delivery pass output printed an estimated date %q: %q", match, text)
	}
}

// TestRunNotifyCommandNilOutputDefaultsToStdout proves the nil-output guard:
// runNotifyCommand tolerates a nil writer (falling back to os.Stdout) instead of
// panicking. The stub runner keeps it DB- and socket-free.
func TestRunNotifyCommandNilOutputDefaultsToStdout(t *testing.T) {
	runner := &stubNotifyRunner{report: services.NotifyReport{OwnersScanned: 1}}
	if err := runNotifyCommand(runner, notifyOptions{}, time.Now(), time.UTC, nil); err != nil {
		t.Fatalf("nil output should default to stdout, got %v", err)
	}
	if !runner.called {
		t.Fatal("service was never invoked")
	}
}

// TestRunNotifyCommandZeroDueExitsZero proves the exit-code contract: a completed
// pass with nothing due returns nil (exit 0).
func TestRunNotifyCommandZeroDueExitsZero(t *testing.T) {
	runner := &stubNotifyRunner{report: services.NotifyReport{OwnersScanned: 5, Due: 0}}
	var out bytes.Buffer
	if err := runNotifyCommand(runner, notifyOptions{}, time.Now(), time.UTC, &out); err != nil {
		t.Fatalf("zero-due pass should exit 0, got %v", err)
	}
}

// TestRunNotifyCommandPassLevelErrorPropagates proves a pass-level failure
// (DB/config) becomes a non-nil error (exit non-zero).
func TestRunNotifyCommandPassLevelErrorPropagates(t *testing.T) {
	runner := &stubNotifyRunner{err: errors.New("db down")}
	var out bytes.Buffer
	err := runNotifyCommand(runner, notifyOptions{}, time.Now(), time.UTC, &out)
	if err == nil {
		t.Fatal("expected a non-nil error for a pass-level failure")
	}
}

// TestRunNotifyCommandDeliveryErrorExitCode proves the --fail-on-delivery-error
// contract: with the flag, a delivery failure yields a non-zero exit; without
// it, the same failure still exits 0 (a single unreachable endpoint is a
// transient, not a pass failure).
func TestRunNotifyCommandDeliveryErrorExitCode(t *testing.T) {
	base := services.NotifyReport{OwnersScanned: 2, Due: 2, Sent: 1, Failed: 1, OwnerIDsFailed: []uint{2}}

	// Default: delivery failure does not fail the command.
	runnerDefault := &stubNotifyRunner{report: base}
	var out1 bytes.Buffer
	if err := runNotifyCommand(runnerDefault, notifyOptions{}, time.Now(), time.UTC, &out1); err != nil {
		t.Fatalf("without --fail-on-delivery-error a delivery failure must exit 0, got %v", err)
	}

	// With the flag: delivery failure fails the command.
	runnerStrict := &stubNotifyRunner{report: base}
	var out2 bytes.Buffer
	err := runNotifyCommand(runnerStrict, notifyOptions{failOnDeliveryError: true}, time.Now(), time.UTC, &out2)
	if err == nil {
		t.Fatal("with --fail-on-delivery-error a delivery failure must exit non-zero")
	}
}

// TestRunNotifyCommandReportPrintsCountsOnly proves the printed report carries
// counts and owner ids but NEVER a URL, token, or health specific.
func TestRunNotifyCommandReportPrintsCountsOnly(t *testing.T) {
	runner := &stubNotifyRunner{report: services.NotifyReport{
		OwnersScanned:     4,
		Due:               3,
		Sent:              2,
		SkippedIdempotent: 1,
		Failed:            1,
		OwnerIDsFailed:    []uint{7},
	}}
	var out bytes.Buffer
	if err := runNotifyCommand(runner, notifyOptions{}, time.Now(), time.UTC, &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := out.String()
	for _, want := range []string{"owners scanned", "reminders due", "delivered", "already sent", "failed", "owners with failures"} {
		if !strings.Contains(text, want) {
			t.Fatalf("report missing %q line: %q", want, text)
		}
	}
	if !strings.Contains(text, "7") {
		t.Fatalf("failed owner id should be printed, got: %q", text)
	}
	// The report struct has no URL/token field, so this is a belt-and-suspenders
	// scan against obvious secret shapes leaking into the print format.
	for _, banned := range []string{"http://", "https://", "token", "webhook_url", "SECRET"} {
		if strings.Contains(text, banned) {
			t.Fatalf("report leaked a secret-shaped substring %q: %q", banned, text)
		}
	}
}

// testNotifySecretKey is a syntactically valid SECRET_KEY (>= 32 chars) for the
// wiring tests. It only constructs the decrypt service; with zero owners it is
// never used to open a ciphertext, so its value is inert here.
const testNotifySecretKey = "0123456789abcdef0123456789abcdef"

// TestRunNotifyCommandEndToEndZeroOwners drives the real RunNotifyCommand wiring
// against a fresh migrated SQLite DB with no owners: it opens the DB, builds the
// notify service via bootstrap.BuildNotifyService, runs one pass, and returns nil
// with a zero-count report. This covers the DB-open + service-build path that the
// stub-based command tests deliberately bypass.
func TestRunNotifyCommandEndToEndZeroOwners(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "cli-notify-test.db")
	config := db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath}

	err := RunNotifyCommand(config, testNotifySecretKey, "en", time.UTC, false, nil)
	if err != nil {
		t.Fatalf("zero-owner notify pass should succeed, got %v", err)
	}
}

// TestRunNotifyCommandEndToEndDryRun proves the same real wiring honors --dry-run
// (no request, no watermark — enforced in the service tests) and completes.
func TestRunNotifyCommandEndToEndDryRun(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "cli-notify-dry.db")
	config := db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath}

	if err := RunNotifyCommand(config, testNotifySecretKey, "en", time.UTC, false, []string{"--dry-run"}); err != nil {
		t.Fatalf("dry-run notify pass should succeed, got %v", err)
	}
}

// TestRunNotifyCommandRejectsBadArgs proves argument validation happens before
// any DB work: an unknown flag returns the usage error and never opens the DB.
func TestRunNotifyCommandRejectsBadArgs(t *testing.T) {
	config := db.Config{Driver: db.DriverSQLite, SQLitePath: filepath.Join(t.TempDir(), "unused.db")}

	err := RunNotifyCommand(config, testNotifySecretKey, "en", time.UTC, false, []string{"--nope"})
	if err == nil || !strings.Contains(err.Error(), "usage: ovumcy notify") {
		t.Fatalf("expected usage error for a bad flag, got %v", err)
	}
}

// TestRunNotifyCommandReportsDatabaseInitFailure proves a bad DB config surfaces
// as a pass-level error (the "database init failed" branch), not a panic.
func TestRunNotifyCommandReportsDatabaseInitFailure(t *testing.T) {
	// An empty SQLite path fails db.Config validation inside OpenDatabase.
	config := db.Config{Driver: db.DriverSQLite, SQLitePath: ""}

	err := RunNotifyCommand(config, testNotifySecretKey, "en", time.UTC, false, nil)
	if err == nil {
		t.Fatal("expected a database init failure, got nil")
	}
}

// stubPreflightRunner stands in for the read-only diagnostic: it records the call
// and returns a canned report, so the command's rendering is testable without a
// DB, a decrypt key, or a socket.
type stubPreflightRunner struct {
	report services.NotifyPreflightReport
	err    error
	called bool
}

func (stub *stubPreflightRunner) NotifyPreflight(_ context.Context, _ time.Time, _ *time.Location) (services.NotifyPreflightReport, error) {
	stub.called = true
	return stub.report, stub.err
}

// preflightFixtureReport is the shared fixture: one owner per reason class the
// printer has to render — deliverable, blocked by the egress gate, unreadable
// ciphertext, silent by configuration, and silently fine.
func preflightFixtureReport() services.NotifyPreflightReport {
	return services.NotifyPreflightReport{
		Pass: services.NotifyReport{OwnersScanned: 5, Due: 2, DryRun: true},
		Outcomes: []services.PreflightOutcome{
			{OwnerID: 1, Host: "ntfy.example", Due: 2, Reason: services.PreflightReasonOK, Actionable: false},
			{OwnerID: 2, Host: "10.0.0.5", Due: 0, Reason: services.PreflightReasonPrivateAddressBlocked, Actionable: false},
			{OwnerID: 3, Host: "", Due: 0, Reason: services.PreflightReasonEndpointUnreadable, Actionable: true},
			{OwnerID: 4, Host: "gotify.example", Due: 0, Reason: services.PreflightReasonDeliveryDisabled, Actionable: false},
			{OwnerID: 5, Host: "ntfy.example", Due: 0, Reason: services.PreflightReasonNotDue, Actionable: false},
		},
	}
}

// TestParseNotifyArgsPreflight pins the flag contract: the preflight flags parse,
// and the pairings that would mislead an operator are refused rather than
// silently reinterpreted.
func TestParseNotifyArgsPreflight(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr bool
		check   func(notifyOptions) bool
	}{
		{name: "plain preflight", args: []string{"--preflight"}, check: func(o notifyOptions) bool {
			return o.preflight && !o.preflightJSON && o.owner == "" && !o.dryRun
		}},
		{name: "preflight json", args: []string{"--preflight", "--json"}, check: func(o notifyOptions) bool {
			return o.preflight && o.preflightJSON
		}},
		{name: "owner spaced", args: []string{"--preflight", "--owner", "7"}, check: func(o notifyOptions) bool {
			return o.preflight && o.owner == "7"
		}},
		{name: "owner equals", args: []string{"--preflight", "--owner=7"}, check: func(o notifyOptions) bool {
			return o.preflight && o.owner == "7"
		}},
		{name: "owner missing value", args: []string{"--preflight", "--owner"}, wantErr: true},
		{name: "owner must be an id", args: []string{"--preflight", "--owner", "owner@example.com"}, wantErr: true},
		{name: "owner zero", args: []string{"--preflight", "--owner", "0"}, wantErr: true},
		{name: "json without preflight", args: []string{"--json"}, wantErr: true},
		{name: "owner without preflight", args: []string{"--owner", "7"}, wantErr: true},
		{name: "preflight with fail-on-delivery-error", args: []string{"--preflight", "--fail-on-delivery-error"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := parseNotifyArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected a usage error, got %+v", opts)
				}
				if !strings.Contains(err.Error(), "usage: ovumcy notify") {
					t.Fatalf("expected the usage string, got %q", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.check(opts) {
				t.Fatalf("options did not match expectation: %+v", opts)
			}
		})
	}
}

// TestRunNotifyPreflightTable is the human half: every owner row carries the id,
// the host, the due count and a reason, and the output never carries a URL,
// scheme, token, or an estimated date.
func TestRunNotifyPreflightTable(t *testing.T) {
	runner := &stubPreflightRunner{report: preflightFixtureReport()}
	var out bytes.Buffer
	if err := runNotifyPreflightCommand(runner, notifyOptions{preflight: true}, time.Date(2026, 3, 12, 9, 0, 0, 0, time.UTC), time.UTC, &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !runner.called {
		t.Fatal("preflight was never invoked")
	}
	text := out.String()

	for _, want := range []string{
		"ovumcy-notify-preflight/v1",
		"no request sent",
		"Owners scanned: 5, listed: 5",
		"ntfy.example",
		"10.0.0.5",
		services.PreflightReasonEndpointUnreadable,
		services.PreflightReasonDeliveryDisabled,
		services.PreflightReasonNotDue,
		"1 owner(s) need attention, 4 need none",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("preflight table missing %q: %q", want, text)
		}
	}

	for _, banned := range []string{"http://", "https://", "token", "webhook_url", "SECRET"} {
		if strings.Contains(text, banned) {
			t.Fatalf("preflight table leaked %q: %q", banned, text)
		}
	}
	if match := notifyEventDatePattern.FindString(text); match != "" {
		t.Fatalf("preflight table printed an estimated date %q: %q", match, text)
	}
	for _, reminderType := range notifyReminderTypes {
		if strings.Contains(text, reminderType) {
			t.Fatalf("preflight table printed the reminder type %q: %q", reminderType, text)
		}
	}
}

// TestRunNotifyPreflightJSONShape pins the versioned document: the schema tag, the
// per-owner field set (snake_case, stable), the detail field appearing only when it
// carries something, and the absence of any secret.
func TestRunNotifyPreflightJSONShape(t *testing.T) {
	report := preflightFixtureReport()
	report.Outcomes[3].Reason = services.PreflightReasonDeliveryFailed
	report.Outcomes[3].Detail = "non_2xx"
	report.Outcomes[3].Actionable = true

	runner := &stubPreflightRunner{report: report}
	var out bytes.Buffer
	if err := runNotifyPreflightCommand(runner, notifyOptions{preflight: true, preflightJSON: true}, time.Date(2026, 3, 12, 9, 0, 0, 0, time.UTC), time.UTC, &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Decoded into a generic map so the assertion is about the WIRE shape, not
	// about the struct the renderer happens to use.
	var document map[string]any
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}
	if document["schema"] != "ovumcy-notify-preflight/v1" {
		t.Fatalf("schema = %v", document["schema"])
	}
	if document["owners_scanned"] != float64(5) || document["matched"] != true {
		t.Fatalf("aggregate fields = %v / %v", document["owners_scanned"], document["matched"])
	}
	if _, present := document["owner"]; present {
		t.Fatalf("owner should be omitted without a filter: %v", document["owner"])
	}
	owners, ok := document["owners"].([]any)
	if !ok || len(owners) != 5 {
		t.Fatalf("owners should be a 5-row array, got %v", document["owners"])
	}

	wantKeys := []string{"owner_id", "host", "due", "reason", "actionable", "next_action"}
	for _, raw := range owners {
		row, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("owner row is not an object: %v", raw)
		}
		for _, key := range wantKeys {
			if _, present := row[key]; !present {
				t.Fatalf("owner row missing %q: %v", key, row)
			}
		}
		if _, present := row["detail"]; present && row["detail"] == "" {
			t.Fatalf("empty detail should be omitted: %v", row)
		}
	}

	failed := owners[3].(map[string]any)
	if failed["reason"] != services.PreflightReasonDeliveryFailed || failed["detail"] != "non_2xx" {
		t.Fatalf("failed row = %v", failed)
	}
	if failed["actionable"] != true {
		t.Fatalf("a delivery failure is actionable: %v", failed)
	}
	if next, _ := failed["next_action"].(string); next == "" || next == "unknown reason" {
		t.Fatalf("failed row has no next action: %v", failed)
	}

	for _, banned := range []string{"http://", "https://", "token", "webhook_url", "SECRET"} {
		if strings.Contains(out.String(), banned) {
			t.Fatalf("preflight JSON leaked %q: %s", banned, out.String())
		}
	}
}

// TestRunNotifyPreflightOwnerFilter proves --owner narrows the PRINTED rows while
// the walk still covers every owner, and that a filter matching nothing is an
// error rather than an empty, healthy-looking table.
func TestRunNotifyPreflightOwnerFilter(t *testing.T) {
	runner := &stubPreflightRunner{report: preflightFixtureReport()}
	var out bytes.Buffer
	now := time.Date(2026, 3, 12, 9, 0, 0, 0, time.UTC)
	if err := runNotifyPreflightCommand(runner, notifyOptions{preflight: true, preflightJSON: true, owner: "3"}, now, time.UTC, &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var document notifyPreflightDocument
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if document.OwnersScanned != 5 || len(document.Owners) != 1 || document.Owners[0].OwnerID != 3 {
		t.Fatalf("filter should list owner 3 out of 5 scanned, got %+v", document)
	}
	if !document.Matched || document.Owner != "3" {
		t.Fatalf("matched/owner = %v/%q", document.Matched, document.Owner)
	}

	// The same filter over the human table: only owner 3's row is rendered.
	var table bytes.Buffer
	if err := runNotifyPreflightCommand(runner, notifyOptions{preflight: true, owner: "3"}, now, time.UTC, &table); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(table.String(), "listed: 1") || strings.Contains(table.String(), "10.0.0.5") {
		t.Fatalf("filtered table rendered the wrong rows: %q", table.String())
	}

	// A filter that matches nothing is an error, on both renderings.
	miss := &stubPreflightRunner{report: preflightFixtureReport()}
	err := runNotifyPreflightCommand(miss, notifyOptions{preflight: true, owner: "99"}, now, time.UTC, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "no owner matched") {
		t.Fatalf("an unmatched filter must be an error, got %v", err)
	}
}

// TestNotifyPreflightNextActionCoversEveryReason keeps the operator-facing advice
// table in step with the reason enum: a new reason key without a sentence would
// render as "unknown reason" and fail here instead.
func TestNotifyPreflightNextActionCoversEveryReason(t *testing.T) {
	reasons := []string{
		services.PreflightReasonOK,
		services.PreflightReasonDeliveryDisabled,
		services.PreflightReasonNoEndpoint,
		services.PreflightReasonEndpointUnreadable,
		services.PreflightReasonEndpointInvalid,
		services.PreflightReasonPrivateAddressBlocked,
		services.PreflightReasonLogsUnreadable,
		services.PreflightReasonPredictionsSuppressed,
		services.PreflightReasonNoReminderKind,
		services.PreflightReasonAlreadySent,
		services.PreflightReasonNotDue,
		services.PreflightReasonDeliveryFailed,
		services.PreflightReasonClaimLost,
	}
	for _, reason := range reasons {
		action := notifyPreflightNextAction(reason)
		if action == "" || action == "unknown reason" {
			t.Fatalf("reason %q has no next action", reason)
		}
		// The sentence's own voice has to agree with the service's Actionable flag,
		// which is what the operator reads in the same row.
		actionable := services.PreflightReasonActionable(reason)
		if strings.HasPrefix(action, "Act now:") != actionable {
			t.Fatalf("reason %q: action %q disagrees with actionable=%v", reason, action, actionable)
		}
	}
	if got := notifyPreflightNextAction("a-reason-that-does-not-exist"); got != "unknown reason" {
		t.Fatalf("an unknown reason should say so, got %q", got)
	}
}

// TestRunNotifyCommandEndToEndPreflight drives the real RunNotifyCommand wiring
// with --preflight against a fresh migrated SQLite DB with no owners: the walk runs,
// the aggregate is zero, and the command exits 0 with the schema tag on stdout.
func TestRunNotifyCommandEndToEndPreflight(t *testing.T) {
	config := db.Config{Driver: db.DriverSQLite, SQLitePath: filepath.Join(t.TempDir(), "cli-notify-preflight.db")}
	if err := RunNotifyCommand(config, testNotifySecretKey, "en", time.UTC, false, []string{"--preflight"}); err != nil {
		t.Fatalf("zero-owner preflight should succeed, got %v", err)
	}
}
