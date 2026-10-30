package runtime_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Runtime half of task 14.3: an exposure admission denial over a multi-route
// plan (two selected candidates) is denied before any provider open. The real
// quote/admission policy (strict evidence capability, tariff and component
// bounds) is Enterprise: its monetary assertions live in the preservation
// suite (`TestEnterprisePreservationStrictCapabilityDenialOpensNoProviderAndWritesNoExposure`),
// which keeps the original test name and proof over the real
// billingadmission adapter. Here the open-source port fake scripts a denial and
// pins the runtime contract: exactly one admission attempt, no provider open,
// ErrBillingAdmissionDenied.

func TestExecutorStrictRoutePlanExposureDenialOpensNoProvider(t *testing.T) {
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens, admits atomic.Int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.BillingIdentity.AccountID = func(context.Context, aipapi.Call) string { return "acct-strict" }
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in runtime.BillingExposureAdmissionInput) (billingport.CallExposure, error) {
		admits.Add(1)
		if in.Route == nil {
			t.Error("exposure admission did not receive route plan")
		}
		return billingport.CallExposure{}, errors.New("strict route plan refused exposure")
	})
	ex.Backends = map[string]execbackend.Backend{
		"backend": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return nil, errors.New("must not open")
			},
		},
	}
	_, err = ex.Execute(context.Background(), &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "backend:m1|backend:m2"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	})
	if !errors.Is(err, runtime.ErrBillingAdmissionDenied) {
		t.Fatalf("error = %v, want ErrBillingAdmissionDenied", err)
	}
	if admits.Load() != 1 {
		t.Fatalf("exposure admissions = %d, want exactly one", admits.Load())
	}
	if opens.Load() != 0 {
		t.Fatalf("provider opens = %d, want 0 (deny before spend)", opens.Load())
	}
}
