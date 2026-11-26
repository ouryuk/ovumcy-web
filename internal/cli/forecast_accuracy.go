package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/db"
	"github.com/ovumcy/ovumcy-web/internal/models"
	"github.com/ovumcy/ovumcy-web/internal/services"
)

// forecastAccuracyUsage is the single usage string returned for every argument
// error so the contract is stable and testable, mirroring predictUsage.
const forecastAccuracyUsage = "usage: ovumcy forecast-accuracy <email> [--id <id>] [--tz <IANA name>] [--json]"

// forecastAccuracySchemaVersion tags the --json payload so a consumer can
// detect which shape of document it is reading, mirroring predictSchemaVersion.
const forecastAccuracySchemaVersion = "ovumcy-forecast-accuracy/v1"

// forecastAccuracyDisclaimer carries the same caution the predict command
// carries, aimed at the accuracy numbers instead: past accuracy of a
// statistical projection is not a guarantee about the next cycle.
const forecastAccuracyDisclaimer = "accuracy is measured against past cycles and does not guarantee future predictions"

// forecastAccuracyNow resolves "today" for the history anchor. It is a
// variable so tests can pin the clock; production callers never touch it. The
// accuracy derivation itself is clock-free: every per-cycle prediction is made
// at an explicit "as of" date (the actual cycle start being predicted), never
// at the wall clock.
var forecastAccuracyNow = func() time.Time { return time.Now().UTC() }

// forecastAccuracySkipUnpredictable is the reason recorded for a completed
// cycle whose preceding history yielded no projection to compare against.
const forecastAccuracySkipUnpredictable = "no usable projection from the preceding cycles"

// forecastAccuracySkipPregnancyPause is the reason recorded for a completed
// cycle the model would not have predicted at all because a positive
// pregnancy test was the latest signal before it (ResolvePregnancyPause).
const forecastAccuracySkipPregnancyPause = "pregnancy pause active"

// forecastAccuracyOptions holds the parsed `ovumcy forecast-accuracy`
// arguments. The email/id pair is resolved with the same vocabulary as
// `export`, `summary`, `trends` and `predict` (resolveOperatorUser).
type forecastAccuracyOptions struct {
	userID uint
	email  string
	tz     string
	asJSON bool
}

// ForecastAccuracyDocument is the payload `ovumcy forecast-accuracy --json`
// emits and the same struct the human table renders from, so a script and an
// operator reading stdout can never disagree about what the command measured.
type ForecastAccuracyDocument struct {
	Schema      string                  `json:"schema"`
	GeneratedAt string                  `json:"generated_at"`
	User        predictUserSummary      `json:"user"`
	Timezone    string                  `json:"timezone"`
	Completed   int                     `json:"completed_cycles"`
	Evaluated   int                     `json:"evaluated_cycles"`
	Skipped     int                     `json:"skipped_cycles"`
	Cycles      []forecastAccuracyCycle `json:"cycles"`
	Summary     forecastAccuracySummary `json:"summary"`
	Disclaimer  string                  `json:"disclaimer"`
}

// forecastAccuracyCycle is one completed cycle's scorecard: the prediction the
// model WOULD have made from the cycles before it (made at the actual start
// date itself, the "as of" moment a live prediction would have been issued),
// the signed error against what actually happened, and whether the actual
// start landed inside the model's own predicted length range.
//
// PredictedStart is empty and Skipped true when the model had no projection to
// offer for this cycle (too little history, or a pregnancy pause); Reason then
// says which. ErrorDays is signed: positive when the actual start came LATER
// than predicted (the model was early), negative when it came earlier, and is
// zero for skipped rows. WithinWindow reports whether the actual start fell
// inside [LastPeriodStart + MinCycleLength, LastPeriodStart + MaxCycleLength]
// from the same stats — the model's own recent length range, the widest window
// its derivation itself admits.
type forecastAccuracyCycle struct {
	ActualStart    string                `json:"actual_start"`
	PredictedStart string                `json:"predicted_start"`
	ErrorDays      int                   `json:"error_days"`
	Skipped        bool                  `json:"skipped"`
	Reason         string                `json:"reason"`
	WithinWindow   bool                  `json:"within_window"`
	Basis          forecastAccuracyBasis `json:"basis"`
}

type forecastAccuracyBasis struct {
	CompletedCycles      int `json:"completed_cycles"`
	MedianCycleLength    int `json:"median_cycle_length"`
	PredictedCycleLength int `json:"predicted_cycle_length"`
}

type forecastAccuracySummary struct {
	MeanAbsoluteError   float64 `json:"mean_absolute_error_days"`
	MedianAbsoluteError float64 `json:"median_absolute_error_days"`
	WorstErrorDays      int     `json:"worst_error_days"`
	WorstActualStart    string  `json:"worst_actual_start"`
	WorstPredictedStart string  `json:"worst_predicted_start"`
	PredictionsTotal    int     `json:"predictions_total"`
	WithinWindow        int     `json:"within_window"`
	WithinWindowPercent float64 `json:"within_window_percent"`
}

// forecastAccuracyLogReader adapts the CLI's daily-log repository to the
// services.StatsDayReader seam BuildCycleHistory reads through, so the cycle
// starts this command scores come from the SAME detection the stats page and
// the cycle-history API use rather than a second implementation.
type forecastAccuracyLogReader struct {
	repo   *db.DailyLogRepository
	userID uint
}

func (reader *forecastAccuracyLogReader) FetchLogsForUser(ctx context.Context, _ uint, from time.Time, to time.Time, _ *time.Location) ([]models.DailyLog, error) {
	return reader.repo.ListByUserRange(ctx, reader.userID, &from, &to)
}

func (reader *forecastAccuracyLogReader) FetchAllLogsForUser(ctx context.Context, _ uint) ([]models.DailyLog, error) {
	return reader.repo.ListByUser(ctx, reader.userID)
}

// RunForecastAccuracyCommand is the operator entry point for `ovumcy
// forecast-accuracy`: a read-only, local-only command that walks one owner's
// completed cycles in order and, for each one, recomputes the projection the
// model WOULD have made from the cycles before it — through the same
// BuildCycleStatsFromLogs derivation the dashboard, the calendar feed and
// `ovumcy predict` use, run at an explicit past "as of" date — then scores it
// against the actual start. Like `predict` it needs no secret: nothing is
// written and nothing sensitive beyond the owner's own health data is read.
func RunForecastAccuracyCommand(databaseConfig db.Config, args []string) error {
	return runForecastAccuracyCommand(databaseConfig, args, os.Stdout)
}

// runForecastAccuracyCommand parses the arguments, opens the database,
// resolves the target account, scores every completed cycle and renders the
// result. output is injected so tests drive stdout with a buffer.
func runForecastAccuracyCommand(databaseConfig db.Config, args []string, output io.Writer) error {
	opts, err := parseForecastAccuracyArgs(args)
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

	user, found, err := repositories.Users.FindByIDOptional(context.Background(), summary.ID)
	if err != nil {
		return fmt.Errorf("forecast-accuracy user lookup: %w", err)
	}
	if !found {
		return fmt.Errorf("forecast-accuracy user lookup: %w", services.ErrOperatorUserNotFound)
	}

	// The full history — every completed cycle trains the lengths and is a
	// scored prediction target — so no window is applied, unlike `summary`.
	logs, err := repositories.DailyLogs.ListByUserRange(context.Background(), user.ID, nil, nil)
	if err != nil {
		return fmt.Errorf("forecast-accuracy daily logs: %w", err)
	}

	location, err := time.LoadLocation(opts.tz)
	if err != nil {
		return errors.New(forecastAccuracyUsage)
	}

	// The scored cycle starts come from the shared detection
	// (StatsService.BuildCycleHistory over the same log read seam), not a
	// second cycle detector. Only the STARTS are consumed from it; the
	// per-cycle projections are recomputed below at explicit past dates so a
	// cycle is scored against the history that existed BEFORE it.
	reader := &forecastAccuracyLogReader{repo: repositories.DailyLogs, userID: user.ID}
	history, err := services.NewStatsService(reader, nil).BuildCycleHistory(context.Background(), &user, forecastAccuracyNow(), location)
	if err != nil {
		return fmt.Errorf("forecast-accuracy cycle history: %w", err)
	}

	document := buildForecastAccuracyDocument(summary, opts.tz, &user, history, logs, location)

	if output == nil {
		output = os.Stdout
	}
	if opts.asJSON {
		payload, err := json.MarshalIndent(document, "", "  ")
		if err != nil {
			return fmt.Errorf("render forecast-accuracy: %w", err)
		}
		payload = append(payload, '\n')
		_, _ = output.Write(payload)
		return nil
	}
	_, _ = io.WriteString(output, renderForecastAccuracyTable(document))
	return nil
}

// parseForecastAccuracyArgs parses `forecast-accuracy <email> [--id <id>]
// [--tz <IANA name>] [--json]`. Flags accept both `--flag value` and
// `--flag=value`; an --id with no email is accepted as in `export`; --tz
// defaults to UTC and must resolve to a location time.LoadLocation accepts; a
// second positional, an unknown flag or a bad --tz returns
// forecastAccuracyUsage.
func parseForecastAccuracyArgs(args []string) (forecastAccuracyOptions, error) {
	opts := forecastAccuracyOptions{tz: "UTC"}
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		if arg == "" {
			continue
		}
		if !strings.HasPrefix(arg, "--") {
			if opts.email != "" {
				return forecastAccuracyOptions{}, errors.New(forecastAccuracyUsage)
			}
			normalized, err := normalizeOperatorEmailArgument(arg)
			if err != nil {
				return forecastAccuracyOptions{}, err
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
					return forecastAccuracyOptions{}, errors.New(forecastAccuracyUsage)
				}
				index++
				value = strings.TrimSpace(args[index])
			case "json":
				opts.asJSON = true
				continue
			default:
				return forecastAccuracyOptions{}, errors.New(forecastAccuracyUsage)
			}
		}

		switch name {
		case "id":
			parsed, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				return forecastAccuracyOptions{}, errors.New(forecastAccuracyUsage)
			}
			opts.userID = uint(parsed)
		case "tz":
			if _, err := time.LoadLocation(value); err != nil {
				return forecastAccuracyOptions{}, errors.New(forecastAccuracyUsage)
			}
			opts.tz = value
		case "json":
			return forecastAccuracyOptions{}, errors.New(forecastAccuracyUsage)
		default:
			return forecastAccuracyOptions{}, errors.New(forecastAccuracyUsage)
		}
	}

	if opts.email == "" && opts.userID == 0 {
		return forecastAccuracyOptions{}, errors.New(forecastAccuracyUsage)
	}
	return opts, nil
}

// filterLogsBeforeDay keeps only the logs strictly BEFORE the cutoff calendar
// day. The per-cycle training pass must not see the cycle it is predicting:
// BuildCycleStatsFromLogs bounds its input at "not after" its own now, so the
// strict bound is applied here, before the derivation runs.
func filterLogsBeforeDay(logs []models.DailyLog, cutoff time.Time) []models.DailyLog {
	filtered := make([]models.DailyLog, 0, len(logs))
	for _, log := range logs {
		if services.CalendarDaysBetween(log.Date, cutoff) > 0 {
			filtered = append(filtered, log)
		}
	}
	return filtered
}

// scoreForecastAccuracyCycle recomputes the projection the model would have
// made for the cycle starting at actualStart, from only the logs that existed
// before it, and measures the signed error against the actual start. The
// derivation is the shared BuildCycleStatsFromLogs run at an explicit past
// "as of" date — no wall clock, no randomness — so the score is deterministic
// for a given history.
func scoreForecastAccuracyCycle(user *models.User, logs []models.DailyLog, actualStart time.Time, location *time.Location) forecastAccuracyCycle {
	row := forecastAccuracyCycle{ActualStart: predictDate(actualStart)}

	training := filterLogsBeforeDay(logs, actualStart)
	stats := services.BuildCycleStatsFromLogs(user, training, actualStart, location)
	row.Basis = forecastAccuracyBasis{
		CompletedCycles:      stats.CompletedCycleCount,
		MedianCycleLength:    stats.MedianCycleLength,
		PredictedCycleLength: predictedCycleLengthBasis(stats),
	}

	if stats.PregnancyPaused {
		row.Skipped = true
		row.Reason = forecastAccuracySkipPregnancyPause
		return row
	}
	if stats.LastPeriodStart.IsZero() || stats.NextPeriodStart.IsZero() {
		row.Skipped = true
		row.Reason = forecastAccuracySkipUnpredictable
		return row
	}

	row.PredictedStart = predictDate(stats.NextPeriodStart)
	row.ErrorDays = services.CalendarDaysBetween(stats.NextPeriodStart, actualStart)

	// The window is the model's own recent length range from the SAME stats:
	// [last start + min recent length, last start + max recent length]. When
	// no observed lengths exist (the baseline fell back to the onboarding
	// defaults) MinCycleLength is 0 and no window is credited.
	if stats.MinCycleLength > 0 && stats.MaxCycleLength >= stats.MinCycleLength {
		windowStart := services.AddCalendarDays(stats.LastPeriodStart, stats.MinCycleLength, location)
		windowEnd := services.AddCalendarDays(stats.LastPeriodStart, stats.MaxCycleLength, location)
		row.WithinWindow = services.CalendarDaysBetween(windowStart, actualStart) >= 0 &&
			services.CalendarDaysBetween(actualStart, windowEnd) >= 0
	}
	return row
}

// buildForecastAccuracyDocument scores every completed cycle the shared
// history reports (skipping the first, which has no preceding cycle to train
// on) and folds the per-cycle errors into the summary.
func buildForecastAccuracyDocument(user models.OperatorUserSummary, timezone string, owner *models.User, history []services.CycleHistoryEntry, logs []models.DailyLog, location *time.Location) ForecastAccuracyDocument {
	document := ForecastAccuracyDocument{
		Schema:      forecastAccuracySchemaVersion,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		User:        predictUserSummary{ID: user.ID, Email: user.Email},
		Timezone:    timezone,
		Completed:   len(history),
		Cycles:      []forecastAccuracyCycle{},
		Disclaimer:  forecastAccuracyDisclaimer,
	}

	absoluteErrors := make([]int, 0, len(history))
	for index := 1; index < len(history); index++ {
		row := scoreForecastAccuracyCycle(owner, logs, history[index].Start, location)
		document.Cycles = append(document.Cycles, row)
		if row.Skipped {
			document.Skipped++
			continue
		}
		document.Evaluated++
		absoluteErrors = append(absoluteErrors, absInt(row.ErrorDays))
	}

	document.Summary = buildForecastAccuracySummary(document.Cycles, absoluteErrors)
	return document
}

// buildForecastAccuracySummary folds the evaluated rows into mean/median
// absolute error, the worst cycle and the within-window count. With nothing
// evaluated every field holds its zero value — "cannot say" is a zero
// summary, never a fabricated number.
func buildForecastAccuracySummary(cycles []forecastAccuracyCycle, absoluteErrors []int) forecastAccuracySummary {
	summary := forecastAccuracySummary{PredictionsTotal: len(absoluteErrors)}
	if len(absoluteErrors) == 0 {
		return summary
	}

	total := 0
	for _, value := range absoluteErrors {
		total += value
	}
	summary.MeanAbsoluteError = math.Round(float64(total)/float64(len(absoluteErrors))*10) / 10
	summary.MedianAbsoluteError = medianFloat(absoluteErrors)

	for _, row := range cycles {
		if row.Skipped {
			continue
		}
		if errorDays := absInt(row.ErrorDays); summary.WorstPredictedStart == "" || errorDays > summary.WorstErrorDays {
			summary.WorstErrorDays = errorDays
			summary.WorstActualStart = row.ActualStart
			summary.WorstPredictedStart = row.PredictedStart
		}
		if row.WithinWindow {
			summary.WithinWindow++
		}
	}
	summary.WithinWindowPercent = math.Round(float64(summary.WithinWindow)/float64(summary.PredictionsTotal)*1000) / 10
	return summary
}

// medianFloat returns the median of a copied, sorted int slice as a float,
// averaging the two middle values for an even count (so 0 and 1 → 0.5).
func medianFloat(values []int) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := make([]int, len(values))
	copy(sorted, values)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j-1] > sorted[j]; j-- {
			sorted[j-1], sorted[j] = sorted[j], sorted[j-1]
		}
	}
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return float64(sorted[mid])
	}
	return float64(sorted[mid-1]+sorted[mid]) / 2
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

// renderForecastAccuracyTable renders the human table: one row per completed
// cycle with its predicted and actual start and the signed error, then the
// summary line.
func renderForecastAccuracyTable(document ForecastAccuracyDocument) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "ovumcy forecast-accuracy — %s\n", document.User.Email)
	fmt.Fprintf(&builder, "timezone: %s, completed cycles: %d\n", document.Timezone, document.Completed)
	if document.Completed == 0 {
		builder.WriteString("no cycle history: accuracy cannot be measured yet\n")
		builder.WriteString(fmt.Sprintf("%s\n", document.Disclaimer))
		return builder.String()
	}
	if document.Evaluated == 0 {
		builder.WriteString("not enough completed cycles with a usable projection: accuracy cannot be measured yet\n")
		builder.WriteString(fmt.Sprintf("%s\n", document.Disclaimer))
		return builder.String()
	}

	fmt.Fprintf(&builder, "evaluated %d of %d completed cycles (%d skipped)\n", document.Evaluated, document.Completed, document.Skipped)
	for _, row := range document.Cycles {
		if row.Skipped {
			fmt.Fprintf(&builder, "%s  skipped (%s)\n", row.ActualStart, row.Reason)
			continue
		}
		fmt.Fprintf(&builder, "%s  predicted %s  error %+dd  in window: %t\n",
			row.ActualStart, row.PredictedStart, row.ErrorDays, row.WithinWindow)
	}
	summary := document.Summary
	fmt.Fprintf(&builder, "summary: mean abs error %.1fd, median %.1fd, worst %dd (actual %s, predicted %s)\n",
		summary.MeanAbsoluteError, summary.MedianAbsoluteError, summary.WorstErrorDays, summary.WorstActualStart, summary.WorstPredictedStart)
	fmt.Fprintf(&builder, "within predicted window: %d/%d (%.1f%%)\n", summary.WithinWindow, summary.PredictionsTotal, summary.WithinWindowPercent)
	fmt.Fprintf(&builder, "%s\n", document.Disclaimer)
	return builder.String()
}
