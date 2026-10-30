package auth

import (
	"errors"
	"testing"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// TestPhase6_attributionRichnessDoesNotChangeAllowOutcome proves the presence or absence of
// optional tenant/project/department/cost-center attribution does not change the allow/deny
// outcome of BuildScope: a rich scope and a minimal scope on an allowed decision both succeed,
// and attribution by itself never turns a denied decision into an allowed one (requirement 8.5,
// 7.2). BuildScope is a normalizer, not a policy engine.
func TestPhase6_attributionRichnessDoesNotChangeAllowOutcome(t *testing.T) {
	t.Parallel()
	rich := trustedScope()
	minScope := scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("user-1"),
	}
	richRes, richErr := BuildScope(ScopeBuildInput{
		Decision: sdkauth.Decision{Outcome: sdkauth.OutcomeAllow, Scope: &rich},
	})
	minRes, minErr := BuildScope(ScopeBuildInput{
		Decision: sdkauth.Decision{Outcome: sdkauth.OutcomeAllow, Scope: &minScope},
	})
	if richErr != nil || minErr != nil {
		t.Fatalf("attribution richness changed allow outcome: richErr=%v minErr=%v", richErr, minErr)
	}
	if !richRes.Scope.PrincipalID.Equal(minRes.Scope.PrincipalID) {
		t.Fatalf("principal id drifted with optional attribution: rich=%+v min=%+v", richRes.Scope.PrincipalID, minRes.Scope.PrincipalID)
	}
	// Optional fields are present on the rich scope and remain unknown on the minimal scope;
	// neither outcome was denied for the absence or presence of optional attribution.
	if !richRes.Scope.TenantID.IsKnown() {
		t.Fatal("rich scope must preserve known optional TenantID")
	}
	if minRes.Scope.TenantID.IsKnown() {
		t.Fatal("minimal scope must leave optional TenantID unknown, not inferred")
	}
}

// TestPhase6_attributionDoesNotAllowDeniedDecision proves a denied decision never yields a
// successful lifecycle scope regardless of how rich the attached attribution is (requirements 8.5,
// 1.6). Attribution cannot override the trusted auth outcome.
func TestPhase6_attributionDoesNotAllowDeniedDecision(t *testing.T) {
	t.Parallel()
	rich := trustedScope()
	_, err := BuildScope(ScopeBuildInput{
		Decision: sdkauth.Decision{Outcome: sdkauth.OutcomeDeny, Scope: &rich, ReasonCode: "missing_api_key"},
	})
	if err == nil {
		t.Fatal("rich attribution must not turn a denied decision into an allowed lifecycle scope")
	}
	if !isDeniedNoScope(err) {
		t.Fatalf("denied decision must return ErrDeniedNoScope, got %v", err)
	}
}

// TestPhase6_noIdentityNoAttributionDoesNotAllow proves that without any trusted identity or
// attribution and without local-mode fallback, BuildScope does not synthesize authority
// (requirement 8.5, 1.4). Attribution alone is never a basis for allow.
func TestPhase6_noIdentityNoAttributionDoesNotAllow(t *testing.T) {
	t.Parallel()
	_, err := BuildScope(ScopeBuildInput{
		Decision: sdkauth.Decision{Outcome: sdkauth.OutcomeAllow},
	})
	if err == nil {
		t.Fatal("allow decision with no identity and no local fallback must not produce a scope")
	}
}

func isDeniedNoScope(err error) bool {
	return errors.Is(err, ErrDeniedNoScope)
}
