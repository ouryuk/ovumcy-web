package models

import (
	"errors"
	"fmt"
	"testing"
)

// TestErrorSentinelsHaveDistinctMessages pins the two facts the sentinels
// live on: each one says something, and no two of them say the same thing.
//
// The distinctness is not cosmetic. This package is the one place both the
// database layer and the service layer name a shared failure, and the barrier
// that keeps them from declaring the same text twice (internal/services'
// layer-sentinel barrier) reads the TEXT: two errors.New values with
// identical messages are never errors.Is-equal, so a comparison across the
// layer boundary would be false for the very error it was testing for — while
// reading as a working check everywhere it appears.
func TestErrorSentinelsHaveDistinctMessages(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
	}{
		{name: "reset token already consumed", err: ErrResetTokenAlreadyConsumed},
		{name: "oidc logout state unattributed", err: ErrOIDCLogoutStateUnattributed},
	}

	// The messages are collected first and compared afterwards: the verdict
	// is about the whole set, not about one sentinel at a time.
	messages := make([]string, len(cases))
	for index, test := range cases {
		if test.err == nil {
			t.Fatalf("%s is nil", test.name)
		}
		message := test.err.Error()
		if message == "" {
			t.Fatalf("%s has an empty message", test.name)
		}
		messages[index] = message
	}

	for first := range cases {
		for second := first + 1; second < len(cases); second++ {
			if messages[first] == messages[second] {
				t.Fatalf("%s and %s share the message %q", cases[first].name, cases[second].name, messages[first])
			}
		}
	}
}

// TestErrorSentinelMessagesAreStable pins the exact wording of each sentinel.
//
// The message is what an operator reads in a log and what the layer barrier
// compares, so a reworded sentinel is a change with reach beyond this
// package: it would change what a log line says and what the barrier sees as
// a duplicate.
func TestErrorSentinelMessagesAreStable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "reset token already consumed",
			err:  ErrResetTokenAlreadyConsumed,
			want: "reset token already consumed",
		},
		{
			name: "oidc logout state unattributed",
			err:  ErrOIDCLogoutStateUnattributed,
			want: "oidc logout state requires an owner id",
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := test.err.Error(); got != test.want {
				t.Fatalf("message = %q, want %q", got, test.want)
			}
		})
	}
}

// TestErrorSentinelsMatchThemselves proves each sentinel still identifies
// itself through errors.Is — the only thing a caller across the layer
// boundary has to rely on — and that one sentinel never matches the other.
//
// A sentinel that failed to match itself would be indistinguishable from a
// freshly built error with the same text, which is exactly the defect the
// shared home in this package exists to prevent.
func TestErrorSentinelsMatchThemselves(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
	}{
		{name: "reset token already consumed", err: ErrResetTokenAlreadyConsumed},
		{name: "oidc logout state unattributed", err: ErrOIDCLogoutStateUnattributed},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if !errors.Is(test.err, test.err) {
				t.Fatalf("errors.Is(%s, %s) is false, want true", test.err, test.err)
			}
			// A sentinel is its own match; it is not a match for anything
			// else, however similar the text.
			for _, other := range cases {
				if other.err == test.err {
					continue
				}
				if errors.Is(test.err, other.err) {
					t.Fatalf("errors.Is(%s, %s) is true, want false", test.err, other.err)
				}
			}
		})
	}
}

// TestErrorSentinelsSurviveWrapping proves the identity of a sentinel is not
// lost when it travels: every layer that re-exports it adds context with
// %w, and a caller at the far end still has to recognize the failure.
//
// The negative half matters as much as the positive one: a sentinel must not
// match an error that merely carries the same text, or the comparison would
// be a string match wearing the costume of an identity check.
func TestErrorSentinelsSurviveWrapping(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
	}{
		{name: "reset token already consumed", err: ErrResetTokenAlreadyConsumed},
		{name: "oidc logout state unattributed", err: ErrOIDCLogoutStateUnattributed},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			wrapped := fmt.Errorf("redeem the token: %w", test.err)
			if !errors.Is(wrapped, test.err) {
				t.Fatalf("errors.Is(%s, %s) is false, want true", wrapped, test.err)
			}

			// The same text, freshly built: not the sentinel, so not a match.
			lookalike := errors.New(test.err.Error())
			if errors.Is(lookalike, test.err) {
				t.Fatalf("a lookalike error with the same text matched %s", test.err)
			}
			if errors.Is(test.err, lookalike) {
				t.Fatalf("%s matched a lookalike error with the same text", test.err)
			}
		})
	}
}
