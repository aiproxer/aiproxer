package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/affinity"
	"github.com/aiproxer/aiproxer/internal/core/affinity/memorystore"
	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

type waitThenStream struct {
	wait   <-chan struct{}
	events []aipapi.Event
	idx    int
}

func (s *waitThenStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if s.idx == 0 && s.wait != nil {
		select {
		case <-s.wait:
		case <-ctx.Done():
			return aipapi.Event{}, ctx.Err()
		}
	}
	if s.idx >= len(s.events) {
		return aipapi.Event{Kind: aipapi.EventResponseFinished}, nil
	}
	ev := s.events[s.idx]
	s.idx++
	return ev, nil
}

func (s *waitThenStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{}
}

func (s *waitThenStream) Close() error { return nil }

// TestParallelRace_FastWinnerReturnsWhileSlowOpenBlocked validates DoD 2 & 3:
// When a fast winner produces winning output, tryOpenParallelGroup returns immediately
// to the caller without waiting for a slow/cancellation-insensitive Open on a losing arm.
// When the slow Open finally releases, the losing arm self-cleans its own tx exactly once,
// and bridge stream Close joins the completed cleanup.
func TestParallelRace_FastWinnerReturnsWhileSlowOpenBlocked(t *testing.T) {
	t.Parallel()

	auth := reservedAuthorityRecorder("res-fast-winner")
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	leg, err := store.CreateALeg(context.Background(), "fast-winner-test")
	if err != nil {
		t.Fatalf("create a-leg: %v", err)
	}

	ex := TestExecutor()
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.UsageAuthority = auth

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(leg.ALegID)

	slowOpenEntered := make(chan struct{})
	slowOpenRelease := make(chan struct{})
	var slowOpenEnteredOnce atomic.Bool

	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
	transportCaps := parallelTransportCaps()

	slowBackend := execbackend.Backend{
		Caps:          caps,
		TransportCaps: transportCaps,
		Open: func(ctx context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			if slowOpenEnteredOnce.CompareAndSwap(false, true) {
				close(slowOpenEntered)
			}
			// Simulate cancellation-insensitive slow backend Open
			<-slowOpenRelease
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventTextDelta, Delta: "slow"},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		},
	}

	fastBackend := execbackend.Backend{
		Caps:          caps,
		TransportCaps: transportCaps,
		Open: func(ctx context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			// Fast stream emits winning delta only after slow backend has entered Open
			return &waitThenStream{
				wait: slowOpenEntered,
				events: []aipapi.Event{
					{Kind: aipapi.EventTextDelta, Delta: "winner"},
					{Kind: aipapi.EventResponseFinished},
				},
			}, nil
		},
	}

	ex.Backends = map[string]execbackend.Backend{
		"fast": fastBackend,
		"slow": slowBackend,
	}

	fastCand := routing.AttemptCandidate{
		Key:             "fast:model",
		Primary:         routing.Primary{Backend: "fast", Model: "model"},
		InterleavedRole: interleavedstate.RoleExecutor,
	}
	slowCand := routing.AttemptCandidate{
		Key:             "slow:model",
		Primary:         routing.Primary{Backend: "slow", Model: "model"},
		InterleavedRole: interleavedstate.RoleExecutor,
	}

	req := authorityOpenRequest(t, leg.ALegID, &attemptBudget{max: 10})
	req.reqFacts.aScope = aScope

	ctx := context.Background()
	raceResultCh := make(chan openedAttempt, 1)
	raceErrCh := make(chan error, 1)

	go func() {
		opened, err := ex.tryOpenParallelGroup(ctx, req, []routing.AttemptCandidate{fastCand, slowCand}, nil, "", false)
		if err != nil {
			raceErrCh <- err
			return
		}
		raceResultCh <- opened
	}()

	// Wait for slow Open to be entered
	select {
	case <-slowOpenEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("slow Open was not entered")
	}

	// Verify tryOpenParallelGroup returned the winner WITHOUT waiting for slowOpenRelease
	var opened openedAttempt
	select {
	case err := <-raceErrCh:
		t.Fatalf("unexpected race error: %v", err)
	case opened = <-raceResultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("tryOpenParallelGroup did not return fast winner while slow Open is blocked")
	}

	if opened.ready == nil {
		t.Fatal("expected non-nil readyAttempt for fast winner")
	}

	opened.ready.state = readyStatePrepared
	sess, err := opened.ready.Consume()
	if err != nil {
		t.Fatalf("failed to consume ready attempt: %v", err)
	}

	// Fast winner event can be read immediately
	ev, err := sess.inner.Recv(ctx)
	if err != nil {
		t.Fatalf("winner Recv failed: %v", err)
	}
	if ev.Delta != "winner" {
		t.Fatalf("winner text = %q, want 'winner'", ev.Delta)
	}

	// Now unblock slow Open
	close(slowOpenRelease)

	// Close the stream, joining background loser cleanup
	if err := sess.inner.Close(); err != nil {
		t.Fatalf("sess Close failed: %v", err)
	}

	// Terminalize winner session
	sess.TerminalizeAttempt(ctx, IntentSuccess, attemptEvidence{
		Command:    sdkterminal.CommandNormalFinish,
		LegOutcome: usageevidence.LegOutcomeWinner,
	})

	// Verify authority was settled for both winner and loser
	if got := auth.settleCalls.Load(); got != 2 {
		t.Fatalf("settle calls count = %d, want 2 (winner + loser)", got)
	}
}

// TestParallelRace_ParentCancelWithLateOpenCleansExactlyOnce validates DoD 3 & 4:
// When parent context is canceled, any late Open on parallel arms completes its own
// self-cleanup exactly once without an arbitrary 50ms window or resource leak.
func TestParallelRace_ParentCancelWithLateOpenCleansExactlyOnce(t *testing.T) {
	t.Parallel()

	auth := reservedAuthorityRecorder("res-late-cancel")
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	leg, err := store.CreateALeg(context.Background(), "late-cancel-test")
	if err != nil {
		t.Fatalf("create a-leg: %v", err)
	}

	ex := TestExecutor()
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.UsageAuthority = auth

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(leg.ALegID)

	openEntered1 := make(chan struct{})
	openEntered2 := make(chan struct{})
	openRelease := make(chan struct{})

	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
	transportCaps := parallelTransportCaps()

	blockingBackend := func(entered chan struct{}) execbackend.Backend {
		var once atomic.Bool
		return execbackend.Backend{
			Caps:          caps,
			TransportCaps: transportCaps,
			Open: func(ctx context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if once.CompareAndSwap(false, true) {
					close(entered)
				}
				<-openRelease
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventTextDelta, Delta: "text"},
				}), nil
			},
		}
	}

	ex.Backends = map[string]execbackend.Backend{
		"arm1": blockingBackend(openEntered1),
		"arm2": blockingBackend(openEntered2),
	}

	cand1 := routing.AttemptCandidate{
		Key:             "arm1:model",
		Primary:         routing.Primary{Backend: "arm1", Model: "model"},
		InterleavedRole: interleavedstate.RoleExecutor,
	}
	cand2 := routing.AttemptCandidate{
		Key:             "arm2:model",
		Primary:         routing.Primary{Backend: "arm2", Model: "model"},
		InterleavedRole: interleavedstate.RoleExecutor,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := authorityOpenRequest(t, leg.ALegID, &attemptBudget{max: 10})
	req.reqFacts.aScope = aScope

	doneCh := make(chan error, 1)
	go func() {
		_, err := ex.tryOpenParallelGroup(ctx, req, []routing.AttemptCandidate{cand1, cand2}, nil, "", false)
		doneCh <- err
	}()

	// Wait for both arms to enter Open
	select {
	case <-openEntered1:
	case <-time.After(5 * time.Second):
		t.Fatal("arm1 did not enter Open")
	}
	select {
	case <-openEntered2:
	case <-time.After(5 * time.Second):
		t.Fatal("arm2 did not enter Open")
	}

	// Cancel parent context
	cancel()

	// tryOpenParallelGroup must return context.Canceled PROMPTLY without waiting for openRelease
	select {
	case err := <-doneCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("tryOpenParallelGroup did not return promptly upon parent cancellation while late open was blocked")
	}

	// Now release late Opens so background workers can complete their self-cleanups
	close(openRelease)

	// Wait for background worker cleanups to settle authority
	for start := time.Now(); time.Since(start) < 2*time.Second; time.Sleep(5 * time.Millisecond) {
		if auth.settleCalls.Load() >= 2 {
			break
		}
	}

	// Both arms must have settled authority exactly once
	if got := auth.settleCalls.Load(); got != 2 {
		t.Fatalf("expected 2 settled handles on cancel with late open, got %d", got)
	}
	if auth.releaseCalls.Load() != 0 {
		t.Fatalf("expected 0 releases, got %d", auth.releaseCalls.Load())
	}
}

// TestParallelRace_AffinityIsolatedFromLosingArmAndStableReducer validates DoD 5:
// 1. Losing arm failure/rejection does not invalidate or mutate shared affinity bindings when a winner exists.
// 2. When all arms fail, serial reducer applies affinity invalidation in stable candidate declaration order.
func TestParallelRace_AffinityIsolatedFromLosingArmAndStableReducer(t *testing.T) {
	t.Parallel()

	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
	transportCaps := parallelTransportCaps()

	t.Run("WinnerExists_LosingArmDoesNotClearAffinity", func(t *testing.T) {
		t.Parallel()
		store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		if err != nil {
			t.Fatalf("new memory store: %v", err)
		}
		leg, err := store.CreateALeg(context.Background(), "aff-win-test")
		if err != nil {
			t.Fatalf("create a-leg: %v", err)
		}

		affStore := memorystore.New()

		ex := TestExecutor()
		ex.Store = store
		ex.Bus = hooks.New(hooks.Config{})
		ex.AffinityStore = affStore

		ctx := context.Background()

		// Bind sticky backend in store
		stickyKey := affinity.Key{Scope: affinity.ScopeSession, ID: "sticky-sess-win"}
		if err := affStore.Set(ctx, affinity.Binding{Key: stickyKey, BackendID: "sticky-backend"}); err != nil {
			t.Fatalf("set affinity: %v", err)
		}

		ex.Backends = map[string]execbackend.Backend{
			"sticky-backend": {
				Caps:          caps,
				TransportCaps: transportCaps,
				Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					// Recoverable error on sticky loser
					return nil, fmt.Errorf("temporary connection error: %w", aipapi.ErrRecoverablePreOutput)
				},
			},
			"winner-backend": {
				Caps:          caps,
				TransportCaps: transportCaps,
				Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					return aipapi.NewFixedEventStream([]aipapi.Event{
						{Kind: aipapi.EventTextDelta, Delta: "winner"},
						{Kind: aipapi.EventResponseFinished},
					}), nil
				},
			},
		}

		stickyCand := routing.AttemptCandidate{
			Key:             "sticky-backend:model",
			Primary:         routing.Primary{Backend: "sticky-backend", Model: "model"},
			InterleavedRole: interleavedstate.RoleExecutor,
		}
		winnerCand := routing.AttemptCandidate{
			Key:             "winner-backend:model",
			Primary:         routing.Primary{Backend: "winner-backend", Model: "model"},
			InterleavedRole: interleavedstate.RoleExecutor,
		}

		req := authorityOpenRequest(t, leg.ALegID, &attemptBudget{max: 10})
		req.routeFacts.affinityKey = stickyKey
		req.routeFacts.affinitySet = true
		req.routeFacts.sel = &routing.Selector{Affinity: routing.AffinitySession}

		opened, err := ex.tryOpenParallelGroup(ctx, req, []routing.AttemptCandidate{stickyCand, winnerCand}, nil, "sticky-backend", true)
		if err != nil {
			t.Fatalf("tryOpenParallelGroup failed: %v", err)
		}
		if opened.ready == nil {
			t.Fatal("expected winner readyAttempt")
		}

		// Verify sticky binding was NOT cleared in store because winner succeeded
		binding, bound, err := affStore.Get(ctx, stickyKey)
		if err != nil {
			t.Fatalf("lookup affinity: %v", err)
		}
		if !bound || binding.BackendID != "sticky-backend" {
			t.Fatalf("expected affinity to remain 'sticky-backend', got bound=%v backend=%q", bound, binding.BackendID)
		}
	})

	t.Run("AllArmsFail_ReducerAppliesAffinityInStableOrder", func(t *testing.T) {
		t.Parallel()
		store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		if err != nil {
			t.Fatalf("new memory store: %v", err)
		}
		leg, err := store.CreateALeg(context.Background(), "aff-fail-test")
		if err != nil {
			t.Fatalf("create a-leg: %v", err)
		}

		affStore := memorystore.New()

		ex := TestExecutor()
		ex.Store = store
		ex.Bus = hooks.New(hooks.Config{})
		ex.AffinityStore = affStore

		ctx := context.Background()

		stickyKey := affinity.Key{Scope: affinity.ScopeSession, ID: "sticky-sess-fail"}
		if err := affStore.Set(ctx, affinity.Binding{Key: stickyKey, BackendID: "cand1-backend"}); err != nil {
			t.Fatalf("set affinity: %v", err)
		}

		cand2Done := make(chan struct{})

		// Cand1 (sticky) is slower to complete than Cand2, inverting completion order
		cand1Backend := execbackend.Backend{
			Caps:          caps,
			TransportCaps: transportCaps,
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				<-cand2Done
				return nil, fmt.Errorf("recoverable cand1 error: %w", aipapi.ErrRecoverablePreOutput)
			},
		}

		// Cand2 fails immediately
		cand2Backend := execbackend.Backend{
			Caps:          caps,
			TransportCaps: transportCaps,
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				close(cand2Done)
				return nil, errors.New("non-recoverable cand2 error")
			},
		}

		ex.Backends = map[string]execbackend.Backend{
			"cand1-backend": cand1Backend,
			"cand2-backend": cand2Backend,
		}

		cand1 := routing.AttemptCandidate{
			Key:             "cand1-backend:model",
			Primary:         routing.Primary{Backend: "cand1-backend", Model: "model"},
			InterleavedRole: interleavedstate.RoleExecutor,
		}
		cand2 := routing.AttemptCandidate{
			Key:             "cand2-backend:model",
			Primary:         routing.Primary{Backend: "cand2-backend", Model: "model"},
			InterleavedRole: interleavedstate.RoleExecutor,
		}

		req := authorityOpenRequest(t, leg.ALegID, &attemptBudget{max: 10})
		req.routeFacts.affinityKey = stickyKey
		req.routeFacts.affinitySet = true
		req.routeFacts.sel = &routing.Selector{Affinity: routing.AffinitySession}

		opened, err := ex.tryOpenParallelGroup(ctx, req, []routing.AttemptCandidate{cand1, cand2}, nil, "cand1-backend", true)
		if err != nil {
			t.Fatalf("unexpected tryOpenParallelGroup err: %v", err)
		}
		if opened.ready != nil {
			t.Fatal("expected nil ready on all arms fail")
		}

		// Verify reducer cleared affinity binding for cand1-backend upon all failure
		_, bound, err := affStore.Get(ctx, stickyKey)
		if err != nil {
			t.Fatalf("lookup affinity: %v", err)
		}
		if bound {
			t.Fatal("expected affinity to be cleared after all arms failed")
		}

		// Verify failures.ParallelFailure was populated
		failures := req.progress.getFailures()
		if failures.ParallelFailure == nil {
			t.Fatal("expected ParallelFailure to be recorded on progress")
		}
	})
}

// TestParallelRace_WinnerAtomicallyPublishedInSnapshot_NoWakeBeforeAppend exposes and validates
// that coordinator atomically appends winner to outcomes before publishing decision snapshot,
// eliminating wake-before-append race across concurrent arm arrivals.
func TestParallelRace_WinnerAtomicallyPublishedInSnapshot_NoWakeBeforeAppend(t *testing.T) {
	t.Parallel()

	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
	transportCaps := parallelTransportCaps()

	for iter := range 10 {
		store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		if err != nil {
			t.Fatalf("new memory store: %v", err)
		}
		leg, err := store.CreateALeg(context.Background(), fmt.Sprintf("wake-append-%d", iter))
		if err != nil {
			t.Fatalf("create a-leg: %v", err)
		}

		auth := reservedAuthorityRecorder(fmt.Sprintf("res-wake-%d", iter))
		ex := TestExecutor()
		ex.Store = store
		ex.Bus = hooks.New(hooks.Config{})
		ex.UsageAuthority = auth

		coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
		aScope := coord.StartALeg(leg.ALegID)

		ex.Backends = map[string]execbackend.Backend{
			"b-fast": {
				Caps:          caps,
				TransportCaps: transportCaps,
				Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					return aipapi.NewFixedEventStream([]aipapi.Event{
						{Kind: aipapi.EventTextDelta, Delta: "winner-text"},
						{Kind: aipapi.EventResponseFinished},
					}), nil
				},
			},
			"b-fail": {
				Caps:          caps,
				TransportCaps: transportCaps,
				Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					return nil, errors.New("immediate arm failure")
				},
			},
		}

		candFast := routing.AttemptCandidate{
			Key:             "b-fast:model",
			Primary:         routing.Primary{Backend: "b-fast", Model: "model"},
			InterleavedRole: interleavedstate.RoleExecutor,
		}
		candFail := routing.AttemptCandidate{
			Key:             "b-fail:model",
			Primary:         routing.Primary{Backend: "b-fail", Model: "model"},
			InterleavedRole: interleavedstate.RoleExecutor,
		}

		req := authorityOpenRequest(t, leg.ALegID, &attemptBudget{max: 10})
		req.reqFacts.aScope = aScope

		ctx := context.Background()
		opened, err := ex.tryOpenParallelGroup(ctx, req, []routing.AttemptCandidate{candFast, candFail}, nil, "", false)
		if err != nil {
			t.Fatalf("iter %d: unexpected tryOpenParallelGroup error: %v", iter, err)
		}
		if opened.ready == nil {
			t.Fatalf("iter %d: readyAttempt is nil; winner was dropped by wake-before-append race", iter)
		}

		opened.ready.state = readyStatePrepared
		sess, err := opened.ready.Consume()
		if err != nil {
			t.Fatalf("iter %d: failed to consume ready attempt: %v", iter, err)
		}
		ev, err := sess.inner.Recv(ctx)
		if err != nil {
			t.Fatalf("iter %d: Recv error: %v", iter, err)
		}
		if ev.Delta != "winner-text" {
			t.Fatalf("iter %d: got delta %q, want 'winner-text'", iter, ev.Delta)
		}
		_ = sess.inner.Close()
		sess.TerminalizeAttempt(ctx, IntentSuccess, attemptEvidence{
			Command:    sdkterminal.CommandNormalFinish,
			LegOutcome: usageevidence.LegOutcomeWinner,
		})
	}
}

// TestParallelRace_NoDoubleCancelCloseAuthority ensures that winner stream Close and
// terminalization do not double-cancel or double-settle authority, and loser cleanup
// completes cleanly.
func TestParallelRace_NoDoubleCancelCloseAuthority(t *testing.T) {
	t.Parallel()

	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
	transportCaps := parallelTransportCaps()

	auth := reservedAuthorityRecorder("res-no-double")
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	leg, err := store.CreateALeg(context.Background(), "no-double-test")
	if err != nil {
		t.Fatalf("create a-leg: %v", err)
	}

	ex := TestExecutor()
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.UsageAuthority = auth

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(leg.ALegID)

	var winnerCloseCount atomic.Int32
	var loserCloseCount atomic.Int32
	loserOpenEntered := make(chan struct{})
	var loserOpenEnteredOnce atomic.Bool

	ex.Backends = map[string]execbackend.Backend{
		"winner": {
			Caps:          caps,
			TransportCaps: transportCaps,
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return &customCloseStream{
					ManagedEventStream: &waitThenStream{
						wait: loserOpenEntered,
						events: []aipapi.Event{
							{Kind: aipapi.EventTextDelta, Delta: "win"},
							{Kind: aipapi.EventResponseFinished},
						},
					},
					onClose: func() { winnerCloseCount.Add(1) },
				}, nil
			},
		},
		"loser": {
			Caps:          caps,
			TransportCaps: transportCaps,
			Open: func(ctx context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if loserOpenEnteredOnce.CompareAndSwap(false, true) {
					close(loserOpenEntered)
				}
				return &customCloseStream{
					ManagedEventStream: &waitThenStream{
						wait: ctx.Done(),
					},
					onClose: func() { loserCloseCount.Add(1) },
				}, nil
			},
		},
	}

	candWin := routing.AttemptCandidate{
		Key:             "winner:model",
		Primary:         routing.Primary{Backend: "winner", Model: "model"},
		InterleavedRole: interleavedstate.RoleExecutor,
	}
	candLose := routing.AttemptCandidate{
		Key:             "loser:model",
		Primary:         routing.Primary{Backend: "loser", Model: "model"},
		InterleavedRole: interleavedstate.RoleExecutor,
	}

	req := authorityOpenRequest(t, leg.ALegID, &attemptBudget{max: 10})
	req.reqFacts.aScope = aScope

	ctx := context.Background()
	opened, err := ex.tryOpenParallelGroup(ctx, req, []routing.AttemptCandidate{candWin, candLose}, nil, "", false)
	if err != nil {
		t.Fatalf("tryOpenParallelGroup: %v", err)
	}

	opened.ready.state = readyStatePrepared
	sess, err := opened.ready.Consume()
	if err != nil {
		t.Fatalf("consume: %v", err)
	}

	// Close stream multiple times - must be idempotent and safe
	if err := sess.inner.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := sess.inner.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	if got := winnerCloseCount.Load(); got != 1 {
		t.Fatalf("winner stream closed %d times, want exactly 1", got)
	}
	if got := loserCloseCount.Load(); got > 1 {
		t.Fatalf("loser stream closed %d times, want at most 1", got)
	}

	settleBefore := auth.settleCalls.Load()
	sess.TerminalizeAttempt(ctx, IntentSuccess, attemptEvidence{
		Command:    sdkterminal.CommandNormalFinish,
		LegOutcome: usageevidence.LegOutcomeWinner,
	})
	settleAfter1 := auth.settleCalls.Load()
	if settleAfter1 <= settleBefore {
		t.Fatalf("expected settleCalls to increment after TerminalizeAttempt: before=%d, after=%d", settleBefore, settleAfter1)
	}

	// Idempotent second terminalize must not double-settle authority
	sess.TerminalizeAttempt(ctx, IntentSuccess, attemptEvidence{
		Command:    sdkterminal.CommandNormalFinish,
		LegOutcome: usageevidence.LegOutcomeWinner,
	})
	settleAfter2 := auth.settleCalls.Load()
	if settleAfter2 != settleAfter1 {
		t.Fatalf("expected settleCalls not to increment on second TerminalizeAttempt: first=%d, second=%d", settleAfter1, settleAfter2)
	}
}

type customCloseStream struct {
	aipapi.ManagedEventStream
	onClose func()
}

func (s *customCloseStream) Close() error {
	if s.onClose != nil {
		s.onClose()
	}
	if s.ManagedEventStream != nil {
		return s.ManagedEventStream.Close()
	}
	return nil
}
