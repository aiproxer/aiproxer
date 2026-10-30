package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

func TestPhase5_ParallelRoundReducer_DeterministicWinnerSelection(t *testing.T) {
	t.Parallel()

	ex := TestExecutor()
	candA := routing.AttemptCandidate{Key: "cand-a", Primary: routing.Primary{Backend: "b-a", Model: "m-a"}}
	candB := routing.AttemptCandidate{Key: "cand-b", Primary: routing.Primary{Backend: "b-b", Model: "m-b"}}
	candC := routing.AttemptCandidate{Key: "cand-c", Primary: routing.Primary{Backend: "b-c", Model: "m-c"}}

	entries := []legEntry{
		{cand: candA, delay: 0},
		{cand: candB, delay: 0},
		{cand: candC, delay: 0},
	}
	candidates := []routing.AttemptCandidate{candA, candB, candC}

	req := openNextRequest{
		reqFacts: requestFacts{
			recvTurnFacts: recvTurnFacts{traceID: "trace-1", aLegID: "aleg-1"},
		},
		routeFacts: routeFacts{},
		progress: &recoveryController{
			excluded: map[string]struct{}{},
			failures: &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}},
		},
	}

	sessionA := &attemptSession{terminal: newStreamTerminal(sdkterminal.ScopeAttempt), bleg: b2bua.BLegRecord{BLegID: "bleg-a"}}
	sessionB := &attemptSession{terminal: newStreamTerminal(sdkterminal.ScopeAttempt), bleg: b2bua.BLegRecord{BLegID: "bleg-b"}}
	sessionC := &attemptSession{terminal: newStreamTerminal(sdkterminal.ScopeAttempt), bleg: b2bua.BLegRecord{BLegID: "bleg-c"}}

	readyA := &readyAttempt{session: sessionA}
	readyB := &readyAttempt{session: sessionB}
	readyC := &readyAttempt{session: sessionC}

	// B arrived first (arrival: 1), A arrived second (arrival: 2), C arrived third (arrival: 3)
	outcomeA := parallelArmOutcome{cand: candA, ready: readyA, arrival: 2, winnerBuf: []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "resp-a"}}}
	outcomeB := parallelArmOutcome{cand: candB, ready: readyB, arrival: 1, winnerBuf: []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "resp-b"}}}
	outcomeC := parallelArmOutcome{cand: candC, ready: readyC, arrival: 3, winnerBuf: []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "resp-c"}}}

	// Even if collected outcomes are in permutation [A, C, B], B must always win due to earliest arrival
	collected := []parallelArmOutcome{outcomeA, outcomeC, outcomeB}

	reducer := newParallelRoundReducer(ex, req, entries, candidates, nil)
	opened, err := reducer.Reduce(context.Background(), collected)
	if err != nil {
		t.Fatalf("unexpected reducer error: %v", err)
	}

	if opened.ready.session != sessionB {
		t.Fatalf("expected earliest arrival (candB) to win, got session %v", opened.ready.session)
	}

	// Ready A and Ready C must be disposed
	if !readyA.IsConsumed() {
		t.Errorf("expected losing readyA to be marked consumed/disposed")
	}
	if !readyC.IsConsumed() {
		t.Errorf("expected losing readyC to be marked consumed/disposed")
	}
}

func TestPhase5_ParallelRoundReducer_StableFailureMergeOrder(t *testing.T) {
	t.Parallel()

	ex := TestExecutor()
	cand1 := routing.AttemptCandidate{Key: "cand-1", Primary: routing.Primary{Backend: "b-1", Model: "m-1"}}
	cand2 := routing.AttemptCandidate{Key: "cand-2", Primary: routing.Primary{Backend: "b-2", Model: "m-2"}}
	cand3 := routing.AttemptCandidate{Key: "cand-3", Primary: routing.Primary{Backend: "b-3", Model: "m-3"}}

	entries := []legEntry{
		{cand: cand1, delay: 0},
		{cand: cand2, delay: 0},
		{cand: cand3, delay: 0},
	}
	candidates := []routing.AttemptCandidate{cand1, cand2, cand3}

	progress := &recoveryController{
		excluded: map[string]struct{}{},
		failures: &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}},
	}
	req := openNextRequest{
		reqFacts: requestFacts{
			recvTurnFacts: recvTurnFacts{traceID: "trace-2", aLegID: "aleg-2"},
		},
		routeFacts: routeFacts{},
		progress:   progress,
	}

	// Outcomes arrive in order [3, 1, 2] — reducer must apply in entries order [1,2,3] regardless of arrival.
	outcome3 := parallelArmOutcome{
		cand:    cand3,
		failErr: errors.New("stream err 3"),
		arrival: 1,
		delta:   parallelFailureDeltaFromHistory(candidateFailureHistory{ContextLimit: true}),
	}
	outcome1 := parallelArmOutcome{
		cand:      cand1,
		rejected:  true,
		rejection: candidateRejection{kind: rejectAdmission, detail: aipapi.CandidateAdmissionResult{Kind: aipapi.NegotiationReject, Capability: aipapi.NegotiationResult{Kind: aipapi.NegotiationReject, Missing: []aipapi.Capability{aipapi.CapabilityVision}}}},
		arrival:   2,
		delta:     parallelFailureDeltaFromHistory(candidateFailureHistory{CapabilityReject: aipapi.NegotiationResult{Kind: aipapi.NegotiationReject, Missing: []aipapi.Capability{aipapi.CapabilityVision}}}),
	}
	outcome2 := parallelArmOutcome{
		cand:    cand2,
		failErr: errors.New("stream err 2"),
		arrival: 3,
		delta:   parallelFailureDeltaFromHistory(candidateFailureHistory{}),
	}

	collected := []parallelArmOutcome{outcome3, outcome1, outcome2}

	reducer := newParallelRoundReducer(ex, req, entries, candidates, nil)
	opened, err := reducer.Reduce(context.Background(), collected)
	if err != nil {
		t.Fatalf("unexpected reduce error: %v", err)
	}
	if opened.ready != nil && opened.ready.session != nil {
		t.Fatalf("expected nil session on all-failed round")
	}

	// Verify all candidates are excluded
	for _, c := range candidates {
		if _, ok := progress.excluded[c.Key]; !ok {
			t.Errorf("expected candidate %q to be in excluded map", c.Key)
		}
	}

	// Verify failure history has CapabilityReject and ContextLimit merged
	sh := progress.getFailures()
	if sh.CapabilityReject.Kind != aipapi.NegotiationReject {
		t.Errorf("expected CapabilityReject to be recorded in failure history")
	}
	if !sh.ContextLimit {
		t.Errorf("expected ContextLimit to be recorded in failure history")
	}
	if sh.ParallelFailure == nil {
		t.Errorf("expected ParallelFailure to be recorded on all-failure round")
	}

	// Precedence check: FinalError must return CapabilityReject before ContextLimit or ParallelFailure
	finalErr := sh.FinalError(errors.New("base"))
	var rejErr *aipapi.RejectError
	if finalErr == nil || !errors.As(finalErr, &rejErr) {
		t.Errorf("expected FinalError to prioritize CapabilityReject (*aipapi.RejectError), got %v", finalErr)
	}
}

func TestPhase5_ParallelRoundReducer_ContextCancellation_DisposesAll(t *testing.T) {
	t.Parallel()

	ex := TestExecutor()
	candA := routing.AttemptCandidate{Key: "cand-a", Primary: routing.Primary{Backend: "b-a", Model: "m-a"}}
	entries := []legEntry{{cand: candA, delay: 0}}
	candidates := []routing.AttemptCandidate{candA}

	req := openNextRequest{
		reqFacts: requestFacts{
			recvTurnFacts: recvTurnFacts{traceID: "trace-3", aLegID: "aleg-3"},
		},
		progress: &recoveryController{
			excluded: map[string]struct{}{},
			failures: &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}},
		},
	}

	sessionA := &attemptSession{terminal: newStreamTerminal(sdkterminal.ScopeAttempt), bleg: b2bua.BLegRecord{BLegID: "bleg-a"}}
	readyA := &readyAttempt{session: sessionA}
	outcomeA := parallelArmOutcome{cand: candA, ready: readyA, arrival: 1}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Already canceled

	reducer := newParallelRoundReducer(ex, req, entries, candidates, nil)
	_, err := reducer.Reduce(ctx, []parallelArmOutcome{outcomeA})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled error, got %v", err)
	}

	if !readyA.IsConsumed() {
		t.Errorf("expected readyA to be disposed on context cancellation")
	}
}

func TestPhase5_ParallelRoundReducer_FatalError_WinnerSurvives(t *testing.T) {
	t.Parallel()

	ex := TestExecutor()
	candA := routing.AttemptCandidate{Key: "cand-a", Primary: routing.Primary{Backend: "b-a", Model: "m-a"}}
	candB := routing.AttemptCandidate{Key: "cand-b", Primary: routing.Primary{Backend: "b-b", Model: "m-b"}}
	entries := []legEntry{{cand: candA, delay: 0}, {cand: candB, delay: 0}}
	candidates := []routing.AttemptCandidate{candA, candB}

	req := openNextRequest{
		reqFacts: requestFacts{
			recvTurnFacts: recvTurnFacts{traceID: "trace-4", aLegID: "aleg-4"},
		},
		progress: &recoveryController{
			excluded: map[string]struct{}{},
			failures: &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}},
		},
	}

	sessionA := &attemptSession{terminal: newStreamTerminal(sdkterminal.ScopeAttempt), bleg: b2bua.BLegRecord{BLegID: "bleg-a"}}
	readyA := &readyAttempt{session: sessionA}

	// Arm A succeeded (ready), but Arm B produced a fatal TTFT error.
	// Winner (Arm A) must survive; loser's fatal error must not abort the round.
	outcomeA := parallelArmOutcome{cand: candA, ready: readyA, arrival: 1}
	outcomeB := parallelArmOutcome{cand: candB, failErr: aipapi.ErrTTFTTimeout, arrival: 2}

	reducer := newParallelRoundReducer(ex, req, entries, candidates, nil)
	opened, err := reducer.Reduce(context.Background(), []parallelArmOutcome{outcomeA, outcomeB})
	if err != nil {
		t.Fatalf("expected nil error (winner survives loser fatal), got %v", err)
	}

	if opened.ready != readyA {
		t.Fatalf("expected winner readyA, got %v", opened.ready)
	}
	if opened.ready.session != sessionA {
		t.Fatalf("expected winner sessionA via compat, got %v", opened.ready.session)
	}
	if readyA.IsConsumed() {
		t.Errorf("expected readyA to remain unpublished through winner effects (not yet consumed)")
	}
	if readyA.session != sessionA {
		t.Errorf("expected readyA session to still be attached before publication")
	}
}

func TestPhase5_ParallelRoundReducer_FatalError_NoWinner_AbortsRound(t *testing.T) {
	t.Parallel()

	ex := TestExecutor()
	candA := routing.AttemptCandidate{Key: "cand-a", Primary: routing.Primary{Backend: "b-a", Model: "m-a"}}
	candB := routing.AttemptCandidate{Key: "cand-b", Primary: routing.Primary{Backend: "b-b", Model: "m-b"}}
	entries := []legEntry{{cand: candA, delay: 0}, {cand: candB, delay: 0}}
	candidates := []routing.AttemptCandidate{candA, candB}

	req := openNextRequest{
		reqFacts: requestFacts{
			recvTurnFacts: recvTurnFacts{traceID: "trace-fatal-no-winner", aLegID: "aleg-fatal"},
		},
		progress: &recoveryController{
			excluded: map[string]struct{}{},
			failures: &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}},
		},
	}

	// Arm A failed with non-fatal stream error, Arm B produced fatal ErrTTFTTimeout.
	outcomeA := parallelArmOutcome{cand: candA, failErr: errors.New("stream err a"), arrival: 1}
	outcomeB := parallelArmOutcome{cand: candB, failErr: aipapi.ErrTTFTTimeout, arrival: 2}

	reducer := newParallelRoundReducer(ex, req, entries, candidates, nil)
	_, err := reducer.Reduce(context.Background(), []parallelArmOutcome{outcomeA, outcomeB})
	if err == nil || !errors.Is(err, aipapi.ErrTTFTTimeout) {
		t.Fatalf("expected fatal TTFT error when no winner exists, got %v", err)
	}
}

type dummyTestStream struct{}

func (dummyTestStream) Recv(context.Context) (aipapi.Event, error) { return aipapi.Event{}, nil }
func (dummyTestStream) Close() error                               { return nil }
func (dummyTestStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func TestPhase5_ParallelRoundReducer_WinnerSurvivesEarlierLoserFatal(t *testing.T) {
	t.Parallel()

	ex := TestExecutor()
	candA := routing.AttemptCandidate{Key: "cand-a", Primary: routing.Primary{Backend: "b-a", Model: "m-a"}}
	candB := routing.AttemptCandidate{Key: "cand-b", Primary: routing.Primary{Backend: "b-b", Model: "m-b"}}
	entries := []legEntry{{cand: candA, delay: 0}, {cand: candB, delay: 0}}
	candidates := []routing.AttemptCandidate{candA, candB}

	req := openNextRequest{
		reqFacts: requestFacts{
			recvTurnFacts: recvTurnFacts{traceID: "trace-early-fatal", aLegID: "aleg-early-fatal"},
		},
		progress: &recoveryController{
			excluded: map[string]struct{}{},
			failures: &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}},
		},
	}

	sessionA := &attemptSession{terminal: newStreamTerminal(sdkterminal.ScopeAttempt), bleg: b2bua.BLegRecord{BLegID: "bleg-a"}}
	readyA := &readyAttempt{session: sessionA}

	streamA := dummyTestStream{}
	streamB := dummyTestStream{}

	armLegA := &parallelLeg{
		cand:   candA,
		bleg:   b2bua.BLegRecord{BLegID: "bleg-a"},
		stream: streamA,
	}

	txB := &attemptTx{
		e:             ex,
		stream:        streamB,
		bleg:          b2bua.BLegRecord{BLegID: "bleg-b"},
		cand:          candB,
		reqFacts:      req.reqFacts,
		authLifecycle: ex.newAttemptAuthorityLifecycle(attemptAuthorityState{}, candB),
	}

	armLegB := &parallelLeg{
		cand:   candB,
		bleg:   b2bua.BLegRecord{BLegID: "bleg-b"},
		stream: streamB,
		tx:     txB,
	}

	// Arm B arrived earlier (arrival: 1) with fatal ErrTTFTTimeout.
	// Arm A arrived later (arrival: 2) with ready attempt.
	outcomeB := parallelArmOutcome{cand: candB, failErr: aipapi.ErrTTFTTimeout, arrival: 1, armLeg: armLegB}
	outcomeA := parallelArmOutcome{cand: candA, ready: readyA, arrival: 2, armLeg: armLegA, winnerBuf: []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "win"}}}

	collected := []parallelArmOutcome{outcomeB, outcomeA}

	reducer := newParallelRoundReducer(ex, req, entries, candidates, nil)
	opened, err := reducer.Reduce(context.Background(), collected)
	if err != nil {
		t.Fatalf("unexpected error (winner must survive earlier loser fatal): %v", err)
	}

	if opened.ready.session != sessionA {
		t.Fatalf("expected winner sessionA, got %v", opened.ready.session)
	}
}

func TestPhase5_ParallelRoundReducer_SingleAttemptRecordPerOpenedLeg(t *testing.T) {
	t.Parallel()

	type logEntry struct {
		params recordAttemptParams
		attrs  diag.AttrOpts
	}

	setupTest := func(t *testing.T) (*Executor, *[]logEntry) {
		t.Helper()
		ex := TestExecutor()
		var logs []logEntry
		ex.Store = &recordingAttemptStore{onRecord: func(ctx context.Context, rec aipapi.AttemptRecord) {}}
		return ex, &logs
	}

	t.Run("winner_path_records_loser_exactly_once", func(t *testing.T) {
		ex, _ := setupTest(t)
		candA := routing.AttemptCandidate{Key: "cand-a", Primary: routing.Primary{Backend: "b-a", Model: "m-a"}}
		candB := routing.AttemptCandidate{Key: "cand-b", Primary: routing.Primary{Backend: "b-b", Model: "m-b"}}
		candC := routing.AttemptCandidate{Key: "cand-c", Primary: routing.Primary{Backend: "b-c", Model: "m-c"}} // Unopened

		entries := []legEntry{{cand: candA, delay: 0}, {cand: candB, delay: 0}, {cand: candC, delay: 0}}
		candidates := []routing.AttemptCandidate{candA, candB, candC}

		var recordedParams []recordAttemptParams
		recordFn := func(ctx context.Context, p recordAttemptParams, opts diag.AttrOpts) {
			recordedParams = append(recordedParams, p)
		}

		req := openNextRequest{
			reqFacts: requestFacts{
				recvTurnFacts: recvTurnFacts{traceID: "trace-rec-1", aLegID: "aleg-rec-1"},
			},
			progress: &recoveryController{
				excluded: map[string]struct{}{},
				failures: &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}},
			},
		}

		sessionA := &attemptSession{
			terminal:              newStreamTerminal(sdkterminal.ScopeAttempt),
			bleg:                  b2bua.BLegRecord{BLegID: "bleg-a"},
			cand:                  candA,
			recordAttemptLoggedFn: recordFn,
		}
		readyA := &readyAttempt{session: sessionA}

		streamB := dummyTestStream{}
		txB := &attemptTx{
			e:                     ex,
			stream:                streamB,
			bleg:                  b2bua.BLegRecord{BLegID: "bleg-b"},
			cand:                  candB,
			reqFacts:              req.reqFacts,
			authLifecycle:         ex.newAttemptAuthorityLifecycle(attemptAuthorityState{}, candB),
			recordAttemptLoggedFn: recordFn,
		}

		armLegA := &parallelLeg{cand: candA, bleg: b2bua.BLegRecord{BLegID: "bleg-a"}, stream: dummyTestStream{}}
		armLegB := &parallelLeg{cand: candB, bleg: b2bua.BLegRecord{BLegID: "bleg-b"}, stream: streamB, tx: txB, recvErr: errors.New("custom loser failure")}

		outcomeA := parallelArmOutcome{cand: candA, ready: readyA, arrival: 1, armLeg: armLegA, winnerBuf: []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "win"}}}
		outcomeB := parallelArmOutcome{cand: candB, failErr: armLegB.recvErr, arrival: 2, armLeg: armLegB}
		outcomeC := parallelArmOutcome{cand: candC, failErr: errors.New("never opened"), arrival: 3} // armLeg == nil (stream never opened)

		reducer := newParallelRoundReducer(ex, req, entries, candidates, nil)
		opened, err := reducer.Reduce(context.Background(), []parallelArmOutcome{outcomeA, outcomeB, outcomeC})
		if err != nil {
			t.Fatalf("Reduce: %v", err)
		}
		if opened.ready.session != sessionA {
			t.Fatalf("expected sessionA winner")
		}

		// Wait briefly for loser cleanup goroutine to finish
		if bstream, ok := opened.ready.session.inner.(*parallelBridgeStream); ok && bstream.losersDone != nil {
			<-bstream.losersDone
		}

		// Exactly ONE AttemptRecord for bleg-b, ZERO for bleg-c (never opened)
		if len(recordedParams) != 1 {
			t.Fatalf("expected exactly 1 AttemptRecord for loser leg, got %d: %+v", len(recordedParams), recordedParams)
		}
		if recordedParams[0].BLeg.BLegID != "bleg-b" {
			t.Errorf("expected recorded BLegID 'bleg-b', got %s", recordedParams[0].BLeg.BLegID)
		}
		if recordedParams[0].Outcome != aipapi.AttemptSwallowedFailure {
			t.Errorf("expected outcome AttemptSwallowedFailure, got %s", recordedParams[0].Outcome)
		}
		if recordedParams[0].Reason != attemptReasonDetail(armLegB.recvErr) {
			t.Errorf("expected reason %q, got %q", attemptReasonDetail(armLegB.recvErr), recordedParams[0].Reason)
		}
	})

	t.Run("no_winner_path_records_each_opened_leg_exactly_once", func(t *testing.T) {
		ex, _ := setupTest(t)
		candA := routing.AttemptCandidate{Key: "cand-a", Primary: routing.Primary{Backend: "b-a", Model: "m-a"}}
		candB := routing.AttemptCandidate{Key: "cand-b", Primary: routing.Primary{Backend: "b-b", Model: "m-b"}}
		candC := routing.AttemptCandidate{Key: "cand-c", Primary: routing.Primary{Backend: "b-c", Model: "m-c"}} // Unopened

		entries := []legEntry{{cand: candA, delay: 0}, {cand: candB, delay: 0}, {cand: candC, delay: 0}}
		candidates := []routing.AttemptCandidate{candA, candB, candC}

		var recordedParams []recordAttemptParams
		recordFn := func(ctx context.Context, p recordAttemptParams, opts diag.AttrOpts) {
			recordedParams = append(recordedParams, p)
		}

		req := openNextRequest{
			reqFacts: requestFacts{
				recvTurnFacts: recvTurnFacts{traceID: "trace-rec-2", aLegID: "aleg-rec-2"},
			},
			progress: &recoveryController{
				excluded: map[string]struct{}{},
				failures: &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}},
			},
		}

		txA := &attemptTx{
			e:                     ex,
			stream:                dummyTestStream{},
			bleg:                  b2bua.BLegRecord{BLegID: "bleg-a"},
			cand:                  candA,
			reqFacts:              req.reqFacts,
			authLifecycle:         ex.newAttemptAuthorityLifecycle(attemptAuthorityState{}, candA),
			recordAttemptLoggedFn: recordFn,
		}
		txB := &attemptTx{
			e:                     ex,
			stream:                dummyTestStream{},
			bleg:                  b2bua.BLegRecord{BLegID: "bleg-b"},
			cand:                  candB,
			reqFacts:              req.reqFacts,
			authLifecycle:         ex.newAttemptAuthorityLifecycle(attemptAuthorityState{}, candB),
			recordAttemptLoggedFn: recordFn,
		}

		armLegA := &parallelLeg{cand: candA, bleg: b2bua.BLegRecord{BLegID: "bleg-a"}, stream: txA.stream, tx: txA, recvErr: errors.New("err a")}
		armLegB := &parallelLeg{cand: candB, bleg: b2bua.BLegRecord{BLegID: "bleg-b"}, stream: txB.stream, tx: txB, recvErr: context.Canceled}

		outcomeA := parallelArmOutcome{cand: candA, failErr: armLegA.recvErr, arrival: 1, armLeg: armLegA}
		outcomeB := parallelArmOutcome{cand: candB, failErr: armLegB.recvErr, arrival: 2, armLeg: armLegB}
		outcomeC := parallelArmOutcome{cand: candC, failErr: errors.New("err c"), arrival: 3} // never opened

		reducer := newParallelRoundReducer(ex, req, entries, candidates, nil)
		_, err := reducer.Reduce(context.Background(), []parallelArmOutcome{outcomeA, outcomeB, outcomeC})
		if err != nil {
			t.Fatalf("Reduce: %v", err)
		}

		// Exactly TWO AttemptRecords: one for bleg-a, one for bleg-b, none for bleg-c
		if len(recordedParams) != 2 {
			t.Fatalf("expected exactly 2 AttemptRecords, got %d: %+v", len(recordedParams), recordedParams)
		}
		bLegs := map[string]recordAttemptParams{
			recordedParams[0].BLeg.BLegID: recordedParams[0],
			recordedParams[1].BLeg.BLegID: recordedParams[1],
		}
		if recA, ok := bLegs["bleg-a"]; !ok {
			t.Errorf("missing record for bleg-a")
		} else if recA.Outcome != aipapi.AttemptSwallowedFailure {
			t.Errorf("expected bleg-a outcome AttemptSwallowedFailure, got %s", recA.Outcome)
		}
		if recB, ok := bLegs["bleg-b"]; !ok {
			t.Errorf("missing record for bleg-b")
		} else if recB.Outcome != aipapi.AttemptCancelled {
			t.Errorf("expected bleg-b outcome AttemptCancelled, got %s", recB.Outcome)
		}
	})

	t.Run("fatal_teardown_records_each_opened_leg_exactly_once", func(t *testing.T) {
		ex, _ := setupTest(t)
		candA := routing.AttemptCandidate{Key: "cand-a", Primary: routing.Primary{Backend: "b-a", Model: "m-a"}}
		candB := routing.AttemptCandidate{Key: "cand-b", Primary: routing.Primary{Backend: "b-b", Model: "m-b"}}

		entries := []legEntry{{cand: candA, delay: 0}, {cand: candB, delay: 0}}
		candidates := []routing.AttemptCandidate{candA, candB}

		var recordedParams []recordAttemptParams
		recordFn := func(ctx context.Context, p recordAttemptParams, opts diag.AttrOpts) {
			recordedParams = append(recordedParams, p)
		}

		req := openNextRequest{
			reqFacts: requestFacts{
				recvTurnFacts: recvTurnFacts{traceID: "trace-rec-fatal", aLegID: "aleg-rec-fatal"},
			},
			progress: &recoveryController{
				excluded: map[string]struct{}{},
				failures: &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}},
			},
		}

		txA := &attemptTx{
			e:                     ex,
			stream:                dummyTestStream{},
			bleg:                  b2bua.BLegRecord{BLegID: "bleg-a"},
			cand:                  candA,
			reqFacts:              req.reqFacts,
			authLifecycle:         ex.newAttemptAuthorityLifecycle(attemptAuthorityState{}, candA),
			recordAttemptLoggedFn: recordFn,
		}

		armLegA := &parallelLeg{cand: candA, bleg: b2bua.BLegRecord{BLegID: "bleg-a"}, stream: txA.stream, tx: txA, recvErr: errors.New("err a")}
		outcomeA := parallelArmOutcome{cand: candA, failErr: armLegA.recvErr, arrival: 1, armLeg: armLegA}
		outcomeB := parallelArmOutcome{cand: candB, failErr: aipapi.ErrTTFTTimeout, arrival: 2}

		reducer := newParallelRoundReducer(ex, req, entries, candidates, nil)
		_, err := reducer.Reduce(context.Background(), []parallelArmOutcome{outcomeA, outcomeB})
		if err == nil || !errors.Is(err, aipapi.ErrTTFTTimeout) {
			t.Fatalf("expected fatal error, got %v", err)
		}

		if len(recordedParams) != 1 {
			t.Fatalf("expected exactly 1 AttemptRecord for bleg-a, got %d", len(recordedParams))
		}
		if recordedParams[0].BLeg.BLegID != "bleg-a" {
			t.Errorf("expected bleg-a, got %s", recordedParams[0].BLeg.BLegID)
		}
	})

	t.Run("context_cancellation_teardown_records_each_opened_leg_exactly_once", func(t *testing.T) {
		ex, _ := setupTest(t)
		candA := routing.AttemptCandidate{Key: "cand-a", Primary: routing.Primary{Backend: "b-a", Model: "m-a"}}

		entries := []legEntry{{cand: candA, delay: 0}}
		candidates := []routing.AttemptCandidate{candA}

		var recordedParams []recordAttemptParams
		recordFn := func(ctx context.Context, p recordAttemptParams, opts diag.AttrOpts) {
			recordedParams = append(recordedParams, p)
		}

		req := openNextRequest{
			reqFacts: requestFacts{
				recvTurnFacts: recvTurnFacts{traceID: "trace-rec-cancel", aLegID: "aleg-rec-cancel"},
			},
			progress: &recoveryController{
				excluded: map[string]struct{}{},
				failures: &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}},
			},
		}

		txA := &attemptTx{
			e:                     ex,
			stream:                dummyTestStream{},
			bleg:                  b2bua.BLegRecord{BLegID: "bleg-a"},
			cand:                  candA,
			reqFacts:              req.reqFacts,
			authLifecycle:         ex.newAttemptAuthorityLifecycle(attemptAuthorityState{}, candA),
			recordAttemptLoggedFn: recordFn,
		}

		armLegA := &parallelLeg{cand: candA, bleg: b2bua.BLegRecord{BLegID: "bleg-a"}, stream: txA.stream, tx: txA, recvErr: context.Canceled}
		outcomeA := parallelArmOutcome{cand: candA, failErr: armLegA.recvErr, arrival: 1, armLeg: armLegA}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		reducer := newParallelRoundReducer(ex, req, entries, candidates, nil)
		_, err := reducer.Reduce(ctx, []parallelArmOutcome{outcomeA})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}

		if len(recordedParams) != 1 {
			t.Fatalf("expected exactly 1 AttemptRecord for bleg-a on cancel teardown, got %d", len(recordedParams))
		}
		if recordedParams[0].Outcome != aipapi.AttemptCancelled {
			t.Errorf("expected outcome AttemptCancelled, got %s", recordedParams[0].Outcome)
		}
	})
}

type recordingAttemptStore struct {
	b2bua.Store
	onRecord func(context.Context, aipapi.AttemptRecord)
}

func (s *recordingAttemptStore) RecordAttempt(ctx context.Context, rec aipapi.AttemptRecord) error {
	if s.onRecord != nil {
		s.onRecord(ctx, rec)
	}
	return nil
}
