package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/db"
	"github.com/ovumcy/ovumcy-web/internal/services"
	"gorm.io/gorm"
)

// This file holds the three decisions every read-only operator subcommand
// (`export`, `summary`, `trends`, `predict`) has to make identically: which
// account was addressed, whether the database may be opened read-only, and
// which owner clock "today" is derived from. They are spelled once here so the
// four commands cannot answer them differently — each of those divergences was
// a real defect, and each is a property of one argument or one row, not of a
// particular subcommand.

// errOperatorIDNotPositive is the message for an --id that was passed but names
// no row. Row ids are positive (the column is an autoincrement primary key), so
// `--id 0` can only be a mistake, a shell variable that expanded empty, or a
// script that computed the wrong number. It must never be silently reinterpreted
// as "no --id given", which is what the zero value used to mean: an explicit
// `--id 0` then fell through to the positional EMAIL and resolved a different
// account — the one thing a data exit must not do.
func errOperatorIDNotPositive(userID uint) error {
	return fmt.Errorf("--id must be a positive account id, got %d (see ovumcy users list)", userID)
}

// parseOperatorUserID parses one --id value. Pointing every subcommand at this
// function keeps the accepted vocabulary, the 32/64-bit truncation behaviour
// and the rejection of a non-positive id identical across all of them.
func parseOperatorUserID(value string) (uint, error) {
	parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, err
	}
	userID := uint(parsed)
	if userID == 0 {
		return 0, errOperatorIDNotPositive(0)
	}
	return userID, nil
}

// operatorUserIDSelected reports whether the identity form of the target was
// addressed, i.e. whether an --id was actually given.
//
// It must be asked of the ID ITSELF, not of a separate "was --id passed" boolean
// that a future caller could forget to set: every parse path reaches its options
// struct through parseOperatorUserID, which refuses a zero id, so a zero userID
// here can only mean "no --id was given". The one thing it is NOT allowed to
// mean is "the operator typed --id 0 and we should try the email instead".
func operatorUserIDSelected(userID uint) bool {
	return userID != 0
}

// openCLIDatabase opens the database for an operator subcommand.
//
// readOnly is spelled by the command, because it is a property of what the
// command DOES, not of how it happens to be implemented today: the four
// reporting commands never write, so they open read-only (and run no
// migrations); everything that changes a row keeps the read-write pool. The
// defect this closes is that a reporting command pointed at a live instance
// opened the server's own database read-write and ran the embedded migrations
// against it — a read command that can rewrite the operator's data file, and
// does the moment the binary is newer than the deployment.
func openCLIDatabase(databaseConfig db.Config, readOnly bool) (*gorm.DB, error) {
	if readOnly {
		return db.OpenDatabaseReadOnly(databaseConfig)
	}
	return db.OpenDatabase(databaseConfig)
}

// ownerClockZone resolves the calendar day a command measures "today" in.
//
// It takes the override the operator typed (--tz) together with the flag that
// says whether they typed it at all, plus the account's id, and it reads the
// stored zone from the FULL user row itself.
//
// Reading the row here rather than taking a `string` from the caller is the
// point: the four reporting commands hold different values after resolving the
// owner. `predict` loads the full row anyway, while `export`, `summary` and
// `trends` hold a models.OperatorUserSummary — which carries no Timezone field
// at all. Deriving the zone from whatever the caller happened to have in hand is
// exactly how `summary` and `trends` came to reference a field that does not
// exist; asking the repository once, here, keeps every command on the same
// column (users.timezone) with the same fallback.
//
// A lookup failure is NOT swallowed into the UTC fallback: "this account has no
// usable zone" and "the users table could not be read" are different answers,
// and silently reporting the second as the first would hand an operator a
// window that looks authoritative and is off by a day. The caller wraps it.
func ownerClockZone(
	ctx context.Context,
	users *db.UserRepository,
	userID uint,
	override string,
	overrideSet bool,
) (location *time.Location, name string, err error) {
	if overrideSet {
		location, name = resolveOperatorLocation(override)
		return location, name, nil
	}

	user, found, err := users.FindByIDOptional(ctx, userID)
	if err != nil {
		return nil, "", fmt.Errorf("look up account timezone: %w", err)
	}
	if !found {
		return nil, "", fmt.Errorf("look up account timezone: %w", services.ErrOperatorUserNotFound)
	}

	location, name = resolveOperatorLocation(user.Timezone)
	return location, name, nil
}

// resolveOperatorLocation resolves the clock a command derives "today" from.
//
// ownerTimezone is the account's persisted IANA name (users.timezone, written by
// the settings handler whenever a browser reports a zone). It is empty for an
// account that has never been seen from a browser, and it can be useless after a
// restore from another host, so every failure degrades to UTC — never to
// time.Local, which would make the same account's answer depend on the server's
// own TZ and, on a homelab, silently shift a day.
//
// The returned name is what the document reports, so the value a consumer reads
// is exactly the zone the numbers were computed in (see services'
// resolveOwnerLocation, which applies the same rules to the same column for the
// request-free passes and trends).
func resolveOperatorLocation(ownerTimezone string) (location *time.Location, name string) {
	name = strings.TrimSpace(ownerTimezone)
	if name == "" {
		return time.UTC, "UTC"
	}
	// time.LoadLocation("Local") returns the SERVER's zone with no error, which
	// is the one name a stored value must never be honoured as: it would pin the
	// owner to whatever TZ the operator's shell happens to carry.
	if strings.EqualFold(name, "Local") {
		return time.UTC, "UTC"
	}
	loaded, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC, "UTC"
	}
	return loaded, loaded.String()
}
