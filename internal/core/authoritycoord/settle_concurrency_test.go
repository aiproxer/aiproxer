package authoritycoord_test

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
)

func TestRequestCoordinator_ConcurrentSettle_InvokesEachProviderOnce(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	release := make(chan struct{})
	var inFlight atomic.Int32
	var overlap atomic.Bool
	var enterOnce sync.Once
	p := &fakeRequestProvider{id: "p"}
	p.settle = func(_ context.Context, in authority.RequestSettlement) (authority.Settlement, error) {
		if inFlight.Add(1) != 1 {
			overlap.Store(true)
		}
		enterOnce.Do(func() { close(entered) })
		<-release
		inFlight.Add(-1)
		return authority.OwnedFinalSettlement(in.Handles), nil
	}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "p", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: p, Strength: authority.StrengthRequired},
		},
		CleanupTimeout: time.Second,
	}
	d, err := coord.Admit(context.Background(), validRequestAdmission())
	if err != nil {
		t.Fatal(err)
	}
	settleIn := authority.RequestSettlement{RequestID: "req-1", Handles: d.Stack.Handles()}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		wg.Go(func() {
			<-start
			errs <- coord.Settle(context.Background(), d.Stack, settleIn)
		})
	}
	close(start)
	<-entered
	for d.Stack.SettleWaitingCount("p") < 1 {
		runtime.Gosched()
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("settle err=%v", err)
		}
	}
	if overlap.Load() {
		t.Fatal("provider settle overlapped under concurrent Settle")
	}
	if p.settled.Load() != 1 {
		t.Fatalf("settled=%d want 1 (single invoke under concurrent Settle)", p.settled.Load())
	}
}

func TestRequestCoordinator_ConcurrentSettle_WaiterObservesFailureThenRetry(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	var enterOnce sync.Once
	p := &fakeRequestProvider{id: "p"}
	p.settle = func(_ context.Context, in authority.RequestSettlement) (authority.Settlement, error) {
		n := calls.Add(1)
		if n == 1 {
			enterOnce.Do(func() { close(entered) })
			<-release
			return authority.Settlement{}, errors.New("transient")
		}
		return authority.OwnedFinalSettlement(in.Handles), nil
	}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "p", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: p, Strength: authority.StrengthRequired},
		},
		CleanupTimeout: time.Second,
	}
	d, err := coord.Admit(context.Background(), validRequestAdmission())
	if err != nil {
		t.Fatal(err)
	}
	settleIn := authority.RequestSettlement{RequestID: "req-1", Handles: d.Stack.Handles()}

	errCh := make(chan error, 2)
	go func() { errCh <- coord.Settle(context.Background(), d.Stack, settleIn) }()
	<-entered
	go func() { errCh <- coord.Settle(context.Background(), d.Stack, settleIn) }()
	for d.Stack.SettleWaitingCount("p") < 1 {
		runtime.Gosched()
	}
	close(release)
	var sawFail int
	for range 2 {
		if err := <-errCh; err == nil {
			t.Fatal("concurrent callers must observe the in-flight failure")
		} else {
			sawFail++
		}
	}
	if sawFail != 2 {
		t.Fatalf("want 2 failures, got %d", sawFail)
	}
	if p.settled.Load() != 1 {
		t.Fatalf("first wave settled calls=%d want 1", p.settled.Load())
	}
	if err := coord.Settle(context.Background(), d.Stack, settleIn); err != nil {
		t.Fatalf("retry after failure: %v", err)
	}
	if p.settled.Load() != 2 {
		t.Fatalf("retry settled=%d want 2", p.settled.Load())
	}
}

func TestPhase2_RejectsHandleOnlyReservationAmount(t *testing.T) {
	t.Parallel()
	p := &fakeRequestProvider{id: "p"}
	p.admit = func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
		return authority.Decision{
			Kind:       authority.DecisionAllow,
			ProviderID: "p",
			Reservations: []authority.Reservation{{
				Handle: "bare",
				Kind:   authority.ReservationQuota,
			}},
		}, nil
	}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "p", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: p, Strength: authority.StrengthRequired},
		},
	}
	_, err := coord.Admit(context.Background(), validRequestAdmission())
	if err == nil {
		t.Fatal("handle-only reservation must fail req 4.3")
	}
	if _, ok := errors.AsType[*authoritycoord.UnavailableError](err); !ok {
		t.Fatalf("want UnavailableError, got %T %v", err, err)
	}
}
