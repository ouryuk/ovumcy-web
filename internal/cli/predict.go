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

// predictUsage is the single usage string returned for every argument error so
// the contract is stable and testable, mirroring trendsUsage. --tz is described
// as overriding the owner's stored zone because that is what it now does.
const predictUsage = "usage: ovumcy predict <email> [--id <id>] [--tz <IANA name>] [--json]"

// predictSchemaVersion tags the --json payload so a consumer can detect which
// shape of document it is reading, mirroring summarySchemaVersion.
const predictSchemaVersion = "ovumcy-predict/v1"

// predictDisclaimer is the one sentence every predict document carries, in
// both renderings, so no consumer — human or script — can present a projected
// date as a fact. The dashboard and the calendar feed say the same thing in
// their own copy; this command says it here.
const predictDisclaimer = "these dates are projections from past cycles, not guarantees"

// predictNow resolves the instant the projection is anchored at. It is a
// variable so tests can pin the clock; production callers never touch it.
//
// It returns the INSTANT, and the command resolves "today" from it through the
// OWNER's zone (resolveOperatorLocation): taking time.Now().UTC() as the anchor
// itself made the command answer for the server's calendar day, so an owner in
// America/Chicago was projected from the previous day every evening after 18:00
// CST — a cycle day, and near a cycle boundary a whole phase, behind what the
// dashboard showed for the same account at the same instant.
var predictNow = func() time.Time { return time.Now() }

// predictOptions holds the parsed `ovumcy predict` arguments. The email/id
// pair is resolved with the same vocabulary as `export` and `summary`
// (resolveOperatorUser).
type predictOptions struct {
	userID uint
	email  string
	// tz is the --tz override; tzSet records that it was given. Without the
	// second field an omitted --tz and an explicit `--tz UTC` would be
	// indistinguishable, and the command could not tell "use the owner's zone"
	// from "the operator asked for UTC on purpose".
	tz     string
	tzSet  bool
	asJSON bool
}

// PredictDocument is the payload `ovumcy predict --json` emits and the same
// struct the human table renders from, so a script and an operator reading
// stdout can never disagree about what the command projected.
type PredictDocument struct {
	Schema      string             `json:"schema"`
	GeneratedAt string             `json:"generated_at"`
	User        predictUserSummary `json:"user"`
	Timezone    string             `json:"timezone"`
	AsOf        string             `json:"as_of"`

	// LastPeriodStart is the anchor every projection steps from: the most
	// recent detected cycle start. Empty when the owner has no log history.
	LastPeriodStart string `json:"last_period_start"`

	// CurrentCycleDay / CurrentPhase / CurrentFertility report where the
	// owner stands today, exactly as the dashboard does (the same
	// BuildCycleStatsFromLogs derivation).
	CurrentCycleDay     int    `json:"current_cycle_day"`
	CurrentPhase        string `json:"current_phase"`
	CurrentFertility    string `json:"current_fertility"`
	PregnancyPaused     bool   `json:"pregnancy_paused"`
	OvulationImpossible bool   `json:"ovulation_impossible"`

	// CycleBasis is the statistics the projections are computed from, so an
	// operator can judge how much to trust them: a median over twenty
	// completed cycles is not a default carried from onboarding.
	CycleBasis predictCycleBasis `json:"cycle_basis"`

	// NextPeriodStart is the projected first day of the next cycle: the
	// anchor plus the predicted cycle length. Empty when no window could be
	// projected.
	NextPeriodStart string `json:"next_period_start"`

	// OvulationDate is the projected ovulation day of the cycle anchored at
	// LastPeriodStart — the cycle the owner is in — and FertilityWindow is
	// its six-day fertile range [ovulation − 5, ovulation]. OvulationExact is
	// false when the luteal phase had to be clamped to fit the cycle length.
	// All three are empty/false when no window could be projected.
	OvulationDate        string `json:"ovulation_date"`
	FertilityWindowStart string `json:"fertility_window_start"`
	FertilityWindowEnd   string `json:"fertility_window_end"`
	OvulationExact       bool   `json:"ovulation_exact"`

	Disclaimer string `json:"disclaimer"`
}

type predictUserSummary struct {
	ID    uint   `json:"id"`
	Email string `json:"email"`
}

type predictCycleBasis struct {
	CompletedCycles         int     `json:"completed_cycles"`
	AverageCycleLength      float64 `json:"average_cycle_length"`
	MedianCycleLength       int     `json:"median_cycle_length"`
	PredictedCycleLength    int     `json:"predicted_cycle_length"`
	PredictedPeriodLength   int     `json:"predicted_period_length"`
	LutealPhase             int     `json:"luteal_phase"`
	LutealPhasePersonalised bool    `json:"luteal_phase_personalised"`
}

// RunPredictCommand is the operator entry point for `ovumcy predict`: a
// read-only, local-only command that projects one owner's next cycle window —
// next period start, ovulation date and fertile range — from the same
// derivation the dashboard, the calendar feed and the webhook notify pass all
// use (BuildCycleStatsFromLogs). Like `export` it needs no secret: the payload
// is derived from the owner's health data, and nothing is written.
func RunPredictCommand(databaseConfig db.Config, args []string) error {
	return runPredictCommand(databaseConfig, args, os.Stdout)
}

// runPredictCommand parses the arguments, opens the database, resolves the
// target account, projects the next window and renders it. output is injected
// so tests drive stdout with a buffer.
func runPredictCommand(databaseConfig db.Config, args []string, output io.Writer) error {
	opts, err := parsePredictArgs(args)
	if err != nil {
		return err
	}

	// Read-only: `predict` projects from the owner's logs and never writes.
	database, err := openCLIDatabase(databaseConfig, true)
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

	// The baseline pass consumes the FULL user row (onboarding cycle and
	// period lengths, the explicit-start flag), not just the summary
	// resolveOperatorUser returned.
	user, found, err := repositories.Users.FindByIDOptional(context.Background(), summary.ID)
	if err != nil {
		return fmt.Errorf("predict user lookup: %w", err)
	}
	if !found {
		return fmt.Errorf("predict user lookup: %w", services.ErrOperatorUserNotFound)
	}

	// The projection needs the owner's full history — every completed cycle
	// trains the lengths — so no window is applied, unlike `summary`.
	logs, err := repositories.DailyLogs.ListByUserRange(context.Background(), user.ID, nil, nil)
	if err != nil {
		return fmt.Errorf("predict daily logs: %w", err)
	}

	// The owner's persisted zone is the default; --tz overrides it for the
	// invocation. Both resolve through resolveOperatorLocation, so an absent or
	// unusable stored zone degrades to UTC instead of to the server's own zone.
	timezoneName := opts.tz
	if !opts.tzSet {
		_, timezoneName = resolveOperatorLocation(user.Timezone)
	}
	location, timezoneName := resolveOperatorLocation(timezoneName)
	now := predictNow()

	// Package-level, not a method: the derivation consults no repositories,
	// so the CLI cannot drift from what the dashboard shows for the same
	// account at the same instant.
	stats := services.BuildCycleStatsFromLogs(&user, logs, now, location)
	document := buildPredictDocument(summary, timezoneName, now, stats)

	if output == nil {
		output = os.Stdout
	}
	if opts.asJSON {
		payload, err := json.MarshalIndent(document, "", "  ")
		if err != nil {
			return fmt.Errorf("render predict: %w", err)
		}
		payload = append(payload, '\n')
		if _, err := output.Write(payload); err != nil {
			return fmt.Errorf("write predict: %w", err)
		}
		return nil
	}
	if _, err := io.WriteString(output, renderPredictTable(document)); err != nil {
		return fmt.Errorf("write predict: %w", err)
	}
	return nil
}

// parsePredictArgs parses `predict <email> [--id <id>] [--tz <IANA name>]
// [--json]`. Flags accept both `--flag value` and `--flag=value`; an --id
// with no email is accepted as in `export`; --tz defaults to UTC and must
// resolve to a location time.LoadLocation accepts; a second positional, an
// unknown flag or a bad --tz returns predictUsage.
func parsePredictArgs(args []string) (predictOptions, error) {
	opts := predictOptions{}
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		if arg == "" {
			continue
		}
		if !strings.HasPrefix(arg, "--") {
			if opts.email != "" {
				return predictOptions{}, errors.New(predictUsage)
			}
			normalized, err := normalizeOperatorEmailArgument(arg)
			if err != nil {
				return predictOptions{}, err
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
					return predictOptions{}, errors.New(predictUsage)
				}
				index++
				value = strings.TrimSpace(args[index])
			case "json":
				opts.asJSON = true
				continue
			default:
				return predictOptions{}, errors.New(predictUsage)
			}
		}

		switch name {
		case "id":
			parsed, err := parseOperatorUserID(value)
			if err != nil {
				return predictOptions{}, predictIDError(value)
			}
			opts.userID = parsed
		case "tz":
			if _, err := time.LoadLocation(value); err != nil {
				return predictOptions{}, errors.New(predictUsage)
			}
			opts.tz = value
			opts.tzSet = true
		case "json":
			return predictOptions{}, errors.New(predictUsage)
		default:
			return predictOptions{}, errors.New(predictUsage)
		}
	}

	if opts.email == "" && !operatorUserIDSelected(opts.userID) {
		return predictOptions{}, errors.New(predictUsage)
	}
	return opts, nil
}

// predictIDError keeps the non-positive --id message apart from the usage
// string, exactly as export does: the flag was typed correctly and the VALUE is
// the mistake, so the operator has to be told what was wrong with it rather than
// handed the synopsis again. Treating the id as unset is what used to let
// `--id 0 <email>` project a different account than the one addressed.
func predictIDError(value string) error {
	if _, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64); err == nil {
		return errOperatorIDNotPositive(0)
	}
	return errors.New(predictUsage)
}

// predictedCycleLengthBasis mirrors services.predictedCycleLength's choice
// (median, then rounded average, then the model default) so the document's
// basis field states the length the projections were actually stepped by.
// It stays a projection on the document side: if the service ever changes its
// statistic, its own surfaces move with it and the basis test below fails,
// which is the intended tripwire rather than a silent divergence.
func predictedCycleLengthBasis(stats services.CycleStats) int {
	switch {
	case stats.MedianCycleLength > 0:
		return stats.MedianCycleLength
	case stats.AverageCycleLength > 0:
		return int(stats.AverageCycleLength + 0.5)
	default:
		return models.DefaultCycleLength
	}
}

// predictedPeriodLengthBasis mirrors services.predictedPeriodLength over the
// average period length the observed cycles produced.
func predictedPeriodLengthBasis(stats services.CycleStats) int {
	length := int(stats.AveragePeriodLength + 0.5)
	if length > 0 {
		return length
	}
	return models.DefaultPeriodLength
}

// predictDate renders a projection date as YYYY-MM-DD, or "" when the
// projection is absent (a zero time). It reads the calendar components the
// value carries as-is (CalendarDayKey semantics): the baseline anchors hold
// request-location wall clocks, so a zone conversion here would fabricate a
// different calendar day and silently disagree with the calendar feed.
func predictDate(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format("2006-01-02")
}

// buildPredictDocument projects the cycle stats onto the document `ovumcy
// predict` renders. Every number comes from services.BuildCycleStatsFromLogs
// — nothing is recomputed here.
func buildPredictDocument(user models.OperatorUserSummary, timezone string, now time.Time, stats services.CycleStats) PredictDocument {
	document := PredictDocument{
		Schema:               predictSchemaVersion,
		GeneratedAt:          time.Now().UTC().Format(time.RFC3339),
		User:                 predictUserSummary{ID: user.ID, Email: user.Email},
		Timezone:             timezone,
		AsOf:                 predictDate(now),
		CurrentCycleDay:      stats.CurrentCycleDay,
		CurrentPhase:         stats.CurrentPhase,
		CurrentFertility:     stats.CurrentFertility,
		PregnancyPaused:      stats.PregnancyPaused,
		OvulationImpossible:  stats.OvulationImpossible,
		LastPeriodStart:      predictDate(stats.LastPeriodStart),
		NextPeriodStart:      predictDate(stats.NextPeriodStart),
		OvulationDate:        predictDate(stats.OvulationDate),
		FertilityWindowStart: predictDate(stats.FertilityWindowStart),
		FertilityWindowEnd:   predictDate(stats.FertilityWindowEnd),
		OvulationExact:       stats.OvulationExact,
		CycleBasis: predictCycleBasis{
			CompletedCycles:         stats.CompletedCycleCount,
			AverageCycleLength:      stats.AverageCycleLength,
			MedianCycleLength:       stats.MedianCycleLength,
			PredictedCycleLength:    predictedCycleLengthBasis(stats),
			PredictedPeriodLength:   predictedPeriodLengthBasis(stats),
			LutealPhase:             stats.LutealPhase,
			LutealPhasePersonalised: stats.LutealPhasePersonalised,
		},
		Disclaimer: predictDisclaimer,
	}
	return document
}

// renderPredictTable renders the human table: where the owner stands today,
// the basis the projection was computed from, then the three projected dates.
func renderPredictTable(document PredictDocument) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "ovumcy predict — %s\n", document.User.Email)
	fmt.Fprintf(&builder, "as of: %s (%s)\n", document.AsOf, document.Timezone)
	if document.PregnancyPaused {
		builder.WriteString("pregnancy pause active: predictions are suppressed\n")
		return builder.String()
	}
	if document.LastPeriodStart == "" {
		builder.WriteString("no cycle history: nothing can be projected yet\n")
		return builder.String()
	}
	fmt.Fprintf(&builder, "cycle day %d, phase %s, fertility %s\n", document.CurrentCycleDay, document.CurrentPhase, document.CurrentFertility)
	basis := document.CycleBasis
	fmt.Fprintf(&builder, "basis: %d completed cycles, median %dd, predicted cycle %dd, predicted period %dd, luteal %dd\n",
		basis.CompletedCycles, basis.MedianCycleLength, basis.PredictedCycleLength, basis.PredictedPeriodLength, basis.LutealPhase)
	fmt.Fprintf(&builder, "last period start: %s\n", document.LastPeriodStart)
	if document.NextPeriodStart == "" {
		builder.WriteString("projection: not predictable (irregular or too few cycles)\n")
	} else {
		fmt.Fprintf(&builder, "next period start: %s\n", document.NextPeriodStart)
		fmt.Fprintf(&builder, "ovulation (projected): %s (exact: %t)\n", document.OvulationDate, document.OvulationExact)
		fmt.Fprintf(&builder, "fertile window (projected): %s to %s\n", document.FertilityWindowStart, document.FertilityWindowEnd)
	}
	fmt.Fprintf(&builder, "%s\n", document.Disclaimer)
	return builder.String()
}
