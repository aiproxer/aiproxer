package runtime

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	coreterm "github.com/aiproxer/aiproxer/internal/core/terminal"
	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	terminalworkapp "github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/internal/infra/terminalwork/workstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// Phase 4.5 RED/GREEN: truthful live state, durable post-output recovery, privacy
// (requirements 7.4, 7.7, 7.8, 8.3, 8.7–8.9, 12.1–12.8; design D8, D9, D14).

type countingSettleProvider struct {
	id          string
	settleErr   error
	settleCalls atomic.Int32
	invokeOK    atomic.Bool
}

func (p *countingSettleProvider) AdmitRequest(context.Context, authority.RequestAdmission) (authority.Decision, error) {
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

func (p *countingSettleProvider) SettleRequest(_ context.Context, in authority.RequestSettlement) (authority.Settlement, error) {
	p.settleCalls.Add(1)
	if p.settleErr != nil {
		return authority.Settlement{}, p.settleErr
	}
	return authority.OwnedFinalSettlement(in.Handles), nil
}

func (p *countingSettleProvider) ReleaseRequest(context.Context, authority.RequestRelease) error {
	return nil
}

func (p *countingSettleProvider) ProviderID() string { return p.id }
func (p *countingSettleProvider) SupportedKinds() []sdk.WorkKind {
	return []sdk.WorkKind{sdk.WorkKindSettleRequestProvider}
}
func (p *countingSettleProvider) Version() string { return "1" }
func (p *countingSettleProvider) Invoke(context.Context, terminalwork.WorkRecord, string) error {
	if !p.invokeOK.Load() {
		return terminalworkapp.ErrProviderOutage
	}
	p.settleCalls.Add(1)
	return nil
}

type releaseFailProvider struct {
	inner      *settleRecordingRequestProvider
	releaseErr error
}

func (p *releaseFailProvider) AdmitRequest(ctx context.Context, in authority.RequestAdmission) (authority.Decision, error) {
	return p.inner.AdmitRequest(ctx, in)
}

func (p *releaseFailProvider) SettleRequest(ctx context.Context, in authority.RequestSettlement) (authority.Settlement, error) {
	return p.inner.SettleRequest(ctx, in)
}

func (p *releaseFailProvider) ReleaseRequest(context.Context, authority.RequestRelease) error {
	return p.releaseErr
}

type phase45Clock struct{ t time.Time }

func (c phase45Clock) Now() time.Time { return c.t }

func TestPhase45_SettleFailureAcceptsDurableIntentAndKeepsLiveStateOpen(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{
		StoreID: "phase45-settle-intent",
		Now:     func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	intents := terminalworkapp.NewIntentService(store, terminalworkapp.IntentServiceConfig{
		Clock: func() time.Time { return clock },
	})
	secret := "SUPER_SECRET_PROMPT_CONTENT_xyz"
	prov := &countingSettleProvider{id: "quota", settleErr: errors.New("settle boom: " + secret)}
	ex := &Executor{AccountingRuntime: AccountingRuntime{
		RequestCoordinator: &authoritycoord.RequestCoordinator{
			Slots: []authoritycoord.RequestSlot{{
				ID: "quota", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: prov, Strength: authority.StrengthRequired,
			}},
		},
		TerminalWork: intents,
	}}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-45-a", "a-1", "trace-45-a", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if err := ex.settleRequestAuthority(ctx, nil); !errors.Is(err, terminalworkapp.ErrDurablePending) {
		t.Fatalf("settle got %v want ErrDurablePending", err)
	}
	st := requestAuthorityFrom(ctx)
	if st == nil {
		t.Fatal("expected request authority state")
	}
	if st.Settled || st.Released {
		t.Fatalf("failure must not mark settled/released; settled=%v released=%v", st.Settled, st.Released)
	}
	page, err := store.List(context.Background(), workstore.Query{
		RequestID: "req-45-a",
		States:    []sdk.WorkState{sdk.WorkStatePending, sdk.WorkStateIntent, sdk.WorkStateRetry},
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Records) != 1 {
		t.Fatalf("durable work rows=%d want 1 (failure must not disappear)", len(page.Records))
	}
	rec := page.Records[0]
	if rec.Kind != sdk.WorkKindSettleRequestProvider {
		t.Fatalf("kind=%s", rec.Kind)
	}
	if rec.Error.Code == "" {
		t.Fatal("expected bounded error code on durable work")
	}
	blob := rec.Error.Code + rec.Error.Message + string(rec.Payload) + rec.SourceKey.Key
	if strings.Contains(blob, secret) {
		t.Fatalf("privacy: raw settle error/content leaked into durable work: %q", blob)
	}
}

func TestPhase45_ReleaseFailureDoesNotMarkReleasedWithoutDurableIntent(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 7, 18, 4, 5, 0, 0, time.UTC)
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{
		StoreID: "phase45-release-intent",
		Now:     func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	intents := terminalworkapp.NewIntentService(store, terminalworkapp.IntentServiceConfig{
		Clock: func() time.Time { return clock },
	})
	inner := &settleRecordingRequestProvider{id: "quota"}
	failRelease := &releaseFailProvider{inner: inner, releaseErr: errors.New("lease release failed")}
	ex := &Executor{AccountingRuntime: AccountingRuntime{
		RequestCoordinator: &authoritycoord.RequestCoordinator{
			Slots: []authoritycoord.RequestSlot{{
				ID: "quota", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: failRelease, Strength: authority.StrengthRequired,
			}},
		},
		TerminalWork: intents,
	}}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-45-rel", "a-1", "trace-45-rel", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if err := ex.releaseRequestAuthority(ctx); !errors.Is(err, terminalworkapp.ErrDurablePending) {
		t.Fatalf("release got %v want ErrDurablePending", err)
	}
	st := requestAuthorityFrom(ctx)
	if st == nil {
		t.Fatal("expected request authority state")
	}
	if st.Released {
		t.Fatal("release failure must not mark Released until success or durable intent accepted")
	}
	page, err := store.List(context.Background(), workstore.Query{
		RequestID: "req-45-rel",
		Kind:      sdk.WorkKindReleaseRequestProvider,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Records) != 1 {
		t.Fatalf("expected durable release intent, got %d", len(page.Records))
	}
}

func TestPhase45_PostOutputFailurePreservesOutputAndConvergesAfterRestart(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 7, 18, 4, 10, 0, 0, time.UTC)
	state := workstore.NewMemoryState()
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{
		StoreID: "phase45-restart",
		State:   state,
		Now:     func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	intents := terminalworkapp.NewIntentService(store, terminalworkapp.IntentServiceConfig{
		Clock: func() time.Time { return clock },
	})
	prov := &countingSettleProvider{id: "quota", settleErr: errors.New("db outage")}
	ex := &Executor{AccountingRuntime: AccountingRuntime{
		RequestCoordinator: &authoritycoord.RequestCoordinator{
			Slots: []authoritycoord.RequestSlot{{
				ID: "quota", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: prov, Strength: authority.StrengthRequired,
			}},
		},
		TerminalWork: intents,
	}}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-45-restart", "a-1", "trace-45-restart", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}

	term := newStreamTerminal(sdk.ScopeRequest)
	r := term.Terminalize(context.Background(), sdk.CommandEOF, func() coreterm.AccumulatorSnapshot {
		return coreterm.NewAccumulatorSnapshot([]byte(`{"e":1}`), true)
	}, func(context.Context, coreterm.Outcome) error {
		_ = ex.settleRequestAuthority(ctx, nil)
		st := requestAuthorityFrom(ctx)
		if st.Settled || st.Released {
			return errors.New("live state incorrectly marked complete")
		}
		return terminalworkapp.ErrDurablePending
	})
	if !r.Outcome.Snapshot.OutputCommitted() {
		t.Fatal("output commitment must be preserved on post-output failure")
	}
	if r.State != sdk.StateWorkPending && r.State != sdk.StateReleasePending {
		t.Fatalf("owner state=%q want work_pending or release_pending after durable intent", r.State)
	}

	page, err := store.List(context.Background(), workstore.Query{RequestID: "req-45-restart", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) == 0 {
		t.Fatal("durable work must survive after live request path returns")
	}

	// Process restart: authority effect adapter decodes payload and calls SettleRequest.
	prov.settleErr = nil
	prov.invokeOK.Store(true)
	effect, err := terminalworkapp.NewAuthorityRequestEffectProvider(terminalworkapp.AuthorityRequestEffectConfig{
		ProviderID: "quota",
		Provider:   prov,
		Version:    "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	reg := terminalworkapp.NewRegistry()
	if err := reg.Register(effect); err != nil {
		t.Fatal(err)
	}
	proc, err := terminalworkapp.NewProcessor(store, reg, terminalworkapp.Config{
		OwnerID:    "restart-worker",
		ClaimTTL:   time.Minute,
		ClaimLimit: 10,
		Clock:      phase45Clock{t: clock},
	})
	if err != nil {
		t.Fatal(err)
	}
	before := prov.settleCalls.Load()
	if err := proc.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}
	if prov.settleCalls.Load() <= before {
		t.Fatal("processor must invoke authority SettleRequest via effect adapter")
	}
	page, err = store.List(context.Background(), workstore.Query{
		RequestID: "req-45-restart",
		State:     sdk.WorkStateCompleted,
		Limit:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) == 0 {
		t.Fatal("work must converge to completed after processor restart")
	}
	after := prov.settleCalls.Load()
	if err := proc.ProcessDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if prov.settleCalls.Load() != after {
		t.Fatalf("completed provider must not be repeated; calls before=%d after=%d", after, prov.settleCalls.Load())
	}
}
