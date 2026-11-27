package services

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/models"
)

// Notify preflight diagnostics (the `ovumcy notify --preflight` slice). The
// request-free notify pass has always reported only aggregates — owners scanned,
// due, sent, failed, plus the ids that failed — and its per-owner detail went to
// the service log as one line per reason. An operator whose reminders silently
// stopped could therefore see THAT an owner was not being reached, but not WHY,
// without grepping the pass's log for that owner id.
//
// This file adds the missing seam: every branch of processOwner that already
// decided something about one owner also records an OUTCOME for that owner, on a
// per-pass RECORDER — never on the service. It is a diagnostic, not a second
// pass: it delivers nothing, claims nothing, marks nothing, and does not touch a
// watermark or the report counters. The existing log lines and the aggregate
// report stay exactly as they were, so nothing observable about delivery,
// idempotency or output changes for a plain `ovumcy notify`.
//
// Why a recorder and not a field on NotifyReport: NotifyReport is the CLI's
// printed contract (counts + ids, host-only on a dry run). The recorder lets ONE
// walk of the pass feed both the aggregates and the per-owner table, so
// preflight cannot drift from what a real pass would decide, and it lets the
// service stay stateless — two concurrent passes cannot read each other's
// outcomes.
//
// Reason precedence. An owner can match several blocks at once (an enabled owner
// with an unreadable endpoint and no cycle history, say), so the walk resolves
// exactly one reason per owner, in the order the branches can occur, and the first
// one wins:
//
//  1. delivery_disabled — the master switch: nothing downstream can matter.
//  2. no_endpoint — nothing to send to.
//  3. endpoint_unreadable — a ciphertext this instance cannot open. Checked before
//     the cycle state because a rotated SECRET_KEY makes every owner silent, and
//     that is the answer the operator is looking for.
//  4. endpoint_invalid / private_address_blocked — the target could never be
//     reached, whatever the cycle says. Recorded BEFORE the decision runs and
//     left standing when the decision finds nothing due; overwritten by a real
//     would-send verdict, because "this would fail" beats "this would not send".
//  5. logs_unreadable — the decision could not run at all.
//  6. no_reminder_kind / predictions_suppressed / already_sent / not_due — nothing
//     is due, and this is WHY; already_sent and not_due split on the decision's
//     own idempotency counter.
//  7. ok — a reminder would be delivered (due = how many).
//  8. delivery_failed / claim_lost — only a REAL pass can reach these, because a
//     preflight makes no claim and no request; they exist so the same recorder can
//     describe a delivery pass when one attaches it.
//
// `due` is always the count the decision produced, independent of the reason: an
// owner blocked by the egress gate still reports how many reminders would have
// gone out if the gate let them.
//
// Secret discipline is unchanged and now explicit: an outcome carries the owner
// id, the destination HOST (never the URL, path, query, userinfo, or token), the
// number of reminders that owner would receive, a stable reason key, and whether
// a fix is something the operator can do in Ovumcy. No response body, no
// decrypted health specifics, no ciphertext.

// The stable, machine-readable reason keys a preflight outcome carries. They are
// the `reason` field of the versioned JSON document, so they are part of a public
// contract: rename one and a consumer breaks. Each key names the CONDITION that
// stops or allows delivery, not the code path that found it.
const (
	// PreflightReasonOK: this owner's next reminder would be delivered. Due is
	// the number of reminders the same decision would send.
	PreflightReasonOK = "ok"
	// PreflightReasonDeliveryDisabled: the owner switched webhook delivery off.
	PreflightReasonDeliveryDisabled = "delivery_disabled"
	// PreflightReasonNoEndpoint: delivery is on but no endpoint is stored.
	PreflightReasonNoEndpoint = "no_endpoint"
	// PreflightReasonEndpointUnreadable: a ciphertext is stored but this instance
	// cannot open it — the SECRET_KEY that wrote it is not the one loaded now.
	PreflightReasonEndpointUnreadable = "endpoint_unreadable"
	// PreflightReasonEndpointInvalid: the decrypted endpoint cannot be delivered
	// to at all (unparseable, not http/https, or a hostless authority).
	PreflightReasonEndpointInvalid = "endpoint_invalid"
	// PreflightReasonPrivateAddressBlocked: the host is a private/loopback/
	// link-local literal and WEBHOOK_BLOCK_PRIVATE_ADDRESSES is on.
	PreflightReasonPrivateAddressBlocked = "private_address_blocked"
	// PreflightReasonLogsUnreadable: the owner's day logs could not be read, so
	// the decision could not run.
	PreflightReasonLogsUnreadable = "logs_unreadable"
	// PreflightReasonPredictionsSuppressed: the medical-safety gate withholds
	// predictions for this owner (unpredictable-cycle mode, pregnancy pause, or
	// an overdue cycle), so no reminder is emitted.
	PreflightReasonPredictionsSuppressed = "predictions_suppressed"
	// PreflightReasonNoReminderKind: both per-kind reminder switches are off.
	PreflightReasonNoReminderKind = "no_reminder_kind"
	// PreflightReasonAlreadySent: every reminder inside the lead window is
	// covered by its per-kind watermark — already delivered for this cycle.
	PreflightReasonAlreadySent = "already_sent"
	// PreflightReasonNotDue: nothing is inside the lead window right now, so no
	// reminder is pending. Not a defect; the next scheduled pass decides anew.
	PreflightReasonNotDue = "not_due"
	// PreflightReasonDeliveryFailed: a REAL pass attempted the delivery and it
	// failed (transport error, refused redirect, non-2xx, or a claim that lost
	// to a storage error). A dry run and a preflight never produce it.
	PreflightReasonDeliveryFailed = "delivery_failed"
	// PreflightReasonClaimLost: the send was skipped because a concurrent pass
	// owns it or the configuration this pass read was revoked. Not a failure.
	PreflightReasonClaimLost = "claim_lost"
)

// preflightActionable reports whether the reason names something the operator
// can act on in Ovumcy itself. The CLI's "Act now" column is exactly this flag, so
// the classification lives beside the enum rather than in the printer.
func preflightActionable(reason string) bool {
	switch reason {
	case PreflightReasonEndpointUnreadable, PreflightReasonEndpointInvalid,
		PreflightReasonDeliveryFailed, PreflightReasonLogsUnreadable:
		return true
	default:
		return false
	}
}

// PreflightOutcome is one owner's last-pass verdict: what the pass decided about
// that owner and, therefore, whether their next reminder can be delivered.
//
// Host may be empty — no endpoint was readable, or none is configured. It is a
// hostname at most; the URL, its path, query and userinfo never reach it.
type PreflightOutcome struct {
	OwnerID uint
	Host    string
	// Due is the number of reminders a delivery pass would have sent for this
	// owner in the same walk, before any delivery was attempted.
	Due int
	// Reason is one of the PreflightReason* keys.
	Reason string
	// Detail qualifies Reason where the key alone is not the whole answer: it is
	// the deliverer's own key for a delivery_failed outcome (transport_error,
	// redirect_refused, non_2xx, scheme_not_allowed, private_address_blocked) or
	// claim_error when the watermark claim itself could not be taken. Empty for
	// every other reason.
	Detail string
	// Actionable reports whether Reason names a defect the operator can fix.
	Actionable bool
}

// Code is the machine-readable reason a JSON consumer branches on: Reason when it
// stands alone, Reason:Detail where Detail refines it — the same spelling the
// preflight prints, so the table and the document cannot disagree.
func (outcome PreflightOutcome) Code() string {
	if outcome.Detail == "" {
		return outcome.Reason
	}
	return outcome.Reason + ":" + outcome.Detail
}

// PreflightReasonActionable reports whether reason names something the operator can
// fix in Ovumcy. It is exported so a consumer (the CLI's advice table, a test)
// can check its own wording against the same classification the walk applied,
// instead of duplicating it.
func PreflightReasonActionable(reason string) bool {
	return preflightActionable(reason)
}

// newPreflightOutcome builds an outcome and derives Actionable from the reason,
// so a caller cannot set the two out of step with each other.
func newPreflightOutcome(ownerID uint, host string, due int, reason string, detail string) PreflightOutcome {
	return PreflightOutcome{
		OwnerID:    ownerID,
		Host:       host,
		Due:        due,
		Reason:     reason,
		Detail:     detail,
		Actionable: preflightActionable(reason),
	}
}

// NotifyPreflightReport is the outcome of one request-free preflight. It holds
// the same aggregates a dry run reports plus one outcome per owner examined. The
// pass itself is what always inspects every owner — a preflight does not filter
// by --owner, because the diagnostic is only trustworthy if it comes from the same
// walk that would do the sending.
type NotifyPreflightReport struct {
	// Pass is the aggregate report of the SAME walk, with DryRun set and every
	// delivery counter left where a compute-only pass leaves it.
	Pass NotifyReport
	// Outcomes has one entry per owner record the pass examined, in the
	// deterministic owner-id order the listing produces.
	Outcomes []PreflightOutcome
}

// NotifyPreflight walks every owner through the notify pass's own decision
// (dry-run mode: no request, no claim, no watermark) and records WHY each owner
// is or is not reachable. It is the CLI's `--preflight` path; RunOnce(…, true)
// remains the plain dry run, unchanged.
//
// It is a dedicated read-only walk rather than a reuse of the `--dry-run` preview
// path, and the difference is the point: the preview is a catalogue of what WOULD
// be sent, so it says nothing about an owner whose loop never reaches a reminder
// (disabled, unreadable endpoint, suppressed predictions), which is exactly the
// owner an operator asking "why is this one silent?" is looking at. Holding the
// recorder on the shared walk gives the diagnostic WITHOUT a second traversal: the
// outcomes come from the same decisions a delivery pass would make.
//
// Duplicate work, stated plainly: on the branch where an owner has NO due
// reminder, the suppression reason is resolved through
// BuildCycleStatsFromLogs a second time. It is pure and repository-free, and it
// runs only for owners with nothing due, so one scheduled `--preflight` keeps the
// cost of a dry run plus its own output; a delivery pass pays nothing at all,
// because no recorder is attached.
func (service *WebhookNotifyService) NotifyPreflight(ctx context.Context, now time.Time, location *time.Location) (NotifyPreflightReport, error) {
	recorder := &preflightRecorder{}
	report, err := service.run(ctx, now, location, true, recorder)
	if err != nil {
		return NotifyPreflightReport{}, err
	}
	// A defensive copy: the report is handed to a caller that may outlive the walk
	// and slice the outcomes itself, and nothing else holds the recorder.
	outcomes := make([]PreflightOutcome, len(recorder.outcomes))
	copy(outcomes, recorder.outcomes)
	return NotifyPreflightReport{Pass: report, Outcomes: outcomes}, nil
}

// preflightRecorder collects one outcome per owner for a single pass. A later
// record for the same owner REPLACES the earlier one, so a walk that stops at
// "would send" and then hits a real delivery failure (only a delivery pass can)
// reports the outcome that matters rather than the optimistic one.
type preflightRecorder struct {
	outcomes []PreflightOutcome
	byOwner  map[uint]int
}

// record stores the outcome for outcome.OwnerID, replacing any earlier entry for
// that owner. Order follows first sighting, which is the listing's order.
func (recorder *preflightRecorder) record(outcome PreflightOutcome) {
	if recorder.byOwner == nil {
		recorder.byOwner = make(map[uint]int, 4)
	}
	if position, seen := recorder.byOwner[outcome.OwnerID]; seen {
		recorder.outcomes[position] = outcome
		return
	}
	recorder.byOwner[outcome.OwnerID] = len(recorder.outcomes)
	recorder.outcomes = append(recorder.outcomes, outcome)
}

// classifyPreflightEndpoint maps a decrypted endpoint to the reason that stops
// delivery to it, or "" when the endpoint is deliverable. It mirrors the checks
// Deliver applies BEFORE any request leaves (parse, scheme allowlist, authority
// shape, private literal under the gate), so a preflight verdict explains the
// failure the next real pass would hit rather than inventing a different cause.
//
// It performs no I/O: a hostname that resolves into a private range is caught by
// the deliverer's resolve-and-check dialer at delivery time, and deliberately
// NOT re-resolved here — a preflight must not depend on live DNS, and a transient
// resolver answer must not be reported as a configuration defect. Such a target
// classifies as deliverable, and the pass that then fails records transport_error
// as delivery_failed.
func classifyPreflightEndpoint(decryptedURL string, blockPrivateAddresses bool) string {
	parsed, err := url.Parse(strings.TrimSpace(decryptedURL))
	if err != nil {
		return PreflightReasonEndpointInvalid
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return PreflightReasonEndpointInvalid
	}
	if validateWebhookAuthority(parsed) != nil {
		return PreflightReasonEndpointInvalid
	}
	if blockPrivateAddresses && isPrivateHost(parsed.Hostname()) {
		return PreflightReasonPrivateAddressBlocked
	}
	return ""
}

// classifyNotifyDeliveryError maps a deliverer error to the reason key the
// delivery log line already prints for it (webhook_delivery.go). The deliverer
// returns host-only errors carrying that key as text, so the vocabulary is the
// same one an operator already greps for and a new transport branch needs no
// extension point here.
func classifyNotifyDeliveryError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case isRedirectRefusal(err):
		return "redirect_refused"
	case strings.Contains(err.Error(), "private address refused"):
		return PreflightReasonPrivateAddressBlocked
	case strings.Contains(err.Error(), "returned status"):
		return "non_2xx"
	case strings.Contains(err.Error(), "scheme not allowed"):
		return PreflightReasonEndpointInvalid
	default:
		return "transport_error"
	}
}

// preflightPendingReason is the reason a compute-only walk reports when nothing
// was due for an owner whose configuration and endpoint are fine. It separates
// "everything works, nothing to send today" (not_due) from "this would send"
// (ok) and from "the watermark already covered it" (already_sent), using the
// decision's own idempotency counter.
func preflightPendingReason(watermarkSuppressed int) string {
	if watermarkSuppressed > 0 {
		return PreflightReasonAlreadySent
	}
	return PreflightReasonNotDue
}

// ownerKindsEnabled reports whether the owner opted into at least one reminder
// kind. When neither is on the pass emits nothing for any cycle, which is a
// setting the operator can point at rather than a cycle state.
func ownerKindsEnabled(record models.WebhookNotifyRecord) bool {
	return record.WebhookNotifyPeriod || record.WebhookNotifyOvulation
}