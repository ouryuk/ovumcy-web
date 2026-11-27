package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/db"
	"github.com/ovumcy/ovumcy-web/internal/i18n"
	"github.com/ovumcy/ovumcy-web/internal/models"
	"github.com/ovumcy/ovumcy-web/internal/services"
)

// digestUsage is the single usage string returned for every argument error so
// the contract is stable and testable, mirroring summaryUsage.
const digestUsage = "usage: ovumcy digest <email> [--id <id>] [--week] [--days N] [--format markdown|json] [--tz <IANA name>] [--ntfy]"

// digestSchemaVersion tags the --format json payload so a consumer can detect
// which shape of document it is reading before it touches anything in it,
// mirroring summarySchemaVersion.
const digestSchemaVersion = "ovumcy-digest/v1"

// digestFormats are the renderings `ovumcy digest` accepts. "markdown" is the
// default because the digest's whole purpose is to be pasted into a shareable
// surface (a journal, a message to a clinician, a note app); "json" is the same
// document as a versioned machine payload.
var digestFormats = map[string]bool{"markdown": true, "json": true}

// digestWeekDays is the window --week selects. It is a named constant rather
// than a literal in the parser so the flag's meaning and the document's
// window_days cannot drift apart.
const digestWeekDays = 7

// digestNow resolves "today" for the window's end and the projection anchor.
// It is a variable so tests can pin the clock; production callers never touch
// it. The digest composes three shipped commands, so it needs ONE clock for all
// three — summary's window end, predict's "as of" — or a digest generated at
// midnight could report a window ending yesterday beside a projection anchored
// today.
var digestNow = func() time.Time { return time.Now().UTC() }

// digestOptions holds the parsed `digest` arguments. The email/id pair is
// resolved with the same vocabulary as `export` and `summary`
// (resolveOperatorUser).
type digestOptions struct {
	userID uint
	email  string
	days   int
	// week reports whether the window came from --week rather than --days. It
	// only affects the usage error text and the document's window_source; the
	// aggregation reads days.
	week bool
	// tz is the explicit --tz override. Empty means "use the owner's persisted
	// users.timezone", which is the digest's default and its whole point: a
	// shareable digest must land on the owner's calendar days, not the server's.
	tz string
	// format is "markdown" or "json".
	format string
	// ntfy opts the digest into delivery through the owner's stored webhook
	// endpoint. It is never implied: an operator has to ask for it.
	ntfy bool
}

// DigestDocument is the payload `ovumcy digest --format json` emits and the
// same struct the Markdown rendering composes from, so a script and a person
// reading the digest can never disagree about what the command measured. It
// carries the three shipped documents verbatim rather than re-deriving them:
// a digest is a VIEW over summary, trends and predict, and a view that
// recomputed its own numbers would be a fourth implementation to keep in step.
type DigestDocument struct {
	Schema      string             `json:"schema"`
	GeneratedAt string             `json:"generated_at"`
	User        digestUserSummary  `json:"user"`
	Timezone    string             `json:"timezone"`
	// Language is the language the digest is rendered in (Spanish, fixed by this
	// slice). OwnerLanguage is the owner's persisted interface language, carried
	// so a consumer can see what the rest of the product shows them.
	Language      string `json:"language"`
	OwnerLanguage string `json:"owner_language"`
	AsOf        string             `json:"as_of"`
	WindowDays  int                `json:"window_days"`
	From        string             `json:"from"`
	To          string             `json:"to"`
	Summary     SummaryDocument    `json:"summary"`
	Trends      TrendsDocument     `json:"trends"`
	Predict     PredictDocument    `json:"predict"`
	Disclaimer  string             `json:"disclaimer"`
	Delivery    *digestDeliveryDoc `json:"delivery,omitempty"`
}

type digestUserSummary struct {
	ID    uint   `json:"id"`
	Email string `json:"email"`
}

// digestDeliveryDoc reports an --ntfy attempt. It is present only when the
// operator asked for delivery, and it names the destination HOST at most — never
// the URL, its path or its token, mirroring the notify pass's own report.
type digestDeliveryDoc struct {
	Attempted bool   `json:"attempted"`
	Host      string `json:"host"`
	Delivered bool   `json:"delivered"`
	Error     string `json:"error,omitempty"`
}

// digestDelivery is the narrow behavior the --ntfy path needs: hand one
// already-rendered Markdown digest to the owner's configured endpoint. It is an
// interface so the command can be tested without a socket, and the production
// implementation is the shipped webhook settings service — the SAME delivery
// path and the SAME endpoint the reminder pass uses, so the digest introduces no
// second egress target.
type digestDelivery interface {
	// Deliver sends markdown to the owner's stored endpoint and returns the
	// destination HOST (never the URL, its path or its token) plus a nil error on
	// success. A non-nil error means nothing was delivered.
	Deliver(ctx context.Context, userID uint, markdown string) (host string, err error)
}

// digestNotifier adapts the shipped webhook settings service to digestDelivery:
// the owner's row is read through the settings service's own repository, the
// stored ciphertext is decrypted through the shipped decrypt seam, the decrypted
// URL is re-checked with the shipped save-time guard, and the POST goes through
// the shipped hardened deliverer. The payload carries the digest in the message
// body and the mandatory localized medical disclaimer in the disclaimer field,
// exactly as a reminder does.
type digestNotifier struct {
	users     services.WebhookSettingsRepository
	settings  *services.WebhookSettingsService
	deliverer services.WebhookDeliverer
}

// NewDigestNotifier builds the --ntfy delivery seam over the shipped webhook
// components. secretKey is the same SECRET_KEY the web binary and `ovumcy notify`
// use; blockPrivateAddresses is the same off-by-default egress gate.
func NewDigestNotifier(users services.WebhookSettingsRepository, secretKey string, blockPrivateAddresses bool) *digestNotifier {
	return &digestNotifier{
		users:     users,
		settings:  services.NewWebhookSettingsService(users, []byte(secretKey)),
		deliverer: services.NewWebhookDeliverer(blockPrivateAddresses),
	}
}

// Deliver sends the digest to the owner's stored endpoint. Errors are values
// from the shipped webhook vocabulary so the command can report them without
// echoing the URL: an owner with no endpoint, an unreadable ciphertext and a
// refused delivery each get their own sentinel.
func (notifier *digestNotifier) Deliver(ctx context.Context, userID uint, markdown string) (string, error) {
	row, err := notifier.users.LoadSettingsByID(ctx, userID)
	if err != nil {
		return "", ErrDigestDeliveryUnavailable
	}
	plaintext, err := notifier.settings.DecryptWebhookURL(userID, row.WebhookURL)
	if err != nil {
		return "", ErrDigestDeliveryUnavailable
	}
	if strings.TrimSpace(plaintext) == "" {
		return "", ErrDigestDeliveryNotConfigured
	}
	endpoint, err := services.ValidateWebhookURL(plaintext)
	if err != nil {
		return "", ErrDigestDeliveryUnavailable
	}
	host := digestHostOnly(endpoint)
	payload := services.WebhookPayload{
		Title:      digestNtfyTitle,
		Message:    markdown,
		Disclaimer: digestNtfyDisclaimer,
		Type:       services.WebhookTestType,
		EventDate:  time.Now().UTC().Format("2006-01-02"),
		LeadDays:   0,
	}
	if err := notifier.deliverer.Deliver(ctx, endpoint, payload); err != nil {
		return host, ErrDigestDeliveryFailed
	}
	return host, nil
}

// digestHostOnly renders the destination hostname of a validated endpoint. It is
// the same redaction rule the notify pass and the webhook CLI print through: a
// webhook URL may embed an ntfy/Gotify token in its path, so only the host may
// appear in any output.
func digestHostOnly(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

// digestNtfyTitle and digestNtfyDisclaimer are the two fixed strings the digest
// payload carries. The title names the message so a consumer can route on it; the
// disclaimer is the medical-safety sentence a shareable health summary must carry
// (the same wording the predict document and the webhook payloads use).
const (
	digestNtfyTitle      = "Resumen semanal de Ovumcy"
	digestNtfyDisclaimer = "these dates are projections from past cycles, not guarantees"
)

// ErrDigestDeliveryNotConfigured is returned when the owner has no webhook
// endpoint stored. ErrDigestDeliveryUnavailable covers an unreadable ciphertext
// and a malformed endpoint. ErrDigestDeliveryFailed is a refused or failed POST.
// None of them carries the URL, its path or its token.
var (
	ErrDigestDeliveryNotConfigured = errors.New("no webhook endpoint is configured for this owner")
	ErrDigestDeliveryUnavailable   = errors.New("the stored webhook endpoint is not usable")
	ErrDigestDeliveryFailed        = errors.New("the webhook endpoint refused the digest")
)

// deliverDigest is the seam runDigestCommand calls for --ntfy. It is a variable so
// tests can substitute a stub without opening a socket; the production value is
// runDigestDelivery, which wires the shipped webhook components.
var deliverDigest = runDigestDelivery

// runDigestDelivery is the production --ntfy path: it opens its own short-lived
// repository handle (the caller's handle is already closed by the time delivery
// runs), builds the shipped webhook components and reports the attempt. It never
// returns an error — the failure is carried in the document so the digest still
// prints — and it never names the URL.
func runDigestDelivery(databaseConfig db.Config, secretKey string, blockPrivateAddresses bool, userID uint, markdown string) *digestDeliveryDoc {
	report := &digestDeliveryDoc{Attempted: true}
	database, err := db.OpenDatabase(databaseConfig)
	if err != nil {
		report.Error = ErrDigestDeliveryUnavailable.Error()
		return report
	}
	sqlDB, err := database.DB()
	if err != nil {
		report.Error = ErrDigestDeliveryUnavailable.Error()
		return report
	}
	defer func() {
		_ = sqlDB.Close()
	}()

	repositories, _ := buildRepositories(database, calendarFeedFencePath())
	notifier := NewDigestNotifier(repositories.Users, secretKey, blockPrivateAddresses)
	host, err := notifier.Deliver(context.Background(), userID, markdown)
	report.Host = host
	if err != nil {
		report.Error = err.Error()
		return report
	}
	report.Delivered = true
	return report
}

// RunDigestCommand is the operator entry point for `ovumcy digest`: a read-only
// composition of three shipped commands — `summary` (adherence, symptom tallies,
// best/worst mood days, longest streak), `trends` (symptom and mood correlation
// with the four cycle phases) and `predict` (the next cycle window) — rendered as
// ONE Spanish-language Markdown digest on the owner's own calendar, with an
// opt-in --ntfy delivery through the owner's existing webhook endpoint. Like
// `summary` and `predict` it needs no secret unless --ntfy is set: the payload is
// derived from the owner's health data, and nothing is written to the database in
// either mode.
func RunDigestCommand(databaseConfig db.Config, secretKey string, blockPrivateAddresses bool, args []string) error {
	return runDigestCommand(databaseConfig, secretKey, blockPrivateAddresses, args, os.Stdout)
}

// runDigestCommand parses the arguments, opens the database, resolves the target
// account, composes the three shipped documents, renders the digest and — only
// when --ntfy was asked for — delivers it. output is injected so tests drive
// stdout with a buffer.
func runDigestCommand(databaseConfig db.Config, secretKey string, blockPrivateAddresses bool, args []string, output io.Writer) error {
	opts, err := parseDigestArgs(args)
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

	// The digest needs the FULL user row — the persisted timezone for the
	// calendar, the interface language for the document, and the cycle baseline
	// the projection consumes — exactly as `predict` does.
	user, found, err := repositories.Users.FindByIDOptional(context.Background(), summary.ID)
	if err != nil {
		return fmt.Errorf("digest user lookup: %w", err)
	}
	if !found {
		return fmt.Errorf("digest user lookup: %w", services.ErrOperatorUserNotFound)
	}

	// The digest's calendar is the owner's: the persisted users.timezone is the
	// default (that is the whole point of a shareable digest — it must agree with
	// the calendar the owner actually reads), and an explicit --tz overrides it.
	timezone := resolveDigestTimezone(opts.tz, user.Timezone)
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return errors.New(digestUsage)
	}

	// ONE clock for the whole document: the summary window's end and the
	// projection's anchor are the same instant, so a digest can never show a
	// window ending on a different day than the projection's "as of".
	now := digestNow()
	asOf := services.DateAtLocation(now, location)
	from := asOf.AddDate(0, 0, -(opts.days - 1))
	// ListByUserRange's upper bound is EXCLUSIVE (`date < to`), so the window's
	// "to" day is included by querying one day past it — the same bound handling
	// `summary` uses.
	windowEnd := asOf.AddDate(0, 0, 1)
	windowLogs, err := repositories.DailyLogs.ListByUserRange(context.Background(), user.ID, &from, &windowEnd)
	if err != nil {
		return fmt.Errorf("digest daily logs: %w", err)
	}
	symptoms, err := repositories.Symptoms.ListByUser(context.Background(), user.ID)
	if err != nil {
		return fmt.Errorf("digest symptoms: %w", err)
	}
	// The trends and predict halves need the owner's FULL history — every
	// completed cycle trains the phase analytics and the projection — so they read
	// a second, unbounded query, exactly as `trends` and `predict` do.
	historyLogs, err := repositories.DailyLogs.ListByUserRange(context.Background(), user.ID, nil, nil)
	if err != nil {
		return fmt.Errorf("digest history logs: %w", err)
	}

	document := buildDigestDocument(user, summary, timezone, user.InterfaceLanguage, opts.days, asOf, from, windowLogs, historyLogs, symptoms, now, location)
	rendered := renderDigestOutput(document, opts.format)

	if opts.ntfy {
		// The digest is delivered as Markdown whatever --format prints: the
		// endpoint renders text, and the Markdown is the shareable form. The
		// delivered body is the same string the operator sees with the default
		// format, so there is exactly one digest text.
		document.Delivery = deliverDigest(databaseConfig, secretKey, blockPrivateAddresses, user.ID, renderDigestMarkdown(document))
		rendered = renderDigestOutput(document, opts.format)
		if document.Delivery != nil && !document.Delivery.Delivered {
			// The digest still prints — losing the content because the endpoint was
			// unreachable would be the worse failure — but the command exits non-zero
			// so a cron job or script sees the delivery did not happen.
			if output == nil {
				output = os.Stdout
			}
			_, _ = io.WriteString(output, rendered)
			return fmt.Errorf("digest delivery failed: %w", errors.New(document.Delivery.Error))
		}
	}

	if output == nil {
		output = os.Stdout
	}
	_, _ = io.WriteString(output, rendered)
	return nil
}

// renderDigestOutput renders the document in the requested format. The JSON
// rendering is the same struct the Markdown rendering reads, so a script and a
// human reading the digest can never disagree about what it measured.
func renderDigestOutput(document DigestDocument, format string) string {
	if format == "json" {
		payload, err := json.MarshalIndent(document, "", "  ")
		if err != nil {
			// codecov:ignore -- unreachable: every field of the document is a
			// string, number or bool. Mirrors the same guard in summary.go.
			return ""
		}
		return string(payload) + "\n"
	}
	return renderDigestMarkdown(document)
}

// resolveDigestTimezone picks the digest's calendar. An explicit --tz wins; the
// owner's persisted users.timezone is the default; a stored value that no longer
// resolves falls back to UTC rather than failing the command — a renamed or
// retired zone should not make an owner's digest unreadable.
func resolveDigestTimezone(override string, stored string) string {
	if candidate := strings.TrimSpace(override); candidate != "" {
		return candidate
	}
	if candidate := strings.TrimSpace(stored); candidate != "" {
		if _, err := time.LoadLocation(candidate); err == nil {
			return candidate
		}
	}
	return "UTC"
}

// digestLanguage is the language the digest is rendered in. It is Spanish by
// definition: a shareable digest is written for the person or clinician reading
// it, and this shipped slice fixes that language. The owner's persisted interface
// language is still carried on the document (owner_language) so a consumer can
// see what the rest of the product shows that owner, and so a later slice can
// render per owner without changing the document's shape.
const digestLanguage = i18n.LangES

// buildDigestDocument composes the three shipped documents into one digest. It
// only reorders and renames — every figure comes from buildSummaryDocument,
// buildTrendsDocument and buildPredictDocument, the same functions the three
// shipped commands render through.
func buildDigestDocument(
	user models.User,
	summary models.OperatorUserSummary,
	timezone string,
	ownerLanguage string,
	windowDays int,
	asOf time.Time,
	from time.Time,
	windowLogs []models.DailyLog,
	historyLogs []models.DailyLog,
	symptoms []models.SymptomType,
	now time.Time,
	location *time.Location,
) DigestDocument {
	summaryDocument := buildSummaryDocument(summary, windowDays, from, asOf, windowLogs, symptoms)
	trendsDocument := buildTrendsDocument(summary, timezone, services.BuildCyclePhaseTrends(timezone, historyLogs, symptoms))
	stats := services.BuildCycleStatsFromLogs(&user, historyLogs, now, location)
	predictDocument := buildPredictDocument(summary, timezone, now, stats)

	return DigestDocument{
		Schema:        digestSchemaVersion,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		User:          digestUserSummary{ID: summary.ID, Email: summary.Email},
		Timezone:      timezone,
		Language:      digestLanguage,
		OwnerLanguage: ownerLanguage,
		AsOf:          asOf.Format("2006-01-02"),
		WindowDays:    windowDays,
		From:          summaryDocument.From,
		To:            summaryDocument.To,
		Summary:       summaryDocument,
		Trends:        trendsDocument,
		Predict:       predictDocument,
		Disclaimer:    predictDocument.Disclaimer,
	}
}

// parseDigestArgs parses `digest <email> [--id <id>] [--week] [--days N]
// [--format markdown|json] [--tz <IANA name>] [--ntfy]`. Flags accept both
// `--flag value` and `--flag=value`. An --id with no email is accepted, as in
// `summary`; a second positional, an unknown flag, a --days value outside
// [1,90], a --format outside {markdown,json} or a --tz that time.LoadLocation
// rejects returns digestUsage.
func parseDigestArgs(args []string) (digestOptions, error) {
	opts := digestOptions{days: 30, format: "markdown"}
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		if arg == "" {
			continue
		}
		if !strings.HasPrefix(arg, "--") {
			if opts.email != "" {
				return digestOptions{}, errors.New(digestUsage)
			}
			normalized, err := normalizeOperatorEmailArgument(arg)
			if err != nil {
				return digestOptions{}, err
			}
			opts.email = normalized
			continue
		}

		name, value, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		// --flag value form: consume the next argument as the value.
		if !hasValue {
			switch name {
			case "id", "days", "format", "tz":
				if index+1 >= len(args) {
					return digestOptions{}, errors.New(digestUsage)
				}
				index++
				value = strings.TrimSpace(args[index])
			case "week", "ntfy":
				if name == "week" {
					opts.week = true
					opts.days = digestWeekDays
				} else {
					opts.ntfy = true
				}
				continue
			default:
				return digestOptions{}, errors.New(digestUsage)
			}
		}

		switch name {
		case "id":
			parsed, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				return digestOptions{}, errors.New(digestUsage)
			}
			opts.userID = uint(parsed)
		case "days":
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 1 || parsed > 90 {
				return digestOptions{}, errors.New(digestUsage)
			}
			opts.days = parsed
			opts.week = false
		case "format":
			if !digestFormats[value] {
				return digestOptions{}, errors.New(digestUsage)
			}
			opts.format = value
		case "tz":
			if _, err := time.LoadLocation(value); err != nil {
				return digestOptions{}, errors.New(digestUsage)
			}
			opts.tz = value
		case "week", "ntfy":
			// A bare boolean flag does not take a value.
			return digestOptions{}, errors.New(digestUsage)
		default:
			return digestOptions{}, errors.New(digestUsage)
		}
	}

	if opts.email == "" && opts.userID == 0 {
		return digestOptions{}, errors.New(digestUsage)
	}
	return opts, nil
}

// renderDigestMarkdown renders the digest as Spanish-language Markdown. Every
// section is a view over one of the three shipped documents: the summary block
// reads the summary's adherence/tallies/mood days, the phases block reads the
// trends document's four phases, and the projection block reads the predict
// document's dates. Nothing is recomputed here.
func renderDigestMarkdown(document DigestDocument) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "# Resumen semanal de Ovumcy\n\n")
	fmt.Fprintf(&builder, "**Cuenta:** %s  \n", document.User.Email)
	fmt.Fprintf(&builder, "**Generado:** %s  \n", document.AsOf)
	fmt.Fprintf(&builder, "**Ventana:** %s .. %s (%d días)  \n", document.From, document.To, document.WindowDays)
	fmt.Fprintf(&builder, "**Zona horaria:** %s\n\n", document.Timezone)

	builder.WriteString("## Constancia de registro\n\n")
	summary := document.Summary
	fmt.Fprintf(&builder, "- Días con registro: %d de %d (%.1f%%)\n", summary.LoggedDays, summary.CalendarDays, summary.AdherencePercent)
	fmt.Fprintf(&builder, "- Racha más larga: %d día(s)\n\n", summary.LongestStreak)

	builder.WriteString("## Síntomas más frecuentes\n\n")
	if len(summary.TopSymptoms) == 0 {
		builder.WriteString("_Sin síntomas registrados en esta ventana._\n\n")
	} else {
		for rank, symptom := range summary.TopSymptoms {
			fmt.Fprintf(&builder, "%d. %s — %d vez(veces)\n", rank+1, symptom.Name, symptom.Count)
		}
		builder.WriteString("\n")
	}

	builder.WriteString("## Estado de ánimo\n\n")
	if summary.BestDay == nil && summary.WorstDay == nil {
		builder.WriteString("_Sin estado de ánimo registrado en esta ventana._\n\n")
	} else {
		if summary.BestDay != nil {
			fmt.Fprintf(&builder, "- Mejor día: %s (ánimo %d)\n", summary.BestDay.Date, summary.BestDay.Mood)
		}
		if summary.WorstDay != nil {
			fmt.Fprintf(&builder, "- Día más difícil: %s (ánimo %d)\n", summary.WorstDay.Date, summary.WorstDay.Mood)
		}
		builder.WriteString("\n")
	}

	builder.WriteString("## Patrones por fase\n\n")
	trends := document.Trends
	fmt.Fprintf(&builder, "Ciclos completados: %d\n\n", trends.CompletedCycles)
	if trends.CompletedCycles == 0 {
		builder.WriteString("_Sin ciclos completados: todavía no se pueden calcular los patrones por fase._\n\n")
	} else {
		for _, phase := range trends.Phases {
			fmt.Fprintf(&builder, "### %s\n\n", digestPhaseLabel(phase.Phase))
			if len(phase.Symptoms) == 0 {
				builder.WriteString("- Sin síntomas destacados\n")
			}
			for _, item := range phase.Symptoms {
				fmt.Fprintf(&builder, "- %s %s — %d vez(veces) (%.1f%% de los días)\n", item.Icon, item.Name, item.Count, item.Percentage)
			}
			if phase.HasMood {
				fmt.Fprintf(&builder, "- Ánimo medio: %.1f (%d registros)\n", phase.AverageMood, phase.MoodEntries)
			}
			builder.WriteString("\n")
		}
	}

	builder.WriteString("## Próximo ciclo\n\n")
	predict := document.Predict
	switch {
	case predict.PregnancyPaused:
		builder.WriteString("_Embarazo en pausa: la proyección está suspendida._\n\n")
	case predict.NextPeriodStart == "":
		builder.WriteString("_Sin historial de ciclos: todavía no se puede proyectar la próxima ventana._\n\n")
	default:
		fmt.Fprintf(&builder, "- Próximo periodo: %s\n", predict.NextPeriodStart)
		if predict.OvulationDate != "" {
			fmt.Fprintf(&builder, "- Ovulación estimada: %s\n", predict.OvulationDate)
		}
		if predict.FertilityWindowStart != "" && predict.FertilityWindowEnd != "" {
			fmt.Fprintf(&builder, "- Ventana fértil: %s .. %s\n", predict.FertilityWindowStart, predict.FertilityWindowEnd)
		}
		fmt.Fprintf(&builder, "- Ciclo medio: %d días (mediana %d, %d ciclos completados)\n\n",
			predict.CycleBasis.PredictedCycleLength, predict.CycleBasis.MedianCycleLength, predict.CycleBasis.CompletedCycles)
	}

	if document.Delivery != nil && document.Delivery.Attempted {
		builder.WriteString("## Entrega\n\n")
		if document.Delivery.Delivered {
			fmt.Fprintf(&builder, "Enviado a %s.\n\n", document.Delivery.Host)
		} else {
			fmt.Fprintf(&builder, "No se pudo enviar a %s: %s\n\n", document.Delivery.Host, document.Delivery.Error)
		}
	}

	fmt.Fprintf(&builder, "---\n\n%s\n", document.Disclaimer)
	return builder.String()
}

// digestPhaseLabel renders a phase token in Spanish. The document's phase tokens
// are the shipped machine names (menstrual / follicular / ovulation / luteal) so
// the JSON stays scriptable; only the Markdown rendering translates them.
func digestPhaseLabel(phase string) string {
	switch phase {
	case "menstrual":
		return "Menstrual"
	case "follicular":
		return "Folicular"
	case "ovulation":
		return "Ovulación"
	case "luteal":
		return "Lútea"
	default:
		return phase
	}
}
