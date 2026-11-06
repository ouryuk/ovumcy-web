package models

import "testing"

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
