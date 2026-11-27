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

// trendsUsage is the single usage string returned for every argument error so
// the contract is stable and testable, mirroring summaryUsage.
const trendsUsage = "usage: ovumcy trends <email> [--id <id>] [--tz <IANA name>] [--json]"

// trendsSchemaVersion tags the --json payload so a consumer can detect which
// shape of document it is reading, mirroring summarySchemaVersion.
const trendsSchemaVersion = "ovumcy-trends/v1"

// trendsPhaseNames is the fixed phase order every trends document renders in,
// matching the stats page's phaseInsightOrder.
var trendsPhaseNames = []string{"menstrual", "follicular", "ovulation", "luteal"}

// trendsOptions holds the parsed `ovumcy trends` arguments. The email/id pair
// is resolved with the same vocabulary as `export` and `summary`
// (resolveOperatorUser).
type trendsOptions struct {
	userID uint
	email  string
	// tz is the --tz override; tzSet records that it was given, so an omitted
	// --tz resolves to the OWNER's stored zone rather than to an implicit UTC.
	tz     string
	tzSet  bool
	asJSON bool
}

// TrendsDocument is the payload `ovumcy trends --json` emits and the same
// struct the human table renders from, so a script and an operator reading
// stdout can never disagree about what the command measured.
type TrendsDocument struct {
	Schema          string            `json:"schema"`
	GeneratedAt     string            `json:"generated_at"`
	User            trendsUserSummary `json:"user"`
	Timezone        string            `json:"timezone"`
	CompletedCycles int               `json:"completed_cycles"`

	// Phases carries one entry per phase, in trendsPhaseNames order, each
	// with the symptom items the stats page's phase insight counts and the
	// average mood logged inside that phase.
	Phases []trendsPhase `json:"phases"`
}

type trendsUserSummary struct {
	ID    uint   `json:"id"`
	Email string `json:"email"`
}

type trendsSymptomItem struct {
	Name       string  `json:"name"`
	Icon       string  `json:"icon"`
	Count      int     `json:"count"`
	TotalDays  int     `json:"total_days"`
	Percentage float64 `json:"percentage"`
}

type trendsPhase struct {
	Phase     string `json:"phase"`
	TotalDays int    `json:"total_days"`
	// Symptoms carries the stats page's phase insight counts. The service
	// struct is a template view-model with no JSON tags, so the document
	// projects it onto a tagged item here — the schema tag on the document
	// is a contract, and Go field names are not.
	Symptoms    []trendsSymptomItem `json:"symptoms"`
	HasSymptoms bool                `json:"has_symptoms"`

	// AverageMood / MoodEntries carry the stats page's phase mood insight;
	// MoodPercentage is AverageMood × 20 (mood 1–5 → 20–100%).
	AverageMood float64 `json:"average_mood"`
	MoodEntries int     `json:"mood_entries"`
	HasMood     bool    `json:"has_mood"`
}

// RunTrendsCommand is the operator entry point for `ovumcy trends`: a
// read-only, local-only command that correlates one owner's symptom
// occurrences and logged moods with the four cycle phases, computed from the
// owner's full log history over the same completed-cycle phase contexts the
// stats page uses. Like `summary` it needs no secret: nothing is written and
// nothing sensitive beyond the owner's own health data is read.
func RunTrendsCommand(databaseConfig db.Config, args []string) error {
	return runTrendsCommand(databaseConfig, args, os.Stdout)
}

// runTrendsCommand parses the arguments, opens the database, resolves the
// target account, aggregates the phase trends and renders them. output is
// injected so tests drive stdout with a buffer.
func runTrendsCommand(databaseConfig db.Config, args []string, output io.Writer) error {
	opts, err := parseTrendsArgs(args)
	if err != nil {
		return err
	}

	// Read-only: `trends` correlates the owner's logs and never writes.
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

	user, err := resolveOperatorUser(userService, opts.userID, opts.email)
	if err != nil {
		// resolveOperatorUser's own vocabulary already advises the operator
		// (`users list`, `retry with --id`); this command adds nothing.
		return mapOperatorUserLookupError(err, opts.userID, opts.email)
	}

	// Phase analytics needs the owner's full history — every completed cycle
	// contributes — so no window is applied, unlike `summary`.
	logs, err := repositories.DailyLogs.ListByUserRange(context.Background(), user.ID, nil, nil)
	if err != nil {
		return fmt.Errorf("trends daily logs: %w", err)
	}
	symptoms, err := repositories.Symptoms.ListByUser(context.Background(), user.ID)
	if err != nil {
		return fmt.Errorf("trends symptoms: %w", err)
	}

	// The owner's persisted zone is the default — the phases are calendar days,
	// so computing them in UTC splits an owner's evening log into the next day —
	// and --tz overrides it. ownerClockZone reads users.timezone from the full
	// row, which the summary this command holds does not carry.
	_, timezoneName, err := ownerClockZone(context.Background(), repositories.Users, user.ID, opts.tz, opts.tzSet)
	if err != nil {
		return fmt.Errorf("trends: %w", err)
	}

	trends := services.BuildCyclePhaseTrends(timezoneName, logs, symptoms)
	document := buildTrendsDocument(user, timezoneName, trends)

	if output == nil {
		output = os.Stdout
	}
	if opts.asJSON {
		payload, err := json.MarshalIndent(document, "", "  ")
		if err != nil {
			return fmt.Errorf("render trends: %w", err)
		}
		payload = append(payload, '\n')
		if _, err := output.Write(payload); err != nil {
			return fmt.Errorf("write trends: %w", err)
		}
		return nil
	}
	if _, err := io.WriteString(output, renderTrendsTable(document)); err != nil {
		return fmt.Errorf("write trends: %w", err)
	}
	return nil
}

// parseTrendsArgs parses `trends <email> [--id <id>] [--tz <IANA name>]
// [--json]`. Flags accept both `--flag value` and `--flag=value`; an --id
// with no email is accepted as in `export`; --tz defaults to UTC and must
// resolve to a location time.LoadLocation accepts; a second positional, an
// unknown flag or a bad --tz returns trendsUsage.
func parseTrendsArgs(args []string) (trendsOptions, error) {
	opts := trendsOptions{}
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		if arg == "" {
			continue
		}
		if !strings.HasPrefix(arg, "--") {
			if opts.email != "" {
				return trendsOptions{}, errors.New(trendsUsage)
			}
			normalized, err := normalizeOperatorEmailArgument(arg)
			if err != nil {
				return trendsOptions{}, err
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
					return trendsOptions{}, errors.New(trendsUsage)
				}
				index++
				value = strings.TrimSpace(args[index])
			case "json":
				opts.asJSON = true
				continue
			default:
				return trendsOptions{}, errors.New(trendsUsage)
			}
		}

		switch name {
		case "id":
			parsed, err := parseOperatorUserID(value)
			if err != nil {
				return trendsOptions{}, trendsIDError(value)
			}
			opts.userID = parsed
		case "tz":
			if _, err := time.LoadLocation(value); err != nil {
				return trendsOptions{}, errors.New(trendsUsage)
			}
			opts.tz = value
			opts.tzSet = true
		case "json":
			return trendsOptions{}, errors.New(trendsUsage)
		default:
			return trendsOptions{}, errors.New(trendsUsage)
		}
	}

	if opts.email == "" && !operatorUserIDSelected(opts.userID) {
		return trendsOptions{}, errors.New(trendsUsage)
	}
	return opts, nil
}

// trendsIDError keeps the non-positive --id message apart from the usage string,
// exactly as export does: the flag was typed correctly and the VALUE is the
// mistake. Treating the id as unset is what used to let `--id 0 <email>` report
// another account's phases than the one addressed.
func trendsIDError(value string) error {
	if _, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64); err == nil {
		return errOperatorIDNotPositive(0)
	}
	return errors.New(trendsUsage)
}

// buildTrendsDocument projects the service-level phase trends onto the
// document `ovumcy trends` renders. It only reorders and renames — every
// number comes from services.BuildCyclePhaseTrends.
func buildTrendsDocument(user models.OperatorUserSummary, timezone string, trends services.CyclePhaseTrends) TrendsDocument {
	document := TrendsDocument{
		Schema:          trendsSchemaVersion,
		GeneratedAt:     time.Now().UTC().Format(time.RFC3339),
		User:            trendsUserSummary{ID: user.ID, Email: user.Email},
		Timezone:        timezone,
		CompletedCycles: trends.CompletedCycles,
		Phases:          []trendsPhase{},
	}

	symptomByPhase := make(map[string]services.StatsPhaseSymptomInsight, len(trends.SymptomInsights))
	for _, insight := range trends.SymptomInsights {
		symptomByPhase[insight.Phase] = insight
	}
	moodByPhase := make(map[string]services.StatsPhaseMoodInsight, len(trends.MoodInsights))
	for _, insight := range trends.MoodInsights {
		moodByPhase[insight.Phase] = insight
	}

	for _, phase := range trendsPhaseNames {
		entry := trendsPhase{Phase: phase}
		if insight, ok := symptomByPhase[phase]; ok {
			entry.TotalDays = insight.TotalDays
			entry.HasSymptoms = insight.HasData
			for _, item := range insight.Items {
				entry.Symptoms = append(entry.Symptoms, trendsSymptomItem{
					Name:       item.Name,
					Icon:       item.Icon,
					Count:      item.Count,
					TotalDays:  item.TotalDays,
					Percentage: item.Percentage,
				})
			}
		}
		if mood, ok := moodByPhase[phase]; ok {
			entry.AverageMood = mood.AverageMood
			entry.MoodEntries = mood.EntryCount
			entry.HasMood = mood.HasData
		}
		if entry.Symptoms == nil {
			entry.Symptoms = []trendsSymptomItem{}
		}
		document.Phases = append(document.Phases, entry)
	}

	return document
}

// renderTrendsTable renders the human table: one phase per row with its
// symptom items and the average mood inside the phase, then the cycle count
// the phases were computed over.
func renderTrendsTable(document TrendsDocument) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "ovumcy trends — %s\n", document.User.Email)
	fmt.Fprintf(&builder, "completed cycles: %d\n", document.CompletedCycles)
	if document.CompletedCycles == 0 {
		builder.WriteString("no completed cycles: phases cannot be computed yet\n")
		return builder.String()
	}
	for _, phase := range document.Phases {
		fmt.Fprintf(&builder, "%s", phase.Phase)
		if phase.TotalDays > 0 {
			fmt.Fprintf(&builder, "  %dd", phase.TotalDays)
		}
		for _, item := range phase.Symptoms {
			fmt.Fprintf(&builder, "  %s %s×%d", item.Icon, item.Name, item.Count)
		}
		if phase.HasMood {
			fmt.Fprintf(&builder, "  avg mood %.1f (%d entries)", phase.AverageMood, phase.MoodEntries)
		}
		builder.WriteString("\n")
	}
	return builder.String()
}
