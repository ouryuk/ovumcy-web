package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/cli"
	"github.com/ovumcy/ovumcy-web/internal/db"
	"github.com/ovumcy/ovumcy-web/internal/security"
)

func tryRunCLICommand() (bool, error) {
	return tryRunCLICommandWithHandlers(os.Args[1:], cliCommandHandlers{
		runResetPassword:    cli.RunResetPasswordCommand,
		runUsers:            cli.RunUsersCommand,
		runHealthcheck:      cli.RunHealthcheckCommand,
		runReadycheck:       cli.RunReadycheckCommand,
		runNotify:           cli.RunNotifyCommand,           // codecov:ignore -- main() composition-root wiring; this os.Args dispatch wrapper runs only in the binary (the handler is unit-tested via tryRunCLICommandWithHandlers with a stub)
		runWebhook:          cli.RunWebhookCommand,          // codecov:ignore -- main() composition-root wiring; this os.Args dispatch wrapper runs only in the binary (the handler is unit-tested via tryRunCLICommandWithHandlers with a stub)
		runRepair:           cli.RunRepairCommand,           // codecov:ignore -- main() composition-root wiring; this os.Args dispatch wrapper runs only in the binary (the handler is unit-tested via tryRunCLICommandWithHandlers with a stub)
		runLinkOIDCIdentity: cli.RunLinkOIDCIdentityCommand, // codecov:ignore -- main() composition-root wiring; this os.Args dispatch wrapper runs only in the binary (the handler is unit-tested via tryRunCLICommandWithHandlers with a stub)
		runExport:           cli.RunExportCommand,
		runSummary:          cli.RunSummaryCommand,
		runTrends:           cli.RunTrendsCommand,
		runPredict:          cli.RunPredictCommand,
		runDigest:           cli.RunDigestCommand,           // codecov:ignore -- main() composition-root wiring; this os.Args dispatch wrapper runs only in the binary (the handler is unit-tested via tryRunCLICommandWithHandlers with a stub)
	})
}

type cliCommandHandlers struct {
	runResetPassword    func(databaseConfig db.Config, args []string) error
	runUsers            func(databaseConfig db.Config, args []string) error
	runHealthcheck      func(port string, timeout time.Duration) error
	runReadycheck       func(port string, timeout time.Duration) error
	runNotify           func(databaseConfig db.Config, secretKey string, defaultLanguage string, location *time.Location, blockPrivateAddresses bool, args []string) error
	runWebhook          func(databaseConfig db.Config, secretKey string, args []string) error
	runRepair           func(databaseConfig db.Config, args []string) error
	runLinkOIDCIdentity func(databaseConfig db.Config, oidcConfig security.OIDCConfig, args []string) error
	runExport           func(databaseConfig db.Config, args []string) error
	runSummary          func(databaseConfig db.Config, args []string) error
	runTrends           func(databaseConfig db.Config, args []string) error
	runPredict          func(databaseConfig db.Config, args []string) error
	// runDigest composes summary + trends + predict into one shareable digest.
	// Its --ntfy delivery needs the same secret the notify pass uses to decrypt
	// the owner's stored webhook URL, so it takes the same configuration surface.
	runDigest func(databaseConfig db.Config, secretKey string, blockPrivateAddresses bool, args []string) error
}

func tryRunCLICommandWithHandlers(args []string, handlers cliCommandHandlers) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}

	switch strings.TrimSpace(args[0]) {
	case "reset-password":
		return handleResetPasswordCommand(args, handlers)
	case "users":
		return handleUsersCommand(args, handlers)
	case "healthcheck":
		return handleHealthcheckCommand(args, handlers)
	case "readycheck":
		return handleReadycheckCommand(args, handlers)
	case "notify":
		return handleNotifyCommand(args, handlers)
	case "webhook":
		return handleWebhookCommand(args, handlers)
	case "repair":
		return handleRepairCommand(args, handlers)
	case "link-oidc-identity":
		return handleLinkOIDCIdentityCommand(args, handlers)
	case "export":
		return handleExportCommand(args, handlers)
	case "summary":
		return handleSummaryCommand(args, handlers)
	case "trends":
		return handleTrendsCommand(args, handlers)
	case "predict":
		return handlePredictCommand(args, handlers)
	case "digest":
		return handleDigestCommand(args, handlers)
	default:
		return false, nil
	}
}

func handleResetPasswordCommand(args []string, handlers cliCommandHandlers) (bool, error) {
	if handlers.runResetPassword == nil {
		return true, fmt.Errorf("reset-password handler is required")
	}
	databaseConfig, err := resolveDatabaseConfig()
	if err != nil {
		return true, fmt.Errorf("invalid database config: %w", err)
	}
	return true, handlers.runResetPassword(databaseConfig, args[1:])
}

func handleUsersCommand(args []string, handlers cliCommandHandlers) (bool, error) {
	if len(args) < 2 {
		return true, fmt.Errorf("usage: ovumcy users <list|delete|create|set-email>")
	}
	if handlers.runUsers == nil {
		return true, fmt.Errorf("users handler is required")
	}
	databaseConfig, err := resolveDatabaseConfig()
	if err != nil {
		return true, fmt.Errorf("invalid database config: %w", err)
	}
	return true, handlers.runUsers(databaseConfig, args[1:])
}

func handleHealthcheckCommand(args []string, handlers cliCommandHandlers) (bool, error) {
	if len(args) != 1 {
		return true, fmt.Errorf("usage: ovumcy healthcheck")
	}
	if handlers.runHealthcheck == nil {
		return true, fmt.Errorf("healthcheck handler is required")
	}
	port, err := resolvePort()
	if err != nil {
		return true, fmt.Errorf("invalid PORT: %w", err)
	}
	return true, handlers.runHealthcheck(port, 0)
}

func handleReadycheckCommand(args []string, handlers cliCommandHandlers) (bool, error) {
	if len(args) != 1 {
		return true, fmt.Errorf("usage: ovumcy readycheck")
	}
	if handlers.runReadycheck == nil {
		return true, fmt.Errorf("readycheck handler is required")
	}
	port, err := resolvePort()
	if err != nil {
		return true, fmt.Errorf("invalid PORT: %w", err)
	}
	return true, handlers.runReadycheck(port, 0)
}

func handleNotifyCommand(args []string, handlers cliCommandHandlers) (bool, error) {
	if handlers.runNotify == nil {
		return true, fmt.Errorf("notify handler is required")
	}
	databaseConfig, err := resolveDatabaseConfig()
	if err != nil {
		return true, fmt.Errorf("invalid database config: %w", err)
	}
	secretKey, err := resolveSecretKey()
	if err != nil {
		return true, fmt.Errorf("invalid SECRET_KEY: %w", err)
	}
	location := mustLoadLocation(getEnv("TZ", "Local"))
	defaultLanguage := getEnv("DEFAULT_LANGUAGE", "en")
	// Strict, exactly as at server boot: this pass is where the egress gate is
	// enforced, so a value the server would refuse to start on must not be
	// reinterpreted here into an unguarded delivery run.
	blockPrivateAddresses, err := getEnvBoolStrict("WEBHOOK_BLOCK_PRIVATE_ADDRESSES", false)
	if err != nil {
		return true, err
	}
	return true, handlers.runNotify(databaseConfig, secretKey, defaultLanguage, location, blockPrivateAddresses, args[1:])
}

// handleRepairCommand is deliberately the only subcommand that takes no secret
// and reaches the database with migrations left unapplied: it runs when a
// migration has stopped every other path into this instance, so anything it
// needed from a healthy boot would make it unreachable exactly when it is
// wanted.
func handleRepairCommand(args []string, handlers cliCommandHandlers) (bool, error) {
	if handlers.runRepair == nil {
		return true, fmt.Errorf("repair handler is required")
	}
	databaseConfig, err := resolveDatabaseConfig()
	if err != nil {
		return true, fmt.Errorf("invalid database config: %w", err)
	}
	return true, handlers.runRepair(databaseConfig, args[1:])
}

// handleLinkOIDCIdentityCommand resolves the OIDC config the same way boot
// does (resolveOIDCConfig, defined in config.go) rather than a lighter
// bespoke reader: `link-oidc-identity` is the operator's no-session recovery
// path for issue #701, and a config this instance's boot would refuse (e.g.
// COOKIE_SECURE=false with OIDC_ENABLED=true) must refuse here too, not mint a
// binding a real sign-in could never reach.
func handleLinkOIDCIdentityCommand(args []string, handlers cliCommandHandlers) (bool, error) {
	if handlers.runLinkOIDCIdentity == nil {
		return true, fmt.Errorf("link-oidc-identity handler is required")
	}
	databaseConfig, err := resolveDatabaseConfig()
	if err != nil {
		return true, fmt.Errorf("invalid database config: %w", err)
	}
	cookieSecure, err := getEnvBoolStrict("COOKIE_SECURE", false)
	if err != nil {
		return true, err
	}
	registrationMode, err := resolveRegistrationMode()
	if err != nil {
		return true, err
	}
	oidcConfig, err := resolveOIDCConfig(cookieSecure, registrationMode)
	if err != nil {
		return true, fmt.Errorf("invalid OIDC config: %w", err)
	}
	return true, handlers.runLinkOIDCIdentity(databaseConfig, oidcConfig, args[1:])
}

// handleExportCommand dispatches `ovumcy export`: a read-only, no-secret data
// exit for one owner's records. Like `users`, it needs only the database
// config — the payload is the owner's health data, not any stored secret.
func handleExportCommand(args []string, handlers cliCommandHandlers) (bool, error) {
	if len(args) < 2 {
		return true, fmt.Errorf("usage: ovumcy export <email> [--id <id>] [--out <path>]")
	}
	if handlers.runExport == nil {
		return true, fmt.Errorf("export handler is required")
	}
	databaseConfig, err := resolveDatabaseConfig()
	if err != nil {
		return true, fmt.Errorf("invalid database config: %w", err)
	}
	return true, handlers.runExport(databaseConfig, args[1:])
}

// handleSummaryCommand dispatches `ovumcy summary`: a read-only, no-secret
// aggregation of one owner's daily logs over a 7/30/90-day window. Like
// `export`, it needs only the database config — nothing is written and no
// stored secret is read.
func handleSummaryCommand(args []string, handlers cliCommandHandlers) (bool, error) {
	if len(args) < 2 {
		return true, fmt.Errorf("usage: ovumcy summary <email> [--id <id>] [--days 7|30|90] [--json]")
	}
	if handlers.runSummary == nil {
		return true, fmt.Errorf("summary handler is required")
	}
	databaseConfig, err := resolveDatabaseConfig()
	if err != nil {
		return true, fmt.Errorf("invalid database config: %w", err)
	}
	return true, handlers.runSummary(databaseConfig, args[1:])
}

// handleTrendsCommand dispatches `ovumcy trends`: a read-only, no-secret
// correlation of one owner's symptoms and moods with the four cycle phases.
// Like `summary`, it needs only the database config — nothing is written and
// no stored secret is read.
func handleTrendsCommand(args []string, handlers cliCommandHandlers) (bool, error) {
	if len(args) < 2 {
		return true, fmt.Errorf("usage: ovumcy trends <email> [--id <id>] [--tz <IANA name>] [--json]")
	}
	if handlers.runTrends == nil {
		return true, fmt.Errorf("trends handler is required")
	}
	databaseConfig, err := resolveDatabaseConfig()
	if err != nil {
		return true, fmt.Errorf("invalid database config: %w", err)
	}
	return true, handlers.runTrends(databaseConfig, args[1:])
}

// handlePredictCommand dispatches `ovumcy predict`: a read-only, no-secret
// projection of one owner's next cycle window — next period start, ovulation
// date and fertile range — through the same derivation the dashboard uses.
// Like `summary`, it needs only the database config — nothing is written and
// no stored secret is read.
func handlePredictCommand(args []string, handlers cliCommandHandlers) (bool, error) {
	if len(args) < 2 {
		return true, fmt.Errorf("usage: ovumcy predict <email> [--id <id>] [--tz <IANA name>] [--json]")
	}
	if handlers.runPredict == nil {
		return true, fmt.Errorf("predict handler is required")
	}
	databaseConfig, err := resolveDatabaseConfig()
	if err != nil {
		return true, fmt.Errorf("invalid database config: %w", err)
	}
	return true, handlers.runPredict(databaseConfig, args[1:])
}

// handleDigestCommand dispatches `ovumcy digest`: a read-only composition of
// summary, trends and predict into one shareable weekly digest. It resolves the
// same configuration the notify pass does — the secret key that decrypts the
// owner's stored webhook URL and the egress gate — because --ntfy delivers
// through that same endpoint. Without --ntfy the secret is never used.
func handleDigestCommand(args []string, handlers cliCommandHandlers) (bool, error) {
	if len(args) < 2 {
		return true, fmt.Errorf("usage: ovumcy digest <email> [--id <id>] [--week] [--days N] [--format markdown|json] [--tz <IANA name>] [--ntfy]")
	}
	if handlers.runDigest == nil {
		return true, fmt.Errorf("digest handler is required")
	}
	databaseConfig, err := resolveDatabaseConfig()
	if err != nil {
		return true, fmt.Errorf("invalid database config: %w", err)
	}
	secretKey, err := resolveSecretKey()
	if err != nil {
		return true, fmt.Errorf("invalid SECRET_KEY: %w", err)
	}
	// Strict, exactly as `notify` and the server boot read it: the digest's
	// --ntfy path is an egress path, so a value the server would refuse to start
	// on must not be reinterpreted here into an unguarded delivery.
	blockPrivateAddresses, err := getEnvBoolStrict("WEBHOOK_BLOCK_PRIVATE_ADDRESSES", false)
	if err != nil {
		return true, err
	}
	return true, handlers.runDigest(databaseConfig, secretKey, blockPrivateAddresses, args[1:])
}

func handleWebhookCommand(args []string, handlers cliCommandHandlers) (bool, error) {
	if len(args) < 2 {
		return true, fmt.Errorf("usage: ovumcy webhook <show|set> <email> [flags]")
	}
	if handlers.runWebhook == nil {
		return true, fmt.Errorf("webhook handler is required")
	}
	databaseConfig, err := resolveDatabaseConfig()
	if err != nil {
		return true, fmt.Errorf("invalid database config: %w", err)
	}
	secretKey, err := resolveSecretKey()
	if err != nil {
		return true, fmt.Errorf("invalid SECRET_KEY: %w", err)
	}
	return true, handlers.runWebhook(databaseConfig, secretKey, args[1:])
}
