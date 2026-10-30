package runtime

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
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type mockFailingAppender struct {
	callAppends atomic.Int32
	legAppends  atomic.Int32
}

func (m *mockFailingAppender) AppendCall(ctx context.Context, record usageevidence.CallUsageRecord) error {
	m.callAppends.Add(1)
	return errors.New("failing call usage append")
}

func (m *mockFailingAppender) AppendLeg(ctx context.Context, record usageevidence.CallLegUsageRecord) error {
	m.legAppends.Add(1)
	return errors.New("failing leg usage append")
}

func TestBillingAppendRetryOutputPersistenceFailureAfterSuccess(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	var opens atomic.Int32
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(3)

	// Set up the failing terminal sink.
	appender := &mockFailingAppender{}
	ex.TerminalUsageSink = appender

	// Setup Billing Identity
	ex.BillingIdentity = BillingIdentity{
		AccountID: func(context.Context, aipapi.Call) string { return "acct-test" },
		CustomerPricingRef: func(context.Context, aipapi.Call) usageevidence.VersionRef {
			return usageevidence.VersionRef{ID: "pricing:test", Version: "1"}
		},
		ChargePolicyRef: func(context.Context, aipapi.Call) usageevidence.VersionRef {
			return usageevidence.VersionRef{ID: "policy:test", Version: "1"}
		},
		OperatorRateRef: func(context.Context, string, string) usageevidence.VersionRef {
			return usageevidence.VersionRef{ID: "operator:test", Version: "1"}
		},
	}
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in BillingExposureAdmissionInput) (billingport.CallExposure, error) {
		return billingport.CallExposure{
			AccountID:       "acct-test",
			CallID:          in.CallID,
			PricingRef:      usageevidence.VersionRef{ID: "pricing:test", Version: "1"},
			ChargePolicyRef: usageevidence.VersionRef{ID: "policy:test", Version: "1"},
			Status:          billingport.ExposureOpen,
		}, nil
	})

	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "ok"},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}

	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}

	col, err := aipapi.Collect(context.Background(), stream)
	// Strict terminal durability failures are returned after the already
	// committed client output; they must not trigger an upstream retry.
	if err == nil {
		t.Fatal("Collect unexpectedly succeeded despite strict terminal append failure")
	}
	if col.Text.String() != "ok" {
		t.Fatalf("text: %q, want 'ok'", col.Text.String())
	}

	// Verify that only 1 backend open happened (no retry/failover triggered by database failure)
	if got := opens.Load(); got != 1 {
		t.Fatalf("backend opens = %d, want 1 (no retry/failover occurred)", got)
	}
	if got := appender.legAppends.Load(); got == 0 {
		t.Fatal("AppendLeg was not attempted")
	}
}
