package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func TestMapRequestAuthorityError_ConcurrencyLimitIsClientSafe(t *testing.T) {
	t.Parallel()
	err := mapRequestAuthorityError(&authoritycoord.DeniedError{ProviderID: "concurrency"})
	if !aipapi.IsPolicyDenied(err) {
		t.Fatalf("want policy denied, got %T %v", err, err)
	}
	var pol *aipapi.PolicyDecisionError
	if !errors.As(err, &pol) {
		t.Fatal("expected PolicyDecisionError")
	}
	if pol.ReasonCode != "concurrency_limit" || pol.ClientCategory != "concurrency_limit" {
		t.Fatalf("reason/category = %q/%q", pol.ReasonCode, pol.ClientCategory)
	}
	if pol.ClientMessage == "" || containsInsensitive(pol.ClientMessage, "cls_") {
		t.Fatalf("client message must stay lease-id free: %q", pol.ClientMessage)
	}
}

func TestMapRequestAuthorityError_PreservesUnavailableProviderDecision(t *testing.T) {
	t.Parallel()
	cause := errors.New("quota reader canceled")
	decision := authority.Decision{
		Kind:       authority.DecisionDeny,
		ProviderID: "quota",
		Stage:      authority.StageRequestAdmit,
		Readiness:  authority.ReadinessUnavailable,
		Evidence: authority.SafeEvidence{
			Category: "provider_quota",
			Code:     "telemetry_unavailable",
			Attrs:    map[string]string{"evidence_status": "unavailable"},
		},
	}
	input := &authoritycoord.UnavailableError{ProviderID: "quota", Err: cause, Decision: decision}

	mapped := mapRequestAuthorityError(input)
	var got *authoritycoord.UnavailableError
	if !errors.As(mapped, &got) {
		t.Fatalf("mapped error=%T %v must retain UnavailableError", mapped, mapped)
	}
	if !errors.Is(mapped, cause) {
		t.Fatalf("mapped error=%v must retain reader cause", mapped)
	}
	if got.Decision.Kind != authority.DecisionDeny || got.Decision.Readiness != authority.ReadinessUnavailable {
		t.Fatalf("mapped decision=%+v", got.Decision)
	}
	if got.Decision.Evidence.Category != "provider_quota" || got.Decision.Evidence.Attrs["evidence_status"] != "unavailable" {
		t.Fatalf("mapped evidence=%+v", got.Decision.Evidence)
	}
}

func TestAdmitRequestAuthorityOnce_ConcurrencyDenyMaps(t *testing.T) {
	t.Parallel()
	conc := &denyConcurrency{}
	ex := &Executor{
		AccountingRuntime: AccountingRuntime{
			RequestCoordinator: &authoritycoord.RequestCoordinator{Concurrency: conc},
		},
	}
	_, err := ex.admitRequestAuthorityOnce(context.Background(), "req-deny", "a1", "t1", scope.PrincipalScopeView{
		PrincipalID: scope.Known("alice"),
	})
	if !aipapi.IsPolicyDenied(err) {
		t.Fatalf("want policy denied, got %v", err)
	}
}

type denyConcurrency struct{}

func (denyConcurrency) AdmitLease(context.Context, authority.LeaseAdmission) (authority.LeaseDecision, error) {
	return authority.LeaseDecision{
		Kind: authority.LeaseDeny,
		Evidence: authority.SafeEvidence{
			Category: "concurrency_limit",
			Code:     "capacity_exceeded",
			Message:  "active request limit reached",
		},
	}, nil
}

func (denyConcurrency) RenewLease(context.Context, authority.LeaseRenew) (authority.LeaseDecision, error) {
	return authority.LeaseDecision{}, nil
}
func (denyConcurrency) ReleaseLease(context.Context, authority.LeaseRelease) error { return nil }
func (denyConcurrency) QueryLeases(context.Context, authority.LeaseQuery) (authority.LeasePage, error) {
	return authority.LeasePage{}, nil
}

func containsInsensitive(s, sub string) bool {
	return len(sub) > 0 && (s == sub || len(s) >= len(sub) && stringContainsFold(s, sub))
}

func stringContainsFold(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if equalFoldASCII(s[i:i+len(sub)], sub) {
			return true
		}
	}
	return false
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
