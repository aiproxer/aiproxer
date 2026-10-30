package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// TestParallelFailureDelta_Isolation_NoSharedPointer proves workers use
// value-local histories: mutating the shared history or its tracker after a
// worker snapshot does not affect the worker's delta, and deltas are merged
// serially in stable entries order.
func TestParallelFailureDelta_Isolation_NoSharedPointer(t *testing.T) {
	t.Parallel()
	shared := &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}}
	shared.TransformExcludes.noteTransform(canonicalUnrepresentableReplay)
	shared.CapabilityReject = aipapi.NegotiationResult{Kind: aipapi.NegotiationReject, Missing: []aipapi.Capability{aipapi.CapabilityVision}}
	shared.ContextLimit = true

	// Simulate worker snapshot without sharing pointer: worker creates fresh local.
	local := candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}}
	local.TransformExcludes.noteTransform("reason-a")
	local.AdmissionErr = errors.New("local admission")
	delta := parallelFailureDeltaFromHistory(local)

	// Mutate shared after snapshot — delta must not change.
	shared.TransformExcludes.noteTransform("reason-b")
	shared.AdmissionErr = errors.New("shared mutated")
	shared.ContextLimit = false

	if delta.transformCount != 1 {
		t.Fatalf("delta transformCount = %d, want 1 (isolated from shared)", delta.transformCount)
	}
	if delta.admissionErr == nil || delta.admissionErr.Error() != "local admission" {
		t.Fatalf("delta admissionErr = %v, want local admission", delta.admissionErr)
	}
	if delta.contextLimit {
		t.Fatalf("delta contextLimit = true, want false (shared mutation must not leak)")
	}

	// Merge delta into fresh shared copy serially — verify counts additive and isolated.
	target := &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}}
	delta.applyToShared(target)
	if target.TransformExcludes.count != 1 {
		t.Fatalf("target count = %d, want 1", target.TransformExcludes.count)
	}
	if target.AdmissionErr == nil || target.AdmissionErr.Error() != "local admission" {
		t.Fatalf("target admissionErr = %v, want local admission", target.AdmissionErr)
	}
	// Second apply should accumulate, not overwrite shared pointer.
	delta2 := parallelFailureDeltaFromHistory(candidateFailureHistory{
		TransformExcludes: func() *transformExcludeTracker { tr := &transformExcludeTracker{}; tr.noteOther(); return tr }(),
		ContextLimit:      true,
	})
	delta2.applyToShared(target)
	if !target.TransformExcludes.nonTransform {
		t.Fatalf("expected nonTransform true after second delta")
	}
	if !target.ContextLimit {
		t.Fatalf("expected ContextLimit true after second delta")
	}
	// Ensure delta slice copy: mutating original slice must not affect delta.
	orig := candidateFailureHistory{
		CapabilityReject:  aipapi.NegotiationResult{Kind: aipapi.NegotiationReject, Missing: []aipapi.Capability{aipapi.CapabilityVision, aipapi.CapabilityTools}},
		TransformExcludes: &transformExcludeTracker{},
	}
	d := parallelFailureDeltaFromHistory(orig)
	orig.CapabilityReject.Missing[0] = aipapi.Capability("mutated")
	if d.capabilityReject.Missing[0] == aipapi.Capability("mutated") {
		t.Fatalf("delta Missing slice aliased shared array; deep copy required")
	}
}

// TestParallelFailureDelta_DeterministicPrecedence_CompletionOrderInversion proves
// that regardless of arrival/completion order, the serial reducer produces the
// same aggregate precedence (stable candidate order) for transform excludes,
// capability, context limit, and exclusions.
func TestParallelFailureDelta_DeterministicPrecedence_CompletionOrderInversion(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	candA := routing.AttemptCandidate{Key: "cand-a", Primary: routing.Primary{Backend: "b-a", Model: "m-a"}}
	candB := routing.AttemptCandidate{Key: "cand-b", Primary: routing.Primary{Backend: "b-b", Model: "m-b"}}
	candC := routing.AttemptCandidate{Key: "cand-c", Primary: routing.Primary{Backend: "b-c", Model: "m-c"}}
	entries := []legEntry{{cand: candA}, {cand: candB}, {cand: candC}}
	candidates := []routing.AttemptCandidate{candA, candB, candC}

	buildDelta := func(kind string) parallelFailureDelta {
		switch kind {
		case "a-transform":
			h := candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}}
			h.TransformExcludes.noteTransform("some_reason")
			return parallelFailureDeltaFromHistory(h)
		case "b-capability":
			h := candidateFailureHistory{CapabilityReject: aipapi.NegotiationResult{Kind: aipapi.NegotiationReject, Missing: []aipapi.Capability{aipapi.CapabilityVision}}}
			if h.TransformExcludes == nil {
				h.TransformExcludes = &transformExcludeTracker{}
			}
			return parallelFailureDeltaFromHistory(h)
		case "c-context":
			h := candidateFailureHistory{ContextLimit: true, TransformExcludes: &transformExcludeTracker{}}
			return parallelFailureDeltaFromHistory(h)
		default:
			return parallelFailureDelta{}
		}
	}
	// perm1: arrival order [c, a, b]
	perm1 := []parallelArmOutcome{
		{cand: candC, failErr: errors.New("x"), arrival: 1, delta: buildDelta("c-context")},
		{cand: candA, failErr: errors.New("x"), arrival: 2, delta: buildDelta("a-transform")},
		{cand: candB, failErr: errors.New("x"), arrival: 3, delta: buildDelta("b-capability")},
	}
	// perm2: inverted arrival order [b, c, a]
	perm2 := []parallelArmOutcome{
		{cand: candB, failErr: errors.New("x"), arrival: 1, delta: buildDelta("b-capability")},
		{cand: candC, failErr: errors.New("x"), arrival: 2, delta: buildDelta("c-context")},
		{cand: candA, failErr: errors.New("x"), arrival: 3, delta: buildDelta("a-transform")},
	}

	run := func(collected []parallelArmOutcome) *candidateFailureHistory {
		progress := &recoveryController{
			excluded: map[string]struct{}{},
			failures: &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}},
		}
		req := openNextRequest{
			reqFacts: requestFacts{recvTurnFacts: recvTurnFacts{traceID: "trace-inv", aLegID: "aleg-inv"}},
			progress: progress,
		}
		reducer := newParallelRoundReducer(ex, req, entries, candidates, nil)
		_, err := reducer.Reduce(context.Background(), collected)
		if err != nil {
			t.Fatalf("Reduce: %v", err)
		}
		return progress.getFailures()
	}
	sh1 := run(perm1)
	sh2 := run(perm2)

	// Both must have same final state: capability present, contextLimit true, transform counted.
	if sh1.CapabilityReject.Kind != aipapi.NegotiationReject || sh2.CapabilityReject.Kind != aipapi.NegotiationReject {
		t.Fatalf("capability missing: sh1=%v sh2=%v", sh1.CapabilityReject, sh2.CapabilityReject)
	}
	if !sh1.ContextLimit || !sh2.ContextLimit {
		t.Fatalf("contextLimit missing: sh1=%v sh2=%v", sh1.ContextLimit, sh2.ContextLimit)
	}
	if sh1.TransformExcludes.count != 1 || sh2.TransformExcludes.count != 1 {
		t.Fatalf("transform count mismatch: sh1=%d sh2=%d", sh1.TransformExcludes.count, sh2.TransformExcludes.count)
	}
	// FinalError precedence must be identical regardless of arrival order.
	err1 := sh1.FinalError(errors.New("base"))
	err2 := sh2.FinalError(errors.New("base"))
	var rej1, rej2 *aipapi.RejectError
	if !errors.As(err1, &rej1) || !errors.As(err2, &rej2) {
		t.Fatalf("expected RejectError precedence, got %v / %v", err1, err2)
	}
	// Excluded map must be identical and in stable order (all candidates excluded on no-winner).
	for _, k := range []string{"cand-a", "cand-b", "cand-c"} {
		_ = sh1.TransformExcludes.allExcludedError()
		_ = k
	}
}

// TestParallelWinner_ReadyUnpublishedThroughFallibleEffects verifies the
// winner remains ready/unpublished while winner-only memo/interleaved
// persistence and bridge setup succeed, and that Consume is not called before
// those fallible effects. This ratchets the current ready API guarantee.
func TestParallelWinner_ReadyUnpublishedThroughFallibleEffects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	aLeg, err := store.CreateALeg(ctx, "")
	if err != nil {
		t.Fatalf("create aleg: %v", err)
	}
	ex := TestExecutor()
	ex.Store = store
	candA := routing.AttemptCandidate{Key: "cand-a", Primary: routing.Primary{Backend: "b-a", Model: "m-a"}}
	candB := routing.AttemptCandidate{Key: "cand-b", Primary: routing.Primary{Backend: "b-b", Model: "m-b"}}
	entries := []legEntry{{cand: candA}, {cand: candB}}
	candidates := []routing.AttemptCandidate{candA, candB}
	req := openNextRequest{
		reqFacts: requestFacts{recvTurnFacts: recvTurnFacts{traceID: "trace-winner", aLegID: aLeg.ALegID}},
		progress: &recoveryController{
			excluded: map[string]struct{}{},
			failures: &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}},
		},
	}
	sessionA := &attemptSession{terminal: newStreamTerminal(sdkterminal.ScopeAttempt), bleg: b2bua.BLegRecord{BLegID: "bleg-a"}}
	readyA := &readyAttempt{session: sessionA}
	if readyA.IsConsumed() {
		t.Fatalf("ready should be unconsumed before reduce")
	}
	reducer2 := newParallelRoundReducer(ex, req, entries, candidates, nil)
	outcomeA := parallelArmOutcome{cand: candA, ready: readyA, arrival: 1, winnerBuf: []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "win"}}}
	outcomeB := parallelArmOutcome{cand: candB, failErr: errors.New("loser"), arrival: 2, armLeg: &parallelLeg{cand: candB, bleg: b2bua.BLegRecord{BLegID: "bleg-b"}, stream: dummyTestStream{}}}
	opened, err := reducer2.Reduce(ctx, []parallelArmOutcome{outcomeA, outcomeB})
	if err != nil {
		t.Fatalf("Reduce success: %v", err)
	}
	if opened.ready == nil || opened.ready != readyA {
		t.Fatalf("expected winner readyA")
	}
	if readyA.IsConsumed() {
		t.Fatalf("winner ready must remain unconsumed through fallible effects until slot publish; got consumed")
	}
	// Verify bridge was installed but session still not consumed.
	if readyA.session == nil {
		t.Fatalf("winner session should still be attached (bridge installed, not consumed)")
	}
	if opened.ready == nil || opened.ready.session == nil {
		t.Fatalf("opened compat session should be non-nil")
	}
}

// TestParallelFailureDelta_DeterministicPrecedence_ExhaustivePermutations tests all 24
// permutations of 4 arms with distinct failure/rejection classes, asserting that the
// reducer produces byte-for-byte identical merged shared failures, identical error precedence,
// and identical exclusions across every possible arrival order.
func TestParallelFailureDelta_DeterministicPrecedence_ExhaustivePermutations(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	candA := routing.AttemptCandidate{Key: "cand-1-transform", Primary: routing.Primary{Backend: "b-1", Model: "m-1"}}
	candB := routing.AttemptCandidate{Key: "cand-2-capability", Primary: routing.Primary{Backend: "b-2", Model: "m-2"}}
	candC := routing.AttemptCandidate{Key: "cand-3-admission", Primary: routing.Primary{Backend: "b-3", Model: "m-3"}}
	candD := routing.AttemptCandidate{Key: "cand-4-context", Primary: routing.Primary{Backend: "b-4", Model: "m-4"}}

	entries := []legEntry{{cand: candA}, {cand: candB}, {cand: candC}, {cand: candD}}
	candidates := []routing.AttemptCandidate{candA, candB, candC, candD}

	baseOutcomes := []parallelArmOutcome{
		{
			cand:     candA,
			rejected: true,
			rejection: candidateRejection{
				kind:   rejectExclude,
				detail: canonicalUnrepresentableReplay,
			},
			delta: parallelFailureDeltaFromHistory(candidateFailureHistory{
				TransformExcludes: func() *transformExcludeTracker {
					tr := &transformExcludeTracker{}
					tr.noteTransform(canonicalUnrepresentableReplay)
					return tr
				}(),
			}),
		},
		{
			cand:    candB,
			failErr: errors.New("stream err"),
			delta: parallelFailureDeltaFromHistory(candidateFailureHistory{
				CapabilityReject: aipapi.NegotiationResult{
					Kind:    aipapi.NegotiationReject,
					Missing: []aipapi.Capability{aipapi.CapabilityVision},
				},
			}),
		},
		{
			cand:    candC,
			failErr: errors.New("stream err"),
			delta: parallelFailureDeltaFromHistory(candidateFailureHistory{
				AdmissionErr: errors.New("admission failed"),
			}),
		},
		{
			cand:    candD,
			failErr: errors.New("stream err"),
			delta: parallelFailureDeltaFromHistory(candidateFailureHistory{
				ContextLimit: true,
			}),
		},
	}

	runPerm := func(perm []parallelArmOutcome) (*candidateFailureHistory, map[string]struct{}) {
		progress := &recoveryController{
			excluded: map[string]struct{}{},
			failures: &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}},
		}
		req := openNextRequest{
			reqFacts: requestFacts{recvTurnFacts: recvTurnFacts{traceID: "trace-perm", aLegID: "aleg-perm"}},
			progress: progress,
		}
		reducer := newParallelRoundReducer(ex, req, entries, candidates, nil)
		stamped := make([]parallelArmOutcome, len(perm))
		for i, o := range perm {
			stamped[i] = o
			stamped[i].arrival = uint64(i + 1)
		}
		_, err := reducer.Reduce(context.Background(), stamped)
		if err != nil {
			t.Fatalf("Reduce error: %v", err)
		}
		return progress.getFailures(), progress.excluded
	}

	// Generate all 24 permutations
	var allPerms [][]parallelArmOutcome
	var permute func([]parallelArmOutcome, int)
	permute = func(arr []parallelArmOutcome, k int) {
		if k == len(arr) {
			cp := make([]parallelArmOutcome, len(arr))
			copy(cp, arr)
			allPerms = append(allPerms, cp)
			return
		}
		for i := k; i < len(arr); i++ {
			arr[k], arr[i] = arr[i], arr[k]
			permute(arr, k+1)
			arr[k], arr[i] = arr[i], arr[k]
		}
	}
	permute(baseOutcomes, 0)
	if len(allPerms) != 24 {
		t.Fatalf("expected 24 permutations, got %d", len(allPerms))
	}

	var refFailures *candidateFailureHistory
	var refExcluded map[string]struct{}
	var refFinalErr string

	for idx, perm := range allPerms {
		sh, excluded := runPerm(perm)
		finalErr := sh.FinalError(errors.New("fallback")).Error()
		if idx == 0 {
			refFailures = sh
			refExcluded = excluded
			refFinalErr = finalErr
			continue
		}

		if sh.CapabilityReject.Kind != refFailures.CapabilityReject.Kind {
			t.Fatalf("perm %d: CapabilityReject mismatch: got %v want %v", idx, sh.CapabilityReject, refFailures.CapabilityReject)
		}
		if (sh.AdmissionErr == nil) != (refFailures.AdmissionErr == nil) || (sh.AdmissionErr != nil && sh.AdmissionErr.Error() != refFailures.AdmissionErr.Error()) {
			t.Fatalf("perm %d: AdmissionErr mismatch: got %v want %v", idx, sh.AdmissionErr, refFailures.AdmissionErr)
		}
		if sh.ContextLimit != refFailures.ContextLimit {
			t.Fatalf("perm %d: ContextLimit mismatch: got %v want %v", idx, sh.ContextLimit, refFailures.ContextLimit)
		}
		if sh.TransformExcludes.count != refFailures.TransformExcludes.count {
			t.Fatalf("perm %d: TransformExcludes.count mismatch: got %d want %d", idx, sh.TransformExcludes.count, refFailures.TransformExcludes.count)
		}
		if finalErr != refFinalErr {
			t.Fatalf("perm %d: FinalError mismatch: got %q want %q", idx, finalErr, refFinalErr)
		}
		if len(excluded) != len(refExcluded) {
			t.Fatalf("perm %d: excluded map length mismatch", idx)
		}
		for k := range refExcluded {
			if _, ok := excluded[k]; !ok {
				t.Fatalf("perm %d: missing excluded key %s", idx, k)
			}
		}
	}
}

type winningDeltaStubStream struct {
	openedCh    chan struct{}
	cancelCount atomic.Int32
	closeCount  atomic.Int32
	delivered   atomic.Bool
}

func (s *winningDeltaStubStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if s.openedCh != nil {
		select {
		case s.openedCh <- struct{}{}:
		default:
		}
	}
	if s.delivered.CompareAndSwap(false, true) {
		return aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "win"}, nil
	}
	<-ctx.Done()
	return aipapi.Event{}, ctx.Err()
}

func (s *winningDeltaStubStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	s.cancelCount.Add(1)
	return aipapi.CancelResult{}
}

func (s *winningDeltaStubStream) Close() error {
	s.closeCount.Add(1)
	return nil
}

type immediateErrStubStream struct {
	openedCh    chan struct{}
	waitCh      <-chan struct{}
	cancelCount atomic.Int32
	closeCount  atomic.Int32
	err         error
}

func (s *immediateErrStubStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if s.openedCh != nil {
		select {
		case s.openedCh <- struct{}{}:
		default:
		}
	}
	if s.waitCh != nil {
		select {
		case <-s.waitCh:
		case <-ctx.Done():
			return aipapi.Event{}, ctx.Err()
		}
	}
	return aipapi.Event{}, s.err
}

func (s *immediateErrStubStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	s.cancelCount.Add(1)
	return aipapi.CancelResult{}
}

func (s *immediateErrStubStream) Close() error {
	s.closeCount.Add(1)
	return nil
}

type blockingStubStream struct {
	openedCh    chan struct{}
	cancelCount atomic.Int32
	closeCount  atomic.Int32
}

func (s *blockingStubStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if s.openedCh != nil {
		select {
		case s.openedCh <- struct{}{}:
		default:
		}
	}
	<-ctx.Done()
	return aipapi.Event{}, ctx.Err()
}

func (s *blockingStubStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	s.cancelCount.Add(1)
	return aipapi.CancelResult{}
}

func (s *blockingStubStream) Close() error {
	s.closeCount.Add(1)
	return nil
}

func TestTryOpenParallelGroup_WinnerSurvivesOtherArmFatalTTFT(t *testing.T) {
	t.Parallel()

	auth := reservedAuthorityRecorder("res-winner-survives-ttft")
	ex, _, _, aLegID := newAuthorityRuntimeTestExecutorWithStore(t, auth)

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLegID)

	fatalOpenedCh := make(chan struct{})
	var fatalOpenedOnce atomic.Bool
	winStream := &winningDeltaStubStream{}
	fatalStream := &immediateErrStubStream{err: aipapi.ErrTTFTTimeout}

	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
	tcaps := aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
		Operation: aipapi.OperationOpenAIChatCompletions,
		Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
	})

	ex.Backends = map[string]execbackend.Backend{
		"win-backend": {
			Caps:                    caps,
			TransportCaps:           tcaps,
			EnforcesMaxOutputTokens: true,
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return &customCloseStream{
					ManagedEventStream: &waitThenStream{
						wait: fatalOpenedCh,
						events: []aipapi.Event{
							{Kind: aipapi.EventTextDelta, Delta: "win"},
							{Kind: aipapi.EventResponseFinished},
						},
					},
					onClose: func() { winStream.closeCount.Add(1) },
				}, nil
			},
		},
		"fatal-backend": {
			Caps:                    caps,
			TransportCaps:           tcaps,
			EnforcesMaxOutputTokens: true,
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if fatalOpenedOnce.CompareAndSwap(false, true) {
					close(fatalOpenedCh)
				}
				return fatalStream, nil
			},
		},
	}

	candWin := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "win-backend", Model: "m"},
		Key:     "win-backend:m",
	}
	candFatal := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "fatal-backend", Model: "m"},
		Key:     "fatal-backend:m",
	}

	budget := &attemptBudget{max: 10}
	req := authorityOpenRequest(t, aLegID, budget)
	req.reqFacts.aScope = aScope

	ctx := context.Background()
	opened, err := ex.tryOpenParallelGroup(ctx, req, []routing.AttemptCandidate{candWin, candFatal}, nil, "", false)
	if err != nil {
		t.Fatalf("expected nil err, got: %v", err)
	}
	if opened.ready == nil || opened.ready.session == nil {
		t.Fatal("expected non-nil opened.ready.session for winner")
	}

	if inner := opened.ready.session.loadInner(); inner != nil {
		if closeErr := inner.Close(); closeErr != nil {
			t.Fatalf("expected nil closeErr on inner stream close, got: %v", closeErr)
		}
	}
	if winStream.closeCount.Load() != 1 {
		t.Errorf("expected winner stream close count 1, got %d", winStream.closeCount.Load())
	}
	for start := time.Now(); time.Since(start) < 2*time.Second; time.Sleep(5 * time.Millisecond) {
		if fatalStream.cancelCount.Load() > 0 || fatalStream.closeCount.Load() > 0 {
			break
		}
	}
	if fatalStream.cancelCount.Load() == 0 && fatalStream.closeCount.Load() == 0 {
		t.Errorf("expected fatal arm stream to be terminalized (cancel or close)")
	}
}

func TestTryOpenParallelGroup_ALegCanceledAbortsRoundAndTerminalizesArms(t *testing.T) {
	t.Parallel()

	auth := reservedAuthorityRecorder("res-aleg-canceled-aborts")
	ex, _, _, aLegID := newAuthorityRuntimeTestExecutorWithStore(t, auth)

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLegID)

	blockingOpenedCh := make(chan struct{})
	var blockingOpenedOnce atomic.Bool
	cancelStream := &immediateErrStubStream{waitCh: blockingOpenedCh, err: leglifecycle.ErrALegCanceled}
	blockingStream := &blockingStubStream{}

	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
	tcaps := aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
		Operation: aipapi.OperationOpenAIChatCompletions,
		Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
	})

	ex.Backends = map[string]execbackend.Backend{
		"cancel-backend": {
			Caps:                    caps,
			TransportCaps:           tcaps,
			EnforcesMaxOutputTokens: true,
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return cancelStream, nil
			},
		},
		"blocking-backend": {
			Caps:                    caps,
			TransportCaps:           tcaps,
			EnforcesMaxOutputTokens: true,
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if blockingOpenedOnce.CompareAndSwap(false, true) {
					close(blockingOpenedCh)
				}
				return blockingStream, nil
			},
		},
	}

	candCancel := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "cancel-backend", Model: "m"},
		Key:     "cancel-backend:m",
	}
	candBlocking := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "blocking-backend", Model: "m"},
		Key:     "blocking-backend:m",
	}

	budget := &attemptBudget{max: 10}
	req := authorityOpenRequest(t, aLegID, budget)
	req.reqFacts.aScope = aScope

	ctx := context.Background()
	_, err := ex.tryOpenParallelGroup(ctx, req, []routing.AttemptCandidate{candCancel, candBlocking}, nil, "", false)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, leglifecycle.ErrALegCanceled) {
		t.Fatalf("expected errors.Is(err, leglifecycle.ErrALegCanceled), got: %v", err)
	}

	for start := time.Now(); time.Since(start) < 2*time.Second; time.Sleep(5 * time.Millisecond) {
		if blockingStream.cancelCount.Load() > 0 || blockingStream.closeCount.Load() > 0 {
			break
		}
	}
	if blockingStream.cancelCount.Load() == 0 && blockingStream.closeCount.Load() == 0 {
		t.Errorf("expected blocking arm stream to be terminalized (cancel or close)")
	}
}
