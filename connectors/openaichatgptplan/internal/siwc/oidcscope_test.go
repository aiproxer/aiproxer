package siwc

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The granted-scope policy.
//
// The documentation is explicit: "A valid ID token alone does not authorize
// ChatGPT plan usage", and a sign-in that granted the identity scopes
// without the direct plan-use scope keeps its registration with the plan
// usage DISABLED.

// TestCheckDirectPlanUsage proves that the direct plan-use scope is the
// one that authorizes the plan usage, and that its absence is a CLASSIFIED
// outcome rather than an unclassified failure.
func TestCheckDirectPlanUsage(t *testing.T) {
	cases := []struct {
		name      string
		scopes    []string
		wantGrant bool
	}{
		{
			name:      "the documented scope set",
			scopes:    []string{"chatgpt.tokens.use.direct", "email", "offline_access", "openid", "profile", "resource.invoke"},
			wantGrant: true,
		},
		{
			name:      "the direct scope alone",
			scopes:    []string{PlanUseScope},
			wantGrant: true,
		},
		{
			name:      "the identity scopes alone",
			scopes:    []string{"openid", "profile", "email", "offline_access", "resource.invoke"},
			wantGrant: false,
		},
		{
			name:      "no scope at all",
			scopes:    nil,
			wantGrant: false,
		},
		{
			name:      "a scope that only contains the name",
			scopes:    []string{"chatgpt.tokens.use.direct.other"},
			wantGrant: false,
		},
		{
			name:      "a scope that only contains the name as a prefix",
			scopes:    []string{"xchatgpt.tokens.use.direct"},
			wantGrant: false,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if granted := PlanUsageEnabled(testCase.scopes); granted != testCase.wantGrant {
				t.Fatalf("PlanUsageEnabled = %t, want %t", granted, testCase.wantGrant)
			}
			err := CheckDirectPlanUsage(testCase.scopes)
			if testCase.wantGrant {
				if err != nil {
					t.Fatalf("CheckDirectPlanUsage: %v", err)
				}
				return
			}
			requireValidatorClass(t, err, ValidatorClassScope)
			if !IsPlanUsageNotGranted(err) {
				t.Error("the failure is not classified as the plan-usage outcome")
			}
			if !errors.Is(err, ErrPlanUsageNotGranted) {
				t.Error("the failure does not carry the plan-usage cause")
			}
		})
	}
}

// TestRegister_RetainsTheSignInWhenTheDirectPlanScopeIsMissing proves that
// a sign-in without the direct plan-use scope keeps its registration, and
// that the registration says the plan usage is not authorized.
//
// The documentation requires the retention: the account IS authenticated,
// and only the plan usage is disabled.
func TestRegister_RetainsTheSignInWhenTheDirectPlanScopeIsMissing(t *testing.T) {
	h := newHarness(t)
	h.fake.setScope("openid profile email offline_access resource.invoke")
	h.setDelivery(callbackDelivery{code: "auth-code-1", clientID: "oaiapp_issued_1"})

	record, err := h.authorizer.Register(context.Background(), RegisterRequest{ProfileID: "acct-test"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if record.AllowsPlanUsage() {
		t.Error("the registration claims the plan usage is authorized")
	}
	if len(record.GrantedScopes) == 0 {
		t.Error("the registration carries no granted scopes")
	}
	// The registration is PERSISTED: the sign-in is retained.
	stored, err := h.profiles.Load("acct-test")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if stored.AllowsPlanUsage() {
		t.Error("the stored registration claims the plan usage is authorized")
	}
	if len(stored.GrantedScopes) == 0 {
		t.Error("the stored registration carries no granted scopes")
	}
	// The operator is told that the plan usage is disabled.
	var found bool
	for _, notice := range h.noticeMessages() {
		if strings.Contains(notice, "plan usage is disabled") {
			found = true
		}
	}
	if !found {
		t.Errorf("the flow did not report the plan-usage outcome: %v", h.noticeMessages())
	}
}

// TestRegister_ReportsTheDirectPlanScope proves that a sign-in that
// granted the direct plan-use scope reports nothing about the plan usage,
// and that the registration says the plan usage is authorized.
func TestRegister_ReportsTheDirectPlanScope(t *testing.T) {
	h := newHarness(t)
	h.setDelivery(callbackDelivery{code: "auth-code-1", clientID: "oaiapp_issued_1"})

	record, err := h.authorizer.Register(context.Background(), RegisterRequest{ProfileID: "acct-test"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !record.AllowsPlanUsage() {
		t.Error("the registration does not claim the plan usage is authorized")
	}
	for _, notice := range h.noticeMessages() {
		if strings.Contains(notice, "plan usage is disabled") {
			t.Errorf("the flow reported the plan-usage outcome for a granted scope: %q", notice)
		}
	}
}

// TestReauthorize_RetainsTheSignInWhenTheDirectPlanScopeIsMissing proves
// that a returning authorization keeps the registration when the provider
// no longer grants the direct plan-use scope.
func TestReauthorize_RetainsTheSignInWhenTheDirectPlanScopeIsMissing(t *testing.T) {
	h := newHarness(t)
	h.setDelivery(callbackDelivery{code: "auth-code-1", clientID: "oaiapp_issued_1"})
	if _, err := h.authorizer.Register(context.Background(), RegisterRequest{ProfileID: "acct-test"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	h.fake.setScope("openid profile email")
	h.setDelivery(callbackDelivery{code: "auth-code-2", omitClientID: true})

	record, err := h.authorizer.Reauthorize(context.Background(), ReauthorizeRequest{ProfileID: "acct-test"})
	if err != nil {
		t.Fatalf("Reauthorize: %v", err)
	}
	if record.AllowsPlanUsage() {
		t.Error("the registration claims the plan usage is authorized")
	}
	stored, err := h.profiles.Load("acct-test")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if stored.AllowsPlanUsage() {
		t.Error("the stored registration claims the plan usage is authorized")
	}
}

// TestRegister_DisablesPlanUsageWhenTheScopeArrivesUnderACaseAlias proves
// that the granted scope set is read from the EXACT member of the token
// response.
//
// The response carries the scope set WITHOUT the direct plan-use scope
// under its registered name, and a scope set WITH it under a case alias.
// A decoder that merges the two would authorize a plan usage the provider
// did not grant.
func TestRegister_DisablesPlanUsageWhenTheScopeArrivesUnderACaseAlias(t *testing.T) {
	h := newHarness(t)
	h.fake.setTokenBody(
		`{"access_token":"access-token-1","refresh_token":"refresh-token-1","id_token":"id-token-1",` +
			`"token_type":"Bearer","expires_in":3600,` +
			`"scope":"openid profile email offline_access",` +
			`"SCOPE":"openid chatgpt.tokens.use.direct"}`,
	)
	h.setDelivery(callbackDelivery{code: "auth-code-1", clientID: "oaiapp_issued_1"})

	record, err := h.authorizer.Register(context.Background(), RegisterRequest{ProfileID: "acct-test"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if record.AllowsPlanUsage() {
		t.Error("the registration claims the plan usage is authorized")
	}
}

// TestRegister_SuppliesTheIssuedClientIDAsTheExpectedAudience proves that
// the flow supplies the ISSUED client id as the audience the ID token is
// checked against.
//
// The issued client id is the flow's own knowledge: the provider hands it
// out during the registration, and a token that names another audience
// belongs to another registration.
func TestRegister_SuppliesTheIssuedClientIDAsTheExpectedAudience(t *testing.T) {
	h := newHarness(t)
	h.setDelivery(callbackDelivery{code: "auth-code-1", clientID: "oaiapp_issued_1"})

	if _, err := h.authorizer.Register(context.Background(), RegisterRequest{ProfileID: "acct-test"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	expectations := h.validator.expectations()
	if len(expectations) != 1 {
		t.Fatalf("the validator received %d expectations, want 1", len(expectations))
	}
	if expectations[0].Audience != "oaiapp_issued_1" {
		t.Errorf("the expected audience = %q, want the issued client id", expectations[0].Audience)
	}
	if expectations[0].Nonce == "" {
		t.Error("the expected nonce is empty")
	}
}

// TestReauthorize_SuppliesTheSavedIssuedClientIDAsTheExpectedAudience
// proves that a returning authorization checks the ID token against the
// SAVED issued client id.
func TestReauthorize_SuppliesTheSavedIssuedClientIDAsTheExpectedAudience(t *testing.T) {
	h := newHarness(t)
	h.setDelivery(callbackDelivery{code: "auth-code-1", clientID: "oaiapp_issued_1"})
	if _, err := h.authorizer.Register(context.Background(), RegisterRequest{ProfileID: "acct-test"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	h.setDelivery(callbackDelivery{code: "auth-code-2", omitClientID: true})
	if _, err := h.authorizer.Reauthorize(context.Background(), ReauthorizeRequest{ProfileID: "acct-test"}); err != nil {
		t.Fatalf("Reauthorize: %v", err)
	}
	expectations := h.validator.expectations()
	if len(expectations) != 2 {
		t.Fatalf("the validator received %d expectations, want 2", len(expectations))
	}
	if expectations[1].Audience != "oaiapp_issued_1" {
		t.Errorf("the expected audience = %q, want the saved issued client id", expectations[1].Audience)
	}
}
