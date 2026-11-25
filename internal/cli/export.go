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

// exportUsage is the single usage string returned for every argument error so
// the contract is stable and testable, mirroring webhookUsage. The target is
// the same `<email>|--id <id>` pair the account subcommands take.
const exportUsage = "usage: ovumcy export <email> [--id <id>] [--out <path>]"

// exportSchemaVersion tags the payload so a consumer (a re-importer, a
// migration script) can detect which shape of document it is reading before it
// touches anything in it. Bumping it is the contract for a breaking change.
const exportSchemaVersion = "ovumcy-export/v1"

// exportOptions holds the parsed `export` arguments. The email/id pair is
// resolved with the same vocabulary as `users delete` (resolveOperatorUser),
// so an ambiguous address is refused with the same "retry with --id" advice.
type exportOptions struct {
	userID uint
	email  string
	out    string
}

// ExportDocument is the payload `ovumcy export` writes. It carries the full
// record an owner could hand to another tracker or an auditor: every daily
// log, the symptom vocabulary as the owner defined it, and the cycle-start
// markers the calendar derives cycles from. It deliberately contains NO auth
// material: no password hash, no recovery-code hash, no session state, no
// webhook URL — the export is the owner's health data, not the account.
type ExportDocument struct {
	Schema       string              `json:"schema"`
	GeneratedAt  string              `json:"generated_at"`
	User         exportUserSummary   `json:"user"`
	DailyLogs    []exportDailyLog    `json:"daily_logs"`
	SymptomTypes []exportSymptomType `json:"symptom_types"`
	Cycles       []exportCycleMarker `json:"cycles"`
}

type exportUserSummary struct {
	ID                  uint   `json:"id"`
	Email               string `json:"email"`
	DisplayName         string `json:"display_name"`
	Role                string `json:"role"`
	OnboardingCompleted bool   `json:"onboarding_completed"`
	CreatedAt           string `json:"created_at"`
}

type exportDailyLog struct {
	Date            string   `json:"date"`
	IsPeriod        bool     `json:"is_period"`
	CycleStart      bool     `json:"cycle_start"`
	IsUncertain     bool     `json:"is_uncertain"`
	Flow            string   `json:"flow"`
	Mood            int      `json:"mood"`
	SexActivity     string   `json:"sex_activity"`
	BBT             *float64 `json:"bbt"`
	CervicalMucus   string   `json:"cervical_mucus"`
	PregnancyTest   string   `json:"pregnancy_test"`
	CycleFactorKeys []string `json:"cycle_factor_keys"`
	SymptomIDs      []uint   `json:"symptom_ids"`
	Notes           string   `json:"notes"`
}

type exportSymptomType struct {
	ID         uint       `json:"id"`
	Name       string     `json:"name"`
	Icon       string     `json:"icon"`
	Color      string     `json:"color"`
	IsBuiltin  bool       `json:"is_builtin"`
	ArchivedAt *time.Time `json:"archived_at"`
}

type exportCycleMarker struct {
	Date string `json:"date"`
}

// RunExportCommand is the operator entry point for `ovumcy export`: a
// read-only, local-only command that writes one owner's data as indented JSON
// to stdout, or to --out <path> written with mode 0600. Data portability is a
// core promise of a self-hosted, privacy-first tracker (README, SECURITY.md's
// GDPR row): until now exiting meant manual SQL against the live database.
//
// SECURITY/PRIVACY: the payload is intimate health data. When --out is given
// the file is created 0600 (owner-only) rather than the umask default, so a
// share-all umask cannot publish it. Auth material (password/recovery hashes,
// webhook endpoints) is never included — this exports the OWNER'S data, not
// the operator's keys.
func RunExportCommand(databaseConfig db.Config, args []string) error {
	return runExportCommand(databaseConfig, args, os.Stdout)
}

// runExportCommand parses the arguments, opens the database, resolves the
// target account, renders the document and writes it. output is injected so
// tests drive stdout with a buffer.
func runExportCommand(databaseConfig db.Config, args []string, output io.Writer) error {
	opts, err := parseExportArgs(args)
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

	logs, err := repositories.DailyLogs.ListByUser(context.Background(), user.ID)
	if err != nil {
		return fmt.Errorf("export daily logs: %w", err)
	}
	symptoms, err := repositories.Symptoms.ListByUser(context.Background(), user.ID)
	if err != nil {
		return fmt.Errorf("export symptoms: %w", err)
	}

	document := buildExportDocument(user, logs, symptoms)
	payload, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("render export: %w", err)
	}
	payload = append(payload, '\n')

	if opts.out != "" {
		return writeExportFile(opts.out, payload)
	}
	if output == nil {
		output = os.Stdout
	}
	_, _ = output.Write(payload)
	return nil
}

// parseExportArgs parses `export <email> [--id <id>] [--out <path>]`. Flags
// accept both `--flag value` and `--flag=value`. An --id with no email is
// accepted (it is how a legacy row the strict email rule refuses is reached);
// a positional that is neither an email nor an --id value, a second positional,
// or an unknown flag returns exportUsage.
func parseExportArgs(args []string) (exportOptions, error) {
	opts := exportOptions{}
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		if arg == "" {
			continue
		}
		if !strings.HasPrefix(arg, "--") {
			if opts.email != "" {
				return exportOptions{}, errors.New(exportUsage)
			}
			normalized, err := normalizeOperatorEmailArgument(arg)
			if err != nil {
				return exportOptions{}, err
			}
			opts.email = normalized
			continue
		}

		name, value, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		// --flag value form: consume the next argument as the value.
		if !hasValue {
			if name == "id" || name == "out" {
				if index+1 >= len(args) {
					return exportOptions{}, errors.New(exportUsage)
				}
				index++
				value = strings.TrimSpace(args[index])
			} else {
				return exportOptions{}, errors.New(exportUsage)
			}
		}

		switch name {
		case "id":
			parsed, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				return exportOptions{}, errors.New(exportUsage)
			}
			opts.userID = uint(parsed)
		case "out":
			opts.out = value
		default:
			return exportOptions{}, errors.New(exportUsage)
		}
	}

	if opts.email == "" && opts.userID == 0 {
		return exportOptions{}, errors.New(exportUsage)
	}
	return opts, nil
}

// buildExportDocument assembles the payload from the resolved account row, its
// daily logs and its symptom vocabulary, deriving the cycle markers from the
// logs the owner marked cycle_start on. Nil slices are written as [] so a
// consumer never has to special-case null.
func buildExportDocument(user models.OperatorUserSummary, logs []models.DailyLog, symptoms []models.SymptomType) ExportDocument {
	document := ExportDocument{
		Schema:       exportSchemaVersion,
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
		User:         exportUserSummaryFrom(user),
		DailyLogs:    make([]exportDailyLog, 0, len(logs)),
		SymptomTypes: make([]exportSymptomType, 0, len(symptoms)),
		Cycles:       make([]exportCycleMarker, 0),
	}

	for _, logEntry := range logs {
		date := logEntry.Date.Format("2006-01-02")
		if logEntry.CycleStart {
			document.Cycles = append(document.Cycles, exportCycleMarker{Date: date})
		}
		document.DailyLogs = append(document.DailyLogs, exportDailyLogFrom(logEntry))
	}
	for _, symptom := range symptoms {
		document.SymptomTypes = append(document.SymptomTypes, exportSymptomTypeFrom(symptom))
	}
	return document
}

func exportUserSummaryFrom(user models.OperatorUserSummary) exportUserSummary {
	created := ""
	if !user.CreatedAt.IsZero() {
		created = user.CreatedAt.UTC().Format(time.RFC3339)
	}
	return exportUserSummary{
		ID:                  user.ID,
		Email:               user.Email,
		DisplayName:         user.DisplayName,
		Role:                user.Role,
		OnboardingCompleted: user.OnboardingCompleted,
		CreatedAt:           created,
	}
}

func exportDailyLogFrom(logEntry models.DailyLog) exportDailyLog {
	keys := logEntry.CycleFactorKeys
	if keys == nil {
		keys = []string{}
	}
	symptomIDs := logEntry.SymptomIDs
	if symptomIDs == nil {
		symptomIDs = []uint{}
	}
	return exportDailyLog{
		Date:            logEntry.Date.Format("2006-01-02"),
		IsPeriod:        logEntry.IsPeriod,
		CycleStart:      logEntry.CycleStart,
		IsUncertain:     logEntry.IsUncertain,
		Flow:            logEntry.Flow,
		Mood:            logEntry.Mood,
		SexActivity:     logEntry.SexActivity,
		BBT:             logEntry.BBT,
		CervicalMucus:   logEntry.CervicalMucus,
		PregnancyTest:   logEntry.PregnancyTest,
		CycleFactorKeys: keys,
		SymptomIDs:      symptomIDs,
		Notes:           logEntry.Notes,
	}
}

func exportSymptomTypeFrom(symptom models.SymptomType) exportSymptomType {
	return exportSymptomType{
		ID:         symptom.ID,
		Name:       symptom.Name,
		Icon:       symptom.Icon,
		Color:      symptom.Color,
		IsBuiltin:  symptom.IsBuiltin,
		ArchivedAt: symptom.ArchivedAt,
	}
}

// writeExportFile writes the payload to path with mode 0600 BEFORE any byte is
// written, so the umask cannot leak a moment of the contents to a wider mode,
// and truncates only through that same handle. The file is closed before the
// error is reported so the operator can act on it immediately.
func writeExportFile(path string, payload []byte) error {
	// #nosec G304 -- the path is the operator's own --out argument to a local CLI command, never
	// request input, and the file is created with mode 0600 before any byte is written.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("write export: %w", err)
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return fmt.Errorf("write export: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("write export: %w", err)
	}
	return nil
}
