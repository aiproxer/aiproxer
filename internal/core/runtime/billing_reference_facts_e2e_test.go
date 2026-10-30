package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/infra/billingbinding"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkbilling "github.com/aiproxer/aiproxer/pkg/aipsdk/billing"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// This black-box regression drives a real request through the real executor
// with the real public binding adapter: admission stamps the declared
// ReferenceFacts onto the exposure, the runtime's terminal handoff generates
// and seals the call closure from those stamped refs, and version-2 carriage
// delivers the exact record. It is the F1 proof that a hand-built record (or a
// direct Identity() read) cannot provide.

const (
	factsE2EStore   = "store-facts-e2e"
	factsE2EAccount = "acct-facts-e2e"
	factsE2ECall    = "call-facts-e2e"
)

func factsE2EScope() scope.PrincipalScopeView {
	return scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("user-facts-e2e"),
		TenantID:    scope.Known("tenant-facts-e2e"),
		Origin:      scope.OriginClient,
	}
}

// factsE2ESink declares the optional version-2 carriage and records exactly the
// envelopes the adapter sends, acknowledging with a durable revision.
type factsE2ESink struct {
	mu        sync.Mutex
	envelopes []sdkbilling.TerminalEnvelope
}

func (*factsE2ESink) CarriesUsageRecords() {}

func (s *factsE2ESink) AppendTerminal(_ context.Context, env sdkbilling.TerminalEnvelope) (sdkbilling.TerminalAck, error) {
	if err := env.Validate(); err != nil {
		return sdkbilling.TerminalAck{}, err
	}
	s.mu.Lock()
	s.envelopes = append(s.envelopes, env.Clone())
	revision := uint64(len(s.envelopes))
	s.mu.Unlock()
	ack := sdkbilling.TerminalAck{StoreID: env.StoreID, EnvelopeID: env.EnvelopeID, Revision: revision}
	if err := ack.Validate(); err != nil {
		return sdkbilling.TerminalAck{}, err
	}
	return ack, nil
}

// callClosure returns the version-2 call-closure envelope, if one was carried.
func (s *factsE2ESink) callClosure() (sdkbilling.TerminalEnvelope, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, env := range s.envelopes {
		if env.Version == sdkbilling.TerminalEnvelopeVersionV2 && env.Record != nil && env.Subject.Kind == metering.SubjectBillingCall {
			return env, true
		}
	}
	return sdkbilling.TerminalEnvelope{}, false
}

// count reports how many envelopes were carried.
func (s *factsE2ESink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.envelopes)
}

// factsE2EQuoter declares the optional ReferenceFacts port with either
// timestamped distinct facts or intentionally absent facts.
type factsE2EQuoter struct {
	absent    bool
	effective time.Time
	fetched   time.Time
}

func (q *factsE2EQuoter) CustomerPricingRef(context.Context) economics.VersionRef {
	if q.absent {
		return economics.VersionRef{}
	}
	return economics.VersionRef{ID: "runtime-pricing", Version: "pricing-v9", EffectiveAt: q.effective, FetchedAt: q.fetched}
}

func (q *factsE2EQuoter) ChargePolicyRef(context.Context) economics.VersionRef {
	if q.absent {
		return economics.VersionRef{}
	}
	return economics.VersionRef{ID: "runtime-policy", Version: "policy-v4", EffectiveAt: q.effective, FetchedAt: q.fetched}
}

// OperatorRateRef keeps the full ReferenceFacts surface wired; this regression
// targets the request-level pricing/policy facts.
func (*factsE2EQuoter) OperatorRateRef(context.Context, string, string) economics.VersionRef {
	return economics.VersionRef{}
}

func (*factsE2EQuoter) Quote(_ context.Context, in economics.QuoteInput) (economics.ExposureQuote, error) {
	return economics.ExposureQuote{
		ID:           "quote-facts-e2e",
		Version:      1,
		Subject:      in.Subject.Clone(),
		Maximum:      economics.Money{NanoUnits: 7, Currency: "USD", Present: true},
		Tariff:       in.Tariff,
		Policy:       in.Policy,
		Completeness: economics.CompletenessComplete,
	}, nil
}

// factsE2EAdmitter admits the frozen quote into a call-scoped handle.
type factsE2EAdmitter struct {
	mu    sync.Mutex
	calls int
}

func (a *factsE2EAdmitter) Admit(_ context.Context, in sdkbilling.ExposureAdmissionInput) (sdkbilling.ExposureHandle, error) {
	if err := in.Validate(); err != nil {
		return sdkbilling.ExposureHandle{}, err
	}
	a.mu.Lock()
	a.calls++
	a.mu.Unlock()
	handle := sdkbilling.ExposureHandle{
		StoreID:       in.Subject.StoreID,
		BillingCallID: in.Subject.BillingCallID,
		ExposureID:    "exp-facts-e2e",
		QuoteID:       in.Quote.ID,
	}
	if err := handle.Validate(); err != nil {
		return sdkbilling.ExposureHandle{}, err
	}
	return handle, nil
}

// factsE2EScreen allows the trusted principal.
type factsE2EScreen struct{}

func (factsE2EScreen) Check(_ context.Context, in sdkbilling.CreditScreenInput) (sdkbilling.CreditScreenResult, error) {
	if err := in.Validate(); err != nil {
		return sdkbilling.CreditScreenResult{}, err
	}
	return sdkbilling.CreditScreenResult{Decision: sdkbilling.CreditAllow, StoreID: in.StoreID, AccountID: in.AccountID}, nil
}

func drainFactsE2E(t *testing.T, stream aipapi.EventStream) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for {
		_, err := stream.Recv(ctx)
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
	}
}

func TestBillingReferenceFactsReachRuntimeTerminalRecord(t *testing.T) {
	t.Parallel()
	effective := time.Date(2031, 5, 6, 7, 8, 9, 0, time.UTC)
	fetched := effective.Add(2 * time.Hour)
	for _, absent := range []bool{false, true} {
		t.Run(fmt.Sprintf("absent=%v", absent), func(t *testing.T) {
			t.Parallel()
			store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
			if err != nil {
				t.Fatal(err)
			}
			sink := &factsE2ESink{}
			admitter := &factsE2EAdmitter{}
			binding := sdkbilling.Binding{
				ID:      "binding-facts-e2e",
				Version: sdkbilling.BindingVersionV1,
				Scope: sdkbilling.BindingScope{
					StoreID: factsE2EStore,
					Tariff:  economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "tariff-facts-e2e", Version: "v1"}},
					Policy:  economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "policy-facts-e2e", Version: "v1"}, PolicyID: "policy-facts-e2e-v1"},
				},
				CreditScreen: factsE2EScreen{},
				Quoter:       &factsE2EQuoter{absent: absent, effective: effective, fetched: fetched},
				Admission:    admitter,
				Terminal:     sink,
			}
			adapter, err := billingbinding.NewAdapter(binding)
			if err != nil {
				t.Fatalf("NewAdapter: %v", err)
			}
			executor := runtime.NewExecutor(runtime.ExecutorConfig{
				Core: runtime.CoreRuntime{
					Store: store,
					Backends: map[string]execbackend.Backend{
						"backend": {
							Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
							Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
								return aipapi.NewFixedEventStream([]aipapi.Event{
									{Kind: aipapi.EventResponseStarted},
									{Kind: aipapi.EventMessageStarted},
									{Kind: aipapi.EventResponseFinished},
								}), nil
							},
						},
					},
					Rand: routing.NewSeededRng(11),
				},
				Billing: runtime.BillingRuntime{
					BillingCreditGate:        adapter,
					BillingExposureAdmission: adapter,
					TerminalUsageSink:        adapter,
					BillingIdentity:          adapter.Identity(),
				},
				Extension: runtime.ExtensionRuntime{Bus: hooks.New(hooks.Config{})},
			})
			ctx := scope.WithScope(context.Background(), factsE2EScope())
			maxOut := 2048
			call := &aipapi.Call{
				ID:       factsE2ECall,
				Route:    aipapi.RouteIntent{Selector: "backend:model"},
				Options:  aipapi.GenerationOptions{MaxOutputTokens: &maxOut},
				Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
			}
			stream, err := executor.Execute(ctx, call)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			drainFactsE2E(t, stream)
			_ = stream.Close()
			env, ok := sink.callClosure()
			if !ok {
				t.Fatalf("no version-2 call closure reached the declaring sink; carried=%d", sink.count())
			}
			var record usageevidence.CallUsageRecord
			if err := json.Unmarshal(env.Record.JSON, &record); err != nil {
				t.Fatalf("decode carried call record: %v", err)
			}
			if absent {
				if record.CustomerPricingRef != (usageevidence.VersionRef{}) || record.ChargePolicyRef != (usageevidence.VersionRef{}) {
					t.Fatalf("absent facts must stay the zero ref in the generated record, got %+v/%+v",
						record.CustomerPricingRef, record.ChargePolicyRef)
				}
				return
			}
			wantPricing := usageevidence.VersionRef{ID: "runtime-pricing", Version: "pricing-v9", EffectiveAt: effective, FetchedAt: fetched}
			wantPolicy := usageevidence.VersionRef{ID: "runtime-policy", Version: "policy-v4", EffectiveAt: effective, FetchedAt: fetched}
			if record.CustomerPricingRef != wantPricing {
				t.Fatalf("generated call pricing ref = %+v, want the declared fact %+v", record.CustomerPricingRef, wantPricing)
			}
			if record.ChargePolicyRef != wantPolicy {
				t.Fatalf("generated call policy ref = %+v, want the declared fact %+v", record.ChargePolicyRef, wantPolicy)
			}
			if strings.Contains(record.CustomerPricingRef.ID, "tariff-facts-e2e") || strings.Contains(record.ChargePolicyRef.ID, "policy-facts-e2e") {
				t.Fatalf("scope snapshots leaked into the terminal facts: %+v/%+v", record.CustomerPricingRef, record.ChargePolicyRef)
			}
		})
	}
}
