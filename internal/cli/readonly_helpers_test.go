package cli

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/db"
	"github.com/ovumcy/ovumcy-web/internal/models"
)

// setCLIUserTimezone writes users.timezone for one seeded owner.
//
// It goes through a direct UPDATE rather than UserRepository.UpdateUserTimezone
// so the test states the stored value it wants to prove the command reads,
// including the hand-edited shapes (an empty string, "Local", a name the tz
// database does not carry) that the settings handler would never write but a
// restored backup can contain.
func setCLIUserTimezone(t *testing.T, databasePath string, userID uint, timezone string) {
	t.Helper()

	database, err := db.OpenDatabase(db.Config{Driver: db.DriverSQLite, SQLitePath: databasePath})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatalf("open sql db: %v", err)
	}
	defer func() { _ = sqlDB.Close() }()

	if err := database.Model(&models.User{}).Where("id = ?", userID).Update("timezone", timezone).Error; err != nil {
		t.Fatalf("set timezone: %v", err)
	}
}

// pinCLIClock swaps one command's clock var for the duration of a test.
func pinCLIClock(t *testing.T, clockVar *func() time.Time, pinned time.Time) {
	t.Helper()
	previous := *clockVar
	pinnedAt := pinned
	*clockVar = func() time.Time { return pinnedAt }
	t.Cleanup(func() { *clockVar = previous })
}

// failingWriter is an io.Writer that refuses every write, standing in for a
// closed pipe or a full disk. It is the input a subcommand's stdout path must
// not ignore: a health export whose tail never reached the file has to exit
// non-zero, not pretend it succeeded.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("broken pipe")
}

// assertNonPositiveIDRefused runs one subcommand with `--id 0` alongside a VALID
// email and checks that the id — not the email — decided the outcome. Before the
// fix the zero id meant "not given", so the command fell through to the email
// and succeeded against that account, which is the silent-redirect defect these
// tests exist for.
func assertNonPositiveIDRefused(t *testing.T, run func(args []string) error, email string) {
	t.Helper()

	for _, args := range [][]string{
		{"--id", "0", email},
		{email, "--id", "0"},
		{"--id=0", email},
		{email, "--id=000"},
	} {
		err := run(args)
		if err == nil {
			t.Fatalf("expected --id 0 to be refused for %v, got success", args)
		}
		if !strings.Contains(err.Error(), "positive account id") {
			t.Fatalf("expected the non-positive-id message for %v, got %v", args, err)
		}
	}
}
