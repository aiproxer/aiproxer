package runtime

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

func TestPhase45_PartialSettlePersistsOnlyFailedProviders(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 7, 18, 7, 0, 0, 0, time.UTC)
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{
		StoreID: "phase45-partial-settle",
		Now:     func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	intents := terminalworkapp.NewIntentService(store, terminalworkapp.IntentServiceConfig{
		Clock: func() time.Time { return clock },
	})
	ok := &countingSettleProvider{id: "ok"}
	fail := &countingSettleProvider{id: "fail", settleErr: errors.New("transient")}
	ex := &Executor{AccountingRuntime: AccountingRuntime{
		RequestCoordinator: &authoritycoord.RequestCoordinator{
			Slots: []authoritycoord.RequestSlot{
				{ID: "ok", Class: authoritycoord.PriorityCreditWallet, Provider: ok, Strength: authority.StrengthRequired},
				{ID: "fail", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: fail, Strength: authority.StrengthRequired},
			},
		},
		TerminalWork: intents,
	}}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-partial", "a-1", "tr-partial", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	err = ex.settleRequestAuthority(ctx, nil)
	if !errors.Is(err, terminalworkapp.ErrDurablePending) {
		t.Fatalf("got %v want ErrDurablePending", err)
	}
	st := requestAuthorityFrom(ctx)
	if st.Settled || st.Released {
		t.Fatalf("live flags must stay open; settled=%v released=%v", st.Settled, st.Released)
	}
	page, err := store.List(context.Background(), workstore.Query{
		RequestID: "req-partial",
		States:    []sdk.WorkState{sdk.WorkStatePending, sdk.WorkStateIntent, sdk.WorkStateRetry},
		Limit:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 1 {
		t.Fatalf("rows=%d want 1 (only failed provider)", len(page.Records))
	}
	if page.Records[0].ProviderID != "fail" {
		t.Fatalf("provider=%q want fail", page.Records[0].ProviderID)
	}
}

func TestPhase45_MultiProviderSettleFailurePersistsEachIndependently(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 7, 18, 7, 5, 0, 0, time.UTC)
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{
		StoreID: "phase45-multi-settle",
		Now:     func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	intents := terminalworkapp.NewIntentService(store, terminalworkapp.IntentServiceConfig{
		Clock: func() time.Time { return clock },
	})
	a := &countingSettleProvider{id: "a", settleErr: errors.New("a down")}
	b := &countingSettleProvider{id: "b", settleErr: errors.New("b down")}
	ex := &Executor{AccountingRuntime: AccountingRuntime{
		RequestCoordinator: &authoritycoord.RequestCoordinator{
			Slots: []authoritycoord.RequestSlot{
				{ID: "a", Class: authoritycoord.PriorityCreditWallet, Provider: a, Strength: authority.StrengthRequired},
				{ID: "b", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: b, Strength: authority.StrengthRequired},
			},
		},
		TerminalWork: intents,
	}}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-multi", "a-1", "tr-multi", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	err = ex.settleRequestAuthority(ctx, nil)
	if !errors.Is(err, terminalworkapp.ErrDurablePending) {
		t.Fatalf("got %v want ErrDurablePending", err)
	}
	page, err := store.List(context.Background(), workstore.Query{
		RequestID: "req-multi",
		States:    []sdk.WorkState{sdk.WorkStatePending, sdk.WorkStateIntent, sdk.WorkStateRetry},
		Limit:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 2 {
		t.Fatalf("rows=%d want 2 independent durable intents", len(page.Records))
	}
	seen := map[string]bool{}
	for _, rec := range page.Records {
		seen[rec.ProviderID] = true
	}
	if !seen["a"] || !seen["b"] {
		t.Fatalf("providers=%v want a and b", seen)
	}
}

func TestPhase45_PartialDurableAcceptReturnsPendingNotRejected(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 7, 18, 7, 8, 0, 0, time.UTC)
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{
		StoreID: "phase45-partial-accept",
		Now:     func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	selective := &selectiveIntentStore{ok: store, rejectProvider: "b"}
	intents := terminalworkapp.NewIntentService(selective, terminalworkapp.IntentServiceConfig{
		Clock: func() time.Time { return clock },
	})
	a := &countingSettleProvider{id: "a", settleErr: errors.New("a down")}
	b := &countingSettleProvider{id: "b", settleErr: errors.New("b down")}
	ex := &Executor{AccountingRuntime: AccountingRuntime{
		RequestCoordinator: &authoritycoord.RequestCoordinator{
			Slots: []authoritycoord.RequestSlot{
				{ID: "a", Class: authoritycoord.PriorityCreditWallet, Provider: a, Strength: authority.StrengthRequired},
				{ID: "b", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: b, Strength: authority.StrengthRequired},
			},
		},
		TerminalWork: intents,
	}}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-partial-accept", "a-1", "tr-partial-accept", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	err = ex.settleRequestAuthority(ctx, nil)
	if !errors.Is(err, terminalworkapp.ErrDurablePending) {
		t.Fatalf("got %v want ErrDurablePending", err)
	}
	if !errors.Is(err, terminalworkapp.ErrDurableIntentRejected) {
		t.Fatalf("got %v want also ErrDurableIntentRejected (partial)", err)
	}
	st := requestAuthorityFrom(ctx)
	if st.Settled || st.Released {
		t.Fatalf("live flags must stay open; settled=%v released=%v", st.Settled, st.Released)
	}
	settleErr := err
	page, listErr := store.List(context.Background(), workstore.Query{
		RequestID: "req-partial-accept",
		States:    []sdk.WorkState{sdk.WorkStatePending, sdk.WorkStateIntent, sdk.WorkStateRetry},
		Limit:     10,
	})
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(page.Records) != 1 || page.Records[0].ProviderID != "a" {
		t.Fatalf("rows=%v want single accepted provider a", page.Records)
	}
	term := newStreamTerminal(sdk.ScopeRequest)
	r := term.Terminalize(context.Background(), sdk.CommandEOF, func() coreterm.AccumulatorSnapshot {
		return coreterm.NewAccumulatorSnapshot([]byte(`{"e":1}`), true)
	}, func(context.Context, coreterm.Outcome) error {
		return settleErr
	})
	if r.State != sdk.StateWorkPending {
		t.Fatalf("state=%q want work_pending", r.State)
	}
	if !errors.Is(r.Err, terminalworkapp.ErrDurableIntentRejected) {
		t.Fatalf("Result.Err=%v want ErrDurableIntentRejected", r.Err)
	}
	if !errors.Is(r.Err, terminalworkapp.ErrDurablePending) {
		t.Fatalf("Result.Err=%v want ErrDurablePending", r.Err)
	}
}

func TestPhase45_DurableIntentAcceptFailureDoesNotClaimPending(t *testing.T) {
	t.Parallel()
	const secret = "ACCEPT_SECRET_PWD=hunter2"
	broken := &rejectingIntentStore{secret: secret}
	intents := terminalworkapp.NewIntentService(broken, terminalworkapp.IntentServiceConfig{})
	prov := &countingSettleProvider{id: "quota", settleErr: errors.New("settle boom")}
	ex := &Executor{AccountingRuntime: AccountingRuntime{
		RequestCoordinator: &authoritycoord.RequestCoordinator{
			Slots: []authoritycoord.RequestSlot{{
				ID: "quota", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: prov, Strength: authority.StrengthRequired,
			}},
		},
		TerminalWork: intents,
	}}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-reject", "a-1", "tr-reject", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	err = ex.settleRequestAuthority(ctx, nil)
	if err == nil || errors.Is(err, terminalworkapp.ErrDurablePending) {
		t.Fatalf("got %v want durable-intent rejection (not durable pending)", err)
	}
	if !errors.Is(err, terminalworkapp.ErrDurableIntentRejected) {
		t.Fatalf("got %v want ErrDurableIntentRejected", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(fmt.Sprintf("%v", err), secret) {
		t.Fatalf("total reject leaked store error: %v", err)
	}
	st := requestAuthorityFrom(ctx)
	if st.Settled || st.Released {
		t.Fatalf("must not mark complete without accepted durable intent; settled=%v released=%v", st.Settled, st.Released)
	}
	if broken.appends.Load() == 0 {
		t.Fatal("expected AcceptSettleFailure to be attempted")
	}
}

type selectiveIntentStore struct {
	ok             *workstore.MemoryStore
	rejectProvider string
}

func (s *selectiveIntentStore) AppendIntent(ctx context.Context, rec terminalwork.WorkRecord) error {
	if rec.ProviderID == s.rejectProvider {
		return errors.New("append rejected for provider")
	}
	return s.ok.AppendIntent(ctx, rec)
}

func (s *selectiveIntentStore) PromotePending(ctx context.Context, cmd terminalwork.PromotePendingCommand) error {
	return s.ok.PromotePending(ctx, cmd)
}

type rejectingIntentStore struct {
	appends atomic.Int32
	secret  string
}

func (s *rejectingIntentStore) AppendIntent(context.Context, terminalwork.WorkRecord) error {
	s.appends.Add(1)
	msg := "store unavailable"
	if s.secret != "" {
		msg = "store unavailable: " + s.secret
	}
	return errors.New(msg)
}

func (s *rejectingIntentStore) PromotePending(context.Context, terminalwork.PromotePendingCommand) error {
	return errors.New("unreachable")
}
