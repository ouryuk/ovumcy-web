package services

import (
	"context"
	"testing"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/models"
)

// bothKindsDueRecord builds an owner whose period and ovulation reminders are
// BOTH inside the lead window on the same day. With cycle starts logged on
// 2026-02-01 and 2026-03-01 and a 28-day cycle, the next period is predicted
// for 2026-03-29; on 2026-03-28 (the pass clock these tests use) that is one
// day out, and the ovulation projection — this cycle's 2026-03-14 having
// passed — has rolled to the NEXT cycle's estimate, 14 days out. With the
// maximum 14-day lead both are in-window. Their anchors COINCIDE here (the
// rolled-forward cycle start IS the next period start, 2026-03-29): the
// watermarks are per-kind columns, so the same date in each slot still
// suppresses its own kind only. The owner
// has one completed cycle behind them, which lifts the fertility floor that
// otherwise withholds the first ovulation projection.
func bothKindsDueRecord(id uint, urlToken string) (models.WebhookNotifyRecord, stubLogReader) {
	cycleStart := time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC)
	previousStart := cycleStart.AddDate(0, 0, -28)
	record := models.WebhookNotifyRecord{
		ID:                     id,
		CycleLength:            28,
		PeriodLength:           5,
		LutealPhase:            14,
		LastPeriodStart:        &cycleStart,
		WebhookEnabled:         true,
		WebhookURL:             urlToken,
		WebhookNotifyPeriod:    true,
		WebhookNotifyOvulation: true,
		ReminderLeadDays:       MaxReminderLeadDays,
	}
	logs := stubLogReader{byUser: map[uint][]models.DailyLog{id: {
		periodStartLog(id, previousStart),
		periodStartLog(id, cycleStart),
	}}}
	return record, logs
}

// TestNotifyBothKindsWatermarkedCountsTwoSkipsAndClaimsNothing drives the
// notify pass for an owner whose two due reminders are BOTH already covered by
// their watermarks. The pass must account one skip per kind (the counter the
// decision layer derives from its single traversal), take no claim, and make
// no request. Before this test only the single-kind case was pinned, so a
// regression that counted the pair as one — or that still claimed — would
// have passed.
func TestNotifyBothKindsWatermarkedCountsTwoSkipsAndClaimsNothing(t *testing.T) {
	now := time.Date(2026, time.March, 28, 9, 0, 0, 0, time.UTC)
	record, logs := bothKindsDueRecord(1, "https://a.example/hook")

	// Baseline: both kinds are due with no watermarks. Each is suppressed by
	// its OWN kind's watermark (see bothKindsDueRecord for why the anchors
	// coincide without interfering).
	baseline := DecideDueReminders(notifyUser(record), WebhookReminderSettingsFromNotifyRecord(record), logs.byUser[1], now, time.UTC)
	period, okPeriod := findDueReminder(baseline, DueReminderTypePeriod)
	ovulation, okOvulation := findDueReminder(baseline, DueReminderTypeOvulation)
	if !okPeriod || !okOvulation {
		t.Fatalf("fixture: expected both kinds due, got %#v", baseline)
	}
	if got := period.CycleAnchor.Format("2006-01-02"); got != "2026-03-29" {
		t.Fatalf("fixture: period anchor = %s, want 2026-03-29", got)
	}
	if got := ovulation.CycleAnchor.Format("2006-01-02"); got != "2026-03-29" {
		t.Fatalf("fixture: ovulation anchor = %s, want 2026-03-29", got)
	}

	periodAnchor := period.CycleAnchor
	ovulationAnchor := ovulation.CycleAnchor
	record.WebhookPeriodLastSentCycleStart = &periodAnchor
	record.WebhookOvulationLastSentCycleStart = &ovulationAnchor

	repo := &stubNotifyRepo{records: []models.WebhookNotifyRecord{record}}
	deliverer := &stubDeliverer{}
	service := newTestNotifyService(repo, logs, stubDecryptor{}, deliverer)

	report, err := service.RunOnce(context.Background(), now, time.UTC, false)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if report.OwnersScanned != 1 {
		t.Fatalf("owners scanned = %d, want 1", report.OwnersScanned)
	}
	if report.Due != 0 || report.Sent != 0 || report.Failed != 0 {
		t.Fatalf("both kinds watermarked: due/sent/failed = %d/%d/%d, want 0/0/0", report.Due, report.Sent, report.Failed)
	}
	if report.SkippedIdempotent != 2 {
		t.Fatalf("skipped (already sent) = %d, want one per watermarked kind (2)", report.SkippedIdempotent)
	}
	if claims := repo.writes(); len(claims) != 0 {
		t.Fatalf("a watermarked reminder must not be claimed, got %d claim(s)", len(claims))
	}
	if deliveries := deliverer.deliveries(); len(deliveries) != 0 {
		t.Fatalf("a watermarked reminder must not be delivered, got %d delivery attempt(s)", len(deliveries))
	}
	if len(report.OwnerIDsFailed) != 0 {
		t.Fatalf("skips are not failures, got failed owners %v", report.OwnerIDsFailed)
	}
}

// TestNotifyClaimsEachKindAgainstItsOwnWatermark pins watermarkForReminderType
// through the pass: when only one kind is watermarked, the other kind's claim
// must be compared against ITS OWN (unset) slot — never the sibling kind's
// stored anchor — while the watermarked kind is counted as skipped and not
// claimed. A claim taken against the wrong slot would be lost against a real
// compare-and-set repository (the column holds nil, the claim expects a date),
// silently turning every mixed case into a skipped reminder.
func TestNotifyClaimsEachKindAgainstItsOwnWatermark(t *testing.T) {
	now := time.Date(2026, time.March, 28, 9, 0, 0, 0, time.UTC)

	cases := []struct {
		name               string
		watermarkPeriod    bool
		watermarkOvulation bool
		wantSentType       string
		wantSkipped        int
	}{
		{name: "period watermarked: ovulation is claimed against an unset slot", watermarkPeriod: true, wantSentType: DueReminderTypeOvulation, wantSkipped: 1},
		{name: "ovulation watermarked: period is claimed against an unset slot", watermarkOvulation: true, wantSentType: DueReminderTypePeriod, wantSkipped: 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record, logs := bothKindsDueRecord(1, "https://a.example/hook")
			baseline := DecideDueReminders(notifyUser(record), WebhookReminderSettingsFromNotifyRecord(record), logs.byUser[1], now, time.UTC)
			if tc.watermarkPeriod {
				anchor := mustFindDueReminder(t, baseline, DueReminderTypePeriod).CycleAnchor
				record.WebhookPeriodLastSentCycleStart = &anchor
			}
			if tc.watermarkOvulation {
				anchor := mustFindDueReminder(t, baseline, DueReminderTypeOvulation).CycleAnchor
				record.WebhookOvulationLastSentCycleStart = &anchor
			}

			repo := &stubNotifyRepo{records: []models.WebhookNotifyRecord{record}}
			deliverer := &stubDeliverer{}
			service := newTestNotifyService(repo, logs, stubDecryptor{}, deliverer)

			report, err := service.RunOnce(context.Background(), now, time.UTC, false)
			if err != nil {
				t.Fatalf("RunOnce: %v", err)
			}
			if report.Due != 1 || report.Sent != 1 || report.Failed != 0 {
				t.Fatalf("due/sent/failed = %d/%d/%d, want 1/1/0", report.Due, report.Sent, report.Failed)
			}
			if report.SkippedIdempotent != tc.wantSkipped {
				t.Fatalf("skipped (already sent) = %d, want %d", report.SkippedIdempotent, tc.wantSkipped)
			}

			claims := repo.writes()
			if len(claims) != 1 {
				t.Fatalf("expected exactly one claim, got %#v", claims)
			}
			if claims[0].reminderType != tc.wantSentType {
				t.Fatalf("claimed kind = %q, want the un-watermarked %q", claims[0].reminderType, tc.wantSentType)
			}
			expected := repo.claimExpected[0]
			if expected != nil {
				t.Fatalf("the un-watermarked kind must be claimed against its own unset slot, got previous=%s", expected.Format("2006-01-02"))
			}
			if repo.claimEpochs[0] != record.WebhookConfigVersion {
				t.Fatalf("claim epoch = %d, want the snapshot's %d", repo.claimEpochs[0], record.WebhookConfigVersion)
			}

			deliveries := deliverer.deliveries()
			if len(deliveries) != 1 || deliveries[0].payload.Type != tc.wantSentType {
				t.Fatalf("expected one %s delivery, got %#v", tc.wantSentType, deliveries)
			}
			if marks := repo.marks(); len(marks) != 1 || marks[0].at != now {
				t.Fatalf("expected one delivery mark at the pass clock, got %#v", marks)
			}
		})
	}
}

// TestNotifyFailedDeliveryReleasesTheClaimIntoTheMatchingKindSlot extends the
// release path to the mixed case: with only the ovulation kind due, a failed
// POST must release the ovulation claim and restore the ovulation slot's
// previous value (nil), leaving the period slot — which carries a real anchor —
// untouched. A release written into the wrong slot would clear the period
// watermark and re-send that reminder on the next pass.
func TestNotifyFailedDeliveryReleasesTheClaimIntoTheMatchingKindSlot(t *testing.T) {
	now := time.Date(2026, time.March, 28, 9, 0, 0, 0, time.UTC)
	record, logs := bothKindsDueRecord(1, "https://a.example/hook")
	baseline := DecideDueReminders(notifyUser(record), WebhookReminderSettingsFromNotifyRecord(record), logs.byUser[1], now, time.UTC)

	periodAnchor := mustFindDueReminder(t, baseline, DueReminderTypePeriod).CycleAnchor
	record.WebhookPeriodLastSentCycleStart = &periodAnchor

	repo := &stubNotifyRepo{records: []models.WebhookNotifyRecord{record}}
	deliverer := &stubDeliverer{failEvery: true}
	service := newTestNotifyService(repo, logs, stubDecryptor{}, deliverer)

	report, err := service.RunOnce(context.Background(), now, time.UTC, false)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if report.Failed != 1 || report.Sent != 0 || report.SkippedIdempotent != 1 {
		t.Fatalf("failed/sent/skipped = %d/%d/%d, want 1/0/1", report.Failed, report.Sent, report.SkippedIdempotent)
	}
	if len(report.OwnerIDsFailed) != 1 || report.OwnerIDsFailed[0] != 1 {
		t.Fatalf("expected owner 1 listed once as failed, got %v", report.OwnerIDsFailed)
	}

	releases := repo.released()
	if len(releases) != 1 || releases[0].reminderType != DueReminderTypeOvulation {
		t.Fatalf("expected the ovulation claim to be released, got %#v", releases)
	}
	if restored := repo.releaseRestored[0]; restored != nil {
		t.Fatalf("the ovulation release must restore its own (nil) previous value, got %s", restored.Format("2006-01-02"))
	}
	if len(repo.writes()) != 0 {
		t.Fatalf("the claim must be gone after the release, got %#v", repo.writes())
	}
	if marks := repo.marks(); len(marks) != 0 {
		t.Fatalf("a failed delivery must leave no delivery mark, got %#v", marks)
	}
}

func mustFindDueReminder(t *testing.T, reminders []DueReminder, reminderType string) DueReminder {
	t.Helper()
	reminder, ok := findDueReminder(reminders, reminderType)
	if !ok {
		t.Fatalf("fixture: expected a %s reminder, got %#v", reminderType, reminders)
	}
	return reminder
}

// notifyUser builds the decision-layer user exactly as the notify pass does,
// so the baseline decision here and the pass cannot disagree about inputs.
func notifyUser(record models.WebhookNotifyRecord) *models.User {
	user := userFromNotifyRecord(record)
	return &user
}
