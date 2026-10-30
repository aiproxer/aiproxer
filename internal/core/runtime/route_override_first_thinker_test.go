package runtime_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestExecutor_firstOverrideObservesExistingWeightedFirstConsumed(t *testing.T) {
	t.Parallel()
	const firstSelector = "[first]cheapfirst:m^[weight=100]expensive:m"
	cases := []struct {
		name           string
		consumeFirst   bool
		wantFirstTurn  string
		wantSecondTurn string
	}{
		{name: "beforeConsumed", consumeFirst: false, wantFirstTurn: "cheapfirst", wantSecondTurn: "expensive"},
		{name: "afterConsumed", consumeFirst: true, wantFirstTurn: "expensive", wantSecondTurn: "expensive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cap := &routeOpenCapture{}
			ex, st := firstOverrideExecutor(t, cap)
			var seed *aipapi.Call
			if tc.consumeFirst {
				seed = seedOverrideALeg(t, ex, st, "ov-first-"+tc.name, "")
				resetRouteOpenCapture(cap)
				collectExecute(t, ex, context.Background(), resumeOverrideCall(seed, firstSelector))
				assertWeightedFirstConsumed(t, st, seed.Session.ALegID, true)
				if _, err := st.Replace(context.Background(), seed.Session.ALegID, firstSelector, time.Now().UTC()); err != nil {
					t.Fatalf("replace after consume: %v", err)
				}
			} else {
				seed = seedOverrideALeg(t, ex, st, "ov-first-"+tc.name, firstSelector)
				assertWeightedFirstConsumed(t, st, seed.Session.ALegID, false)
			}
			resetRouteOpenCapture(cap)
			turn := resumeOverrideCall(seed, overrideClientSelector)
			collectExecute(t, ex, context.Background(), turn)
			got := cap.snapshot()
			if len(got) != 1 || got[0].backend != tc.wantFirstTurn || got[0].selector != firstSelector {
				t.Fatalf("first overridden turn: %+v want %s", got, tc.wantFirstTurn)
			}
			assertWeightedFirstConsumed(t, st, seed.Session.ALegID, true)

			resetRouteOpenCapture(cap)
			collectExecute(t, ex, context.Background(), resumeOverrideCall(seed, overrideClientSelector))
			got = cap.snapshot()
			if len(got) != 1 || got[0].backend != tc.wantSecondTurn {
				t.Fatalf("second overridden turn: %+v want %s", got, tc.wantSecondTurn)
			}
		})
	}
}

func TestExecutor_setReplaceClearDoesNotResetWeightedFirstConsumed(t *testing.T) {
	t.Parallel()
	cap := &routeOpenCapture{}
	ex, st := firstOverrideExecutor(t, cap)
	const firstSelector = "[first]cheapfirst:m^[weight=100]expensive:m"
	seed := seedOverrideALeg(t, ex, st, "ov-first-preserve", "")
	collectExecute(t, ex, context.Background(), resumeOverrideCall(seed, firstSelector))
	assertWeightedFirstConsumed(t, st, seed.Session.ALegID, true)

	if _, err := st.Replace(context.Background(), seed.Session.ALegID, "adminbe:m", time.Now().UTC()); err != nil {
		t.Fatalf("replace: %v", err)
	}
	assertWeightedFirstConsumed(t, st, seed.Session.ALegID, true)

	if _, err := st.Replace(context.Background(), seed.Session.ALegID, firstSelector, time.Now().UTC()); err != nil {
		t.Fatalf("replace first selector: %v", err)
	}
	assertWeightedFirstConsumed(t, st, seed.Session.ALegID, true)

	if _, err := st.Clear(context.Background(), seed.Session.ALegID, time.Now().UTC()); err != nil {
		t.Fatalf("clear: %v", err)
	}
	assertWeightedFirstConsumed(t, st, seed.Session.ALegID, true)

	resetRouteOpenCapture(cap)
	collectExecute(t, ex, context.Background(), resumeOverrideCall(seed, firstSelector))
	got := cap.snapshot()
	if len(got) != 1 || got[0].backend != "expensive" {
		t.Fatalf("after clear, consumed [first] must keep weighted arm: %+v", got)
	}
}

func TestExecutor_thinkerOverrideUsesExistingALegMemoAndDoesNotResetOnMutate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		visible      bool
		existingMemo bool
	}{
		{name: "hiddenNoMemo", visible: false},
		{name: "visibleNoMemo", visible: true},
		{name: "hiddenExistingMemo", visible: false, existingMemo: true},
		{name: "visibleExistingMemo", visible: true, existingMemo: true},
	}
	const thinkerSel = "[thinker]thinker-be:m^exec-be:m"
	const otherSel = "[thinker]thinker2-be:m^exec2-be:m"
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cap := &routeOpenCapture{}
			ex, st := thinkerOverrideExecutor(t, cap, tc.visible)
			session := seedThinkerSession(t, ex, st, tc.existingMemo, "ov-think-"+tc.name)
			if tc.existingMemo {
				assertThinkerMemoPresent(t, ex, st, session.Session.ALegID)
				assertThinkerAndExecOpens(t, cap.snapshot(), thinkerSel, "thinker-be", "exec-be")
			}
			before, err := st.FetchInterleavedState(context.Background(), session.Session.ALegID)
			if err != nil {
				t.Fatalf("fetch interleaved: %v", err)
			}

			if _, err := st.Replace(context.Background(), session.Session.ALegID, thinkerSel, time.Now().UTC()); err != nil {
				t.Fatalf("replace: %v", err)
			}
			afterSet, err := st.FetchInterleavedState(context.Background(), session.Session.ALegID)
			if err != nil {
				t.Fatal(err)
			}
			if !afterSet.Equal(before) {
				t.Fatalf("set must not mutate interleaved state: before=%+v after=%+v", before, afterSet)
			}

			resetRouteOpenCapture(cap)
			turn := interleavedBaseCall(overrideClientSelector)
			resumeInterleavedCall(session, turn)
			collectExecute(t, ex, context.Background(), turn)
			assertOverrideSelectorOnOpens(t, cap.snapshot(), thinkerSel)
			if tc.existingMemo {
				assertThinkerMemoPresent(t, ex, st, session.Session.ALegID)
			}

			if _, err := st.FetchInterleavedState(context.Background(), session.Session.ALegID); err != nil {
				t.Fatal(err)
			}
			if _, err := st.Replace(context.Background(), session.Session.ALegID, otherSel, time.Now().UTC()); err != nil {
				t.Fatalf("replace other: %v", err)
			}
			if _, err := st.Clear(context.Background(), session.Session.ALegID, time.Now().UTC()); err != nil {
				t.Fatalf("clear: %v", err)
			}
			if _, err := st.FetchInterleavedState(context.Background(), session.Session.ALegID); err != nil {
				t.Fatal(err)
			}
			if tc.existingMemo {
				assertThinkerMemoPresent(t, ex, st, session.Session.ALegID)
			}
		})
	}
}

func TestExecutor_thinkerMidCycleAdminMutationKeepsSnapshottedRevision(t *testing.T) {
	t.Parallel()
	cap := &routeOpenCapture{}
	ex, st := thinkerOverrideExecutor(t, cap, false)
	const snapSel = "[thinker]thinker-be:m^exec-be:m"
	session := seedThinkerSession(t, ex, st, true, "ov-think-mid")
	if _, err := st.Replace(context.Background(), session.Session.ALegID, snapSel, time.Now().UTC()); err != nil {
		t.Fatalf("replace: %v", err)
	}
	resetRouteOpenCapture(cap)

	barrier := runtime.NewRouteAuthoritySnapshotBarrier()
	defer barrier.Release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = runtime.WithRouteAuthoritySnapshotBarrier(ctx, barrier)
	turn := interleavedBaseCall(overrideClientSelector)
	resumeInterleavedCall(session, turn)
	done := make(chan error, 1)
	var stream aipapi.EventStream
	go func() {
		s, err := ex.Execute(ctx, turn)
		stream = s
		done <- err
	}()
	waitOverrideBarrier(t, barrier, cancel, done)
	if _, err := st.Replace(context.Background(), session.Session.ALegID, "[thinker]thinker2-be:m^exec2-be:m", time.Now().UTC()); err != nil {
		cancel()
		barrier.Release()
		<-done
		t.Fatalf("replace after snapshot: %v", err)
	}
	barrier.Release()
	if err := <-done; err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}
	got := cap.snapshot()
	if len(got) == 0 {
		t.Fatal("expected snapshotted B-legs")
	}
	assertOverrideSelectorOnOpens(t, got, snapSel)
	for _, o := range got {
		if o.backend == "thinker2-be" || o.backend == "exec2-be" {
			t.Fatalf("mid-cycle replace must not open new selector: %+v", got)
		}
	}
}

func firstOverrideExecutor(t *testing.T, cap *routeOpenCapture) (*runtime.Executor, *b2bua.MemoryStore) {
	t.Helper()
	ex, st := routePlanLifetimeExecutor(t, map[string]execbackend.Backend{
		"clientbe":   overrideStreamingBackend(cap, "clientbe"),
		"adminbe":    overrideStreamingBackend(cap, "adminbe"),
		"cheapfirst": overrideStreamingBackend(cap, "cheapfirst"),
		"expensive":  overrideStreamingBackend(cap, "expensive"),
	})
	ex.Rand = routing.NewSeededRng(1)
	return ex, st
}

func thinkerOverrideExecutor(t *testing.T, cap *routeOpenCapture, visible bool) (*runtime.Executor, *b2bua.MemoryStore) {
	t.Helper()
	note := func(backend string) func(aipapi.Call) {
		return func(call aipapi.Call) {
			cap.count.Add(1)
			cap.mu.Lock()
			cap.opens = append(cap.opens, routeOpen{backend: backend, selector: call.Route.Selector})
			cap.mu.Unlock()
		}
	}
	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	backends := map[string]execbackend.Backend{
		"clientbe":    overrideStreamingBackend(cap, "clientbe"),
		"exec-be":     *interleavedBackendWithStream(caps, note("exec-be"), func() aipapi.ManagedEventStream { return executorTextStream("executor answer") }),
		"thinker-be":  *interleavedBackendWithStream(caps, note("thinker-be"), func() aipapi.ManagedEventStream { return thinkerMemoStream("plan") }),
		"exec2-be":    *interleavedBackendWithStream(caps, note("exec2-be"), func() aipapi.ManagedEventStream { return executorTextStream("executor 2") }),
		"thinker2-be": *interleavedBackendWithStream(caps, note("thinker2-be"), func() aipapi.ManagedEventStream { return thinkerMemoStream("plan2") }),
	}
	var ex *runtime.Executor
	var st *b2bua.MemoryStore
	if visible {
		ex, st = interleavedVisibleExecutor(t, backends)
	} else {
		ex, st = interleavedExecutor(t, backends)
	}
	ex.RouteOverrideReader = st
	ex.MaxAttempts = 4
	return ex, st
}

func seedThinkerSession(t *testing.T, ex *runtime.Executor, st *b2bua.MemoryStore, thinkerFirst bool, continuity string) *aipapi.Call {
	t.Helper()
	ex.RouteOverrideReader = st
	if !thinkerFirst {
		call := routePlanLifetimeCall("clientbe:m", continuity)
		collectExecute(t, ex, context.Background(), call)
		if call.Session.ALegID == "" {
			t.Fatal("expected A-leg after seed")
		}
		return call
	}
	first := interleavedBaseCall("[thinker]thinker-be:m^exec-be:m")
	first.Session.ContinuityKey = continuity
	collectExecute(t, ex, context.Background(), first)
	second := interleavedBaseCall("[thinker]thinker-be:m^exec-be:m")
	resumeInterleavedCall(first, second)
	collectExecute(t, ex, context.Background(), second)
	if second.Session.ALegID == "" {
		second.Session.ALegID = first.Session.ALegID
	}
	if second.Session.ALegID == "" {
		t.Fatal("expected A-leg after thinker seed")
	}
	return second
}

func assertWeightedFirstConsumed(t *testing.T, st *b2bua.MemoryStore, aLegID string, want bool) {
	t.Helper()
	got, err := st.FetchALeg(context.Background(), aLegID)
	if err != nil {
		t.Fatalf("FetchALeg: %v", err)
	}
	if got.WeightedFirstConsumed != want {
		t.Fatalf("WeightedFirstConsumed=%v want %v", got.WeightedFirstConsumed, want)
	}
}

func assertThinkerMemoPresent(t *testing.T, ex *runtime.Executor, st *b2bua.MemoryStore, aLegID string) {
	t.Helper()
	memoStore, ok := runtime.GetTestMemoStore(ex).(*interleavedthinking.InMemoryMemoStore)
	if !ok {
		t.Fatal("test memo store must be *interleavedthinking.InMemoryMemoStore")
	}
	stored, ok, err := memoStore.Latest(context.Background(), interleavedthinking.Scope(aLegID))
	if err != nil || !ok {
		t.Fatalf("memo Get ok=%v err=%v", ok, err)
	}
	if stored.Memo == "" {
		t.Fatal("expected memo body")
	}
}

func assertOverrideSelectorOnOpens(t *testing.T, got []routeOpen, selector string) {
	t.Helper()
	if len(got) == 0 {
		t.Fatalf("expected opens on %q", selector)
	}
	for i, o := range got {
		if o.selector != selector {
			t.Fatalf("open[%d] selector=%q want %q (%+v)", i, o.selector, selector, got)
		}
	}
}

func assertThinkerAndExecOpens(t *testing.T, got []routeOpen, selector, thinker, exec string) {
	t.Helper()
	var sawThinker, sawExec bool
	for i, o := range got {
		if o.selector != selector {
			t.Fatalf("open[%d] selector=%q want %q", i, o.selector, selector)
		}
		switch o.backend {
		case thinker:
			sawThinker = true
		case exec:
			sawExec = true
		}
	}
	if !sawThinker || !sawExec {
		t.Fatalf("want thinker %s and executor %s, got %+v", thinker, exec, got)
	}
}
