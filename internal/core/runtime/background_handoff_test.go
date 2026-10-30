package runtime

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// durableHandoffSink is a terminal usage sink that holds the obligation
// durably before it acknowledges the append. The readiness is the live fact
// the funding gate consults: a bounded durable holder that is full or closed
// must refuse new payable work.
type durableHandoffSink struct {
	billingport.TerminalUsageSink
	ready bool
}

func (s durableHandoffSink) TerminalHandoffReady() (bool, string) {
	if !s.ready {
		return false, "durable handoff is not ready"
	}
	return true, ""
}

// durableHandoffPointer is a pointer-shaped durable holder. Its typed nil
// must never count as a usable handoff.
type durableHandoffPointer struct{}

func (p *durableHandoffPointer) AppendCall(context.Context, usageevidence.CallUsageRecord) error {
	return nil
}

func (p *durableHandoffPointer) AppendLeg(context.Context, usageevidence.CallLegUsageRecord) error {
	return nil
}

func (p *durableHandoffPointer) TerminalHandoffReady() (bool, string) {
	if p == nil {
		return false, "durable handoff is unavailable"
	}
	return true, ""
}

// TestTerminalHandoffReadinessIsConsultedAtDispatch proves that the live
// durable-handoff readiness is a dispatch-time fact: a host that publishes
// the durable-handoff statement but cannot hold an obligation right now
// refuses a customer-funded background operation before any provider open.
func TestTerminalHandoffReadinessIsConsultedAtDispatch(t *testing.T) {
	principalID := "principal-handoff-readiness"
	accountID := "acct:" + principalID
	for _, tc := range []struct {
		name       string
		durable    bool
		ready      bool
		stated     bool
		admitted   bool
		wantReason string
	}{
		{name: "a durable and ready handoff is admitted", durable: true, ready: true, stated: true, admitted: true},
		{name: "a durable handoff that is not ready is refused", durable: true, ready: false, stated: true, admitted: false, wantReason: ReasonSponsorshipHandoffNotReady},
		{name: "a sink without a durable handoff is refused", durable: false, stated: true, admitted: false, wantReason: ReasonSponsorshipHandoffNotReady},
		{
			// The composition states the durable handoff only where it
			// validated a durable transport. A host that never published the
			// statement is not a complete host, even when its sink happens to
			// be durable.
			name: "a durable handoff the host never stated is refused", durable: true, ready: true,
			admitted: false, wantReason: ReasonSponsorshipMonetaryHostIncomplete,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture := &reasoningCompressorCapture{}
			ex := newReasoningCompressorExecutor(t, capture, accountID)
			observation := &sponsorshipObservation{}
			ex.Metrics = observation
			ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
			ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in BillingExposureAdmissionInput) (billingport.CallExposure, error) {
				return billingport.CallExposure{
					AccountID: accountID, CallID: in.CallID,
					PricingRef:      usageevidence.VersionRef{ID: "pricing:handoff", Version: "1"},
					ChargePolicyRef: usageevidence.VersionRef{ID: "policy:handoff", Version: "1"},
					Status:          billingport.ExposureOpen,
				}, nil
			})
			ex.BillingIdentity.MonetaryHost = MonetaryHost{Authority: MonetaryAuthorityStockRoot}
			if tc.stated {
				ex.BillingIdentity.MonetaryHost.TerminalHandoff = TerminalHandoffDurable
			}
			var opens atomic.Int64
			ex.Backends = map[string]execbackend.Backend{
				"backend": {
					Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
					Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
						opens.Add(1)
						return aipapi.NewFixedEventStream([]aipapi.Event{
							{Kind: aipapi.EventResponseStarted},
							{Kind: aipapi.EventResponseFinished},
						}), nil
					},
				},
			}
			if tc.durable {
				ex.TerminalUsageSink = durableHandoffSink{
					TerminalUsageSink: ex.TerminalUsageSink,
					ready:             tc.ready,
				}
			}
			ctx := scope.WithScope(context.Background(), scope.PrincipalScopeView{
				SubjectKind: scope.SubjectHuman,
				PrincipalID: scope.Known(principalID),
				Origin:      scope.OriginClient,
			})
			ctx = execctx.WithSponsorship(ctx, execctx.Sponsorship{Mode: execctx.SponsorshipModeCustomerFunded})
			client := auxreq.NewClient(func() auxreq.ExecutorRunner { return ex })
			stream, err := client.Stream(ctx, auxiliary.Request{
				Role:        "proxy_memory_phase1",
				Visibility:  "private",
				SessionMode: auxiliary.SessionModeDetached,
				Call: &aipapi.Call{
					Route:    aipapi.RouteIntent{Selector: "backend:aux-model"},
					Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("auxiliary request")}}},
				},
			})
			if tc.admitted {
				if err != nil {
					t.Fatalf("the background work was refused: %v", err)
				}
				if _, err := aipapi.Collect(ctx, stream); err != nil {
					t.Fatalf("collect: %v", err)
				}
				if got := opens.Load(); got != 1 {
					t.Fatalf("the backend opened %d times, want exactly one", got)
				}
				return
			}
			if err == nil {
				_, _ = aipapi.Collect(ctx, stream)
				t.Fatal("the background work was admitted without a usable durable handoff")
			}
			if got := opens.Load(); got != 0 {
				t.Fatalf("the refused background work opened the backend %d times, want zero", got)
			}
			if got := observation.refusalReason(); got != tc.wantReason {
				t.Fatalf("the refusal reason = %q, want %q", got, tc.wantReason)
			}
		})
	}
}

// TestTerminalHandoffReadinessRejectsATypedNilHolder proves that a typed-nil
// durable holder is not a durable handoff: the live readiness refuses it, so
// no provider is opened for money that cannot be held.
func TestTerminalHandoffReadinessRejectsATypedNilHolder(t *testing.T) {
	principalID := "principal-handoff-typed-nil"
	accountID := "acct:" + principalID
	capture := &reasoningCompressorCapture{}
	ex := newReasoningCompressorExecutor(t, capture, accountID)
	observation := &sponsorshipObservation{}
	ex.Metrics = observation
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in BillingExposureAdmissionInput) (billingport.CallExposure, error) {
		return billingport.CallExposure{AccountID: accountID, CallID: in.CallID, Status: billingport.ExposureOpen}, nil
	})
	ex.BillingIdentity.MonetaryHost = MonetaryHost{
		Authority:       MonetaryAuthorityStockRoot,
		TerminalHandoff: TerminalHandoffDurable,
	}
	var opens atomic.Int64
	ex.Backends = map[string]execbackend.Backend{
		"backend": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	var typedNil *durableHandoffPointer
	ex.TerminalUsageSink = typedNil
	ctx := scope.WithScope(context.Background(), scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known(principalID),
		Origin:      scope.OriginClient,
	})
	ctx = execctx.WithSponsorship(ctx, execctx.Sponsorship{Mode: execctx.SponsorshipModeCustomerFunded})
	client := auxreq.NewClient(func() auxreq.ExecutorRunner { return ex })
	stream, err := client.Stream(ctx, auxiliary.Request{
		Role:        "proxy_memory_phase1",
		Visibility:  "private",
		SessionMode: auxiliary.SessionModeDetached,
		Call: &aipapi.Call{
			Route:    aipapi.RouteIntent{Selector: "backend:aux-model"},
			Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("auxiliary request")}}},
		},
	})
	if err == nil {
		_, _ = aipapi.Collect(ctx, stream)
		t.Fatal("a typed-nil durable holder was admitted")
	}
	if got := opens.Load(); got != 0 {
		t.Fatalf("the refused background work opened the backend %d times, want zero", got)
	}
	if got := observation.refusalReason(); got != ReasonSponsorshipHandoffNotReady {
		t.Fatalf("the refusal reason = %q, want %q", got, ReasonSponsorshipHandoffNotReady)
	}
}
