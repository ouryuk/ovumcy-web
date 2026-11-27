package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/services"
)

// `ovumcy notify --preflight` — the read-only diagnostic that answers "will this
// owner's next reminder actually be delivered, and if not, what blocks it".
//
// It runs the SAME walk a delivery pass runs, in dry-run mode, and prints one row
// per owner: the owner id, the destination HOST (never the URL or its token), how
// many reminders are due, the machine-readable reason, and the next actionable fix
// in words. `--json` prints the same facts as a versioned document.
//
// Output discipline, unchanged from the rest of the notify command: no URL, path,
// query, userinfo, token, ciphertext, response body, or reminder type/estimated
// date appears in either form. Every row is safe to capture in an operator log —
// unlike the `--dry-run --show-health-details` preview, which is opt-in for
// exactly that reason.
//
// One walk, one filter: the preflight ALWAYS examines every owner (the
// diagnostic is only trustworthy if it comes from the same walk that would send),
// and `--owner <id>` narrows the PRINTED rows and the document's `matched` flag.
// A filter that matches nothing is an error, so a typo cannot pass for "this owner
// is fine". The filter takes an owner ID, not an email: the notify projection
// deliberately does not carry an address, and resolving one here would mean a
// second, different query against the accounts table.

// notifyPreflightDocument is the `--preflight --json` payload. Schema is the first
// field for the same reason as every other operator document: a consumer detects
// the shape before reading anything else.
type notifyPreflightDocument struct {
	Schema      string                     `json:"schema"`
	GeneratedAt string                     `json:"generated_at"`
	// OwnersScanned is every owner the walk examined; Owners is the (possibly
	// filtered) set of rows. When Owner is non-empty, Matched says whether it
	// named a real owner — a document that matched nothing must never read as a
	// clean bill of health.
	OwnersScanned int                       `json:"owners_scanned"`
	Owner         string                    `json:"owner,omitempty"`
	Matched       bool                      `json:"matched"`
	Owners        []notifyPreflightOwnerRow `json:"owners"`
}

// notifyPreflightOwnerRow is one owner's verdict in the document. Due is the raw
// count the decision produced, so a blocked-but-pending owner still shows how many
// reminders are waiting; Host is a hostname at most.
type notifyPreflightOwnerRow struct {
	OwnerID    uint   `json:"owner_id"`
	Host       string `json:"host"`
	Due        int    `json:"due"`
	Reason     string `json:"reason"`
	Detail     string `json:"detail,omitempty"`
	Actionable bool   `json:"actionable"`
	NextAction string `json:"next_action"`
}

// notifyPreflightNextAction is the operator-facing half of the reason enum: one
// sentence naming the next concrete fix (or that none is needed). It is NOT the
// JSON contract — a consumer branches on Reason — but it must stay in step with
// the enum, which the table-driven test pins.
//
// The prefix is the actionable half an operator scans for: "Act now:" opens every
// row whose reason the operator can fix in Ovumcy (the outcome's Actionable flag),
// "Nothing to do:" every other row. Both prefix maps are pinned by test, so a reason
// added on one side without the other fails rather than rendering a shrug.
func notifyPreflightNextAction(reason string) string {
	switch reason {
	case services.PreflightReasonOK:
		return "Nothing to do: the next due reminder would be delivered"
	case services.PreflightReasonDeliveryDisabled:
		return "Act now: delivery is switched off - enable it with `ovumcy webhook set <email> --enabled=true`"
	case services.PreflightReasonNoEndpoint:
		return "Act now: no endpoint is stored - save one with `ovumcy webhook set <email> --url-stdin`"
	case services.PreflightReasonEndpointUnreadable:
		return "Act now: the endpoint cannot be decrypted - restore the application secret that saved it, or re-save the URL"
	case services.PreflightReasonEndpointInvalid:
		return "Act now: the endpoint is not a usable http(s) URL with a host - re-save it with `ovumcy webhook set <email> --url-stdin`"
	case services.PreflightReasonPrivateAddressBlocked:
		return "Act now: the host is private and WEBHOOK_BLOCK_PRIVATE_ADDRESSES is on - point at a routable host or turn the gate off"
	case services.PreflightReasonLogsUnreadable:
		return "Act now: the owner's logs could not be read - check the database, then re-run"
	case services.PreflightReasonPredictionsSuppressed:
		return "Nothing to do: unpredictability mode, a pregnancy pause, or an overdue cycle withholds predictions on purpose"
	case services.PreflightReasonNoReminderKind:
		return "Act now: both reminder kinds are off - enable one with `ovumcy webhook set <email> --notify-period=true`"
	case services.PreflightReasonAlreadySent:
		return "Nothing to do: this cycle's reminder was already delivered"
	case services.PreflightReasonNotDue:
		return "Nothing to do: nothing is inside the lead window; the next scheduled pass decides again"
	case services.PreflightReasonDeliveryFailed:
		return "Act now: the last delivery failed - check the endpoint host, then re-run the pass"
	case services.PreflightReasonClaimLost:
		return "Nothing to do: another pass owns this send, or the configuration changed mid-pass"
	default:
		// Unknown key: name it rather than invent advice, so a new reason that
		// reaches the printer before this table can be seen doing so.
		return "unknown reason"
	}
}

// runNotifyPreflightCommand runs the read-only diagnostic and renders it. now and
// location are injected so tests drive the decision deterministically.
func runNotifyPreflightCommand(
	service notifyPreflightRunner,
	opts notifyOptions,
	now time.Time,
	location *time.Location,
	output io.Writer,
) error {
	if output == nil {
		output = os.Stdout
	}

	report, err := service.NotifyPreflight(context.Background(), now, location)
	if err != nil {
		return fmt.Errorf("notify preflight failed: %w", err)
	}

	document := buildNotifyPreflightDocument(report, opts.owner, now)
	if opts.owner != "" && !document.Matched {
		return fmt.Errorf("no owner matched %q (the walk examined %d owner(s); list them with `ovumcy users list`)", opts.owner, document.OwnersScanned)
	}

	if opts.preflightJSON {
		payload, err := json.MarshalIndent(document, "", "  ")
		if err != nil {
			return fmt.Errorf("render preflight: %w", err) // codecov:ignore -- unreachable: MarshalIndent of this all-marshalable document cannot fail
		}
		payload = append(payload, '\n')
		_, _ = output.Write(payload)
		return nil
	}

	_, _ = io.WriteString(output, renderNotifyPreflightTable(document))
	return nil
}

// buildNotifyPreflightDocument turns the service report into the printed shape,
// applying the optional --owner filter. A filter value that is not a number
// matches no owner, which the caller reports as an error rather than an empty,
// healthy-looking table.
func buildNotifyPreflightDocument(report services.NotifyPreflightReport, owner string, now time.Time) notifyPreflightDocument {
	document := notifyPreflightDocument{
		Schema:        notifyPreflightSchemaVersion,
		GeneratedAt:   now.UTC().Format(time.RFC3339),
		OwnersScanned: report.Pass.OwnersScanned,
		Owner:         owner,
		Owners:        []notifyPreflightOwnerRow{},
		// A document without a filter reports the whole instance, so it matched by
		// definition — including the legitimate, if useless, zero-owner walk.
		Matched: owner == "",
	}
	var wantedID uint
	if owner != "" {
		// A non-numeric filter parses as 0, which matches no real owner (ids start
		// at 1), so it falls through to the caller's unmatched error.
		if parsed, err := strconv.ParseUint(strings.TrimSpace(owner), 10, 64); err == nil {
			wantedID = uint(parsed)
		}
	}

	for _, outcome := range report.Outcomes {
		if owner != "" && outcome.OwnerID != wantedID {
			continue
		}
		document.Matched = true
		document.Owners = append(document.Owners, notifyPreflightOwnerRow{
			OwnerID:    outcome.OwnerID,
			Host:       outcome.Host,
			Due:        outcome.Due,
			Reason:     outcome.Reason,
			Detail:     outcome.Detail,
			Actionable: outcome.Actionable,
			NextAction: notifyPreflightNextAction(outcome.Reason),
		})
	}
	return document
}

// renderNotifyPreflightTable renders the human view. Columns are owner id, host,
// due, reason (plus its detail), and the next actionable fix; the bottom line counts
// how many rows need attention, so an operator does not have to scan for them.
func renderNotifyPreflightTable(document notifyPreflightDocument) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Webhook notify preflight (%s) - computed only: no request sent, no watermark written.\n", document.Schema)
	if document.Owner != "" {
		fmt.Fprintf(&out, "Owner filter: %s\n", document.Owner)
	}
	fmt.Fprintf(&out, "Owners scanned: %d, listed: %d\n\n", document.OwnersScanned, len(document.Owners))

	if len(document.Owners) == 0 {
		fmt.Fprintf(&out, "  (no owner examined)\n")
		return out.String()
	}

	table := tabwriter.NewWriter(&out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(table, "  OWNER\tHOST\tDUE\tREASON\tNEXT ACTION")
	actionable := 0
	for _, row := range document.Owners {
		reason := row.Reason
		if row.Detail != "" {
			reason += ":" + row.Detail
		}
		if row.Actionable {
			actionable++
		}
		host := row.Host
		if host == "" {
			host = "-"
		}
		_, _ = fmt.Fprintf(table, "  %d\t%s\t%d\t%s\t%s\n", row.OwnerID, host, row.Due, reason, row.NextAction)
	}
	_ = table.Flush()

	fmt.Fprintf(&out, "\n%d owner(s) need attention, %d need none.\n", actionable, len(document.Owners)-actionable)
	return out.String()
}
