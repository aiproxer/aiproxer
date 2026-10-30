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
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type exposureAdmissionFunc func(context.Context, runtime.BillingExposureAdmissionInput) (billingport.CallExposure, error)

func (f exposureAdmissionFunc) Admit(ctx context.Context, in runtime.BillingExposureAdmissionInput) (billingport.CallExposure, error) {
	return f(ctx, in)
}

func TestExecutorAuthoritativeExposureAdmissionUsesCallIDWithoutHold(t *testing.T) {
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var calls, opens atomic.Int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.BillingIdentity.AccountID = func(context.Context, aipapi.Call) string { return "acct-exposure" }
	ex.BillingIdentity.CustomerPricingRef = func(context.Context, aipapi.Call) usageevidence.VersionRef {
		return usageevidence.VersionRef{ID: "pricing", Version: "v1"}
	}
	ex.BillingIdentity.ChargePolicyRef = func(context.Context, aipapi.Call) usageevidence.VersionRef {
		return usageevidence.VersionRef{ID: "policy", Version: "v1"}
	}
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in runtime.BillingExposureAdmissionInput) (billingport.CallExposure, error) {
		calls.Add(1)
		if in.CallID == "" || len(in.CallID) < 3 {
			t.Errorf("missing BillingCallID: %q", in.CallID)
		}
		if in.Route == nil {
			t.Fatal("exposure admission did not receive route plan")
		}
		return billingport.CallExposure{AccountID: "acct-exposure", CallID: in.CallID, Max: struct {
			Nano     int64
			Currency string
		}{Nano: 1, Currency: "USD"}, PricingRef: usageevidence.VersionRef{ID: "pricing", Version: "v1"}, ChargePolicyRef: usageevidence.VersionRef{ID: "policy", Version: "v1"}, Status: billingport.ExposureOpen}, nil
	})
	ex.Backends = map[string]execbackend.Backend{
		"backend": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}, {Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}
	stream, err := ex.Execute(context.Background(), &aipapi.Call{Route: aipapi.RouteIntent{Selector: "backend:model"}, Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || opens.Load() != 1 {
		t.Fatalf("exposure admissions=%d backend opens=%d", calls.Load(), opens.Load())
	}
}

func TestExecutorAuthoritativeExposureAdmissionDenialDoesNotOpenProvider(t *testing.T) {
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens atomic.Int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.BillingIdentity.AccountID = func(context.Context, aipapi.Call) string { return "acct-exposure" }
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(context.Context, runtime.BillingExposureAdmissionInput) (billingport.CallExposure, error) {
		return billingport.CallExposure{}, errors.New("insufficient exposure")
	})
	ex.Backends = map[string]execbackend.Backend{"backend": {Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming), Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		opens.Add(1)
		return nil, nil
	}}}
	_, err = ex.Execute(context.Background(), &aipapi.Call{Route: aipapi.RouteIntent{Selector: "backend:model"}, Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}}})
	if !errors.Is(err, runtime.ErrBillingAdmissionDenied) {
		t.Fatalf("error = %v, want ErrBillingAdmissionDenied", err)
	}
	if opens.Load() != 0 {
		t.Fatalf("provider opens = %d, want 0", opens.Load())
	}
}

func TestExecutorBillingAdmissionUsesTokenEstimateWithoutRouteSizeConstraints(t *testing.T) {
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.BillingIdentity.AccountID = func(context.Context, aipapi.Call) string { return "acct-estimate" }
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
	ex.RequestTokenEstimator = fixedRequestTokenEstimator{available: true, tokens: 42}
	var sawSize routing.RequestSizeEstimate
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in runtime.BillingExposureAdmissionInput) (billingport.CallExposure, error) {
		sawSize = in.RequestSize
		return billingport.CallExposure{
			AccountID: "acct-estimate", CallID: in.CallID, Max: struct {
				Nano     int64
				Currency string
			}{Nano: 1, Currency: "USD"},
			PricingRef: usageevidence.VersionRef{ID: "pricing", Version: "v1"}, ChargePolicyRef: usageevidence.VersionRef{ID: "policy", Version: "v1"},
			Status: billingport.ExposureOpen,
		}, nil
	})
	ex.Backends = map[string]execbackend.Backend{
		"backend": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}, {Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}
	stream, err := ex.Execute(context.Background(), &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "backend:model"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	if !sawSize.Available || sawSize.Tokens != 42 {
		t.Fatalf("billing RequestSize = %+v, want available tokens=42 without route size constraints", sawSize)
	}
}
