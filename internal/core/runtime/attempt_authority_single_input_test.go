package runtime

import (
	"context"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	accountingpreflight "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/preflight"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// admitInputRecorder records every usage-authority admission input and allows it.
type admitInputRecorder struct {
	mu     sync.Mutex
	inputs []usagelimit.AdmissionInput
}

func (r *admitInputRecorder) Admit(_ context.Context, in usagelimit.AdmissionInput) (usagelimit.AdmissionResult, error) {
	r.mu.Lock()
	r.inputs = append(r.inputs, in)
	r.mu.Unlock()
	return usagelimit.AdmissionResult{Allowed: true, Outcome: usagelimit.DecisionOutcomeAllow}, nil
}

func (r *admitInputRecorder) Settle(context.Context, usagelimit.SettleInput) (usagelimit.SettleResult, error) {
	return usagelimit.SettleResult{}, nil
}

func (r *admitInputRecorder) Release(context.Context, usagelimit.ReleaseInput) (usagelimit.ReleaseResult, error) {
	return usagelimit.ReleaseResult{}, nil
}

func (r *admitInputRecorder) ApplyUsage(context.Context, usagelimit.ApplyUsageCommand) (usagelimit.ApplyUsageResult, error) {
	return usagelimit.ApplyUsageResult{}, nil
}

// The reserving admission through the attempt coordinator must evaluate rules
// against the same attribution the estimate-only precheck used; otherwise
// route- or label-scoped rules screen requests but never reserve against them.
func TestAdmitAttemptAuthority_CoordinatorAdmissionMatchesPrecheckAttribution(t *testing.T) {
	t.Parallel()
	rec := &admitInputRecorder{}
	ex := &Executor{}
	ex.UsageAuthority = rec
	_, ex.AttemptCoordinator = BuildAuthorityCoordinators(rec, nil)

	ctx := scope.WithScope(context.Background(), scope.PrincipalScopeView{
		PrincipalID:  scope.Known("principal-1"),
		PolicyLabels: map[string]string{"team": "blue"},
	})
	call := aipapi.Call{ID: "req-1", Route: aipapi.RouteIntent{Selector: "route-a"}}
	bleg := b2bua.BLegRecord{BLegID: "b-1", Seq: 1}
	decision := accountingpreflight.Decision{}

	for _, estimateOnly := range []bool{true, false} {
		if _, err := ex.admitAttemptAuthority(ctx, "trace-1", "a-1", bleg, call, authorityCandidate(), decision, estimateOnly); err != nil {
			t.Fatalf("admitAttemptAuthority(estimateOnly=%v): %v", estimateOnly, err)
		}
	}
	if len(rec.inputs) != 2 {
		t.Fatalf("admissions=%d want 2 (precheck + reserving admission)", len(rec.inputs))
	}
	precheck, reserve := rec.inputs[0], rec.inputs[1]
	if reserve.EstimateOnly {
		t.Fatal("second admission should reserve")
	}
	if got, want := reserve.Dimensions.Route, precheck.Dimensions.Route; got != want || got != scope.Known("route-a") {
		t.Errorf("reserving Route=%v precheck Route=%v want both %v", got, want, scope.Known("route-a"))
	}
	if got := reserve.Dimensions.PolicyLabels["team"]; got != scope.Known("blue") {
		t.Errorf("reserving PolicyLabels[team]=%v want %v", got, scope.Known("blue"))
	}
	if got := reserve.Correlation.TraceID; got != "trace-1" {
		t.Errorf("reserving Correlation.TraceID=%q want trace-1", got)
	}
	if got, want := reserve.ReservationKey, precheck.ReservationKey; got != want {
		t.Errorf("reserving ReservationKey=%+v precheck=%+v want equal", got, want)
	}
}
