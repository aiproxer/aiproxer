package runtime

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// TestAttemptAuthorityDimensions_copiesPolicyLabels pins the bug where
// attemptAuthorityDimensions built domain.Dimensions from scope IDs, backend,
// model, and route but never copied PolicyLabels, so label-based authority
// matchers never saw request labels at admission time.
func TestAttemptAuthorityDimensions_copiesPolicyLabels(t *testing.T) {
	t.Parallel()
	sc := scope.PrincipalScopeView{
		PrincipalID:  scope.Known("principal-1"),
		TenantID:     scope.Known("tenant-1"),
		PolicyLabels: map[string]string{"tier": "gold", "team": "platform"},
	}
	ctx := scope.WithScope(context.Background(), sc)
	call := aipapi.Call{ID: "request-1", Route: aipapi.RouteIntent{Selector: "route-1"}}
	c := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "backend-1", Model: "model-1"},
		Key:     "backend-1:model-1",
	}

	dims := attemptAuthorityDimensions(ctx, call, c)

	if got, want := len(dims.PolicyLabels), 2; got != want {
		t.Fatalf("PolicyLabels len = %d, want %d (request labels must propagate to authority dimensions)", got, want)
	}
	if v := dims.PolicyLabels["tier"]; !v.Equal(scope.Known("gold")) {
		t.Fatalf("PolicyLabels[tier] = %q, want %q", v.String(), "gold")
	}
	if v := dims.PolicyLabels["team"]; !v.Equal(scope.Known("platform")) {
		t.Fatalf("PolicyLabels[team] = %q, want %q", v.String(), "platform")
	}
}

// TestAttemptAuthorityDimensions_noLabelsLeavesNil pins the nil-vs-empty
// semantics: a request carrying no policy labels must yield a nil
// Dimensions.PolicyLabels (not an allocated empty map), matching the
// nil-preservation behavior of domain.Dimensions.NormalizeDimensions.
func TestAttemptAuthorityDimensions_noLabelsLeavesNil(t *testing.T) {
	t.Parallel()
	sc := scope.PrincipalScopeView{
		PrincipalID: scope.Known("principal-1"),
	}
	ctx := scope.WithScope(context.Background(), sc)
	call := aipapi.Call{ID: "request-1", Route: aipapi.RouteIntent{Selector: "route-1"}}
	c := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "backend-1", Model: "model-1"},
		Key:     "backend-1:model-1",
	}

	dims := attemptAuthorityDimensions(ctx, call, c)

	if dims.PolicyLabels != nil {
		t.Fatalf("PolicyLabels = %v, want nil when request carries no labels", dims.PolicyLabels)
	}
}

// TestAttemptAuthorityDimensions_includesCredential pins requirement 1.2: the
// credential from the authoritative scope view must propagate into the
// authority dimensions so rules targeting the credential dimension can match
// at admission time. Both known and unknown credentials must flow through.
func TestAttemptAuthorityDimensions_includesCredential(t *testing.T) {
	t.Parallel()

	known := scope.PrincipalScopeView{
		PrincipalID:  scope.Known("principal-1"),
		CredentialID: scope.Known("cred-1"),
	}
	ctx := scope.WithScope(context.Background(), known)
	call := aipapi.Call{ID: "request-1", Route: aipapi.RouteIntent{Selector: "route-1"}}
	c := routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}, Key: "backend-1:model-1"}

	dims := attemptAuthorityDimensions(ctx, call, c)
	if !dims.Credential.Equal(scope.Known("cred-1")) {
		t.Fatalf("credential = %v, want cred-1", dims.Credential)
	}

	unknown := scope.PrincipalScopeView{
		PrincipalID:  scope.Known("principal-1"),
		CredentialID: scope.Unknown(),
	}
	dimsUnknown := attemptAuthorityDimensions(scope.WithScope(context.Background(), unknown), call, c)
	if !dimsUnknown.Credential.IsUnknown() {
		t.Fatalf("unknown credential = %v, want unknown", dimsUnknown.Credential)
	}
}

// TestAttemptAuthorityDimensions_dropsUnsafePolicyLabelKeys pins requirement
// 13.1: only safe policy-label keys may propagate into authority dimensions.
// Unsafe keys (containing spaces or other disallowed runes) must be dropped
// so they cannot become authority matching dimensions.
func TestAttemptAuthorityDimensions_dropsUnsafePolicyLabelKeys(t *testing.T) {
	t.Parallel()
	sc := scope.PrincipalScopeView{
		PrincipalID:  scope.Known("principal-1"),
		PolicyLabels: map[string]string{"safe-key": "gold", "bad key": "leaked", "also.ok": "v"},
	}
	ctx := scope.WithScope(context.Background(), sc)
	call := aipapi.Call{ID: "request-1", Route: aipapi.RouteIntent{Selector: "route-1"}}
	c := routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}, Key: "backend-1:model-1"}

	dims := attemptAuthorityDimensions(ctx, call, c)
	if _, ok := dims.PolicyLabels["bad key"]; ok {
		t.Fatal("unsafe policy label key must be dropped from authority dimensions")
	}
	if v := dims.PolicyLabels["safe-key"]; !v.Equal(scope.Known("gold")) {
		t.Fatalf("safe-key label = %v, want gold", v)
	}
	if v := dims.PolicyLabels["also.ok"]; !v.Equal(scope.Known("v")) {
		t.Fatalf("also.ok label = %v, want v", v)
	}
}

// TestAttemptAuthorityDimensions_emptyLabelRemainsKnown pins the
// presence-aware label conversion: a present empty policy label is
// scope.Known("") and remains distinct from an absent label.
func TestAttemptAuthorityDimensions_emptyLabelRemainsKnown(t *testing.T) {
	t.Parallel()
	sc := scope.PrincipalScopeView{
		PrincipalID:  scope.Known("principal-1"),
		PolicyLabels: map[string]string{"tier": ""},
	}
	ctx := scope.WithScope(context.Background(), sc)
	call := aipapi.Call{ID: "request-1", Route: aipapi.RouteIntent{Selector: "route-1"}}
	c := routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}, Key: "backend-1:model-1"}

	dims := attemptAuthorityDimensions(ctx, call, c)
	if v, ok := dims.PolicyLabels["tier"]; !ok || !v.Equal(scope.Known("")) {
		t.Fatalf("empty label tier = %v, want known-empty", v)
	}
}
