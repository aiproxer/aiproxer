package runtimebundle_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	terminalworkapp "github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/terminalwork/workstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// flipSettleProvider is a real RequestProvider (no EffectProvider.Invoke).
type flipSettleProvider struct {
	id          string
	ver         string
	settleErr   error
	settleCalls atomic.Int32
}

func (p *flipSettleProvider) AdmitRequest(context.Context, authority.RequestAdmission) (authority.Decision, error) {
	return authority.Decision{
		Kind: authority.DecisionAllow,
		Reservations: []authority.Reservation{{
			Handle: p.id + "-h",
			Kind:   authority.ReservationQuota,
			Quantity: &metering.Quantity{
				Component: metering.ComponentInputToken,
				Unit:      metering.UnitToken,
				Value:     1,
				Present:   true,
			},
		}},
	}, nil
}

func (p *flipSettleProvider) SettleRequest(_ context.Context, in authority.RequestSettlement) (authority.Settlement, error) {
	p.settleCalls.Add(1)
	if p.settleErr != nil {
		return authority.Settlement{}, p.settleErr
	}
	return authority.OwnedFinalSettlement(in.Handles), nil
}

func (p *flipSettleProvider) ReleaseRequest(context.Context, authority.RequestRelease) error {
	return nil
}

func (p *flipSettleProvider) Version() string { return p.ver }

type overrideInvokeStub struct {
	id          string
	invokeCalls atomic.Int32
}

func (p *overrideInvokeStub) ProviderID() string { return p.id }
func (p *overrideInvokeStub) Version() string    { return "9" }
func (p *overrideInvokeStub) SupportedKinds() []sdk.WorkKind {
	return []sdk.WorkKind{sdk.WorkKindSettleRequestProvider, sdk.WorkKindReleaseRequestProvider}
}

func (p *overrideInvokeStub) Invoke(context.Context, terminalwork.WorkRecord, string) error {
	p.invokeCalls.Add(1)
	return nil
}

func TestPhase45_BuildDerivesEffectProvidersFromRequestRegistrations(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 7, 18, 9, 0, 0, 0, time.UTC)
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{
		StoreID: "phase45-compose",
		Now:     func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	prov := &flipSettleProvider{id: "quota", ver: "v-compose", settleErr: errors.New("inline settle outage")}
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t)
	opts.Testing.Clock = func() time.Time { return clock }
	opts.Production = runtimebundle.ProductionOptions{
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: authority.ProviderDescriptor{
				ID:   "quota",
				Kind: authority.ProviderKindAuthority,
				Postures: []authority.StagePosture{{
					Stage:           authority.StageRequestAdmit,
					Strength:        authority.StrengthRequired,
					FailureBehavior: authority.FailureFailClosed,
				}},
			},
			Priority: authority.RequestPriorityQuotaBudgetRate,
			Provider: prov,
		}},
		TerminalWorkStore:        store,
		TerminalWorkOwnerID:      "compose-worker",
		TerminalWorkTickInterval: time.Hour,
		TerminalWorkClaimLimit:   10,
	}
	_, built := mustProcessAndCandidate(t, cfg, opts)
	if runtimebundle.CandidateTerminalWorkProcessor(built) == nil || !runtimebundle.CandidateTerminalWorkProcessor(built).Running() {
		t.Fatal("processor must auto-start")
	}
	if _, err := prov.SettleRequest(context.Background(), authority.RequestSettlement{
		RequestID: "req-compose",
		Handles:   []string{"quota-h"},
	}); err == nil {
		t.Fatal("inline settle must fail while unhealthy")
	}
	if err := built.Executor().TerminalWork.AcceptSettleFailure(context.Background(), terminalworkapp.SettleFailureInput{
		RequestID:  "req-compose",
		AttemptID:  "a-1",
		TraceID:    "tr-compose",
		ProviderID: "quota",
		Handles:    []string{"quota-h"},
		Versions:   terminalwork.BoundVersions{GenerationID: "g1", ProviderID: "quota"},
	}); err != nil {
		t.Fatal(err)
	}
	page, err := store.List(context.Background(), workstore.Query{
		RequestID: "req-compose",
		States:    []sdk.WorkState{sdk.WorkStatePending, sdk.WorkStateIntent, sdk.WorkStateRetry},
		Limit:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) == 0 {
		t.Fatal("durable pending row required after settle failure accept")
	}
	before := prov.settleCalls.Load()
	prov.settleErr = nil
	if err := runtimebundle.CandidateTerminalWorkProcessor(built).ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if prov.settleCalls.Load() <= before {
		t.Fatal("ProcessDue must invoke real RequestProvider.SettleRequest via derived adapter (not stub Invoke)")
	}
	done, err := store.List(context.Background(), workstore.Query{
		RequestID: "req-compose",
		State:     sdk.WorkStateCompleted,
		Limit:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(done.Records) == 0 {
		t.Fatal("work must complete after healthy ProcessDue")
	}
}

func TestPhase45_ExplicitTerminalWorkProviderOverridesDerived(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 7, 18, 9, 5, 0, 0, time.UTC)
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{
		StoreID: "phase45-override",
		Now:     func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	prov := &flipSettleProvider{id: "quota", ver: "v-derived", settleErr: nil}
	stub := &overrideInvokeStub{id: "quota"}
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t)
	opts.Testing.Clock = func() time.Time { return clock }
	opts.Production = runtimebundle.ProductionOptions{
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: authority.ProviderDescriptor{
				ID:   "quota",
				Kind: authority.ProviderKindAuthority,
				Postures: []authority.StagePosture{{
					Stage:           authority.StageRequestAdmit,
					Strength:        authority.StrengthRequired,
					FailureBehavior: authority.FailureFailClosed,
				}},
			},
			Priority: authority.RequestPriorityQuotaBudgetRate,
			Provider: prov,
		}},
		TerminalWorkStore: store,
		TerminalWorkProviders: []terminalworkapp.EffectProvider{
			stub,
		},
		TerminalWorkOwnerID:      "override-worker",
		TerminalWorkTickInterval: time.Hour,
		TerminalWorkClaimLimit:   10,
	}
	_, built := mustProcessAndCandidate(t, cfg, opts)
	if err := built.Executor().TerminalWork.AcceptSettleFailure(context.Background(), terminalworkapp.SettleFailureInput{
		RequestID:  "req-override",
		AttemptID:  "a-1",
		ProviderID: "quota",
		Handles:    []string{"h1"},
		Versions:   terminalwork.BoundVersions{GenerationID: "g1", ProviderID: "quota"},
	}); err != nil {
		t.Fatal(err)
	}
	settleBefore := prov.settleCalls.Load()
	if err := runtimebundle.CandidateTerminalWorkProcessor(built).ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if stub.invokeCalls.Load() < 1 {
		t.Fatal("explicit TerminalWorkProviders must override derived adapter (Invoke used)")
	}
	if prov.settleCalls.Load() != settleBefore {
		t.Fatal("derived SettleRequest must not run when explicit override wins")
	}
}
