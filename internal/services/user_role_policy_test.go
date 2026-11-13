package services

import (
	"errors"
	"testing"

	"github.com/ovumcy/ovumcy-web/internal/models"
)

func TestNormalizeUserRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		role string
		want string
	}{
		{name: "empty role", role: "", want: ""},
		{name: "lowercase owner", role: "owner", want: "owner"},
		{name: "mixed case owner", role: "Owner", want: "owner"},
		{name: "upper case owner", role: "OWNER", want: "owner"},
		{name: "padded owner", role: "  owner\t", want: "owner"},
		{name: "padded mixed case", role: " Owner ", want: "owner"},
		{name: "unsupported role passes through", role: "viewer", want: "viewer"},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := NormalizeUserRole(testCase.role); got != testCase.want {
				t.Fatalf("NormalizeUserRole(%q) = %q, want %q", testCase.role, got, testCase.want)
			}
		})
	}
}

func TestIsOwnerUser(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		user *models.User
		want bool
	}{
		{name: "nil user", user: nil, want: false},
		{name: "owner role", user: &models.User{Role: "owner"}, want: true},
		{name: "owner role padded mixed case", user: &models.User{Role: " Owner "}, want: true},
		{name: "empty role", user: &models.User{Role: ""}, want: false},
		{name: "non-owner role", user: &models.User{Role: "member"}, want: false},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := IsOwnerUser(testCase.user); got != testCase.want {
				t.Fatalf("IsOwnerUser(%v) = %v, want %v", testCase.user, got, testCase.want)
			}
		})
	}
}

func TestValidateSupportedWebUser(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		user    *models.User
		wantErr error
	}{
		{name: "owner user accepted", user: &models.User{Role: "owner"}, wantErr: nil},
		{name: "padded mixed case owner accepted", user: &models.User{Role: " OWNER "}, wantErr: nil},
		{name: "nil user rejected", user: nil, wantErr: ErrAuthUnsupportedRole},
		{name: "empty role rejected", user: &models.User{Role: ""}, wantErr: ErrAuthUnsupportedRole},
		{name: "non-owner role rejected", user: &models.User{Role: "member"}, wantErr: ErrAuthUnsupportedRole},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateSupportedWebUser(testCase.user)
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("ValidateSupportedWebUser(%v) = %v, want %v", testCase.user, err, testCase.wantErr)
			}
		})
	}
}
