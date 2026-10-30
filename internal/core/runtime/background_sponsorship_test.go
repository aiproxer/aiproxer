package runtime

import (
	"context"
	"encoding/json"
	"maps"
	"strings"
	"sync"
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

// sponsorshipObservation records the executor's coarse observations and the
// optional background funding refusal counter. The refusal reason is a bounded
// enum: it never names a principal, an account or a sponsor.
type sponsorshipObservation struct {
	mu       sync.Mutex
	refusals []string
}

func (o *sponsorshipObservation) OnAttemptRecorded(aipapi.AttemptOutcome, string) {}

func (o *sponsorshipObservation) OnBackendOpenDuration(string, float64) {}

func (o *sponsorshipObservation) OnTransportNegotiation(aipapi.Operation, aipapi.TransportMode, string) {
}

func (o *sponsorshipObservation) OnCancellation(CancellationObservation) {}

func (o *sponsorshipObservation) OnBackgroundSponsorshipRefused(reason string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.refusals = append(o.refusals, reason)
}

func (o *sponsorshipObservation) refusalCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.refusals)
}

// refusalReason returns the recorded reason of the single refusal.
func (o *sponsorshipObservation) refusalReason() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.refusals) != 1 {
		return ""
	}
	return o.refusals[0]
}

// TestBackgroundFundingGate is the focused core seam of the operator-declared
// funding decision (design §2, Requirement 2.3):
//
//   - an operator who declared no funding mode has the background work
//     refused before any provider dispatch where a monetary binding is
//     bound, and the refusal is counted; without any monetary binding the
//     work runs usage-only;
//   - a customer-funded decision is admitted only on a host that carries a
//     complete monetary binding, and the customer credit screen and the
//     exposure admission run unchanged;
//   - an operator-sponsored decision runs the work as usage-only: the provider
//     is opened, the usage is metered, and no customer credit screen or
//     customer exposure runs;
//   - a sponsor key the operator did not declare is refused and counted.
//
// The seam is driven through the real auxiliary client and the real executor,
// so the funding decision is the production one. The core package may not
// import the Proxy Memory feature, so the role is named by its registered
// spelling.
func TestBackgroundFundingGate(t *testing.T) {
	principalID := "principal-background-funding"
	accountID := "acct:" + principalID
	for _, tc := range []struct {
		name           string
		declared       bool
		decl           execctx.Sponsorship
		monetary       bool
		authority      MonetaryAuthority
		admitted       bool
		callExtensions map[string]json.RawMessage
		wantScreens    int
		wantExposure   int
		// wantCustomerClosures is the number of durable terminal call
		// closures that carry a customer account.
		wantCustomerClosures int
		// wantReason is the bounded refusal reason the refusal counter
		// recorded. It proves the attempt was denied at the funding gate
		// itself, and not earlier on the way to it.
		wantReason string
	}{
		{
			name:         "no declaration leaves the work ungated",
			declared:     false,
			monetary:     true,
			admitted:     true,
			wantScreens:  1,
			wantExposure: 1,
			// The work was not submitted as sponsored background work, so
			// the ordinary customer path is untouched.
			wantCustomerClosures: 1,
		},
		{
			name:       "an undeclared funding mode is refused",
			declared:   true,
			decl:       execctx.Sponsorship{},
			monetary:   true,
			admitted:   false,
			wantReason: ReasonSponsorshipUndeclared,
		},
		{
			// The open-source build carries no monetary binding, so no
			// customer money can move: an undeclared mode runs usage-only,
			// exactly as background work did before the funding gate.
			name:     "an undeclared funding mode runs usage-only on a money free host",
			declared: true,
			decl:     execctx.Sponsorship{},
			monetary: false,
			admitted: true,
		},
		{
			name:       "customer funded needs a complete monetary host",
			declared:   true,
			decl:       execctx.Sponsorship{Mode: execctx.SponsorshipModeCustomerFunded},
			monetary:   false,
			admitted:   false,
			wantReason: ReasonSponsorshipMonetaryHostIncomplete,
		},
		{
			name:         "customer funded is admitted on a monetary host",
			declared:     true,
			decl:         execctx.Sponsorship{Mode: execctx.SponsorshipModeCustomerFunded},
			monetary:     true,
			authority:    MonetaryAuthorityExternalBinding,
			admitted:     true,
			wantScreens:  1,
			wantExposure: 1,
			// A customer funded leg closes with the customer account, so
			// the host's post-usage settlement can charge it.
			wantCustomerClosures: 1,
		},
		{
			// The negative construction case: every billing port is wired,
			// but nothing proved that a complete monetary authority stands
			// behind them. Four pointers are not a monetary host: the host
			// composition publishes the capability only where it validated a
			// complete binding, so a stock no-op or a partial default can
			// never open a provider.
			name:       "customer funded without a validated monetary authority is refused",
			declared:   true,
			decl:       execctx.Sponsorship{Mode: execctx.SponsorshipModeCustomerFunded},
			monetary:   true,
			admitted:   false,
			wantReason: ReasonSponsorshipMonetaryHostIncomplete,
		},
		{
			name:     "operator sponsored is admitted on a money free host",
			declared: true,
			decl:     execctx.Sponsorship{Mode: execctx.SponsorshipModeOperatorSponsored},
			monetary: false,
			admitted: true,
		},
		{
			name:     "operator sponsored is admitted on a monetary host",
			declared: true,
			decl:     execctx.Sponsorship{Mode: execctx.SponsorshipModeOperatorSponsored},
			monetary: true,
			admitted: true,
		},
		{
			name:     "a sponsor key the operator did not declare is refused",
			declared: true,
			decl: execctx.Sponsorship{
				Mode:                execctx.SponsorshipModeOperatorSponsored,
				SponsorKey:          "other-team",
				DeclaredSponsorKeys: []string{"memory-team"},
			},
			monetary:   true,
			admitted:   false,
			wantReason: ReasonSponsorshipSponsorUndeclared,
		},
		{
			// A canonical extension is client-controlled data: it can never
			// select the funding mode of a background operation.
			name:       "a canonical extension claiming a funding mode is refused",
			declared:   true,
			decl:       execctx.Sponsorship{},
			monetary:   true,
			admitted:   false,
			wantReason: ReasonSponsorshipUndeclared,
			callExtensions: map[string]json.RawMessage{
				"aip.billing.funding_mode": json.RawMessage(`"customer_funded"`),
				"aip.billing.account_id":   json.RawMessage(`"acct:client-chosen"`),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture := &reasoningCompressorCapture{}
			ex := newReasoningCompressorExecutor(t, capture, accountID)
			observation := &sponsorshipObservation{}
			ex.Metrics = observation
			var screens, exposures atomic.Int64
			ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error {
				screens.Add(1)
				return nil
			})
			ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in BillingExposureAdmissionInput) (billingport.CallExposure, error) {
				exposures.Add(1)
				return billingport.CallExposure{
					AccountID:       accountID,
					CallID:          in.CallID,
					PricingRef:      usageevidence.VersionRef{ID: "pricing:background-funding", Version: "1"},
					ChargePolicyRef: usageevidence.VersionRef{ID: "policy:background-funding", Version: "1"},
					Status:          billingport.ExposureOpen,
				}, nil
			})
			if !tc.monetary {
				// A money-free host carries no monetary binding: no credit
				// screen, no exposure admission, no account resolver.
				ex.BillingCreditGate = nil
				ex.BillingExposureAdmission = nil
				ex.BillingIdentity = BillingIdentity{}
			}
			if tc.authority != "" {
				// The host composition publishes the monetary-host capability
				// only where it validated a complete monetary authority, and
				// it states the durable terminal handoff only where the
				// terminal sink holds the obligation durably.
				ex.BillingIdentity.MonetaryHost = MonetaryHost{
					Authority:       tc.authority,
					TerminalHandoff: TerminalHandoffDurable,
				}
				ex.TerminalUsageSink = durableHandoffSink{
					TerminalUsageSink: ex.TerminalUsageSink,
					ready:             true,
				}
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
			ctx := scope.WithScope(context.Background(), scope.PrincipalScopeView{
				SubjectKind: scope.SubjectHuman,
				PrincipalID: scope.Known(principalID),
				Origin:      scope.OriginClient,
			})
			if tc.declared {
				ctx = execctx.WithSponsorship(ctx, tc.decl)
			}
			client := auxreq.NewClient(func() auxreq.ExecutorRunner { return ex })
			call := &aipapi.Call{
				Route:    aipapi.RouteIntent{Selector: "backend:aux-model"},
				Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("auxiliary request")}}},
			}
			if len(tc.callExtensions) > 0 {
				call.Extensions = map[string]json.RawMessage{}
				maps.Copy(call.Extensions, tc.callExtensions)
			}
			stream, err := client.Stream(ctx, auxiliary.Request{
				Role:        "proxy_memory_phase1",
				Visibility:  "private",
				SessionMode: auxiliary.SessionModeDetached,
				Call:        call,
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
			} else {
				if err == nil {
					_, _ = aipapi.Collect(ctx, stream)
					t.Fatal("the background work was admitted without a declared funding mode")
				}
				if got := opens.Load(); got != 0 {
					t.Fatalf("the refused background work opened the backend %d times, want zero", got)
				}
				if got := observation.refusalCount(); got != 1 {
					t.Fatalf("the refusal was counted %d times, want exactly one", got)
				}
				// The bounded reason proves the attempt was denied at the
				// funding gate itself, not earlier on the way to it.
				if got := observation.refusalReason(); got != tc.wantReason {
					t.Fatalf("the refusal reason = %q, want %q", got, tc.wantReason)
				}
			}
			if got := int(screens.Load()); got != tc.wantScreens {
				t.Fatalf("the customer credit screen ran %d times, want %d", got, tc.wantScreens)
			}
			if got := int(exposures.Load()); got != tc.wantExposure {
				t.Fatalf("the customer exposure admission ran %d times, want %d", got, tc.wantExposure)
			}
			// The durable terminal records, not a counter: an operator
			// sponsored leg is metered as a usage leg, and it never carries a
			// customer account, so the host's post-usage settlement can never
			// charge a customer for it. The provider cost is not dropped: it
			// rides the same leg record.
			calls, legs, _ := capture.snapshot()
			customerClosures := 0
			for _, c := range calls {
				if strings.TrimSpace(c.AccountID) != "" {
					customerClosures++
				}
			}
			if tc.wantCustomerClosures != customerClosures {
				t.Fatalf("the durable terminal records carry %d customer closures, want %d", customerClosures, tc.wantCustomerClosures)
			}
			if tc.admitted && len(legs) == 0 {
				t.Fatal("an admitted background leg recorded no usage")
			}
			if !tc.admitted && len(legs) != 0 {
				t.Fatalf("a refused background work recorded %d usage legs, want none", len(legs))
			}
		})
	}
}
