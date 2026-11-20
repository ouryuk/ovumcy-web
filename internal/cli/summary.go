package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/db"
	"github.com/ovumcy/ovumcy-web/internal/models"
	"github.com/ovumcy/ovumcy-web/internal/services"
)

// summaryUsage is the single usage string returned for every argument error so
// the contract is stable and testable, mirroring exportUsage.
const summaryUsage = "usage: ovumcy summary <email> [--id <id>] [--days 7|30|90] [--json]"

// summarySchemaVersion tags the --json payload so a consumer can detect which
// shape of document it is reading before it touches anything in it, mirroring
// the export document's schema tag.
const summarySchemaVersion = "ovumcy-summary/v1"

// summaryWindows are the aggregation windows `ovumcy summary` accepts. A
// summary is only meaningful over a bounded window; anything else invites
// comparing an all-time number against a monthly habit.
var summaryWindows = map[int]bool{7: true, 30: true, 90: true}

// summaryNow resolves "today" for the window's end. It is a variable so tests
// can pin the clock; production callers never touch it.
var summaryNow = func() time.Time { return time.Now().UTC() }

// summaryOptions holds the parsed `summary` arguments. The email/id pair is
// resolved with the same vocabulary as `export` (resolveOperatorUser).
type summaryOptions struct {
	userID uint
	email  string
	days   int
	asJSON bool
}

// SummaryDocument is the payload `ovumcy summary --json` emits and the same
// struct the human table renders from, so a script and an operator reading
// stdout can never disagree about what the command measured.
type SummaryDocument struct {
	Schema      string             `json:"schema"`
	GeneratedAt string             `json:"generated_at"`
	User        summaryUserSummary `json:"user"`
	WindowDays  int                `json:"window_days"`
	From        string             `json:"from"`
	To          string             `json:"to"`

	// LoggedDays is how many calendar days inside the window carry any log.
	// CalendarDays is the size of the window (the bound of the inclusive date
	// range, never more). AdherencePercent is LoggedDays / CalendarDays.
	LoggedDays       int     `json:"logged_days"`
	CalendarDays     int     `json:"calendar_days"`
	AdherencePercent float64 `json:"adherence_percent"`

	// SymptomCounts tallies every symptom occurrence across the window's
	// logs, most-used first (ties broken by name). TopSymptoms is the first
	// three of those — the "what's driving this month" view.
	SymptomCounts []summarySymptomCount `json:"symptom_counts"`
	TopSymptoms   []summarySymptomCount `json:"top_symptoms"`

	// BestDay / WorstDay rank days that logged a mood (1–5) by derived
	// severity (6 − mood: mood 5 → severity 1, mood 1 → severity 5). Days
	// without a mood are excluded — a missing mood is not a severity of zero.
	// When no day in the window logged a mood both are null.
	BestDay  *summaryDay `json:"best_day"`
	WorstDay *summaryDay `json:"worst_day"`

	// LongestStreak is the longest run of consecutive calendar days inside
	// the window that each carry a log.
	LongestStreak int `json:"longest_streak"`
}

type summaryUserSummary struct {
	ID    uint   `json:"id"`
	Email string `json:"email"`
}

type summarySymptomCount struct {
	SymptomID uint   `json:"symptom_id"`
	Name      string `json:"name"`
	Count     int    `json:"count"`
}

type summaryDay struct {
	Date        string  `json:"date"`
	Mood        int     `json:"mood"`
	SeveritySum float64 `json:"-"`
}

// RunSummaryCommand is the operator entry point for `ovumcy summary`: a
// read-only, local-only command that aggregates one owner's daily logs over a
// 7/30/90-day window — adherence, symptom tallies, best/worst mood days and
// the longest logging streak — as a human table on stdout, or the same
// numbers as JSON with --json. Like `export` it needs no secret: the payload
// is derived from the owner's health data, and nothing is written.
func RunSummaryCommand(databaseConfig db.Config, args []string) error {
	return runSummaryCommand(databaseConfig, args, os.Stdout)
}

// runSummaryCommand parses the arguments, opens the database, resolves the
// target account, aggregates the window and renders it. output is injected so
// tests drive stdout with a buffer.
func runSummaryCommand(databaseConfig db.Config, args []string, output io.Writer) error {
	opts, err := parseSummaryArgs(args)
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

	user, err := resolveOperatorUser(userService, opts.userID, opts.email)
	if err != nil {
		// resolveOperatorUser's own vocabulary already advises the operator
		// (`users list`, `retry with --id`); this command adds nothing.
		return mapOperatorUserLookupError(err, opts.userID, opts.email)
	}

	to := summaryNow()
	year, month, day := to.Date()
	to = time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	from := to.AddDate(0, 0, -(opts.days - 1))
	// ListByUserRange's upper bound is EXCLUSIVE (`date < to`), so the window
	// "to" day is included by querying one day past it. The lower bound is
	// inclusive and the logs are stored at midnight, so from must be midnight
	// too — a mid-day anchor would silently drop the window's first day.
	windowEnd := to.AddDate(0, 0, 1)
	logs, err := repositories.DailyLogs.ListByUserRange(context.Background(), user.ID, &from, &windowEnd)
	if err != nil {
		return fmt.Errorf("summary daily logs: %w", err)
	}
	symptoms, err := repositories.Symptoms.ListByUser(context.Background(), user.ID)
	if err != nil {
		return fmt.Errorf("summary symptoms: %w", err)
	}

	document := buildSummaryDocument(user, opts.days, from, to, logs, symptoms)

	if opts.asJSON {
		payload, err := json.MarshalIndent(document, "", "  ")
		if err != nil {
			return fmt.Errorf("render summary: %w", err)
		}
		payload = append(payload, '\n')
		if output == nil {
			output = os.Stdout
		}
		_, _ = output.Write(payload)
		return nil
	}

	if output == nil {
		output = os.Stdout
	}
	_, _ = io.WriteString(output, renderSummaryTable(document))
	return nil
}

// parseSummaryArgs parses `summary <email> [--id <id>] [--days 7|30|90]
// [--json]`. Flags accept both `--flag value` and `--flag=value`. An --id with
// no email is accepted, as in `export`; a second positional, an unknown flag,
// or a --days value outside {7,30,90} returns summaryUsage.
func parseSummaryArgs(args []string) (summaryOptions, error) {
	opts := summaryOptions{days: 30}
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		if arg == "" {
			continue
		}
		if !strings.HasPrefix(arg, "--") {
			if opts.email != "" {
				return summaryOptions{}, errors.New(summaryUsage)
			}
			normalized, err := normalizeOperatorEmailArgument(arg)
			if err != nil {
				return summaryOptions{}, err
			}
			opts.email = normalized
			continue
		}

		name, value, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		// --flag value form: consume the next argument as the value.
		if !hasValue {
			switch name {
			case "id", "days":
				if index+1 >= len(args) {
					return summaryOptions{}, errors.New(summaryUsage)
				}
				index++
				value = strings.TrimSpace(args[index])
			case "json":
				opts.asJSON = true
				continue
			default:
				return summaryOptions{}, errors.New(summaryUsage)
			}
		}

		switch name {
		case "id":
			parsed, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				return summaryOptions{}, errors.New(summaryUsage)
			}
			opts.userID = uint(parsed)
		case "days":
			parsed, err := strconv.Atoi(value)
			if err != nil || !summaryWindows[parsed] {
				return summaryOptions{}, errors.New(summaryUsage)
			}
			opts.days = parsed
		case "json":
			return summaryOptions{}, errors.New(summaryUsage)
		default:
			return summaryOptions{}, errors.New(summaryUsage)
		}
	}

	if opts.email == "" && opts.userID == 0 {
		return summaryOptions{}, errors.New(summaryUsage)
	}
	return opts, nil
}

// buildSummaryDocument aggregates the window from the resolved account's
// daily logs and symptom vocabulary. It only ever reads what the store layer
// already serves: no schema change, no write.
func buildSummaryDocument(user models.OperatorUserSummary, windowDays int, from, to time.Time, logs []models.DailyLog, symptoms []models.SymptomType) SummaryDocument {
	document := SummaryDocument{
		Schema:        summarySchemaVersion,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		User:          summaryUserSummary{ID: user.ID, Email: user.Email},
		WindowDays:    windowDays,
		From:          from.Format("2006-01-02"),
		To:            to.Format("2006-01-02"),
		CalendarDays:  windowDays,
		SymptomCounts: []summarySymptomCount{},
		TopSymptoms:   []summarySymptomCount{},
	}
	if document.CalendarDays < 1 {
		document.CalendarDays = 1
	}

	symptomNames := make(map[uint]string, len(symptoms))
	for _, symptom := range symptoms {
		symptomNames[symptom.ID] = symptom.Name
	}

	counts := make(map[uint]int)
	logged := make(map[string]bool)
	// Streak tracking: walk the logs in date order and count runs of
	// consecutive days.
	var orderedDates []time.Time
	document.LoggedDays = len(logs)
	for _, logEntry := range logs {
		date := logEntry.Date
		year, month, day := date.Date()
		dayKey := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
		orderedDates = append(orderedDates, dayKey)
		logged[dayKey.Format("2006-01-02")] = true
		for _, symptomID := range logEntry.SymptomIDs {
			counts[symptomID]++
		}
		if logEntry.Mood >= services.MinDayMood && logEntry.Mood <= services.MaxDayMood {
			severity := float64(6 - logEntry.Mood)
			day := summaryDay{
				Date:        dayKey.Format("2006-01-02"),
				Mood:        logEntry.Mood,
				SeveritySum: severity,
			}
			if document.BestDay == nil || severity < document.BestDay.SeveritySum ||
				(severity == document.BestDay.SeveritySum && day.Date < document.BestDay.Date) {
				document.BestDay = &summaryDay{Date: day.Date, Mood: day.Mood, SeveritySum: severity}
			}
			if document.WorstDay == nil || severity > document.WorstDay.SeveritySum ||
				(severity == document.WorstDay.SeveritySum && day.Date < document.WorstDay.Date) {
				document.WorstDay = &summaryDay{Date: day.Date, Mood: day.Mood, SeveritySum: severity}
			}
		}
	}
	sort.Slice(orderedDates, func(i, j int) bool { return orderedDates[i].Before(orderedDates[j]) })
	streak, bestStreak := 0, 0
	var previous time.Time
	for _, date := range orderedDates {
		if !previous.IsZero() && date.Equal(previous.AddDate(0, 0, 1)) {
			streak++
		} else {
			streak = 1
		}
		if streak > bestStreak {
			bestStreak = streak
		}
		previous = date
	}
	document.LongestStreak = bestStreak

	for symptomID, count := range counts {
		document.SymptomCounts = append(document.SymptomCounts, summarySymptomCount{
			SymptomID: symptomID,
			Name:      symptomNames[symptomID],
			Count:     count,
		})
	}
	sort.Slice(document.SymptomCounts, func(i, j int) bool {
		if document.SymptomCounts[i].Count != document.SymptomCounts[j].Count {
			return document.SymptomCounts[i].Count > document.SymptomCounts[j].Count
		}
		return document.SymptomCounts[i].Name < document.SymptomCounts[j].Name
	})
	if len(document.SymptomCounts) > 3 {
		document.SymptomCounts = document.SymptomCounts[:3]
	}
	document.TopSymptoms = append(document.TopSymptoms, document.SymptomCounts...)

	if len(logs) > 0 && document.LoggedDays > document.CalendarDays {
		// Defensive: the store layer should never return logs outside the
		// window; adherence must stay a percentage either way.
		document.LoggedDays = document.CalendarDays
	}
	document.AdherencePercent = float64(document.LoggedDays) / float64(document.CalendarDays) * 100
	return document
}

// renderSummaryTable renders the document as a plain-text table for an
// operator's terminal. Every number comes from the same struct the --json
// payload serialises, so the two views cannot drift.
func renderSummaryTable(document SummaryDocument) string {
	var table strings.Builder
	fmt.Fprintf(&table, "Summary for %s (id %d)\n", document.User.Email, document.User.ID)
	fmt.Fprintf(&table, "Window: %s .. %s (%d days)\n\n", document.From, document.To, document.WindowDays)

	fmt.Fprintf(&table, "Adherence\n")
	fmt.Fprintf(&table, "  logged days:    %d / %d (%.1f%%)\n", document.LoggedDays, document.CalendarDays, document.AdherencePercent)
	fmt.Fprintf(&table, "  longest streak: %d day(s)\n\n", document.LongestStreak)

	fmt.Fprintf(&table, "Top symptoms\n")
	if len(document.TopSymptoms) == 0 {
		fmt.Fprintf(&table, "  (none recorded in this window)\n")
	} else {
		for rank, symptom := range document.TopSymptoms {
			fmt.Fprintf(&table, "  %d. %-30s %d occurrence(s)\n", rank+1, symptom.Name, symptom.Count)
		}
	}
	fmt.Fprintf(&table, "\n")

	fmt.Fprintf(&table, "Mood days\n")
	best, worst := document.BestDay, document.WorstDay
	switch {
	case best == nil && worst == nil:
		fmt.Fprintf(&table, "  (no mood logged in this window)\n")
	default:
		fmt.Fprintf(&table, "  best day:  %s (mood %d)\n", best.Date, best.Mood)
		fmt.Fprintf(&table, "  worst day: %s (mood %d)\n", worst.Date, worst.Mood)
	}
	return table.String()
}
