package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/db"
	"github.com/ovumcy/ovumcy-web/internal/models"
	"github.com/ovumcy/ovumcy-web/internal/services"
)

// `ovumcy preflight` answers one question about one owner — "will this account
// actually receive their next reminder?" — without sending anything.
//
// STRICTLY READ-ONLY, and that is the whole point of the command rather than a
// property it happens to have. It is the operator's answer to "the reminders
// are not arriving, is it us?", and the dishonest version of that answer is one
// that fires a test webhook, or advances a watermark, and thereby changes the
// situation it was asked to describe. So:
//
//   - it never makes an outbound request. No webhook is sent, no endpoint is
//     dialled, and the owner's URL is never decrypted — this command takes no
//     SECRET_KEY at all, so it physically cannot open one. The report states
//     whether an endpoint is STORED and nothing more; whether this instance can
//     still decrypt it is `ovumcy webhook show`'s job, which is the one command
//     that does hold the key.
//   - it never writes. No watermark moves, no delivery mark is stamped, no
//     setting changes. There is no code path here that calls a repository
//     writer, and a test asserts the owner row is identical afterwards.
//
// It derives EVERYTHING from what the notify pass itself uses, so the two can
// never disagree about why a reminder is or is not going out:
//
//   - services.BuildCycleStatsFromLogs — the dashboard/feed/notify prediction
//     derivation (internal/cli/predict.go uses the same call for `predict`).
//   - services.PublishedStats — the medical-safety gate, read rather than
//     recombined, exactly as the notify pass reads it.
//   - services.DashboardProjectionCycleLength + DashboardUpcomingPredictions —
//     the authoritative next-period and ovulation dates the notify pass
//     converts into reminders.
//   - services.DecideDueReminders — the notify pass's OWN due-reminder
//     selection, called here unchanged (it is already a pure function, so no
//     extraction was needed) to report what would go out today.
//
// The webhook settings half is composed the way `webhook show` reads it — the
// same `users` columns, the same clamped lead window through
// services.NormalizeReminderLeadDays — but WITHOUT the decrypt: `webhook show`
// is the one command that holds the SECRET_KEY and therefore the one that can
// say whether a stored endpoint still opens. This command states only whether
// one is stored, and names `webhook show` as the place to look when an operator
// needs the endpoint's host or its readability.
//
// EXIT-CODE CONTRACT (what a script may rely on):
//
//	exit 0 — the owner is ready: no blocking finding was reported, so nothing
//	         this command can see stands between the owner and the next
//	         reminder.
//	exit 1 — at least one BLOCKING finding was reported. The report itself is
//	         still written to stdout first (in both renderings), so a script can
//	         capture it; the exit code is the verdict. Operational failures —
//	         bad arguments, an unknown or ambiguous owner, a database/config
//	         error — also exit non-zero (via the caller's usual error path), so a
//	         script that needs to tell "answered, not ready" from "could not
//	         ask" branches on the error text, never on the code alone.
//
// Exit 1 being "answered, and the answer is no" is what makes the command
// usable from a monitoring check without it ever touching the egress path a
// real reminder takes.

// preflightUsage is the single usage string returned for every argument error
// so the contract is stable and testable, mirroring predictUsage.
const preflightUsage = "usage: ovumcy preflight <email> [--id <id>] [--tz <IANA name>] [--json]"

// preflightSchemaVersion tags the --json payload so a consumer can detect which
// shape of document it is reading, mirroring predictSchemaVersion.
const preflightSchemaVersion = "ovumcy-preflight/v1"

// preflightStaleDeliveryDays bounds how old the last accepted delivery may be
// before it counts as a blocking finding. The window is deliberately generous:
// a reminder fires at most once per cycle kind, so a healthy owner whose cycle
// has not come round yet legitimately has a delivery weeks old. It exists to
// catch "this endpoint has never accepted anything since the owner armed it",
// not to police cadence — and the report always states the age, so an operator
// reads the number rather than trusting the label.
const preflightStaleDeliveryDays = 60

// preflightFinding identifies one ordered blocking finding. The values are wire
// values in the --json payload: a script branches on them, so renaming one is a
// breaking change to the document, not a copy edit.
type preflightFinding string

const (
	// preflightFindingNoWebhookNoEndpoint: no endpoint is stored at all.
	preflightFindingNoWebhookNoEndpoint preflightFinding = "no_webhook_configured"
	// preflightFindingWebhookDisabled: an endpoint exists but the master switch
	// is off.
	preflightFindingWebhookDisabled preflightFinding = "webhook_disabled"
	// preflightFindingTogglesOff: neither per-kind opt-in is on, so an armed
	// webhook would still send nothing.
	preflightFindingTogglesOff preflightFinding = "reminder_toggles_off"
	// preflightFindingNoDelivery: the endpoint is configured and armed but this
	// instance has recorded no accepted delivery at all.
	preflightFindingNoDelivery preflightFinding = "no_delivery_recorded"
	// preflightFindingStaleDelivery: the last accepted delivery is older than
	// preflightStaleDeliveryDays.
	preflightFindingStaleDelivery preflightFinding = "stale_delivery"
	// preflightFindingPredictionsSuppressed: the app itself is withholding the
	// projection, so the notify pass emits nothing. This covers the three
	// whole-projection signals the dashboard, feed and notify pass all share.
	preflightFindingPredictionsSuppressed preflightFinding = "predictions_suppressed"
	// preflightFindingNoCycleHistory: nothing has been logged, so there is no
	// anchor to project a window from.
	preflightFindingNoCycleHistory preflightFinding = "no_cycle_history"
	// preflightFindingNoProjection: every setting is right and predictions are
	// not suppressed, but the projection still yielded no date for a kind the
	// owner opted into. Reported last, because it is the residue after the
	// operator has worked through everything above it.
	preflightFindingNoProjection preflightFinding = "no_projection"
)

// preflightNow resolves "today" for the projection anchor. A variable so tests
// can pin the clock, exactly like predictNow.
var preflightNow = func() time.Time { return time.Now().UTC() }

// preflightOptions holds the parsed `ovumcy preflight` arguments, mirroring
// predictOptions: the email/id pair is resolved with the shared
// resolveOperatorUser vocabulary.
type preflightOptions struct {
	userID uint
	email  string
	tz     string
	asJSON bool
}

// PreflightFinding is one blocking finding in the readiness report, carrying a
// one-line fix beside it. Blocking means: while this is true, the owner will not
// receive the reminder this report projects.
type PreflightFinding struct {
	// Code is the stable wire value a script branches on.
	Code preflightFinding `json:"code"`
	// Detail states the observed fact, not the remedy.
	Detail string `json:"detail"`
	// Fix is the one-line remedy.
	Fix string `json:"fix"`
}

// PreflightDocument is the payload `ovumcy preflight --json` emits and the same
// struct the human table renders from, so a script and an operator reading
// stdout can never disagree about the verdict.
type PreflightDocument struct {
	Schema      string                 `json:"schema"`
	GeneratedAt string                 `json:"generated_at"`
	User        preflightUserSummary   `json:"user"`
	Timezone    string                 `json:"timezone"`
	AsOf        string                 `json:"as_of"`
	Ready       bool                   `json:"ready"`
	Next        preflightNextReminder  `json:"next_reminder"`
	Webhook     preflightWebhookStatus `json:"webhook"`
	Findings    []PreflightFinding     `json:"findings"`
}

type preflightUserSummary struct {
	ID    uint   `json:"id"`
	Email string `json:"email"`
}

// preflightNextReminder is the projection half: what the notify pass would send
// next, and when.
//
// Kind is "period-soon", "ovulation-soon", or "" when nothing can be projected
// (no history, a suppressed projection, or a kind the owner has switched off).
// Reason explains the empty Kind so a consumer never has to guess.
type preflightNextReminder struct {
	Kind string `json:"kind"`
	// ProjectedDate is the owner-local calendar day the reminder is about,
	// empty when Kind is empty. Every date here is an estimate, never fact.
	ProjectedDate string `json:"projected_date"`
	// DaysUntil is the calendar distance from AsOf, or null when unknown.
	DaysUntil *int `json:"days_until"`
	// DueToday reports the notify pass's own verdict: DecideDueReminders, run
	// against the owner's real settings at this instant, returned this kind in
	// the due set.
	DueToday bool `json:"due_today"`
	// LeadDays is the clamped window in force.
	LeadDays int `json:"lead_days"`
	// Reason explains the reminder selection, so an empty Kind is never bare.
	Reason string `json:"reason"`
}

// preflightWebhookStatus is the settings half. It carries no endpoint value at
// all: this command holds no SECRET_KEY, so it can say whether one is STORED
// but never what it points at, and it deliberately does not try to. Everything
// else here is a setting that is already safe to print.
type preflightWebhookStatus struct {
	Configured           bool   `json:"configured"`
	Enabled              bool   `json:"enabled"`
	NotifyPeriod         bool   `json:"notify_period"`
	NotifyOvulation      bool   `json:"notify_ovulation"`
	ReminderLeadDays     int    `json:"reminder_lead_days"`
	LastDeliveredAt      string `json:"last_delivered_at"`
	LastDeliveryAgeDays  *int   `json:"last_delivery_age_days"`
	DeliveryRecordExists bool   `json:"delivery_record_exists"`
}

// preflightNotReadyError is the blocking verdict as an error, so the caller's
// existing error path produces exit code 1 without a second mechanism. It
// carries no data a script needs (the report is already on stdout); it exists so
// "answered, not ready" is distinguishable from "could not ask".
type preflightNotReadyError struct {
	findings int
}

func (err *preflightNotReadyError) Error() string {
	return fmt.Sprintf("preflight: %d blocking finding(s): the owner will not receive the next reminder", err.findings)
}

// RunPreflightCommand is the operator entry point for `ovumcy preflight`: a
// read-only, local-only readiness report for one owner's webhook reminders. It
// never sends a webhook and never touches a watermark; see the package comment
// above for the exit-code contract.
func RunPreflightCommand(databaseConfig db.Config, args []string) error {
	return runPreflightCommand(databaseConfig, args, os.Stdout)
}

// runPreflightCommand parses the arguments, opens the database, resolves the
// target account, composes the readiness report and renders it. output is
// injected so tests drive stdout with a buffer; the exported entry point above
// is the only production caller and always supplies os.Stdout, so the helper
// states no nil fallback of its own.
func runPreflightCommand(databaseConfig db.Config, args []string, output io.Writer) error {
	opts, err := parsePreflightArgs(args)
	if err != nil {
		return err
	}

	database, err := db.OpenDatabase(databaseConfig)
	if err != nil {
		return fmt.Errorf("database init failed: %w", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		return fmt.Errorf("database init failed: %w", err)
	}
	defer func() {
		_ = sqlDB.Close()
	}()

	repositories, _ := buildRepositories(database, calendarFeedFencePath())
	userService := services.NewOperatorUserService(repositories.Users, services.NewAuthService(repositories.Users))

	summary, err := resolveOperatorUser(userService, opts.userID, opts.email)
	if err != nil {
		// resolveOperatorUser's own vocabulary already advises the operator
		// (`users list`, `retry with --id`); this command adds nothing.
		return mapOperatorUserLookupError(err, opts.userID, opts.email)
	}

	// The report needs the FULL user row — the id the log query keys on, the
	// role and cycle lengths the baseline pass reads, the webhook settings and
	// the delivery stamp. LoadSettingsByID is the settings page's whitelist read
	// and deliberately omits `id` and `role`, so it is the wrong seam here: the
	// log query below would key on 0 and every owner would look history-less.
	// predict uses FindByIDOptional for the same reason.
	user, found, err := repositories.Users.FindByIDOptional(context.Background(), summary.ID)
	if err != nil {
		return fmt.Errorf("preflight user lookup: %w", err)
	}
	if !found {
		return fmt.Errorf("preflight user lookup: %w", services.ErrOperatorUserNotFound)
	}

	// The decision needs the owner's full history — every completed cycle
	// trains the lengths — so no window is applied, exactly as `predict` does.
	logs, err := repositories.DailyLogs.ListByUserRange(context.Background(), user.ID, nil, nil)
	if err != nil {
		return fmt.Errorf("preflight daily logs: %w", err)
	}

	location, err := time.LoadLocation(opts.tz)
	if err != nil {
		return errors.New(preflightUsage)
	}
	now := preflightNow()

	// The endpoint's presence is all this command can honestly read: it holds no
	// SECRET_KEY, so it never attempts to open the ciphertext. Whether the stored
	// endpoint is still readable after a key rotation is `webhook show`'s verb —
	// the one command that does hold the key — and this report points an operator
	// there rather than guessing.
	document := buildPreflightDocument(summary, opts.tz, now, location, &user, logs)

	if opts.asJSON {
		payload, err := json.MarshalIndent(document, "", "  ")
		if err != nil {
			return fmt.Errorf("render preflight: %w", err)
		}
		payload = append(payload, '\n')
		_, _ = output.Write(payload)
	} else {
		_, _ = io.WriteString(output, renderPreflightTable(document))
	}

	if !document.Ready {
		// The report is already written; the error is the exit code.
		return &preflightNotReadyError{findings: len(document.Findings)}
	}
	return nil
}

// parsePreflightArgs parses `preflight <email> [--id <id>] [--tz <IANA name>]
// [--json]`. Flags accept both `--flag value` and `--flag=value`; an --id with
// no email is accepted as in `export`/`predict`; --tz defaults to UTC and must
// resolve; a second positional, an unknown flag or a bad --tz returns
// preflightUsage.
func parsePreflightArgs(args []string) (preflightOptions, error) {
	opts := preflightOptions{tz: "UTC"}
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		if arg == "" {
			continue
		}
		if !strings.HasPrefix(arg, "--") {
			if opts.email != "" {
				return preflightOptions{}, errors.New(preflightUsage)
			}
			normalized, err := normalizeOperatorEmailArgument(arg)
			if err != nil {
				return preflightOptions{}, err
			}
			opts.email = normalized
			continue
		}

		name, value, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		// --flag value form: consume the next argument as the value.
		if !hasValue {
			switch name {
			case "id", "tz":
				if index+1 >= len(args) {
					return preflightOptions{}, errors.New(preflightUsage)
				}
				index++
				value = strings.TrimSpace(args[index])
			case "json":
				opts.asJSON = true
				continue
			default:
				return preflightOptions{}, errors.New(preflightUsage)
			}
		}

		switch name {
		case "id":
			parsed, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				return preflightOptions{}, errors.New(preflightUsage)
			}
			opts.userID = uint(parsed)
		case "tz":
			if _, err := time.LoadLocation(value); err != nil {
				return preflightOptions{}, errors.New(preflightUsage)
			}
			opts.tz = value
		case "json":
			return preflightOptions{}, errors.New(preflightUsage)
		default:
			return preflightOptions{}, errors.New(preflightUsage)
		}
	}

	if opts.email == "" && opts.userID == 0 {
		return preflightOptions{}, errors.New(preflightUsage)
	}
	return opts, nil
}

// buildPreflightDocument composes every field of the report. It calls the
// services functions the notify pass itself calls and adds no derivation of its
// own: the projection half comes from BuildCycleStatsFromLogs → PublishedStats →
// DashboardUpcomingPredictions, and the "would it go today" verdict comes from
// the notify pass's own DecideDueReminders.
func buildPreflightDocument(
	summary models.OperatorUserSummary,
	timezone string,
	now time.Time,
	location *time.Location,
	user *models.User,
	logs []models.DailyLog,
) PreflightDocument {
	today := services.DateAtLocation(now, location)

	// The SAME derivation the dashboard, the feed, `predict` and the notify pass
	// all run. Nothing here is recomputed.
	stats, suppression := services.PublishedStats(user, services.BuildCycleStatsFromLogs(user, logs, now, location), logs, today, location)
	cycleLength := services.DashboardProjectionCycleLength(user, stats)
	prediction := services.DashboardUpcomingPredictions(stats, user, today, cycleLength)

	// The notify pass's OWN due-reminder selection, called unchanged. It is
	// already a pure function, so preflight does not need to extract one: the
	// rule that decides whether a reminder goes out lives in one place and both
	// callers read it there.
	settings := services.WebhookReminderSettings{
		Enabled:            user.WebhookEnabled,
		NotifyPeriod:       user.WebhookNotifyPeriod,
		NotifyOvulation:    user.WebhookNotifyOvulation,
		ReminderLeadDays:   user.ReminderLeadDays,
		PeriodWatermark:    user.WebhookPeriodLastSentCycleStart,
		OvulationWatermark: user.WebhookOvulationLastSentCycleStart,
	}
	due := services.DecideDueReminders(user, settings, logs, now, location)
	dueTypes := make(map[string]bool, len(due))
	for _, reminder := range due {
		dueTypes[reminder.Type] = true
	}

	leadDays := services.NormalizeReminderLeadDays(user.ReminderLeadDays)
	document := PreflightDocument{
		Schema:      preflightSchemaVersion,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		User:        preflightUserSummary{ID: summary.ID, Email: summary.Email},
		Timezone:    timezone,
		AsOf:        predictDate(today),
		Findings:    []PreflightFinding{},
	}

	document.Next = buildPreflightNextReminder(user, prediction, suppression, dueTypes, today, leadDays)
	document.Webhook = buildPreflightWebhookStatus(user, now)
	document.Findings = buildPreflightFindings(user, stats, suppression, document.Webhook, document.Next)
	document.Ready = len(document.Findings) == 0
	return document
}

// buildPreflightNextReminder projects the next reminder of a kind the owner has
// switched on, independent of the lead window: the report answers "what is
// coming", and "is it due right now" is a separate field. When the owner has
// switched both kinds off the projection is still withheld with a reason, since
// no reminder of any kind would go out.
func buildPreflightNextReminder(
	user *models.User,
	prediction services.DashboardUpcomingPrediction,
	suppression services.PredictionSuppression,
	dueTypes map[string]bool,
	today time.Time,
	leadDays int,
) preflightNextReminder {
	reminder := preflightNextReminder{LeadDays: leadDays}

	if suppression.PredictionsSuppressed {
		reminder.Reason = preflightSuppressionReason(suppression)
		return reminder
	}

	// Gather the candidates the owner has opted into, then take the nearer one.
	// The period date is the more actionable of the two for the owner, so it
	// wins a tie — the same precedence the in-app banner uses — which falls out
	// of `>` below: candidates are appended period-first, so an equal date keeps
	// the period entry.
	type candidate struct {
		kind string
		date time.Time
	}
	candidates := make([]candidate, 0, 2)
	if user.WebhookNotifyPeriod && !prediction.NextPeriodStart.IsZero() {
		candidates = append(candidates, candidate{kind: services.DueReminderTypePeriod, date: prediction.NextPeriodStart})
	}
	if user.WebhookNotifyOvulation && !prediction.OvulationDate.IsZero() {
		candidates = append(candidates, candidate{kind: services.DueReminderTypeOvulation, date: prediction.OvulationDate})
	}
	if len(candidates) == 0 {
		if !user.WebhookNotifyPeriod && !user.WebhookNotifyOvulation {
			reminder.Reason = "both reminder kinds are switched off"
			return reminder
		}
		reminder.Reason = "nothing can be projected yet"
		return reminder
	}

	chosen := candidates[0]
	for _, candidate := range candidates[1:] {
		// Compare as calendar days so a location-midnight and a UTC-midnight
		// value on the same date never reorder the two.
		if services.CalendarDaysBetween(candidate.date, chosen.date) > 0 {
			chosen = candidate
		}
	}

	reminder.Kind = chosen.kind
	reminder.ProjectedDate = predictDate(chosen.date)
	daysUntil := services.CalendarDaysBetween(today, chosen.date)
	reminder.DaysUntil = &daysUntil
	reminder.DueToday = dueTypes[chosen.kind]
	reminder.Reason = "projected from the owner's cycle history"
	return reminder
}

// preflightSuppressionReason renders WHY the app is withholding the projection,
// in the services layer's own published vocabulary so the reason a script reads
// here is the same one the JSON API and the dashboard carry.
func preflightSuppressionReason(suppression services.PredictionSuppression) string {
	reasons := make([]string, 0, len(suppression.Reasons))
	for _, reason := range suppression.Reasons {
		reasons = append(reasons, string(reason))
	}
	if len(reasons) == 0 {
		return "predictions are suppressed"
	}
	return "predictions suppressed: " + strings.Join(reasons, ", ")
}

// buildPreflightWebhookStatus projects the settings half. Configured is read off
// the stored ciphertext's PRESENCE, which is all this command may know without
// the key; the delivery stamp is a timestamp, not a secret.
func buildPreflightWebhookStatus(user *models.User, now time.Time) preflightWebhookStatus {
	status := preflightWebhookStatus{
		Configured:       strings.TrimSpace(user.WebhookURL) != "",
		Enabled:          user.WebhookEnabled,
		NotifyPeriod:     user.WebhookNotifyPeriod,
		NotifyOvulation:  user.WebhookNotifyOvulation,
		ReminderLeadDays: services.NormalizeReminderLeadDays(user.ReminderLeadDays),
	}
	if user.WebhookLastDeliveredAt != nil && !user.WebhookLastDeliveredAt.IsZero() {
		status.DeliveryRecordExists = true
		status.LastDeliveredAt = user.WebhookLastDeliveredAt.UTC().Format(time.RFC3339)
		age := services.CalendarDaysBetween(*user.WebhookLastDeliveredAt, now)
		if age < 0 {
			// A stamp ahead of the clock says nothing honest about age, so the
			// report states none rather than a negative one.
			age = 0
		}
		status.LastDeliveryAgeDays = &age
	}
	return status
}

// buildPreflightFindings orders the blocking findings so the FIRST one is
// always the one to fix first: there is nowhere to deliver, then delivery is
// switched off, then no kind would be sent, then nothing has ever arrived, then
// the prediction is withheld. Everything that blocks delivery is reported — an
// operator fixing only the top line should not have to re-run to discover the
// next — but the order is the recommended repair sequence.
func buildPreflightFindings(
	user *models.User,
	stats services.CycleStats,
	suppression services.PredictionSuppression,
	webhook preflightWebhookStatus,
	next preflightNextReminder,
) []PreflightFinding {
	findings := make([]PreflightFinding, 0, 8)

	if !webhook.Configured {
		findings = append(findings, PreflightFinding{
			Code:   preflightFindingNoWebhookNoEndpoint,
			Detail: "no webhook endpoint is stored for this owner, so no reminder has anywhere to go",
			Fix:    "set one with: ovumcy webhook set <email> --enabled=true --url-stdin",
		})
	}

	if !user.WebhookEnabled {
		findings = append(findings, PreflightFinding{
			Code:   preflightFindingWebhookDisabled,
			Detail: "webhook delivery is switched off for this owner",
			Fix:    "enable it: ovumcy webhook set <email> --enabled=true",
		})
	}

	if !user.WebhookNotifyPeriod && !user.WebhookNotifyOvulation {
		findings = append(findings, PreflightFinding{
			Code:   preflightFindingTogglesOff,
			Detail: "both reminder kinds are switched off, so even an armed webhook would send nothing",
			Fix:    "switch on the kind you want: ovumcy webhook set <email> --notify-period=true (or --notify-ovulation=true)",
		})
	}

	// A delivery record only says something once the endpoint is configured and
	// armed: an owner who has never turned delivery on has simply never asked
	// for one, and reporting that as a defect would misdescribe their account.
	if webhook.Configured && user.WebhookEnabled {
		switch {
		case !webhook.DeliveryRecordExists:
			findings = append(findings, PreflightFinding{
				Code:   preflightFindingNoDelivery,
				Detail: "the endpoint is armed but this instance has recorded no accepted delivery, so nothing is known to have arrived",
				Fix:    "run one pass to verify the path end to end: ovumcy notify --dry-run (then, when the preview looks right, ovumcy notify)",
			})
		case webhook.LastDeliveryAgeDays != nil && *webhook.LastDeliveryAgeDays > preflightStaleDeliveryDays:
			findings = append(findings, PreflightFinding{
				Code: preflightFindingStaleDelivery,
				Detail: fmt.Sprintf("the last accepted delivery was %d days ago (%s), older than the %d-day expectation",
					*webhook.LastDeliveryAgeDays, webhook.LastDeliveredAt, preflightStaleDeliveryDays),
				Fix: "check the schedule that runs `ovumcy notify` is still firing, then run one pass: ovumcy notify --dry-run",
			})
		}
	}

	if suppression.PredictionsSuppressed {
		findings = append(findings, PreflightFinding{
			Code:   preflightFindingPredictionsSuppressed,
			Detail: preflightSuppressionReason(suppression),
			Fix:    preflightSuppressionFix(suppression),
		})
	}

	// No-history and no-projection are the prediction half's two distinct
	// failures, and they get different fixes: the first needs data, the second
	// needs the settings checked. Both are skipped under suppression, which is
	// reported above and is the reason the dates are missing.
	switch {
	case suppression.PredictionsSuppressed:
	case stats.LastPeriodStart.IsZero():
		findings = append(findings, PreflightFinding{
			Code:   preflightFindingNoCycleHistory,
			Detail: "no cycle start has ever been logged, so there is no anchor to project a reminder from",
			Fix:    "log a period start (or complete onboarding) for this owner; the next reminder then has a window to come from",
		})
	case next.Kind == "" && preflightOptedIntoAKind(user):
		// Every setting is right and the whole-projection gate is open, yet no
		// opted-in kind produced a date. The common cause is the first-cycle
		// fertility floor: until one cycle completes, the ovulation date is
		// withheld, so an owner with only that kind switched on has nothing to
		// count down to.
		findings = append(findings, PreflightFinding{
			Code:   preflightFindingNoProjection,
			Detail: "predictions are published but produced no date for either switched-on reminder kind",
			Fix:    "switch the period kind back on (it projects from a recorded cycle start), or wait for the first completed cycle",
		})
	}
	return findings
}

// preflightOptedIntoAKind reports whether the owner has switched at least one
// reminder kind on, so the no-projection finding never fires for an owner who
// simply asked for nothing — that case has a finding of its own.
func preflightOptedIntoAKind(user *models.User) bool {
	return user.WebhookNotifyPeriod || user.WebhookNotifyOvulation
}

// preflightSuppressionFix turns a suppression reason into the one-line remedy
// the operator can actually apply. The signals are the services layer's
// published vocabulary, so a new one lands in the default arm rather than
// silently borrowing a neighbour's advice.
func preflightSuppressionFix(suppression services.PredictionSuppression) string {
	for _, reason := range suppression.Reasons {
		switch reason {
		case services.SuppressionReasonUnpredictableCycle:
			return "this owner marked their cycle as unpredictable; clear it in Settings if that is no longer true"
		case services.SuppressionReasonPregnancyPause:
			return "a pregnancy pause is active; it clears when a new cycle start is logged after the pregnancy"
		case services.SuppressionReasonCycleOverdue:
			return "the running cycle is overdue, so every date would be manufactured; a new cycle start restores the projection"
		case services.SuppressionReasonAwaitingFirstCycle:
			return "no cycle has completed yet; the first completed cycle unlocks the projection"
		}
	}
	return "resolve the suppression in the owner's Settings, then re-run preflight"
}

// renderPreflightTable renders the human report: the verdict, then the next
// reminder, then the webhook state, then the ordered findings with their fixes.
// Every fact comes from the same struct the --json payload serialises.
func renderPreflightTable(document PreflightDocument) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "ovumcy preflight — %s\n", document.User.Email)
	fmt.Fprintf(&builder, "as of: %s (%s)\n", document.AsOf, document.Timezone)
	if document.Ready {
		builder.WriteString("verdict: ready — nothing is blocking the next reminder\n")
	} else {
		fmt.Fprintf(&builder, "verdict: not ready — %d blocking finding(s)\n", len(document.Findings))
	}

	next := document.Next
	builder.WriteString("\nnext reminder\n")
	if next.Kind == "" {
		fmt.Fprintf(&builder, "  none — %s\n", next.Reason)
	} else {
		fmt.Fprintf(&builder, "  kind:      %s\n", next.Kind)
		fmt.Fprintf(&builder, "  projected: %s", next.ProjectedDate)
		if next.DaysUntil != nil {
			fmt.Fprintf(&builder, " (in %d day(s))", *next.DaysUntil)
		}
		builder.WriteString("\n")
		fmt.Fprintf(&builder, "  due today: %t (lead window %d day(s))\n", next.DueToday, next.LeadDays)
	}

	webhook := document.Webhook
	builder.WriteString("\nwebhook\n")
	fmt.Fprintf(&builder, "  enabled:      %t\n", webhook.Enabled)
	if webhook.Configured {
		builder.WriteString("  endpoint:     configured (ovumcy webhook show reports its host)\n")
	} else {
		builder.WriteString("  endpoint:     not configured\n")
	}
	fmt.Fprintf(&builder, "  notify:       period %t, ovulation %t\n", webhook.NotifyPeriod, webhook.NotifyOvulation)
	if webhook.DeliveryRecordExists {
		fmt.Fprintf(&builder, "  last delivery: %s\n", webhook.LastDeliveredAt)
	} else {
		builder.WriteString("  last delivery: none recorded\n")
	}

	builder.WriteString("\nfindings\n")
	if len(document.Findings) == 0 {
		builder.WriteString("  none\n")
		return builder.String()
	}
	for rank, finding := range document.Findings {
		fmt.Fprintf(&builder, "  %d. [%s] %s\n", rank+1, finding.Code, finding.Detail)
		fmt.Fprintf(&builder, "     fix: %s\n", finding.Fix)
	}
	return builder.String()
}
