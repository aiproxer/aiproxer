package auth

import (
	"errors"
	"strings"
	"testing"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func trustedScope() scope.PrincipalScopeView {
	return scope.PrincipalScopeView{
		SubjectKind:    scope.SubjectHuman,
		PrincipalID:    scope.Known("user-1"),
		DisplayName:    scope.Known("Alice"),
		AuthMethod:     scope.Known("oidc"),
		CredentialID:   scope.Known("key-1"),
		Roles:          []string{"admin"},
		SafeClaims:     map[string]string{"team": "core"},
		TenantID:       scope.Known("t1"),
		OrganizationID: scope.Known("org-1"),
		WorkspaceID:    scope.Known("ws-1"),
		ProjectID:      scope.Unknown(),
		DepartmentID:   scope.Unknown(),
		CostCenterID:   scope.Unknown(),
		PolicyLabels:   map[string]string{"env": "prod"},
		Origin:         scope.OriginClient,
	}
}

// TestBuildScope_trustedScopeWins proves a trusted auth-provided scope is returned
// authoritatively and the legacy principal projection is derived from it (precedence rung 1).
func TestBuildScope_trustedScopeWins(t *testing.T) {
	t.Parallel()
	trusted := trustedScope()
	res, err := BuildScope(ScopeBuildInput{
		Decision: sdkauth.Decision{Outcome: sdkauth.OutcomeAllow, Scope: &trusted},
	})
	if err != nil {
		t.Fatalf("BuildScope: %v", err)
	}
	if !res.Scope.PrincipalID.Equal(scope.Known("user-1")) {
		t.Fatalf("scope PrincipalID: %+v", res.Scope.PrincipalID)
	}
	if res.Scope.AuthMethod.String() != "oidc" {
		t.Fatalf("AuthMethod: got %q", res.Scope.AuthMethod)
	}
	if res.Principal.ID != "user-1" {
		t.Fatalf("principal projection ID: got %q", res.Principal.ID)
	}
}

// TestBuildScope_principalDerivedFromTrustedScope proves the returned principal view is the
// projection of the returned scope, not the legacy principal field (requirements 1.5, 4.6).
func TestBuildScope_principalDerivedFromTrustedScope(t *testing.T) {
	t.Parallel()
	trusted := trustedScope()
	trusted.PrincipalID = scope.Known("scope-wins")
	res, err := BuildScope(ScopeBuildInput{
		Decision: sdkauth.Decision{
			Outcome:   sdkauth.OutcomeAllow,
			Principal: execview.PrincipalView{ID: "legacy-loses"},
			Scope:     &trusted,
		},
	})
	if err != nil {
		t.Fatalf("BuildScope: %v", err)
	}
	if res.Principal.ID != "scope-wins" {
		t.Fatalf("expected scope-derived principal %q, got %q", "scope-wins", res.Principal.ID)
	}
	projection := res.Scope.Principal()
	if projection.ID != res.Principal.ID {
		t.Fatalf("principal must equal scope projection: %+v vs %+v", res.Principal, projection)
	}
}

// TestBuildScope_legacyPrincipalFallback proves a principal-only decision (no scope) still
// produces an authoritative scope with unknown optional fields preserved as unknown.
func TestBuildScope_legacyPrincipalFallback(t *testing.T) {
	t.Parallel()
	res, err := BuildScope(ScopeBuildInput{
		Decision: sdkauth.Decision{
			Outcome:        sdkauth.OutcomeAllow,
			Principal:      execview.PrincipalView{ID: "legacy-user", DisplayName: "Legacy", Roles: []string{"ops"}, Claims: map[string]string{"t": "a"}},
			Device:         sdkauth.DeviceIdentity{KeyID: "kid-1"},
			SatisfiedLevel: sdkauth.LevelAPIKey,
		},
	})
	if err != nil {
		t.Fatalf("BuildScope: %v", err)
	}
	if !res.Scope.PrincipalID.Equal(scope.Known("legacy-user")) {
		t.Fatalf("PrincipalID: %+v", res.Scope.PrincipalID)
	}
	if res.Scope.SubjectKind != scope.SubjectUnknown {
		t.Fatalf("SubjectKind: got %v want unknown (no inference)", res.Scope.SubjectKind)
	}
	if !res.Scope.CredentialID.Equal(scope.Known("kid-1")) {
		t.Fatalf("CredentialID: %+v", res.Scope.CredentialID)
	}
	if res.Scope.AuthMethod.String() != "api_key" {
		t.Fatalf("AuthMethod: got %q want api_key (from SatisfiedLevel)", res.Scope.AuthMethod)
	}
	if !res.Scope.TenantID.IsUnknown() {
		t.Fatalf("TenantID must remain unknown, got %+v", res.Scope.TenantID)
	}
	if !res.Scope.ProjectID.IsUnknown() || !res.Scope.DepartmentID.IsUnknown() || !res.Scope.CostCenterID.IsUnknown() {
		t.Fatal("optional org fields must remain unknown (requirement 3.5)")
	}
	if res.Principal.ID != "legacy-user" {
		t.Fatalf("principal projection ID: got %q", res.Principal.ID)
	}
}

// TestBuildScope_deniedDecisionReturnsNoScope proves denied decisions do not create a
// successful lifecycle scope (requirement 1.6, 8.5).
func TestBuildScope_deniedDecisionReturnsNoScope(t *testing.T) {
	t.Parallel()
	_, err := BuildScope(ScopeBuildInput{
		Decision: sdkauth.Decision{Outcome: sdkauth.OutcomeDeny, ReasonCode: "no_key"},
	})
	if err == nil {
		t.Fatal("expected error for denied decision")
	}
	if !errors.Is(err, ErrDeniedNoScope) {
		t.Fatalf("expected ErrDeniedNoScope, got %v", err)
	}
}

// TestBuildScope_challengeDecisionReturnsNoScope proves challenged decisions do not create
// a successful lifecycle scope (requirement 1.6).
func TestBuildScope_challengeDecisionReturnsNoScope(t *testing.T) {
	t.Parallel()
	trusted := trustedScope()
	_, err := BuildScope(ScopeBuildInput{
		Decision: sdkauth.Decision{Outcome: sdkauth.OutcomeChallenge, Scope: &trusted},
	})
	if err == nil {
		t.Fatal("expected error for challenged decision")
	}
	if !errors.Is(err, ErrDeniedNoScope) {
		t.Fatalf("expected ErrDeniedNoScope, got %v", err)
	}
}

// TestBuildScope_noIdentityReturnsError proves a non-local allow without any identity or
// scope does not silently produce an anonymous snapshot (requirement 1.4, 2.2).
func TestBuildScope_noIdentityReturnsError(t *testing.T) {
	t.Parallel()
	_, err := BuildScope(ScopeBuildInput{
		Decision: sdkauth.Decision{Outcome: sdkauth.OutcomeAllow},
	})
	if err == nil {
		t.Fatal("expected error for allow without identity")
	}
	if !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("expected ErrNoIdentity, got %v", err)
	}
}

// TestBuildScope_trustedScopeWithoutIdentityReturnsError proves a trusted scope carrying no
// principal id does not silently produce an anonymous snapshot, mirroring the legacy path
// (requirement 1.4, 2.2).
func TestBuildScope_trustedScopeWithoutIdentityReturnsError(t *testing.T) {
	t.Parallel()
	noID := trustedScope()
	noID.PrincipalID = scope.Unknown()
	_, err := BuildScope(ScopeBuildInput{
		Decision: sdkauth.Decision{Outcome: sdkauth.OutcomeAllow, Scope: &noID},
	})
	if err == nil {
		t.Fatal("expected error for trusted scope without identity")
	}
	if !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("expected ErrNoIdentity, got %v", err)
	}
}

// TestBuildScope_rejectsUnsafeScopeValue proves the normalizer rejects credential-like
// material before it enters request lifecycle evidence (requirement 2.6, 5.4).
func TestBuildScope_rejectsUnsafeScopeValue(t *testing.T) {
	t.Parallel()
	unsafe := trustedScope()
	unsafe.PrincipalID = scope.Known("bearer abcdef0123456789")
	if _, err := BuildScope(ScopeBuildInput{
		Decision: sdkauth.Decision{Outcome: sdkauth.OutcomeAllow, Scope: &unsafe},
	}); err == nil {
		t.Fatal("expected error for credential-like scope value")
	}
}

// TestBuildScope_clientHintsDoNotElevate proves a client-supplied legacy principal cannot
// override a trusted scope (requirement 2.2).
func TestBuildScope_clientHintsDoNotElevate(t *testing.T) {
	t.Parallel()
	trusted := trustedScope()
	trusted.PrincipalID = scope.Known("trusted-id")
	res, err := BuildScope(ScopeBuildInput{
		Decision: sdkauth.Decision{
			Outcome:   sdkauth.OutcomeAllow,
			Principal: execview.PrincipalView{ID: "attacker-id"},
			Scope:     &trusted,
		},
	})
	if err != nil {
		t.Fatalf("BuildScope: %v", err)
	}
	if res.Scope.PrincipalID.String() != "trusted-id" {
		t.Fatalf("client hint elevated authority: got %q", res.Scope.PrincipalID)
	}
}

// TestBuildScope_trustedScopeIsCloned proves the returned scope is a copy so callers cannot
// mutate the trusted source (requirement 5.5).
func TestBuildScope_trustedScopeIsCloned(t *testing.T) {
	t.Parallel()
	trusted := trustedScope()
	res, err := BuildScope(ScopeBuildInput{
		Decision: sdkauth.Decision{Outcome: sdkauth.OutcomeAllow, Scope: &trusted},
	})
	if err != nil {
		t.Fatalf("BuildScope: %v", err)
	}
	res.Scope.Roles[0] = "mutated"
	if trusted.Roles[0] == "mutated" {
		t.Fatal("mutating returned scope affected trusted source")
	}
}

// TestBuildScope_doesNotInferMissingOptionalFields is a focused regression for requirement 3.5.
func TestBuildScope_doesNotInferMissingOptionalFields(t *testing.T) {
	t.Parallel()
	res, err := BuildScope(ScopeBuildInput{
		Decision: sdkauth.Decision{
			Outcome:        sdkauth.OutcomeAllow,
			Principal:      execview.PrincipalView{ID: "u"},
			SatisfiedLevel: sdkauth.LevelAPIKey,
		},
	})
	if err != nil {
		t.Fatalf("BuildScope: %v", err)
	}
	for _, v := range []scope.Value{res.Scope.TenantID, res.Scope.OrganizationID, res.Scope.WorkspaceID, res.Scope.ProjectID, res.Scope.DepartmentID, res.Scope.CostCenterID} {
		if !v.IsUnknown() {
			t.Fatalf("optional field must remain unknown, got %+v", v)
		}
	}
}

// TestBuildScope_preservesNonSecretCredentialID proves non-secret credential identifiers are
// preserved through normalization (requirement 2.5).
func TestBuildScope_preservesNonSecretCredentialID(t *testing.T) {
	t.Parallel()
	trusted := trustedScope()
	trusted.CredentialID = scope.Known("app1:key-1")
	res, err := BuildScope(ScopeBuildInput{
		Decision: sdkauth.Decision{Outcome: sdkauth.OutcomeAllow, Scope: &trusted},
	})
	if err != nil {
		t.Fatalf("BuildScope: %v", err)
	}
	if res.Scope.CredentialID.String() != "app1:key-1" {
		t.Fatalf("CredentialID: got %q", res.Scope.CredentialID)
	}
	if strings.Contains(res.Scope.CredentialID.String(), "secret") {
		t.Fatal("credential id must not be secret material")
	}
}
