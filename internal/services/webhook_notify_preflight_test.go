package services

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/models"
)

// preflightFixture builds a one-owner preflight over the shared stubs, so every
// reason case is expressed as "a record, its logs, a decryptor and a deliverer"
// rather than as its own harness.
type preflightFixture struct {
	record    models.WebhookNotifyRecord
	logs      stubLogReader
	decryptor stubDecryptor
	deliverer WebhookDeliverer
	block     bool
}

// preflightNow is the fixed pass clock: 2026-03-12. dueRecord(…, 26) then puts the
// next period two days out, inside the record's three-day lead window.
var preflightNow = time.Date(2026, 3, 12, 9, 0, 0, 0, time.UTC)

// run executes one preflight and returns the single owner's outcome.
func (fixture preflightFixture) run(t *testing.T) PreflightOutcome {
	t.Helper()
	repo := &stubNotifyRepo{records: []models.WebhookNotifyRecord{fixture.record}}
	service := newTestNotifyService(repo, fixture.logs, fixture.decryptor, fixture.deliverer)
	service.WithPrivateAddressGate(fixture.block)

	report, err := service.NotifyPreflight(context.Background(), preflightNow, time.UTC)
	if err != nil {
		t.Fatalf("NotifyPreflight: %v", err)
	}
	if len(report.Outcomes) != 1 {
		t.Fatalf("expected exactly one outcome for one owner, got %d", len(report.Outcomes))
	}
	// A preflight is compute-only by construction: no claim, no mark, no delivery.
	if report.Pass.OwnersScanned != 1 || !report.Pass.DryRun {
		t.Fatalf("preflight pass should scan one owner in dry-run mode, got %+v", report.Pass)
	}
	if len(repo.writes()) != 0 || len(repo.released()) != 0 || len(repo.marks()) != 0 {
		t.Fatalf("preflight touched a watermark or delivery mark: writes=%d releases=%d marks=%d",
			len(repo.writes()), len(repo.released()), len(repo.marks()))
	}
	return report.Outcomes[0]
}

// TestNotifyPreflightClassifiesEveryOwnerReason is the classification table: one
// case per reason key the walk can produce, each asserting the reason, the host,
// the due count and the actionable flag together — because those four are what the
// operator sees and what a JSON consumer branches on.
func TestNotifyPreflightClassifiesEveryOwnerReason(t *testing.T) {
	due := func() models.WebhookNotifyRecord {
		return dueRecord(1, "https://ntfy.example/topic", preflightNow, 26)
	}
	dueLogs := func(record models.WebhookNotifyRecord) stubLogReader {
		return stubLogReader{byUser: map[uint][]models.DailyLog{1: {periodStartLog(1, *record.LastPeriodStart)}}}
	}

	cases := []struct {
		name       string
		fixture    func() preflightFixture
		wantReason string
		wantHost   string
		wantDue    int
		wantAction bool
	}{
		{
			name: "due reminder would be delivered",
			fixture: func() preflightFixture {
				record := due()
				return preflightFixture{record: record, logs: dueLogs(record), decryptor: stubDecryptor{}, deliverer: &stubDeliverer{}}
			},
			wantReason: PreflightReasonOK, wantHost: "ntfy.example", wantDue: 1,
		},
		{
			name: "delivery disabled outranks everything downstream",
			fixture: func() preflightFixture {
				record := due()
				record.WebhookEnabled = false
				return preflightFixture{record: record, logs: dueLogs(record), decryptor: stubDecryptor{failFor: map[uint]bool{1: true}}, deliverer: &stubDeliverer{}}
			},
			wantReason: PreflightReasonDeliveryDisabled, wantHost: "", wantDue: 0,
		},
		{
			name: "enabled without an endpoint",
			fixture: func() preflightFixture {
				record := due()
				record.WebhookURL = "   "
				return preflightFixture{record: record, logs: dueLogs(record), decryptor: stubDecryptor{}, deliverer: &stubDeliverer{}}
			},
			wantReason: PreflightReasonNoEndpoint, wantHost: "", wantDue: 0,
		},
		{
			name: "unreadable ciphertext after a key rotation",
			fixture: func() preflightFixture {
				record := due()
				return preflightFixture{record: record, logs: dueLogs(record), decryptor: stubDecryptor{failFor: map[uint]bool{1: true}}, deliverer: &stubDeliverer{}}
			},
			wantReason: PreflightReasonEndpointUnreadable, wantHost: "", wantDue: 0, wantAction: true,
		},
		{
			name: "endpoint without a host",
			fixture: func() preflightFixture {
				record := dueRecord(1, "http://:8080/hook", preflightNow, 26)
				return preflightFixture{record: record, logs: dueLogs(record), decryptor: stubDecryptor{}, deliverer: &stubDeliverer{}}
			},
			wantReason: PreflightReasonEndpointInvalid, wantHost: "", wantDue: 0, wantAction: true,
		},
		{
			name: "non-http scheme",
			fixture: func() preflightFixture {
				record := dueRecord(1, "ftp://ntfy.example/topic", preflightNow, 26)
				return preflightFixture{record: record, logs: dueLogs(record), decryptor: stubDecryptor{}, deliverer: &stubDeliverer{}}
			},
			wantReason: PreflightReasonEndpointInvalid, wantHost: "ntfy.example", wantDue: 0, wantAction: true,
		},
		{
			name: "private literal with the egress gate on",
			fixture: func() preflightFixture {
				record := dueRecord(1, "http://10.0.0.5:8080/topic", preflightNow, 26)
				return preflightFixture{record: record, logs: dueLogs(record), decryptor: stubDecryptor{}, deliverer: &stubDeliverer{}, block: true}
			},
			wantReason: PreflightReasonPrivateAddressBlocked, wantHost: "10.0.0.5", wantDue: 1,
		},
		{
			name: "same private literal with the gate off is deliverable",
			fixture: func() preflightFixture {
				record := dueRecord(1, "http://10.0.0.5:8080/topic", preflightNow, 26)
				return preflightFixture{record: record, logs: dueLogs(record), decryptor: stubDecryptor{}, deliverer: &stubDeliverer{}}
			},
			wantReason: PreflightReasonOK, wantHost: "10.0.0.5", wantDue: 1,
		},
		{
			name: "day logs unreadable",
			fixture: func() preflightFixture {
				record := due()
				return preflightFixture{record: record, logs: stubLogReader{err: errors.New("db down")}, decryptor: stubDecryptor{}, deliverer: &stubDeliverer{}}
			},
			wantReason: PreflightReasonLogsUnreadable, wantHost: "ntfy.example", wantDue: 0, wantAction: true,
		},
		{
			name: "both reminder kinds switched off",
			fixture: func() preflightFixture {
				record := due()
				record.WebhookNotifyPeriod = false
				record.WebhookNotifyOvulation = false
				return preflightFixture{record: record, logs: dueLogs(record), decryptor: stubDecryptor{}, deliverer: &stubDeliverer{}}
			},
			wantReason: PreflightReasonNoReminderKind, wantHost: "ntfy.example", wantDue: 0,
		},
		{
			name: "the watermark already covered this cycle",
			fixture: func() preflightFixture {
				record := due()
				anchor := record.LastPeriodStart.AddDate(0, 0, 28)
				record.WebhookPeriodLastSentCycleStart = &anchor
				return preflightFixture{record: record, logs: dueLogs(record), decryptor: stubDecryptor{}, deliverer: &stubDeliverer{}}
			},
			wantReason: PreflightReasonAlreadySent, wantHost: "ntfy.example", wantDue: 0,
		},
		{
			name: "nothing inside the lead window",
			fixture: func() preflightFixture {
				// Last period 10 days ago: the next period is 18 days out, far past
				// the three-day lead window.
				record := dueRecord(1, "https://ntfy.example/topic", preflightNow, 10)
				return preflightFixture{record: record, logs: dueLogs(record), decryptor: stubDecryptor{}, deliverer: &stubDeliverer{}}
			},
			wantReason: PreflightReasonNotDue, wantHost: "ntfy.example", wantDue: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome := tc.fixture().run(t)
			if outcome.Reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q (outcome %+v)", outcome.Reason, tc.wantReason, outcome)
			}
			if outcome.Code() != tc.wantReason {
				t.Fatalf("code = %q, want the bare reason %q", outcome.Code(), tc.wantReason)
			}
			if outcome.Host != tc.wantHost {
				t.Fatalf("host = %q, want %q", outcome.Host, tc.wantHost)
			}
			if outcome.Due != tc.wantDue {
				t.Fatalf("due = %d, want %d", outcome.Due, tc.wantDue)
			}
			if outcome.Actionable != tc.wantAction {
				t.Fatalf("actionable = %v, want %v for %q", outcome.Actionable, tc.wantAction, tc.wantReason)
			}
		})
	}
}

// TestNotifyPreflightSuppressedPredictions proves the medical-safety gate is
// reported as its own reason rather than as "not due": an owner in
// unpredictable-cycle mode is not waiting on a cycle, their predictions are
// withheld on purpose.
func TestNotifyPreflightSuppressedPredictions(t *testing.T) {
	record := dueRecord(1, "https://ntfy.example/topic", preflightNow, 26)
	record.UnpredictableCycle = true
	outcome := preflightFixture{
		record:    record,
		logs:      stubLogReader{byUser: map[uint][]models.DailyLog{1: {periodStartLog(1, *record.LastPeriodStart)}}},
		decryptor: stubDecryptor{},
		deliverer: &stubDeliverer{},
	}.run(t)

	if outcome.Reason != PreflightReasonPredictionsSuppressed {
		t.Fatalf("reason = %q, want %q", outcome.Reason, PreflightReasonPredictionsSuppressed)
	}
	if outcome.Actionable {
		t.Fatal("a suppressed prediction is not an operator-fixable defect")
	}
}

// TestNotifyPreflightLeaksNoSecret is the output-discipline guard for the service
// half: an outcome carries the destination HOST and nothing else from the URL, no
// matter where the URL's token lives.
func TestNotifyPreflightLeaksNoSecret(t *testing.T) {
	const token = "tk_7f3a-notify-token"
	record := dueRecord(1, "https://user:"+token+"@ntfy.example:8443/v1/topic?auth="+token, preflightNow, 26)
	outcome := preflightFixture{
		record:    record,
		logs:      stubLogReader{byUser: map[uint][]models.DailyLog{1: {periodStartLog(1, *record.LastPeriodStart)}}},
		decryptor: stubDecryptor{},
		deliverer: &stubDeliverer{},
	}.run(t)

	if outcome.Host != "ntfy.example" {
		t.Fatalf("host = %q, want the bare hostname", outcome.Host)
	}
	rendered := outcome.Reason + "|" + outcome.Detail + "|" + outcome.Host + "|" + outcome.Code()
	for _, banned := range []string{token, "user:", "8443", "/v1/topic", "auth=", "https"} {
		if strings.Contains(rendered, banned) {
			t.Fatalf("outcome leaked %q: %q", banned, rendered)
		}
	}
}

// TestClassifyNotifyDeliveryError pins the delivery-failure vocabulary to the keys
// the deliverer already logs, so a preflight of a real pass explains a failure in
// the same words the service log uses.
func TestClassifyNotifyDeliveryError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil", err: nil, want: ""},
		{name: "redirect refused", err: errWebhookRedirect, want: "redirect_refused"},
		{name: "private address at dial time", err: errors.New(`webhook delivery to host "x" failed: private_address_blocked`), want: PreflightReasonPrivateAddressBlocked},
		{name: "non-2xx", err: errors.New(`webhook delivery to host "x" returned status 503`), want: "non_2xx"},
		{name: "scheme refused by the deliverer", err: ErrWebhookDeliveryURLScheme, want: PreflightReasonEndpointInvalid},
		{name: "anything else is a transport error", err: errors.New(`webhook delivery to host "x" failed: transport_error`), want: "transport_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyNotifyDeliveryError(tc.err); got != tc.want {
				t.Fatalf("classifyNotifyDeliveryError(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// TestClassifyPreflightEndpoint pins the shape half of the classification directly,
// so the table test above cannot pass by accident when the two disagree.
func TestClassifyPreflightEndpoint(t *testing.T) {
	cases := []struct {
		name  string
		url   string
		block bool
		want  string
	}{
		{name: "public https", url: "https://ntfy.example/topic", want: ""},
		{name: "public http with port", url: "http://ntfy.example:8080/topic", want: ""},
		{name: "private hostname is not resolved here", url: "http://ntfy.lan/topic", block: true, want: ""},
		{name: "unparseable", url: "http://%zz", want: PreflightReasonEndpointInvalid},
		{name: "scheme", url: "ftp://ntfy.example/topic", want: PreflightReasonEndpointInvalid},
		{name: "port-only authority", url: "http://:8080/hook", want: PreflightReasonEndpointInvalid},
		{name: "out-of-range port", url: "http://ntfy.example:70000/hook", want: PreflightReasonEndpointInvalid},
		{name: "loopback literal, gate on", url: "http://127.0.0.1:8080/topic", block: true, want: PreflightReasonPrivateAddressBlocked},
		{name: "loopback literal, gate off", url: "http://127.0.0.1:8080/topic", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyPreflightEndpoint(tc.url, tc.block); got != tc.want {
				t.Fatalf("classifyPreflightEndpoint(%q, %v) = %q, want %q", tc.url, tc.block, got, tc.want)
			}
		})
	}
}

// TestNotifyPreflightRecordsDeliveryFailureWhenARecorderIsAttached proves the seam
// also describes a DELIVERY pass when one is attached (the CLI only ever attaches
// it to a preflight, but the recorder must not lie if that changes): the failure is
// reported with the deliverer's own detail, and the aggregate report is untouched
// by the recorder's presence.
func TestNotifyPreflightRecordsDeliveryFailureWhenARecorderIsAttached(t *testing.T) {
	now := preflightNow
	okRecord := dueRecord(1, "https://ok.example/hook", now, 26)
	failRecord := dueRecord(2, "https://fail.example/hook", now, 26)
	repo := &stubNotifyRepo{records: []models.WebhookNotifyRecord{okRecord, failRecord}}
	logs := stubLogReader{byUser: map[uint][]models.DailyLog{
		1: {periodStartLog(1, *okRecord.LastPeriodStart)},
		2: {periodStartLog(2, *failRecord.LastPeriodStart)},
	}}
	deliverer := &stubDeliverer{failURLs: map[string]bool{"https://fail.example/hook": true}}
	service := newTestNotifyService(repo, logs, stubDecryptor{}, deliverer)

	recorder := &preflightRecorder{}
	report, err := service.run(context.Background(), now, time.UTC, false, recorder)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if report.Sent != 1 || report.Failed != 1 {
		t.Fatalf("recorder changed the aggregate counters: sent=%d failed=%d", report.Sent, report.Failed)
	}
	if len(recorder.outcomes) != 2 {
		t.Fatalf("expected two outcomes, got %d", len(recorder.outcomes))
	}
	if recorder.outcomes[0].Reason != PreflightReasonOK {
		t.Fatalf("owner 1 reason = %q, want %q", recorder.outcomes[0].Reason, PreflightReasonOK)
	}
	failed := recorder.outcomes[1]
	if failed.Reason != PreflightReasonDeliveryFailed || failed.Detail != "transport_error" {
		t.Fatalf("owner 2 outcome = %+v, want delivery_failed:transport_error", failed)
	}
	if failed.Code() != "delivery_failed:transport_error" || !failed.Actionable {
		t.Fatalf("owner 2 code/actionable = %q/%v", failed.Code(), failed.Actionable)
	}
}

// TestPreflightRecorderReplacesALaterOutcomeForTheSameOwner pins the recorder's
// one-row-per-owner contract: a second record for an owner replaces the first, so
// the table never lists an owner twice.
func TestPreflightRecorderReplacesALaterOutcomeForTheSameOwner(t *testing.T) {
	recorder := &preflightRecorder{}
	recorder.record(newPreflightOutcome(4, "a.example", 1, PreflightReasonOK, ""))
	recorder.record(newPreflightOutcome(5, "b.example", 0, PreflightReasonNotDue, ""))
	recorder.record(newPreflightOutcome(4, "a.example", 1, PreflightReasonDeliveryFailed, "non_2xx"))

	if len(recorder.outcomes) != 2 {
		t.Fatalf("expected one row per owner, got %d", len(recorder.outcomes))
	}
	if recorder.outcomes[0].OwnerID != 4 || recorder.outcomes[0].Code() != "delivery_failed:non_2xx" {
		t.Fatalf("owner 4 should hold the later outcome, got %+v", recorder.outcomes[0])
	}
	if recorder.outcomes[1].OwnerID != 5 {
		t.Fatalf("first-sighting order should be preserved, got %+v", recorder.outcomes)
	}
}
// TestNotifyPreflightReportsEveryOwnerInListingOrder proves the report carries one
// row per owner, aligned with the listing, including the owners a plain dry run
// says nothing about.
func TestNotifyPreflightReportsEveryOwnerInListingOrder(t *testing.T) {
	now := preflightNow
	armed := dueRecord(1, "https://a.example/hook", now, 26)
	disabled := dueRecord(2, "https://b.example/hook", now, 26)
	disabled.WebhookEnabled = false
	quiet := dueRecord(3, "https://c.example/hook", now, 10) // nothing inside the lead window

	repo := &stubNotifyRepo{records: []models.WebhookNotifyRecord{armed, disabled, quiet}}
	logs := stubLogReader{byUser: map[uint][]models.DailyLog{
		1: {periodStartLog(1, *armed.LastPeriodStart)},
		2: {periodStartLog(2, *disabled.LastPeriodStart)},
		3: {periodStartLog(3, *quiet.LastPeriodStart)},
	}}
	service := newTestNotifyService(repo, logs, stubDecryptor{}, &stubDeliverer{})

	report, err := service.NotifyPreflight(context.Background(), now, time.UTC)
	if err != nil {
		t.Fatalf("NotifyPreflight: %v", err)
	}
	if report.Pass.OwnersScanned != 3 || len(report.Outcomes) != 3 {
		t.Fatalf("expected three rows for three owners, got scanned=%d rows=%d", report.Pass.OwnersScanned, len(report.Outcomes))
	}
	want := []struct {
		ownerID uint
		reason  string
	}{
		{1, PreflightReasonOK},
		{2, PreflightReasonDeliveryDisabled},
		{3, PreflightReasonNotDue},
	}
	for index, expected := range want {
		got := report.Outcomes[index]
		if got.OwnerID != expected.ownerID || got.Reason != expected.reason {
			t.Fatalf("row %d = {%d,%s}, want {%d,%s}", index, got.OwnerID, got.Reason, expected.ownerID, expected.reason)
		}
	}
	// The diagnostic is compute-only: the dry-run preview still plays the role it
	// always played, and no watermark was written.
	if report.Pass.Due != 1 || len(report.Pass.DryRunPreview) != 1 {
		t.Fatalf("aggregate dry run changed: due=%d preview=%d", report.Pass.Due, len(report.Pass.DryRunPreview))
	}
	if len(repo.writes()) != 0 {
		t.Fatalf("preflight wrote a watermark: %#v", repo.writes())
	}
}

// TestNotifyPreflightIsStatelessAcrossConcurrentWalks runs two preflights at once.
// The recorder lives in the walk, not on the service, so neither pass can observe
// the other's outcomes — and with -race this fails if that ever stops being true.
func TestNotifyPreflightIsStatelessAcrossConcurrentWalks(t *testing.T) {
	now := preflightNow
	one := dueRecord(1, "https://a.example/hook", now, 26)
	two := dueRecord(2, "https://b.example/hook", now, 26)
	repo := &stubNotifyRepo{records: []models.WebhookNotifyRecord{one, two}}
	logs := stubLogReader{byUser: map[uint][]models.DailyLog{
		1: {periodStartLog(1, *one.LastPeriodStart)},
		2: {periodStartLog(2, *two.LastPeriodStart)},
	}}
	service := newTestNotifyService(repo, logs, stubDecryptor{}, &stubDeliverer{})

	const walks = 2
	reports := make([]NotifyPreflightReport, walks)
	errs := make([]error, walks)
	var running sync.WaitGroup
	running.Add(walks)
	for index := range walks {
		go func(position int) {
			defer running.Done()
			reports[position], errs[position] = service.NotifyPreflight(context.Background(), now, time.UTC)
		}(index)
	}
	running.Wait()

	for index, err := range errs {
		if err != nil {
			t.Fatalf("walk %d: %v", index, err)
		}
		if len(reports[index].Outcomes) != 2 {
			t.Fatalf("walk %d saw %d outcomes, want its own 2", index, len(reports[index].Outcomes))
		}
		for _, outcome := range reports[index].Outcomes {
			if outcome.Reason != PreflightReasonOK {
				t.Fatalf("walk %d outcome = %+v, want ok", index, outcome)
			}
		}
	}
}
