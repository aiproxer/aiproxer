package runtime_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestExecutor_generationReloadPinsInFlightTurnAndReinterpretsNewTurn(t *testing.T) {
	t.Parallel()
	oldAliases, err := routing.NewAliasResolver([]routing.ModelAliasRule{
		{Pattern: `^cheap$`, Replacement: "oldbe:m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	newAliases, err := routing.NewAliasResolver([]routing.ModelAliasRule{
		{Pattern: `^cheap$`, Replacement: "newbe:m"},
	})
	if err != nil {
		t.Fatal(err)
	}

	oldCap := &routeOpenCapture{}
	oldEx, st := routePlanLifetimeExecutor(t, map[string]execbackend.Backend{
		"clientbe": overrideStreamingBackend(oldCap, "clientbe"),
		"oldbe":    overrideStreamingBackend(oldCap, "oldbe"),
		"newbe":    overrideStreamingBackend(oldCap, "newbe"),
	})
	oldEx.SelectorAliases = oldAliases
	seed := seedOverrideALeg(t, oldEx, st, "ov-reload-pin", "cheap")
	resetRouteOpenCapture(oldCap)
	oldCap.entered = make(chan struct{}, 1)
	oldCap.hold = make(chan struct{})
	var releaseHold sync.Once
	releaseHoldFn := func() { releaseHold.Do(func() { close(oldCap.hold) }) }
	defer releaseHoldFn()

	newCap := &routeOpenCapture{}
	newEx := runtime.TestExecutor()
	newEx.Store = st
	newEx.Bus = oldEx.Bus
	newEx.Rand = routing.NewSeededRng(1)
	newEx.MaxAttempts = oldEx.MaxAttempts
	newEx.Backends = map[string]execbackend.Backend{
		"clientbe": overrideStreamingBackend(newCap, "clientbe"),
		"oldbe":    overrideStreamingBackend(newCap, "oldbe"),
		"newbe":    overrideStreamingBackend(newCap, "newbe"),
	}
	newEx.SelectorAliases = newAliases
	newEx.RouteOverrideReader = st
	newEx.SecureSession = oldEx.SecureSession
	newEx.SessionDenialMapper = oldEx.SessionDenialMapper
	newEx.SyntheticLocalPrincipal = true

	barrier := runtime.NewRouteAuthoritySnapshotBarrier()
	defer barrier.Release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = runtime.WithRouteAuthoritySnapshotBarrier(ctx, barrier)
	turn := resumeOverrideCall(seed, overrideClientSelector)
	done := make(chan error, 1)
	var stream aipapi.EventStream
	go func() {
		s, err := oldEx.Execute(ctx, turn)
		stream = s
		done <- err
	}()
	waitOverrideBarrier(t, barrier, cancel, done)

	snap, err := st.Snapshot(context.Background(), seed.Session.ALegID)
	if err != nil || !snap.Active || snap.Selector != "cheap" {
		cancel()
		barrier.Release()
		releaseHoldFn()
		<-done
		t.Fatalf("persisted override: %+v err=%v", snap, err)
	}

	barrier.Release()
	waitHeldRouteOpens(t, oldCap, []string{"oldbe"}, cancel, releaseHoldFn, done)
	held := oldCap.snapshot()
	if len(held) != 1 || held[0].backend != "oldbe" || held[0].selector != "cheap" {
		releaseHoldFn()
		cancel()
		<-done
		t.Fatalf("in-flight old generation: %+v want oldbe from alias cheap", held)
	}

	collectExecute(t, newEx, context.Background(), resumeOverrideCall(seed, overrideClientSelector))
	newOpens := newCap.snapshot()
	if len(newOpens) != 1 || newOpens[0].backend != "newbe" || newOpens[0].selector != "cheap" {
		releaseHoldFn()
		cancel()
		<-done
		t.Fatalf("new generation same revision: %+v want newbe from alias cheap", newOpens)
	}

	releaseHoldFn()
	if err := <-done; err != nil {
		t.Fatalf("old-generation execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("old-generation collect: %v", err)
	}
	oldOpens := oldCap.snapshot()
	if len(oldOpens) != 1 || oldOpens[0].backend != "oldbe" {
		t.Fatalf("old generation must stay pinned after new turn: %+v", oldOpens)
	}
	again, err := st.Snapshot(context.Background(), seed.Session.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Revision != snap.Revision || again.Selector != snap.Selector {
		t.Fatalf("override state must not be copied/lost across generation change: old=%+v new=%+v", snap, again)
	}
}

func TestExecutor_brokenAliasAfterReloadFailsPlanningWithoutClientFallback(t *testing.T) {
	t.Parallel()
	oldAliases, err := routing.NewAliasResolver([]routing.ModelAliasRule{
		{Pattern: `^cheap$`, Replacement: "oldbe:m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cap := &routeOpenCapture{}
	oldEx, st := routePlanLifetimeExecutor(t, map[string]execbackend.Backend{
		"clientbe": overrideStreamingBackend(cap, "clientbe"),
		"oldbe":    overrideStreamingBackend(cap, "oldbe"),
	})
	oldEx.SelectorAliases = oldAliases
	seed := seedOverrideALeg(t, oldEx, st, "ov-reload-broken", "cheap")
	resetRouteOpenCapture(cap)
	collectExecute(t, oldEx, context.Background(), resumeOverrideCall(seed, overrideClientSelector))
	if got := cap.snapshot(); len(got) != 1 || got[0].backend != "oldbe" {
		t.Fatalf("pre-break turn: %+v", got)
	}

	broken := runtime.TestExecutor()
	broken.Store = st
	broken.Bus = oldEx.Bus
	broken.Rand = routing.NewSeededRng(1)
	broken.MaxAttempts = oldEx.MaxAttempts
	broken.Backends = oldEx.Backends
	broken.RouteOverrideReader = st
	broken.SecureSession = oldEx.SecureSession
	broken.SessionDenialMapper = oldEx.SessionDenialMapper
	broken.SyntheticLocalPrincipal = true

	resetRouteOpenCapture(cap)
	_, execErr := broken.Execute(context.Background(), resumeOverrideCall(seed, overrideClientSelector))
	if execErr == nil {
		t.Fatal("broken alias must fail route planning")
	}
	if cap.count.Load() != 0 {
		t.Fatalf("must not fall back to client selector, opens=%+v", cap.snapshot())
	}
	if !errors.Is(execErr, aipapi.ErrUnresolvedModelOnlySelector) && !strings.Contains(execErr.Error(), "unresolved") && !strings.Contains(execErr.Error(), "invalid selector") && !strings.Contains(execErr.Error(), "selector") {
		t.Fatalf("want normal route-planning error, got %v", execErr)
	}
}
