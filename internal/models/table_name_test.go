package models

import (
	"testing"

	"gorm.io/gorm/schema"
)

// TestTableNameIsNotTheNameGormWouldDerive pins WHY these TableName methods
// exist: gorm's own naming strategy derives three of the four differently
// (OIDCIdentity -> o_id_c_identities), so the explicit pins are load-bearing.
func TestTableNameIsNotTheNameGormWouldDerive(t *testing.T) {
	t.Parallel()

	namer := schema.NamingStrategy{}

	cases := []struct {
		name       string
		structName string
		pinned     string
		want       string
		differs    bool
	}{
		{name: "app state", structName: "AppState", pinned: AppState{}.TableName(), want: "app_states", differs: true},
		{name: "oidc identity", structName: "OIDCIdentity", pinned: OIDCIdentity{}.TableName(), want: "o_id_c_identities", differs: true},
		{name: "oidc logout state", structName: "OIDCLogoutState", pinned: OIDCLogoutState{}.TableName(), want: "o_id_c_logout_states", differs: true},
		{name: "register pickup token", structName: "RegisterPickupToken", pinned: RegisterPickupToken{}.TableName(), want: "register_pickup_tokens", differs: false},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			derived := namer.TableName(test.structName)
			if derived != test.want {
				t.Fatalf("gorm's naming strategy derived %q for %s, want %q", derived, test.structName, test.want)
			}
			if test.differs && test.pinned == derived {
				t.Fatalf("%s pins %q, which is the name gorm would derive on its own", test.structName, test.pinned)
			}
			if !test.differs && test.pinned != derived {
				t.Fatalf("%s pins %q, want the name gorm derives, %q", test.structName, test.pinned, derived)
			}
		})
	}
}

func TestTableNameMappings(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		model interface{ TableName() string }
		want  string
	}{
		{name: "app state", model: AppState{}, want: "app_state"},
		{name: "oidc identity", model: OIDCIdentity{}, want: "oidc_identities"},
		{name: "oidc logout state", model: OIDCLogoutState{}, want: "oidc_logout_states"},
		{name: "register pickup token", model: RegisterPickupToken{}, want: "register_pickup_tokens"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := testCase.model.TableName(); got != testCase.want {
				t.Fatalf("TableName() = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestTableNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		model interface{ TableName() string }
		want  string
	}{
		{name: "app state", model: AppState{}, want: "app_state"},
		{name: "oidc identity", model: OIDCIdentity{}, want: "oidc_identities"},
		{name: "oidc logout state", model: OIDCLogoutState{}, want: "oidc_logout_states"},
		{name: "register pickup token", model: RegisterPickupToken{}, want: "register_pickup_tokens"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := testCase.model.TableName(); got != testCase.want {
				t.Fatalf("TableName() = %q, want %q", got, testCase.want)
			}
		})
	}
}
