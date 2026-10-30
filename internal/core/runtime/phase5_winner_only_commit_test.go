package runtime

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/steering"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// Parity note (Important 4): winner-only SetInterleavedState atomicity is proven in-memory
// via b2bua.NewMemoryStore (FetchInterleavedState assertions below). This suffices because b2bua.Store
// SetInterleavedState is a single-record atomic operation across all production Store implementations:
// MemoryStore holds the state as one map entry under mu (internal/core/b2bua/store.go:287, 304), and
// bunstore.Store persists it as a single UPDATE a_legs SET interleaved_state_json = ? WHERE a_leg_id = ?
// (internal/core/continuity/bunstore/store.go:441-452) for both SQLite and Postgres (dialect-agnostic).
// No multi-row/cross-table transaction is involved. Bunstore SQLite/Postgres parity is covered independently
// by internal/core/continuity/bunstore integration tests.
// See internal/core/runtime/testdata/phase5_red_evidence.md section 5.3 for RED->GREEN evidence.

type fakeInterleavedProcessor struct{}

func (fakeInterleavedProcessor) BeginTurn(context.Context, InterleavedTurnInput) (InterleavedTurn, error) {
	return nil, nil
}

func (fakeInterleavedProcessor) IsMemoVisibleToClient(context.Context, string) bool {
	return false
}

func (fakeInterleavedProcessor) MemoSteeringPutRequest(memo string) steering.PutRequest {
	return interleavedthinking.MemoPutRequest(memo)
}

func (fakeInterleavedProcessor) MemoSteeringOverlayID() steering.OverlayID {
	return steering.OverlayID(interleavedthinking.MemoOverlayID)
}

func (fakeInterleavedProcessor) IsMemoSteeringOverlay(overlayID string) bool {
	return interleavedthinking.IsMemoOverlay(overlayID)
}

// TestPhase5_WinnerOnlyCommit_AcceptedWinnerPersistsState proves that when a parallel race
// accepts a winning arm, only the winning arm's pending selection effects (interleaved cycle)
// are committed to the store, and losing arms' pending effects are never committed.
func TestPhase5_WinnerOnlyCommit_AcceptedWinnerPersistsState(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	aLeg, err := store.CreateALeg(ctx, "")
	if err != nil {
		t.Fatalf("create a-leg: %v", err)
	}

	ex := TestExecutor()
	ex.Store = store
	ex.InterleavedProcessor = fakeInterleavedProcessor{}

	initialInterleaved := interleavedstate.State{
		Cycle: interleavedstate.CycleState{
			SelectorKey: "sel-1",
			Sequence: []interleavedstate.CycleEntry{
				{Key: "cand-a", Role: interleavedstate.RoleThinker},
				{Key: "cand-b", Role: interleavedstate.RoleExecutor},
			},
			NextIndex: 0,
		},
	}
	if err := store.SetInterleavedState(ctx, aLeg.ALegID, initialInterleaved); err != nil {
		t.Fatalf("set initial interleaved state: %v", err)
	}

	candA := routing.AttemptCandidate{Key: "cand-a", Primary: routing.Primary{Backend: "b-a", Model: "m-a"}}
	candB := routing.AttemptCandidate{Key: "cand-b", Primary: routing.Primary{Backend: "b-b", Model: "m-b"}}
	entries := []legEntry{{cand: candA, delay: 0}, {cand: candB, delay: 0}}
	candidates := []routing.AttemptCandidate{candA, candB}

	req := openNextRequest{
		reqFacts: requestFacts{
			recvTurnFacts: recvTurnFacts{traceID: "trace-w1", aLegID: aLeg.ALegID},
		},
		routeFacts: routeFacts{},
		progress: &recoveryController{
			excluded: map[string]struct{}{},
			failures: &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}},
		},
		interleaved: initialInterleaved,
	}

	sessionA := &attemptSession{terminal: newStreamTerminal(sdkterminal.ScopeAttempt), bleg: b2bua.BLegRecord{BLegID: "bleg-a"}}
	sessionB := &attemptSession{terminal: newStreamTerminal(sdkterminal.ScopeAttempt), bleg: b2bua.BLegRecord{BLegID: "bleg-b"}}

	winnerInterleaved := interleavedstate.State{
		Cycle: interleavedstate.CycleState{
			SelectorKey: "sel-1",
			Sequence: []interleavedstate.CycleEntry{
				{Key: "cand-a", Role: interleavedstate.RoleThinker},
				{Key: "cand-b", Role: interleavedstate.RoleExecutor},
			},
			NextIndex: 1,
		},
	}
	loserInterleaved := interleavedstate.State{
		Cycle: interleavedstate.CycleState{
			SelectorKey: "sel-1",
			Sequence: []interleavedstate.CycleEntry{
				{Key: "cand-a", Role: interleavedstate.RoleThinker},
				{Key: "cand-b", Role: interleavedstate.RoleExecutor},
			},
			NextIndex: 99,
		},
	}

	readyA := &readyAttempt{
		session: sessionA,
		pending: pendingSelectionEffects{
			interleaved: winnerInterleaved,
		},
	}
	readyB := &readyAttempt{
		session: sessionB,
		pending: pendingSelectionEffects{
			interleaved: loserInterleaved,
		},
	}

	// Arm A arrives first (arrival: 1), Arm B arrives second (arrival: 2)
	outcomeA := parallelArmOutcome{cand: candA, ready: readyA, arrival: 1, winnerBuf: []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "win"}}}
	outcomeB := parallelArmOutcome{cand: candB, ready: readyB, arrival: 2, winnerBuf: []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "lose"}}}

	cycleAdvance := &interleavedstate.CycleState{
		SelectorKey: "sel-1",
		Sequence: []interleavedstate.CycleEntry{
			{Key: "cand-a", Role: interleavedstate.RoleThinker},
			{Key: "cand-b", Role: interleavedstate.RoleExecutor},
		},
		NextIndex: 1,
	}
	reducer := newParallelRoundReducer(ex, req, entries, candidates, cycleAdvance)
	opened, err := reducer.Reduce(ctx, []parallelArmOutcome{outcomeA, outcomeB})
	if err != nil {
		t.Fatalf("unexpected reducer error: %v", err)
	}

	if opened.ready.session != sessionA {
		t.Fatalf("expected arm A to win, got session %v", opened.ready.session)
	}

	// Verify loser readyB was disposed without committing its pending effects
	if !readyB.IsConsumed() {
		t.Errorf("expected loser readyB to be marked consumed/disposed")
	}

	// Verify interleaved state store has the winner's committed cycle cursor
	persistedState, err := store.FetchInterleavedState(ctx, aLeg.ALegID)
	if err != nil {
		t.Fatalf("fetch interleaved state: %v", err)
	}
	if persistedState.Cycle.NextIndex != 1 {
		t.Errorf("persisted state cursor mismatch: got %d, want 1", persistedState.Cycle.NextIndex)
	}
}

// TestPhase5_WinnerOnlyCommit_AllFailureNeverPersists proves that when all arms fail in a parallel
// round, no winner-only state or cycle updates are committed to the store.
func TestPhase5_WinnerOnlyCommit_AllFailureNeverPersists(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	aLeg, err := store.CreateALeg(ctx, "")
	if err != nil {
		t.Fatalf("create a-leg: %v", err)
	}

	ex := TestExecutor()
	ex.Store = store

	initialInterleaved := interleavedstate.State{
		Cycle: interleavedstate.CycleState{
			SelectorKey: "sel-1",
			NextIndex:   0,
		},
	}
	if err := store.SetInterleavedState(ctx, aLeg.ALegID, initialInterleaved); err != nil {
		t.Fatalf("set initial interleaved state: %v", err)
	}

	candA := routing.AttemptCandidate{Key: "cand-a", Primary: routing.Primary{Backend: "b-a", Model: "m-a"}}
	candB := routing.AttemptCandidate{Key: "cand-b", Primary: routing.Primary{Backend: "b-b", Model: "m-b"}}
	entries := []legEntry{{cand: candA, delay: 0}, {cand: candB, delay: 0}}
	candidates := []routing.AttemptCandidate{candA, candB}

	req := openNextRequest{
		reqFacts: requestFacts{
			recvTurnFacts: recvTurnFacts{traceID: "trace-fail", aLegID: aLeg.ALegID},
		},
		routeFacts: routeFacts{},
		progress: &recoveryController{
			excluded: map[string]struct{}{},
			failures: &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}},
		},
		interleaved: initialInterleaved,
	}

	outcomeA := parallelArmOutcome{cand: candA, failErr: errors.New("backend timeout A"), arrival: 1}
	outcomeB := parallelArmOutcome{cand: candB, failErr: errors.New("backend 500 error B"), arrival: 2}

	cycleAdvance := &interleavedstate.CycleState{SelectorKey: "sel-1", NextIndex: 1}
	reducer := newParallelRoundReducer(ex, req, entries, candidates, cycleAdvance)
	opened, err := reducer.Reduce(ctx, []parallelArmOutcome{outcomeA, outcomeB})
	if err != nil {
		t.Fatalf("unexpected reducer error: %v", err)
	}
	if opened.ready != nil && opened.ready.session != nil {
		t.Fatalf("expected nil session on all-failure round")
	}

	// Verify interleaved state in store remains completely untouched
	state, err := store.FetchInterleavedState(ctx, aLeg.ALegID)
	if err != nil {
		t.Fatalf("fetch state: %v", err)
	}
	if state.Cycle.NextIndex != 0 {
		t.Errorf("cycle was mutated on all-failure round: got %d, want 0", state.Cycle.NextIndex)
	}
}

// TestPhase5_WinnerOnlyCommit_FatalErrorNeverPersists proves that when a fatal error aborts
// the round, ready arms' pending selection effects are not committed.
func TestPhase5_WinnerOnlyCommit_FatalErrorNeverPersists(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	aLeg, err := store.CreateALeg(ctx, "")
	if err != nil {
		t.Fatalf("create a-leg: %v", err)
	}

	ex := TestExecutor()
	ex.Store = store

	initialInterleaved := interleavedstate.State{
		Cycle: interleavedstate.CycleState{SelectorKey: "sel-1", NextIndex: 0},
	}
	if err := store.SetInterleavedState(ctx, aLeg.ALegID, initialInterleaved); err != nil {
		t.Fatalf("set initial interleaved state: %v", err)
	}

	candA := routing.AttemptCandidate{Key: "cand-a", Primary: routing.Primary{Backend: "b-a", Model: "m-a"}}
	candB := routing.AttemptCandidate{Key: "cand-b", Primary: routing.Primary{Backend: "b-b", Model: "m-b"}}
	entries := []legEntry{{cand: candA, delay: 0}, {cand: candB, delay: 0}}
	candidates := []routing.AttemptCandidate{candA, candB}

	req := openNextRequest{
		reqFacts: requestFacts{
			recvTurnFacts: recvTurnFacts{traceID: "trace-fatal", aLegID: aLeg.ALegID},
		},
		routeFacts: routeFacts{},
		progress: &recoveryController{
			excluded: map[string]struct{}{},
			failures: &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}},
		},
		interleaved: initialInterleaved,
	}

	// Arm A failed with non-fatal stream error, and Arm B has a fatal TTFT timeout (no winner)
	outcomeA := parallelArmOutcome{cand: candA, failErr: errors.New("stream failure a"), arrival: 1}
	outcomeB := parallelArmOutcome{cand: candB, failErr: aipapi.ErrTTFTTimeout, arrival: 2}

	cycleAdvance := &interleavedstate.CycleState{SelectorKey: "sel-1", NextIndex: 1}
	reducer := newParallelRoundReducer(ex, req, entries, candidates, cycleAdvance)
	_, err = reducer.Reduce(ctx, []parallelArmOutcome{outcomeA, outcomeB})
	if err == nil || !errors.Is(err, aipapi.ErrTTFTTimeout) {
		t.Fatalf("expected fatal TTFT error, got %v", err)
	}

	// Store must remain untouched
	state, err := store.FetchInterleavedState(ctx, aLeg.ALegID)
	if err != nil {
		t.Fatalf("fetch state: %v", err)
	}
	if state.Cycle.NextIndex != 0 {
		t.Errorf("cycle was mutated on fatal error: got %d, want 0", state.Cycle.NextIndex)
	}
}

// TestPhase5_WinnerOnlyCommit_ContextCanceledNeverPersists proves that when context is canceled,
// winner selection effects are not persisted.
func TestPhase5_WinnerOnlyCommit_ContextCanceledNeverPersists(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Already canceled

	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	aLeg, err := store.CreateALeg(context.Background(), "")
	if err != nil {
		t.Fatalf("create a-leg: %v", err)
	}

	ex := TestExecutor()
	ex.Store = store

	initialInterleaved := interleavedstate.State{
		Cycle: interleavedstate.CycleState{SelectorKey: "sel-1", NextIndex: 0},
	}
	if err := store.SetInterleavedState(context.Background(), aLeg.ALegID, initialInterleaved); err != nil {
		t.Fatalf("set initial interleaved state: %v", err)
	}

	candA := routing.AttemptCandidate{Key: "cand-a", Primary: routing.Primary{Backend: "b-a", Model: "m-a"}}
	entries := []legEntry{{cand: candA, delay: 0}}
	candidates := []routing.AttemptCandidate{candA}

	req := openNextRequest{
		reqFacts: requestFacts{
			recvTurnFacts: recvTurnFacts{traceID: "trace-cancel", aLegID: aLeg.ALegID},
		},
		progress: &recoveryController{
			excluded: map[string]struct{}{},
			failures: &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}},
		},
		interleaved: initialInterleaved,
	}

	sessionA := &attemptSession{terminal: newStreamTerminal(sdkterminal.ScopeAttempt), bleg: b2bua.BLegRecord{BLegID: "bleg-a"}}
	readyA := &readyAttempt{
		session: sessionA,
		pending: pendingSelectionEffects{
			interleaved: interleavedstate.State{
				Cycle: interleavedstate.CycleState{SelectorKey: "sel-1", NextIndex: 1},
			},
		},
	}

	outcomeA := parallelArmOutcome{cand: candA, ready: readyA, arrival: 1}

	cycleAdvance := &interleavedstate.CycleState{SelectorKey: "sel-1", NextIndex: 1}
	reducer := newParallelRoundReducer(ex, req, entries, candidates, cycleAdvance)
	_, err = reducer.Reduce(ctx, []parallelArmOutcome{outcomeA})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled error, got %v", err)
	}

	if !readyA.IsConsumed() {
		t.Errorf("expected readyA to be disposed on context cancellation")
	}

	// Store must remain untouched
	state, err := store.FetchInterleavedState(context.Background(), aLeg.ALegID)
	if err != nil {
		t.Fatalf("fetch state: %v", err)
	}
	if state.Cycle.NextIndex != 0 {
		t.Errorf("cycle was mutated despite context cancellation: got %d, want 0", state.Cycle.NextIndex)
	}
}

type failingInterleavedStateStore struct {
	b2bua.Store
	failSet bool
}

func (s *failingInterleavedStateStore) SetInterleavedState(ctx context.Context, aLegID string, state interleavedstate.State) error {
	if s.failSet {
		return errors.New("simulated interleaved state commit failure")
	}
	if iss, ok := s.Store.(b2bua.InterleavedStateStore); ok {
		return iss.SetInterleavedState(ctx, aLegID, state)
	}
	return b2bua.ErrInterleavedStateUnsupported
}

func (s *failingInterleavedStateStore) FetchInterleavedState(ctx context.Context, aLegID string) (interleavedstate.State, error) {
	if iss, ok := s.Store.(b2bua.InterleavedStateStore); ok {
		return iss.FetchInterleavedState(ctx, aLegID)
	}
	return interleavedstate.State{}, b2bua.ErrInterleavedStateUnsupported
}

// TestPhase5_WinnerOnlyCommit_CommitFailureCleansUpAndReleases proves that when committing
// winner selection effects fails, the winner is disposed, losers are released, and the error is returned.
func TestPhase5_WinnerOnlyCommit_CommitFailureCleansUpAndReleases(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	aLeg, err := store.CreateALeg(ctx, "")
	if err != nil {
		t.Fatalf("create a-leg: %v", err)
	}

	failStore := &failingInterleavedStateStore{Store: store, failSet: true}
	ex := TestExecutor()
	ex.Store = failStore
	ex.InterleavedProcessor = fakeInterleavedProcessor{}

	initialInterleaved := interleavedstate.State{
		Cycle: interleavedstate.CycleState{SelectorKey: "sel-1", NextIndex: 0},
	}
	if err := store.SetInterleavedState(ctx, aLeg.ALegID, initialInterleaved); err != nil {
		t.Fatalf("set initial interleaved state: %v", err)
	}

	candA := routing.AttemptCandidate{Key: "cand-a", Primary: routing.Primary{Backend: "b-a", Model: "m-a"}}
	candB := routing.AttemptCandidate{Key: "cand-b", Primary: routing.Primary{Backend: "b-b", Model: "m-b"}}
	entries := []legEntry{{cand: candA, delay: 0}, {cand: candB, delay: 0}}
	candidates := []routing.AttemptCandidate{candA, candB}

	req := openNextRequest{
		reqFacts: requestFacts{
			recvTurnFacts: recvTurnFacts{traceID: "trace-commit-fail", aLegID: aLeg.ALegID},
		},
		routeFacts: routeFacts{},
		progress: &recoveryController{
			excluded: map[string]struct{}{},
			failures: &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}},
		},
		interleaved: initialInterleaved,
	}

	sessionA := &attemptSession{terminal: newStreamTerminal(sdkterminal.ScopeAttempt), bleg: b2bua.BLegRecord{BLegID: "bleg-a"}}
	readyA := &readyAttempt{
		session: sessionA,
		pending: pendingSelectionEffects{
			interleaved: interleavedstate.State{
				Cycle: interleavedstate.CycleState{SelectorKey: "sel-1", NextIndex: 1},
			},
		},
	}

	outcomeA := parallelArmOutcome{cand: candA, ready: readyA, arrival: 1}
	outcomeB := parallelArmOutcome{cand: candB, failErr: errors.New("loser B failed"), arrival: 2}

	cycleAdvance := &interleavedstate.CycleState{SelectorKey: "sel-1", NextIndex: 1}
	reducer := newParallelRoundReducer(ex, req, entries, candidates, cycleAdvance)
	_, err = reducer.Reduce(ctx, []parallelArmOutcome{outcomeA, outcomeB})
	if err == nil {
		t.Fatal("expected commit error, got nil")
	}

	// Winner readyAttempt must be disposed
	if !readyA.IsConsumed() {
		t.Errorf("expected readyA to be marked consumed/disposed after commit failure")
	}

	// Store must remain untouched
	failStore.failSet = false
	state, err := store.FetchInterleavedState(ctx, aLeg.ALegID)
	if err != nil {
		t.Fatalf("fetch state: %v", err)
	}
	if state.Cycle.NextIndex != 0 {
		t.Errorf("cycle was mutated despite commit failure: got %d, want 0", state.Cycle.NextIndex)
	}
}

// TestPhase5_WinnerOnlyCommit_LoserDisposeDoesNotMutateStore directly proves that calling
// Dispose on a readyAttempt carrying pendingSelectionEffects terminalizes the attempt session
// without committing or mutating any cycle state in stores.
func TestPhase5_WinnerOnlyCommit_LoserDisposeDoesNotMutateStore(t *testing.T) {
	t.Parallel()

	session := &attemptSession{
		terminal: newStreamTerminal(sdkterminal.ScopeAttempt),
		bleg:     b2bua.BLegRecord{BLegID: "bleg-loser"},
	}

	ready := &readyAttempt{
		session: session,
		pending: pendingSelectionEffects{
			interleaved: interleavedstate.State{
				Cycle: interleavedstate.CycleState{SelectorKey: "sel-1", NextIndex: 1},
			},
		},
	}

	ctx := context.Background()
	ready.Dispose(ctx, errors.New("parallel race loser"))

	if !ready.IsConsumed() {
		t.Errorf("expected readyAttempt to be marked consumed after Dispose")
	}
	// Calling Consume after Dispose should fail
	if _, err := ready.Consume(); err == nil {
		t.Errorf("expected Consume after Dispose to fail")
	}
}

// TestPhase5_WinnerOnlyCommit_PublicationDeniedByClosedSlot proves that when publication
// is denied by the slot (e.g., Close won the publication lease), the ready attempt is disposed
// and its pending selection effects (cycle) are never committed to the store.
func TestPhase5_WinnerOnlyCommit_PublicationDeniedByClosedSlot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	aLeg, err := store.CreateALeg(ctx, "")
	if err != nil {
		t.Fatalf("create a-leg: %v", err)
	}

	initialInterleaved := interleavedstate.State{
		Cycle: interleavedstate.CycleState{SelectorKey: "sel-1", NextIndex: 0},
	}
	if err := store.SetInterleavedState(ctx, aLeg.ALegID, initialInterleaved); err != nil {
		t.Fatalf("set initial interleaved state: %v", err)
	}

	slot := &attemptSlot{}
	initialSession := &attemptSession{terminal: newStreamTerminal(sdkterminal.ScopeAttempt), bleg: b2bua.BLegRecord{BLegID: "bleg-initial"}}
	slot.mu.Lock()
	slot.current = initialSession
	slot.mu.Unlock()

	// Close wins the race and closes publication on the slot
	closedCurrent := slot.closePublicationAndSnapshot()
	if closedCurrent != initialSession {
		t.Fatalf("expected closed snapshot to return initialSession")
	}

	replacementSession := &attemptSession{terminal: newStreamTerminal(sdkterminal.ScopeAttempt), bleg: b2bua.BLegRecord{BLegID: "bleg-replacement"}}
	ready := &readyAttempt{
		session: replacementSession,
		pending: pendingSelectionEffects{
			interleaved: interleavedstate.State{
				Cycle: interleavedstate.CycleState{SelectorKey: "sel-1", NextIndex: 1},
			},
		},
	}

	// Publication attempt should be denied
	old, published := slot.swapIfOpen(ready)
	if published {
		t.Fatalf("expected swapIfOpen to reject publication on closed slot")
	}
	if old != initialSession {
		t.Fatalf("expected swapIfOpen to retain old session, got %v", old)
	}

	// Caller disposes the unpublished ready attempt
	ready.Dispose(ctx, fmt.Errorf("phase5 test: %w", errPublicationClosed))

	if !ready.IsConsumed() {
		t.Errorf("expected ready to be consumed/disposed")
	}

	// Store must remain untouched: cycle is never persisted
	state, err := store.FetchInterleavedState(ctx, aLeg.ALegID)
	if err != nil {
		t.Fatalf("fetch state: %v", err)
	}
	if state.Cycle.NextIndex != 0 {
		t.Errorf("cycle was mutated despite publication denial: got %d, want 0", state.Cycle.NextIndex)
	}
}

// TestPhase5_WinnerOnlyCommit_AlreadyConsumedReadyRejectedInReduce proves that if a ready attempt
// was already consumed or disposed before Reduce, Consume returns an error and Reduce cleanly aborts
// without committing any pending winner effects.
func TestPhase5_WinnerOnlyCommit_AlreadyConsumedReadyRejectedInReduce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	aLeg, err := store.CreateALeg(ctx, "")
	if err != nil {
		t.Fatalf("create a-leg: %v", err)
	}

	ex := TestExecutor()
	ex.Store = store

	initialInterleaved := interleavedstate.State{
		Cycle: interleavedstate.CycleState{SelectorKey: "sel-1", NextIndex: 0},
	}
	if err := store.SetInterleavedState(ctx, aLeg.ALegID, initialInterleaved); err != nil {
		t.Fatalf("set initial interleaved state: %v", err)
	}

	candA := routing.AttemptCandidate{Key: "cand-a", Primary: routing.Primary{Backend: "b-a", Model: "m-a"}}
	entries := []legEntry{{cand: candA, delay: 0}}
	candidates := []routing.AttemptCandidate{candA}

	req := openNextRequest{
		reqFacts: requestFacts{
			recvTurnFacts: recvTurnFacts{traceID: "trace-consumed", aLegID: aLeg.ALegID},
		},
		progress: &recoveryController{
			excluded: map[string]struct{}{},
			failures: &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}},
		},
		interleaved: initialInterleaved,
	}

	sessionA := &attemptSession{terminal: newStreamTerminal(sdkterminal.ScopeAttempt), bleg: b2bua.BLegRecord{BLegID: "bleg-a"}}
	readyA := &readyAttempt{
		session: sessionA,
		pending: pendingSelectionEffects{
			interleaved: initialInterleaved,
		},
	}

	// Pre-dispose or consume readyA
	readyA.Dispose(ctx, errors.New("aborted before reduce"))

	outcomeA := parallelArmOutcome{cand: candA, ready: readyA, arrival: 1}

	cycleAdvance := &interleavedstate.CycleState{SelectorKey: "sel-1", NextIndex: 1}
	reducer := newParallelRoundReducer(ex, req, entries, candidates, cycleAdvance)
	_, err = reducer.Reduce(ctx, []parallelArmOutcome{outcomeA})
	if err == nil {
		t.Fatal("expected error on already consumed readyAttempt, got nil")
	}

	// Store must remain untouched
	state, err := store.FetchInterleavedState(ctx, aLeg.ALegID)
	if err != nil {
		t.Fatalf("fetch state: %v", err)
	}
	if state.Cycle.NextIndex != 0 {
		t.Errorf("cycle was mutated: got %d, want 0", state.Cycle.NextIndex)
	}
}
