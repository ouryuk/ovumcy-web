package services

import (
	"testing"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/models"
)

// This file walks the egress ledger's own state machine — the pure resolvers
// behind BuildEgressLedger — one resolver at a time.
//
// The ledger is reached in production only through BuildEgressLedger, so every
// branch below used to be exercised only insofar as a handful of end-to-end
// ledger builds happened to pass through it. A branch that no build happened to
// reach had no test of its own: reorder the switch arms, invert one comparison,
// or delete one case, and the ledger still produced a plausible ledger for every
// fixture that existed, because the fixtures never asked the question that
// branch answers.
//
// Each resolver is asked here directly, over its full enumeration, so the
// property under test is the precedence the state machine claims rather than
// the shape of the ledger one particular row happens to produce.

// TestResolveIntrinsicWebhookStateEvaluatesReadabilityBeforeEveryToggle walks
// the webhook state machine over every arm, in the order the resolver claims to
// evaluate them.
//
// Readability comes FIRST, before any toggle: a row whose ciphertext no longer
// opens cannot deliver whatever its flags say, and asking "is delivery
// switched off?" first is how a broken key stays invisible behind a flag that
// cannot matter. The two toggle arms come after, and the hostless arm sits
// between them, because a ciphertext that opens and names no host is a
// STORED-VALUE problem, not a key problem, and the owner's next action differs.
func TestResolveIntrinsicWebhookStateEvaluatesReadabilityBeforeEveryToggle(t *testing.T) {
	t.Parallel()

	readable := WebhookURLDisplay{Readability: WebhookURLReadable, Host: "hooks.example.test"}

	tests := []struct {
		name    string
		display WebhookURLDisplay
		row     EgressLedgerInput
		want    EgressWebhookState
	}{
		{
			name:    "no endpoint ciphertext stored",
			display: WebhookURLDisplay{Readability: WebhookURLAbsent},
			row:     EgressLedgerInput{WebhookEnabled: true, NotifyPeriod: true, NotifyOvulation: true},
			want:    EgressWebhookNotConfigured,
		},
		{
			// The toggle flags are set on purpose: an unreadable row must
			// outrank them, not lose to them.
			name:    "endpoint stored but this instance cannot open it",
			display: WebhookURLDisplay{Readability: WebhookURLUnreadable},
			row:     EgressLedgerInput{WebhookEnabled: true, NotifyPeriod: true, NotifyOvulation: true},
			want:    EgressWebhookUnreadable,
		},
		{
			name:    "endpoint opens but names no host",
			display: WebhookURLDisplay{Readability: WebhookURLReadable},
			row:     EgressLedgerInput{WebhookEnabled: true, NotifyPeriod: true, NotifyOvulation: true},
			want:    EgressWebhookUnusable,
		},
		{
			// A host of only whitespace is the same fact as no host at all:
			// the resolver trims before deciding, so a display that carries
			// padding cannot slip past the check.
			name:    "endpoint opens and names only whitespace as its host",
			display: WebhookURLDisplay{Readability: WebhookURLReadable, Host: "   "},
			row:     EgressLedgerInput{WebhookEnabled: true, NotifyPeriod: true},
			want:    EgressWebhookUnusable,
		},
		{
			name:    "owner switched delivery off",
			display: readable,
			row:     EgressLedgerInput{NotifyPeriod: true, NotifyOvulation: true},
			want:    EgressWebhookStoredOff,
		},
		{
			name:    "delivery on but no reminder kind opted in",
			display: readable,
			row:     EgressLedgerInput{WebhookEnabled: true},
			want:    EgressWebhookStoredNoKinds,
		},
		{
			name:    "armed on the period kind alone",
			display: readable,
			row:     EgressLedgerInput{WebhookEnabled: true, NotifyPeriod: true},
			want:    EgressWebhookArmed,
		},
		{
			name:    "armed on the ovulation kind alone",
			display: readable,
			row:     EgressLedgerInput{WebhookEnabled: true, NotifyOvulation: true},
			want:    EgressWebhookArmed,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := resolveIntrinsicWebhookState(testCase.row, testCase.display)
			if got != testCase.want {
				t.Fatalf("resolveIntrinsicWebhookState() = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestResolveIntrinsicWebhookStateReachesEveryWebhookState walks the resolver
// over the package's own enumeration of webhook states and asserts that each one
// the resolver owns is reachable.
//
// The enumeration is the precedence the state machine applies, and it is
// exported so a renderer or a test can walk it. A state added to the
// enumeration but not to the resolver (or a resolver arm that no longer
// produces the state the enumeration names) would leave the two disagreeing,
// and nothing in the ledger's own tests would say so: the ledger tests assert
// the state of a fixture row, not that the machine can reach every state it
// claims to have.
//
// ONE enumerated state is not the resolver's to produce: outbound_disabled is
// an INSTANCE fact, laid over the row's own state by
// applyOutboundDeliveryOverlay, so no row can reach it here. It is excluded
// explicitly rather than by counting, so the exclusion names the state instead
// of hiding it behind an index.
func TestResolveIntrinsicWebhookStateReachesEveryWebhookState(t *testing.T) {
	t.Parallel()

	readable := WebhookURLDisplay{Readability: WebhookURLReadable, Host: "hooks.example.test"}

	rows := []EgressLedgerInput{
		// Absent ciphertext: the toggles are set on purpose, so the row proves
		// that readability outranks them.
		{WebhookEnabled: true, NotifyPeriod: true, NotifyOvulation: true},
		// Unreadable ciphertext: same, and the row would otherwise be armed.
		{WebhookEnabled: true, NotifyPeriod: true, NotifyOvulation: true},
		// Readable but hostless: a stored-value problem, not a key problem.
		{WebhookEnabled: true, NotifyPeriod: true, NotifyOvulation: true},
		// A readable endpoint the owner switched off.
		{NotifyPeriod: true, NotifyOvulation: true},
		// Delivery on, no reminder kind opted in.
		{WebhookEnabled: true},
		// Armed on the period kind alone.
		{WebhookEnabled: true, NotifyPeriod: true},
		// Armed on the ovulation kind alone.
		{WebhookEnabled: true, NotifyOvulation: true},
	}
	displays := []WebhookURLDisplay{
		{Readability: WebhookURLAbsent},
		{Readability: WebhookURLUnreadable},
		{Readability: WebhookURLReadable},
		readable,
		readable,
		readable,
		readable,
	}

	reachable := make(map[EgressWebhookState]bool, len(EgressWebhookStates))
	for index, row := range rows {
		state := resolveIntrinsicWebhookState(row, displays[index])
		reachable[state] = true
	}

	for _, state := range EgressWebhookStates {
		if state == EgressWebhookOutboundDisabled {
			// An instance fact, not a row's own: the overlay produces it.
			if reachable[state] {
				t.Errorf("webhook state %q is an overlay state and must not be reachable from the row's own state", state)
			}
			continue
		}
		if !reachable[state] {
			t.Errorf("webhook state %q is enumerated but no row reaches it", state)
		}
	}
}

// TestApplyOutboundDeliveryOverlayCoversExactlyTheThreeDeliveryStates pins
// which states the instance-wide delivery switch may speak for.
//
// The overlay may cover a row only where the row IS delivery configuration.
// not_configured, unreadable and unusable are facts about the STORED row that
// no instance setting explains away: a broken key reported as "delivery is
// switched off" is how it stays invisible, and a missing endpoint reported as
// "delivery is switched off" tells the owner to look at a switch when there is
// nothing to switch.
//
// The fixture drives the overlay over every enumerated state, so a state ADDED
// to the enumeration later is checked against the rule rather than inheriting
// the current behaviour by default.
func TestApplyOutboundDeliveryOverlayCoversExactlyTheThreeDeliveryStates(t *testing.T) {
	t.Parallel()

	covered := map[EgressWebhookState]EgressWebhookState{
		EgressWebhookStoredOff:        EgressWebhookOutboundDisabled,
		EgressWebhookStoredNoKinds:    EgressWebhookOutboundDisabled,
		EgressWebhookArmed:            EgressWebhookOutboundDisabled,
		EgressWebhookNotConfigured:    EgressWebhookNotConfigured,
		EgressWebhookUnreadable:       EgressWebhookUnreadable,
		EgressWebhookUnusable:         EgressWebhookUnusable,
		EgressWebhookOutboundDisabled: EgressWebhookOutboundDisabled,
	}

	if len(covered) != len(EgressWebhookStates) {
		t.Fatalf("the fixture covers %d states but the enumeration names %d", len(covered), len(EgressWebhookStates))
	}

	quiet := NewEgressLedgerService(nil, nil, false)
	delivering := NewEgressLedgerService(nil, nil, true)

	for _, state := range EgressWebhookStates {
		want, coveredState := covered[state]
		if !coveredState {
			t.Fatalf("webhook state %q is enumerated but the fixture covers no case for it", state)
		}
		if got := quiet.applyOutboundDeliveryOverlay(state); got != want {
			t.Errorf("applyOutboundDeliveryOverlay(%q) on an instance running no delivery pass = %q, want %q", state, got, want)
		}
		// The overlay is a no-op on an instance that DOES run a delivery pass:
		// the row's own state is already the answer there.
		if got := delivering.applyOutboundDeliveryOverlay(state); got != state {
			t.Errorf("applyOutboundDeliveryOverlay(%q) on a delivering instance = %q, want %q", state, got, state)
		}
	}
}

// TestResolveFeedStateAnswersOnlyWhatTheRecordedEpochPermits walks the feed
// state machine over every arm.
//
// The distinction the machine exists to preserve is between "no feed", "a feed
// whose status could not be read", and "a feed whose key epoch says something
// different from the running one". The first is a claim about a row; the second
// is the ABSENCE of a claim, and answering "none" from a read that never saw
// the row invites the owner to mint a second link beside one that still works.
func TestResolveFeedStateAnswersOnlyWhatTheRecordedEpochPermits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status CalendarFeedStatus
		want   EgressFeedState
	}{
		{
			// The zero value is exactly what BuildFeedStatus returns on a load
			// failure, so this arm IS the load-failure arm.
			name:   "the row could not be read",
			status: CalendarFeedStatus{},
			want:   EgressFeedUnknown,
		},
		{
			name:   "the row was read and holds no selector",
			status: CalendarFeedStatus{Known: true},
			want:   EgressFeedNone,
		},
		{
			// A link exists, but this instance cannot derive its own epoch, so
			// it has measured no mismatch and must not report one.
			name:   "the running epoch cannot be derived",
			status: CalendarFeedStatus{Known: true, Configured: true, KeyEpoch: "stored-epoch"},
			want:   EgressFeedUnknown,
		},
		{
			// The epoch column is not backfilled, so a link minted before it
			// was recorded says nothing about which key it was minted under.
			name:   "the link predates the recorded epoch",
			status: CalendarFeedStatus{Known: true, Configured: true, CurrentKeyEpoch: "running-epoch"},
			want:   EgressFeedIssuedBeforeRecorded,
		},
		{
			name:   "the link was minted under a key this instance no longer runs",
			status: CalendarFeedStatus{Known: true, Configured: true, KeyEpoch: "previous-epoch", CurrentKeyEpoch: "running-epoch"},
			want:   EgressFeedIssuedPreviousKey,
		},
		{
			name:   "the link was minted under the running key",
			status: CalendarFeedStatus{Known: true, Configured: true, KeyEpoch: "running-epoch", CurrentKeyEpoch: "running-epoch"},
			want:   EgressFeedIssuedCurrentKey,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := resolveFeedState(testCase.status)
			if got != testCase.want {
				t.Fatalf("resolveFeedState() = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestResolveFeedStateTreatsAPrefixOnlyEpochAsAMismatch is the negative-control
// arm: a stored epoch that merely shares a PREFIX with the running one is
// still a mismatch, and two identical epochs are a match.
//
// The comparison is constant-time, so the shape of the answer cannot be read
// off the time it took. What CAN be asserted is the answer itself: a stored
// epoch that merely shares a prefix with the running one is a mismatch, not a
// match.
func TestResolveFeedStateTreatsAPrefixOnlyEpochAsAMismatch(t *testing.T) {
	t.Parallel()

	prefix := CalendarFeedStatus{Known: true, Configured: true, KeyEpoch: "epoch", CurrentKeyEpoch: "epoch-longer"}
	if got := resolveFeedState(prefix); got != EgressFeedIssuedPreviousKey {
		t.Fatalf("expected a prefix-only epoch to be a mismatch, got %q", got)
	}

	identical := CalendarFeedStatus{Known: true, Configured: true, KeyEpoch: "epoch", CurrentKeyEpoch: "epoch"}
	if got := resolveFeedState(identical); got != EgressFeedIssuedCurrentKey {
		t.Fatalf("expected an identical epoch to match, got %q", got)
	}
}

// TestResolveEgressSectionStateIsAFullAutomatonOverBothPaths walks the
// heading automaton over the cross product of the two paths' states.
//
// The heading is not a summary of either path: "an endpoint saved but switched
// off, no feed" is the ORDINARY configuration and belongs to neither path's
// states, so the heading has its own automaton over the pair. needs_attention
// dominates, because a working path must never mask a broken one.
func TestResolveEgressSectionStateIsAFullAutomatonOverBothPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		webhook EgressWebhookState
		feed    EgressFeedState
		want    EgressSectionState
	}{
		{name: "unreadable endpoint outranks a working feed", webhook: EgressWebhookUnreadable, feed: EgressFeedIssuedCurrentKey, want: EgressSectionNeedsAttention},
		{name: "hostless endpoint outranks a working feed", webhook: EgressWebhookUnusable, feed: EgressFeedIssuedCurrentKey, want: EgressSectionNeedsAttention},
		{name: "an unreadable feed row needs attention", webhook: EgressWebhookStoredOff, feed: EgressFeedUnknown, want: EgressSectionNeedsAttention},
		{name: "a feed minted under a previous key needs attention", webhook: EgressWebhookStoredOff, feed: EgressFeedIssuedPreviousKey, want: EgressSectionNeedsAttention},
		{name: "an armed endpoint alone enables a path", webhook: EgressWebhookArmed, feed: EgressFeedNone, want: EgressSectionPathsEnabled},
		{name: "a current-key feed alone enables a path", webhook: EgressWebhookStoredOff, feed: EgressFeedIssuedCurrentKey, want: EgressSectionPathsEnabled},
		{name: "a pre-recorded feed alone enables a path", webhook: EgressWebhookNotConfigured, feed: EgressFeedIssuedBeforeRecorded, want: EgressSectionPathsEnabled},
		{name: "both paths quiet", webhook: EgressWebhookStoredOff, feed: EgressFeedNone, want: EgressSectionNoPathEnabled},
		{name: "nothing configured at all", webhook: EgressWebhookNotConfigured, feed: EgressFeedNone, want: EgressSectionNoPathEnabled},
		// The heading reads the INTRINSIC webhook state on purpose. The RENDERED
		// state cannot answer it on a default instance: outbound_disabled
		// collapses stored_off, stored_no_kinds and armed into one word, and the
		// heading needs them apart. A heading that asked the rendered state
		// would report "this process runs no reminder pass" for a row the owner
		// armed — which is false, because the operator CLI delivers from cron on
		// exactly the instances that ship the scheduler off.
		{name: "an armed endpoint behind a disabled outbound pass still enables a path", webhook: EgressWebhookArmed, feed: EgressFeedNone, want: EgressSectionPathsEnabled},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := resolveEgressSectionState(testCase.webhook, testCase.feed)
			if got != testCase.want {
				t.Fatalf("resolveEgressSectionState(%q, %q) = %q, want %q", testCase.webhook, testCase.feed, got, testCase.want)
			}
		})
	}
}

// TestWebhookHostForStateSurfacesAHostOnlyWhereOneWasDerived pins which states
// may name a host.
//
// The host is derived from the ciphertext, so a state that never opened it has
// no host to report. not_configured and unreadable are the two such states:
// the first stored nothing, the second stored something this instance cannot
// read. Every other state opened the ciphertext, so its host is real — even
// when the host is empty, which the unusable state reports as a fact of its
// own.
func TestWebhookHostForStateSurfacesAHostOnlyWhereOneWasDerived(t *testing.T) {
	t.Parallel()

	display := WebhookURLDisplay{Readability: WebhookURLReadable, Host: "hooks.example.test"}

	if got := webhookHostForState(EgressWebhookNotConfigured, display); got != "" {
		t.Errorf("expected no host for a row with no endpoint, got %q", got)
	}
	if got := webhookHostForState(EgressWebhookUnreadable, display); got != "" {
		t.Errorf("expected no host for an endpoint this instance cannot open, got %q", got)
	}
	if got := webhookHostForState(EgressWebhookUnusable, display); got != display.Host {
		t.Errorf("expected the derived host for a readable endpoint, got %q", got)
	}
	if got := webhookHostForState(EgressWebhookArmed, display); got != display.Host {
		t.Errorf("expected the derived host for an armed endpoint, got %q", got)
	}
}

// TestDeliveryEvidenceForStateSuppressesTheMarkWhereItCannotVouch walks every
// enumerated webhook state against the delivery mark.
//
// The suppression is not cosmetic. The clearing rule fires on a SAVE, so a row
// whose ciphertext stopped opening — a rotated key, never re-saved — still
// holds the mark from the last delivery under the old key. Rendered
// unconditionally it reads "the last delivery was accepted on <date>" directly
// beneath "this instance can no longer open that endpoint".
func TestDeliveryEvidenceForStateSuppressesTheMarkWhereItCannotVouch(t *testing.T) {
	t.Parallel()

	delivered := time.Date(2026, 7, 4, 6, 0, 0, 0, time.UTC)

	// The states that can still vouch for the endpoint the mark describes.
	vouching := map[EgressWebhookState]bool{
		EgressWebhookStoredOff:     true,
		EgressWebhookStoredNoKinds: true,
		EgressWebhookArmed:         true,
	}

	for _, state := range EgressWebhookStates {
		got := deliveryEvidenceForState(state, &delivered)
		if vouching[state] {
			if got == nil {
				t.Errorf("expected the delivery mark to survive in state %q", state)
			} else if !got.Equal(delivered) {
				t.Errorf("expected the delivery mark %v in state %q, got %v", delivered, state, got)
			}
			continue
		}
		if got != nil {
			t.Errorf("expected the delivery mark suppressed in state %q, got %v", state, got)
		}
	}
}

// TestDeliveryEvidenceForStatePassesAnAbsentMarkThrough pins that the
// resolver does not INVENT a mark for a row that never recorded one: an absent
// mark stays absent in every state that can vouch for the endpoint.
func TestDeliveryEvidenceForStatePassesAnAbsentMarkThrough(t *testing.T) {
	t.Parallel()

	for _, state := range EgressWebhookStates {
		if got := deliveryEvidenceForState(state, nil); got != nil {
			t.Errorf("expected no mark in state %q for a row that recorded none, got %v", state, got)
		}
	}
}

// TestRevealEvidenceForStateSuppressesTheMarkWhereThereIsNoLink pins which feed
// states may carry the reveal mark.
//
// The mark records that a one-time reveal was CONSUMED, so it is only about a
// link. Where the state says there is no link — none — or where the state says
// the row could not be read — unknown — the mark has nothing to be about, and
// rendering it would claim a reveal the ledger never confirmed.
func TestRevealEvidenceForStateSuppressesTheMarkWhereThereIsNoLink(t *testing.T) {
	t.Parallel()

	revealed := time.Date(2026, 7, 4, 6, 0, 0, 0, time.UTC)

	withLink := map[EgressFeedState]bool{
		EgressFeedIssuedBeforeRecorded: true,
		EgressFeedIssuedPreviousKey:    true,
		EgressFeedIssuedCurrentKey:     true,
	}

	for _, state := range EgressFeedStates {
		got := revealEvidenceForState(state, &revealed)
		if withLink[state] {
			if got == nil {
				t.Errorf("expected the reveal mark to survive in state %q", state)
			} else if !got.Equal(revealed) {
				t.Errorf("expected the reveal mark %v in state %q, got %v", revealed, state, got)
			}
			continue
		}
		if got != nil {
			t.Errorf("expected the reveal mark suppressed in state %q, got %v", state, got)
		}
	}
}

// TestRevealEvidenceForStatePassesAnAbsentMarkThrough pins that the resolver
// does not invent a reveal for a row that never recorded one.
func TestRevealEvidenceForStatePassesAnAbsentMarkThrough(t *testing.T) {
	t.Parallel()

	for _, state := range EgressFeedStates {
		if got := revealEvidenceForState(state, nil); got != nil {
			t.Errorf("expected no reveal mark in state %q for a row that recorded none, got %v", state, got)
		}
	}
}

// TestEgressLedgerInputFromUserCopiesExactlyTheSixLedgerFields pins the
// narrowing that is the only place a models.User and the ledger meet.
//
// It copies six fields BY NAME: a seventh cannot arrive by widening a struct
// somewhere else. That is the property worth pinning — the ledger's argument
// type deliberately carries no claim watermark and no feed column, so a claim
// watermark reaching the ledger would have to be added to this copy on purpose,
// and this test is what makes that addition visible.
//
// The feed columns the row also carries are set to values the ledger's own
// fields could not have produced, so a copy that reached for the wrong column
// would carry a different value than the one asserted here. The reveal mark is
// dated differently from the delivery mark for the same reason: the two are
// different facts, and a copy that swapped them would be caught by the value
// rather than by a field name.
func TestEgressLedgerInputFromUserCopiesExactlyTheSixLedgerFields(t *testing.T) {
	t.Parallel()

	delivered := time.Date(2026, 7, 4, 6, 0, 0, 0, time.UTC)
	revealed := time.Date(2026, 9, 8, 7, 30, 0, 0, time.UTC)
	user := models.User{
		ID:                     42,
		WebhookURL:             "sealed-ciphertext",
		WebhookEnabled:         true,
		WebhookNotifyPeriod:    true,
		WebhookNotifyOvulation: false,
		WebhookLastDeliveredAt: &delivered,
		// Columns the ledger must never see: the feed selector is non-secret but
		// still not the ledger's business, the verifier halves are secrets, and
		// the reveal mark is not delivery evidence.
		CalendarFeedSelector:     "selector",
		CalendarFeedVerifierHash: "hash",
		CalendarFeedVerifierMAC:  "mac",
		CalendarFeedRevealedAt:   &revealed,
		CalendarFeedKeyEpoch:     "epoch",
	}

	input := EgressLedgerInputFromUser(user)

	if input.UserID != user.ID {
		t.Errorf("expected the owner id %d, got %d", user.ID, input.UserID)
	}
	if input.EncryptedWebhookURL != user.WebhookURL {
		t.Errorf("expected the endpoint ciphertext %q, got %q", user.WebhookURL, input.EncryptedWebhookURL)
	}
	if !input.WebhookEnabled {
		t.Error("expected the master delivery switch to be carried over")
	}
	if !input.NotifyPeriod {
		t.Error("expected the period opt-in to be carried over")
	}
	if input.NotifyOvulation {
		t.Error("expected the ovulation opt-in to be carried over as stored")
	}
	if input.LastDeliveredAt == nil || !input.LastDeliveredAt.Equal(delivered) {
		t.Errorf("expected the delivery mark %v, got %v", delivered, input.LastDeliveredAt)
	}
	// The reveal mark is a DIFFERENT date from the delivery mark, so a copy
	// that reached for the wrong column would carry it and fail here.
	if input.LastDeliveredAt != nil && input.LastDeliveredAt.Equal(revealed) {
		t.Error("the reveal mark reached the ledger in place of the delivery mark")
	}
}
