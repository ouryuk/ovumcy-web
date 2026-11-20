package services

import (
	"testing"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/models"
)

// Focused coverage for DetectExplicitCycleStarts (cycles.go) and the
// explicit-anchor selection it feeds (cycle_anchor.go:
// latestExplicitCycleStartBeforeOrOn, reached through the exported
// LatestCycleStartAnchorBeforeOrOn).
//
// Every existing exercise of this path (the cycle-baseline and cycle-start
// policy tests) hands it logs carrying exactly ONE clean explicit start, so
// three behaviors of the detector were only incidentally reachable:
//
//   - the filter: a day counts as an explicit start only when BOTH IsPeriod and
//     CycleStart are set — a period day without the flag and a flagged day
//     without bleeding are both not-a-start;
//   - the per-date dedupe: two explicit-start logs on the same calendar day are
//     one start, not two (the baseline would otherwise hand a caller a start
//     list whose "latest" is decided by map/slice order rather than the day);
//   - the order: output is sorted ascending by date, and
//     latestExplicitCycleStartBeforeOrOn walks it BACKWARD so the newest start
//     on-or-before the target day wins and any start after that day is
//     skipped — an anchor in the future would project the owner's cycle from
//     a cycle that has not begun.
//
// The helpers are prefixed cesc (Cycle Explicit Start Coverage) to stay
// disjoint from the mr3cyc* helpers in cycles_mutation_round3_test.go.

func cescDay(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func cescExplicitStartLog(day time.Time) models.DailyLog {
	return models.DailyLog{Date: day, IsPeriod: true, CycleStart: true}
}

// TestDetectExplicitCycleStartsKeepsOnlyPeriodDaysFlaggedAsStarts pins the
// filter: only days carrying both flags become explicit starts, and each
// calendar day appears once no matter how many logs on it carry the pair.
func TestDetectExplicitCycleStartsKeepsOnlyPeriodDaysFlaggedAsStarts(t *testing.T) {
	keep := cescDay(2026, time.March, 1)
	logs := []models.DailyLog{
		// The explicit start itself.
		cescExplicitStartLog(keep),
		// Same calendar day, logged twice: deduped to one start. The date is
		// stored at UTC midnight, so an equal instant collides on the key.
		cescExplicitStartLog(keep),
		// A period day without the cycle-start flag: not a start.
		{Date: cescDay(2026, time.March, 2), IsPeriod: true},
		// A flagged day without bleeding: not a start (CycleStart alone never
		// asserts a new cycle).
		{Date: cescDay(2026, time.March, 3), CycleStart: true},
		// An unrelated day: not a start.
		{Date: cescDay(2026, time.March, 4)},
	}

	starts := DetectExplicitCycleStarts(logs)
	if len(starts) != 1 {
		t.Fatalf("expected exactly one explicit start, got %d: %v", len(starts), starts)
	}
	if !sameDay(starts[0], keep) {
		t.Fatalf("expected explicit start 2026-03-01, got %v", starts[0])
	}
}

// TestDetectExplicitCycleStartsSortsAscending pins the order contract the
// backward walk in latestExplicitCycleStartBeforeOrOn depends on: the detector
// sorts by date ascending whatever order the logs arrive in.
func TestDetectExplicitCycleStartsSortsAscending(t *testing.T) {
	third := cescDay(2026, time.January, 20)
	second := cescDay(2026, time.February, 5)
	first := cescDay(2026, time.February, 6)
	logs := []models.DailyLog{
		cescExplicitStartLog(second),
		cescExplicitStartLog(first),
		cescExplicitStartLog(third),
	}

	starts := DetectExplicitCycleStarts(logs)
	if len(starts) != 3 {
		t.Fatalf("expected three explicit starts, got %d: %v", len(starts), starts)
	}
	for i, want := range []time.Time{third, second, first} {
		if !sameDay(starts[i], want) {
			t.Fatalf("start %d: expected %v, got %v", i, want, starts[i])
		}
	}
}

// TestDetectExplicitCycleStartsEmptyIsEmpty pins the empty contract: no logs
// means no starts, never a zero-length-but-non-nil result a caller could
// distinguish from nil.
func TestDetectExplicitCycleStartsEmptyIsEmpty(t *testing.T) {
	if starts := DetectExplicitCycleStarts(nil); starts != nil {
		t.Fatalf("expected nil for no logs, got %v", starts)
	}
	if starts := DetectExplicitCycleStarts([]models.DailyLog{}); starts != nil {
		t.Fatalf("expected nil for empty logs, got %v", starts)
	}
}

// TestLatestExplicitCycleStartSkipsFutureAndPicksTheNewestOnOrBefore pins the
// selection in cycle_anchor.go: the anchor is the NEWEST explicit start on or
// before the target day, starts strictly after that day are skipped, and a day
// with no on-or-before start yields the zero time. Driven through the exported
// LatestCycleStartAnchorBeforeOrOn with a nil user so the anchor comes purely
// from the explicit history.
func TestLatestExplicitCycleStartSkipsFutureAndPicksTheNewestOnOrBefore(t *testing.T) {
	oldStart := cescDay(2026, time.February, 10)
	latestStart := cescDay(2026, time.March, 5)
	futureStart := cescDay(2026, time.March, 15)
	logs := []models.DailyLog{
		cescExplicitStartLog(futureStart),
		cescExplicitStartLog(latestStart),
		cescExplicitStartLog(oldStart),
	}
	day := cescDay(2026, time.March, 10)

	if got := LatestCycleStartAnchorBeforeOrOn(nil, logs, day, time.UTC); !sameDay(got, latestStart) {
		t.Fatalf("expected newest on-or-before start 2026-03-05, got %v", got)
	}

	// Same-day boundary: "before or ON" includes the target day itself.
	todayStart := cescDay(2026, time.March, 10)
	if got := LatestCycleStartAnchorBeforeOrOn(nil, []models.DailyLog{cescExplicitStartLog(todayStart)}, day, time.UTC); !sameDay(got, todayStart) {
		t.Fatalf("expected same-day start 2026-03-10 to count as on-or-before, got %v", got)
	}

	// Only future starts: no anchor — never project a cycle that has not begun.
	if got := LatestCycleStartAnchorBeforeOrOn(nil, []models.DailyLog{cescExplicitStartLog(futureStart)}, day, time.UTC); !got.IsZero() {
		t.Fatalf("expected zero anchor when every explicit start is after the day, got %v", got)
	}
}
