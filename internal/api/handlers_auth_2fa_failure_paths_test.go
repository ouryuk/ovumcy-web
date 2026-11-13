package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/models"
	"github.com/ovumcy/ovumcy-web/internal/services"
	"github.com/pquerna/otp/totp"
	"gorm.io/gorm"
)

// This file covers the three failure paths the 2FA challenge completion
// (VerifyTOTPLogin, internal/api/handlers_auth_2fa.go) can take AFTER a
// syntactically valid code has been read and BEFORE the owner ends up
// signed in:
//
//   - the TOTP storage layer refuses to consume the step (a genuine storage
//     fault, as opposed to a replay), which the handler must answer as an
//     internal error rather than as "invalid code";
//   - the session cannot be issued for the account (a role the web surface
//     does not support), which must leave the browser without a session;
//   - the provider-logout material staged under the challenge's opaque id
//     cannot be relocated onto the session the challenge just minted, which
//     must tear that session down again rather than leave a signed-in owner
//     whose provider logout integration was silently dropped.
//
// Every one of them is a refusal that must NOT mint a session, and each
// answers with a different spec, so they are pinned separately.

// failingTOTPUserRepository wraps the real TOTP repository and makes
// ClaimTOTPStep fail. Every other call is delegated, so the service under
// test stays a TOTP service in every other respect — the fault is confined
// to the one storage call the regression needs to fail.
//
// The failure has to be a STORAGE failure rather than a replay: ValidateCode
// claims the step and answers ErrTOTPReplayed when the claim is merely lost
// to a concurrent caller, and the handler treats that as an invalid code on
// purpose. Only a genuine error reaches the handler's internal-error arm.
type failingTOTPUserRepository struct {
	inner services.TOTPUserRepository
	err   error
}

func (repo *failingTOTPUserRepository) UpdateTOTPFieldsAndRevokeSessions(ctx context.Context, userID uint, encryptedSecret string, enabled bool) error {
	return repo.inner.UpdateTOTPFieldsAndRevokeSessions(ctx, userID, encryptedSecret, enabled)
}

func (repo *failingTOTPUserRepository) UpdateTOTPSecretCiphertext(ctx context.Context, userID uint, encryptedSecret string) error {
	return repo.inner.UpdateTOTPSecretCiphertext(ctx, userID, encryptedSecret)
}

func (repo *failingTOTPUserRepository) ClaimTOTPStep(ctx context.Context, userID uint, step int64) (bool, error) {
	return false, repo.err
}

// failingOIDCLogoutStateStore is the provider-logout-state store with its
// lookup failing. It is the storage-shaped outcome the challenge's
// relocation step can hit: the row was staged under the opaque id the
// pending cookie carries, but reading it back fails.
//
// DeleteExpired is left working on purpose — it is the first call the
// service makes on every load, and letting it fail would exercise a
// different (also interesting) arm of the same service.
type failingOIDCLogoutStateStore struct {
	err error
}

func (store *failingOIDCLogoutStateStore) Save(ctx context.Context, state *models.OIDCLogoutState) error {
	return nil
}

func (store *failingOIDCLogoutStateStore) FindBySessionID(ctx context.Context, sessionID string, userID uint) (models.OIDCLogoutState, bool, error) {
	return models.OIDCLogoutState{}, false, store.err
}

func (store *failingOIDCLogoutStateStore) DeleteBySessionID(ctx context.Context, sessionID string, userID uint) error {
	return nil
}

func (store *failingOIDCLogoutStateStore) DeleteExpired(ctx context.Context, cutoff time.Time) error {
	return nil
}

// sealTOTPPendingCookieWithLogoutStateForTest seals a pending-challenge
// cookie that carries an OIDC logout-state reference, the shape a challenge
// raised by an OIDC callback for an account with provider-logout material
// produces. The reference is an opaque lookup key — never the material.
func sealTOTPPendingCookieWithLogoutStateForTest(t *testing.T, secretKey []byte, userID uint, oidcLogoutStateID string) string {
	t.Helper()
	payload := totpPendingCookiePayload{
		UserID:            userID,
		ExpiresAt:         time.Now().Add(5 * time.Minute),
		OIDCLogoutStateID: oidcLogoutStateID,
	}
	serialized, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal totp pending payload: %v", err)
	}
	codec, err := newSecureCookieCodec(secretKey)
	if err != nil {
		t.Fatalf("newSecureCookieCodec: %v", err)
	}
	sealed, err := codec.seal(totpPendingCookieName, serialized)
	if err != nil {
		t.Fatalf("seal totp pending: %v", err)
	}
	return totpPendingCookieName + "=" + sealed
}

// TestVerifyTOTPLogin_StorageFailureDuringStepClaim_AnswersInternalError
// pins the handler's internal-error arm: when consuming the TOTP step fails
// for a reason that is NOT a replay, the challenge answers with the internal
// spec — never with "invalid code".
//
// The distinction matters to the caller and to the owner. A wrong code is the
// owner's problem and is retryable; a storage fault is the operator's, and
// answering it with "invalid code" would push the owner into retyping codes
// that are perfectly good while the database is down — and would file the
// fault under a wrong cause in the security log.
//
// The HTML form path answers with a flash redirect (the same shape every
// other auth-form refusal uses), so the HTMX path is used here to observe the
// real status.
func TestVerifyTOTPLogin_StorageFailureDuringStepClaim_AnswersInternalError(t *testing.T) {
	storageErr := errors.New("totp step claim storage unavailable")
	app, database := newOnboardingTestAppWithOptions(t, onboardingTestAppOptions{
		enableCSRF: true,
		totpService: func(database *gorm.DB) *services.TOTPService {
			return services.NewTOTPService(
				&failingTOTPUserRepository{inner: &dbUserRepoForTest{database}, err: storageErr},
				[]byte(testAppSecretKey),
				nil,
			)
		},
	})
	user := createOnboardingTestUser(t, database, "totp-storage-failure@example.com", "StrongPass1", true)
	secretKey := []byte(testAppSecretKey)
	rawSecret := setupTOTPForUser(t, database, user.ID, secretKey)
	pendingCookie := sealTOTPPendingCookieForTest(t, secretKey, user.ID, false)

	code, err := totp.GenerateCode(rawSecret, time.Now())
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}

	csrfToken, csrfCookieHeader := extractCSRFCookieAndToken(t, app)
	form := url.Values{"code": {code}, "csrf_token": {csrfToken}}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/2fa-challenge", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("Cookie", joinCookieHeader(pendingCookie, csrfCookieHeader))
	req.Header.Set("Accept-Language", "en")
	resp, err := app.Test(req, testConfigNoTimeout)
	if err != nil {
		t.Fatalf("POST /api/v1/sessions/2fa-challenge: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 — a storage fault must not be reported as an invalid code", resp.StatusCode)
	}
	if issued := responseCookie(resp.Cookies(), authCookieName); issued != nil && issued.Value != "" {
		t.Error("a storage fault must not issue an auth session")
	}
}

// TestVerifyTOTPLogin_UnsupportedRole_DoesNotIssueSession pins the
// session-issue refusal: the challenge completed successfully — the code was
// right, the step was consumed — but the account's role is not one the web
// surface supports, so no session can be minted for it.
//
// The refusal must leave the browser exactly as it found it: no auth cookie,
// and no lingering pending cookie that could be replayed against the same
// account. A challenge that answered "success" here would hand a signed-in
// browser to an account the rest of the application refuses to serve.
func TestVerifyTOTPLogin_UnsupportedRole_DoesNotIssueSession(t *testing.T) {
	app, database := newOnboardingTestAppWithCSRF(t)
	user := createOnboardingTestUser(t, database, "totp-unsupported-role@example.com", "StrongPass1", true)
	// The role policy accepts the owner and refuses every other role the
	// column allows ("partner"); the account reaches the challenge only
	// because the pending cookie names it.
	if err := database.Model(&models.User{}).Where("id = ?", user.ID).Update("role", "partner").Error; err != nil {
		t.Fatalf("demote user role: %v", err)
	}
	secretKey := []byte(testAppSecretKey)
	rawSecret := setupTOTPForUser(t, database, user.ID, secretKey)
	pendingCookie := sealTOTPPendingCookieForTest(t, secretKey, user.ID, false)

	code, err := totp.GenerateCode(rawSecret, time.Now())
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}

	csrfToken, csrfCookieHeader := extractCSRFCookieAndToken(t, app)
	form := url.Values{"code": {code}, "csrf_token": {csrfToken}}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/2fa-challenge", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("Cookie", joinCookieHeader(pendingCookie, csrfCookieHeader))
	req.Header.Set("Accept-Language", "en")
	resp, err := app.Test(req, testConfigNoTimeout)
	if err != nil {
		t.Fatalf("POST /api/v1/sessions/2fa-challenge: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 — a session that cannot be issued is an internal failure", resp.StatusCode)
	}
	if issued := responseCookie(resp.Cookies(), authCookieName); issued != nil && issued.Value != "" {
		t.Error("an account the web surface refuses must not receive an auth session")
	}
}

// TestVerifyTOTPLogin_LogoutStateRelocationFailure_TearsTheSessionDown
// pins the teardown the challenge owes when the provider-logout material
// staged under the challenge's opaque id cannot be relocated onto the
// session the challenge just minted.
//
// The order in the handler is deliberate: the session is minted first, and
// the relocation runs afterwards. So the failure arrives with a live session
// already written — and the handler's answer is to tear that session down
// again (clear the auth cookie) rather than leave a signed-in owner whose
// provider logout integration was silently dropped on the floor.
//
// The observable is the auth cookie: it must not survive the refusal. A
// session left in place would sign the owner in without the provider-side
// logout their IdP expects, which is exactly the state the teardown exists
// to prevent.
func TestVerifyTOTPLogin_LogoutStateRelocationFailure_TearsTheSessionDown(t *testing.T) {
	storageErr := errors.New("oidc logout state storage unavailable")
	app, database := newOnboardingTestAppWithOptions(t, onboardingTestAppOptions{
		enableCSRF: true,
		oidcLogoutStateService: func(*gorm.DB) *services.OIDCLogoutStateService {
			return services.NewOIDCLogoutStateService(&failingOIDCLogoutStateStore{err: storageErr})
		},
	})
	user := createOnboardingTestUser(t, database, "totp-logout-relocation@example.com", "StrongPass1", true)
	secretKey := []byte(testAppSecretKey)
	rawSecret := setupTOTPForUser(t, database, user.ID, secretKey)
	pendingCookie := sealTOTPPendingCookieWithLogoutStateForTest(t, secretKey, user.ID, "pending-logout-state-id")

	code, err := totp.GenerateCode(rawSecret, time.Now())
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}

	csrfToken, csrfCookieHeader := extractCSRFCookieAndToken(t, app)
	form := url.Values{"code": {code}, "csrf_token": {csrfToken}}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/2fa-challenge", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("Cookie", joinCookieHeader(pendingCookie, csrfCookieHeader))
	req.Header.Set("Accept-Language", "en")
	resp, err := app.Test(req, testConfigNoTimeout)
	if err != nil {
		t.Fatalf("POST /api/v1/sessions/2fa-challenge: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 — a relocation failure is an internal failure", resp.StatusCode)
	}

	// The session was minted before the relocation ran, so the teardown has to
	// retract it. A cleared cookie is written with an empty value and a
	// past expiry; what must NOT happen is a live value surviving.
	authCookie := responseCookie(resp.Cookies(), authCookieName)
	if authCookie == nil {
		t.Fatal("expected the teardown to write an auth-cookie retraction — the session was minted before the relocation ran")
	}
	if authCookie.Value != "" {
		t.Fatalf("the auth cookie survived the relocation failure (value length %d) — the session must be torn down", len(authCookie.Value))
	}
}
