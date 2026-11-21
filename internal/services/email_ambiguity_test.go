package services

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ovumcy/ovumcy-web/internal/models"
)

// stubNormalizedEmailFinder is the minimal fake for resolveUniqueUserByEmail's
// narrow read surface: it replays a canned row list or error without any of the
// operator-repo stub's unrelated fields.
type stubNormalizedEmailFinder struct {
	users []models.User
	err   error
}

func (stub *stubNormalizedEmailFinder) FindAllByNormalizedEmail(context.Context, string) ([]models.User, error) {
	return stub.users, stub.err
}

// TestResolveUniqueUserByEmailPassesRepositoryErrorThrough pins that a storage
// failure is returned as-is rather than rewritten into "not found" or an
// ambiguity refusal — a caller that retried or reported "no such user" on a
// dead database would be worse than the error it started with.
func TestResolveUniqueUserByEmailPassesRepositoryErrorThrough(t *testing.T) {
	t.Parallel()

	repoErr := errors.New("connection refused")
	user, found, err := resolveUniqueUserByEmail(
		context.Background(),
		&stubNormalizedEmailFinder{err: repoErr},
		"owner@example.com",
	)
	if !errors.Is(err, repoErr) {
		t.Fatalf("expected repository error to pass through, got %v", err)
	}
	var ambiguous *AmbiguousEmailError
	if errors.As(err, &ambiguous) {
		t.Fatal("did not expect a storage failure to masquerade as an ambiguity refusal")
	}
	if found || user.ID != 0 {
		t.Fatalf("expected no user on error, got found=%v user=%#v", found, user)
	}
}

// TestResolveUniqueUserByEmailReportsNoMatchWithoutError pins the contract the
// callers rely on to phrase "not found" in their own vocabulary: nothing
// matches is (zero user, false, nil), not an error.
func TestResolveUniqueUserByEmailReportsNoMatchWithoutError(t *testing.T) {
	t.Parallel()

	user, found, err := resolveUniqueUserByEmail(
		context.Background(),
		&stubNormalizedEmailFinder{},
		"nobody@example.com",
	)
	if err != nil {
		t.Fatalf("expected no error on zero matches, got %v", err)
	}
	if found {
		t.Fatal("did not expect a match on an empty row list")
	}
	if user.ID != 0 {
		t.Fatalf("expected zero user on no match, got %#v", user)
	}
}

// TestResolveUniqueUserByEmailReturnsTheSingleMatch pins the unique-row path:
// the one matching row comes back verbatim with found=true.
func TestResolveUniqueUserByEmailReturnsTheSingleMatch(t *testing.T) {
	t.Parallel()

	row := models.User{ID: 7, Email: "owner@example.com"}
	user, found, err := resolveUniqueUserByEmail(
		context.Background(),
		&stubNormalizedEmailFinder{users: []models.User{row}},
		"owner@example.com",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected the single row to resolve as found")
	}
	if user.ID != row.ID || user.Email != row.Email {
		t.Fatalf("expected row %d %q, got %#v", row.ID, row.Email, user)
	}
}

// TestResolveUniqueUserByEmailRefusesMoreThanOneMatch pins the shared refusal
// directly (the call-site tests pin it through their services): two rows on
// one mailbox raise *AmbiguousEmailError naming every id, in the repository's
// order, and no row is returned for a caller to act on.
func TestResolveUniqueUserByEmailRefusesMoreThanOneMatch(t *testing.T) {
	t.Parallel()

	user, found, err := resolveUniqueUserByEmail(
		context.Background(),
		&stubNormalizedEmailFinder{users: []models.User{
			{ID: 5, Email: "owner@example.com"},
			{ID: 18, Email: "owner@example.com"},
		}},
		"owner@example.com",
	)
	var ambiguous *AmbiguousEmailError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("expected *AmbiguousEmailError, got %v", err)
	}
	if got, want := ambiguous.Email, "owner@example.com"; got != want {
		t.Fatalf("expected refusal to name %q, got %q", want, got)
	}
	if got, want := ambiguous.IDs, []uint{5, 18}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("expected ids %v, got %v", want, got)
	}
	if found || user.ID != 0 {
		t.Fatalf("did not expect a row to be returned on refusal, got found=%v user=%#v", found, user)
	}
}

// TestAmbiguousEmailErrorMessage pins the operator-facing wording of the
// refusal for the id-list shapes it can be built with: one id, several ids
// (comma-separated, the style `users list` prints), and the degenerate empty
// list — so the message an operator reads stays actionable and stable.
func TestAmbiguousEmailErrorMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  *AmbiguousEmailError
		want string
	}{
		{
			name: "several ids",
			err:  &AmbiguousEmailError{Email: "owner@example.com", IDs: []uint{5, 18, 204}},
			want: "email owner@example.com matches more than one account (ids 5, 18, 204)",
		},
		{
			name: "one id",
			err:  &AmbiguousEmailError{Email: "owner@example.com", IDs: []uint{5}},
			want: "email owner@example.com matches more than one account (ids 5)",
		},
		{
			name: "no ids",
			err:  &AmbiguousEmailError{Email: "owner@example.com"},
			want: "email owner@example.com matches more than one account (ids )",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := test.err.Error(); got != test.want {
				t.Fatalf("Error() = %q, want %q", got, test.want)
			}
			if !strings.Contains(test.err.Error(), test.err.Email) {
				t.Fatalf("expected message to name the address %q", test.err.Email)
			}
		})
	}
}
