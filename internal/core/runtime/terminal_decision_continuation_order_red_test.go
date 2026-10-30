package runtime

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview/sdkadapter"
	schedulekit "github.com/aiproxer/aiproxer/internal/testkit/terminaldecision"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/steering"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

// RED contract for Task 4.2:
//
//	runContinuationTransaction is the one private continuation boundary. It
//	uses the existing request terminal, current-attempt slot, attempt/session
//	authority, and conversation-view owner. It must publish B2 before it asks
//	the existing B1 owner to settle. A false result is a pre-publication
//	failure; an error after publication is diagnostic only and must not undo B2.
func TestContinuationTransactionPublishesB2BeforeB1Settlement(t *testing.T) {
	schedule := schedulekit.B2PublishedSettlementSchedule()
	terminal, stream, b1, authority, store := newContinuationRedHarness(t, nil)
	var openerCalls atomic.Int32
	var b1SettledInOpen atomic.Bool
	stream.recovery.opener = func(ctx context.Context, req replacementOpenRequest) (replacementOpenResult, error) {
		openerCalls.Add(1)
		if authority.settleCalls.Load() != 0 || b1.authority.Settled() {
			b1SettledInOpen.Store(true)
		}
		schedule.B2Admission.Arrive()
		if req.isRetryPath {
			t.Fatalf("continuation opener was marked retry/replacement")
		}
		return continuationOpenResult(t, b1), nil
	}

	published, err := runContinuationTransaction(context.Background(), terminal, stream, continuationIntent())
	if err != nil {
		t.Fatalf("continuation transaction: %v", err)
	}
	if !published {
		t.Fatal("continuation transaction did not publish B2")
	}
	if b1SettledInOpen.Load() {
		t.Fatal("B1 settled before the B2 opener returned")
	}
	if openerCalls.Load() != 1 {
		t.Fatalf("B2 opener calls = %d, want exactly one", openerCalls.Load())
	}
	current := stream.attempt.snapshot()
	if current == nil || current == b1 {
		t.Fatalf("current attempt = %p, want a distinct published B2", current)
	}
	schedule.B2Publication.Arrive()
	if authority.settleCalls.Load() != 1 {
		t.Fatalf("B1 authority settlement calls = %d, want exactly one after publication", authority.settleCalls.Load())
	}
	if current != stream.attempt.snapshot() {
		t.Fatal("B2 was rolled back after B1 settlement")
	}

	// A successful transaction leaves the fixed platform overlay active for the
	// newly published B2; final terminal ownership deactivates it later.
	snap, err := store.Snapshot(context.Background(), b1.bleg.ALegID)
	if err != nil {
		t.Fatalf("conversation snapshot: %v", err)
	}
	if len(snap.Steering) != 1 || snap.Steering[0].OverlayID != "terminal-decision-continuation" {
		t.Fatalf("active continuation overlay = %+v, want fixed platform continuation overlay", snap.Steering)
	}
	if authority.settleCalls.Load() != 1 {
		t.Fatal("B1 was settled more than once")
	}
	if terminal.requestTerminal().Owner().State() != sdkterminal.StateOpen {
		// The request remains open only after B2 publication; no intermediate
		// A-side terminal is legal here.
		t.Fatalf("request terminal state = %s, want open while B2 continues", terminal.requestTerminal().Owner().State())
	}
}

func TestContinuationTransactionPrePublicationFailureLeavesB1UnsettledAndFinalizesOriginal(t *testing.T) {
	openErr := errors.New("backend open unavailable")
	cases := []struct {
		name       string
		configure  func(*turnTerminal, *retryRecvStream, *continuationSteeringProbe)
		wantOpens  int32
		wantReason string
	}{
		{
			name: "materialization validation",
			configure: func(_ *turnTerminal, _ *retryRecvStream, _ *continuationSteeringProbe) {
				// The bounded intent validator must reject this before any
				// steering, admission, or backend side effect.
			},
			wantReason: "invalid intent",
		},
		{
			name: "protocol capability",
			configure: func(terminal *turnTerminal, _ *retryRecvStream, _ *continuationSteeringProbe) {
				terminal.supportsContinuation = false
			},
			wantReason: "unsupported continuation",
		},
		{
			name: "steering persistence failure",
			configure: func(_ *turnTerminal, _ *retryRecvStream, store *continuationSteeringProbe) {
				store.putErr = errors.New("steering persistence unavailable")
			},
			wantReason: "steering persistence unavailable",
			wantOpens:  0,
		},
		{
			name: "authority admission failure",
			configure: func(_ *turnTerminal, stream *retryRecvStream, _ *continuationSteeringProbe) {
				stream.recovery.opener = func(context.Context, replacementOpenRequest) (replacementOpenResult, error) {
					return replacementOpenResult{}, errors.New("authority admission denied")
				}
			},
			wantReason: "authority admission denied",
			wantOpens:  1,
		},
		{
			name: "backend open failure",
			configure: func(_ *turnTerminal, stream *retryRecvStream, _ *continuationSteeringProbe) {
				stream.recovery.opener = func(context.Context, replacementOpenRequest) (replacementOpenResult, error) {
					return replacementOpenResult{}, openErr
				}
			},
			wantReason: "backend open unavailable",
			wantOpens:  1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			terminal, stream, b1, authority, store := newContinuationRedHarness(t, nil)
			if tc.configure != nil {
				tc.configure(terminal, stream, store)
			}
			var openerCalls atomic.Int32
			originalOpener := stream.recovery.opener
			stream.recovery.opener = func(ctx context.Context, req replacementOpenRequest) (replacementOpenResult, error) {
				openerCalls.Add(1)
				return originalOpener(ctx, req)
			}
			if tc.name == "materialization validation" {
				intent := continuationIntent()
				intent.Instruction = strings.Repeat("x", 1<<20)
				published, err := runContinuationTransaction(context.Background(), terminal, stream, intent)
				assertPrePublicationFailure(t, published, err, tc.wantReason, terminal, stream, b1, authority, store)
				if got := openerCalls.Load(); got != 0 {
					t.Fatalf("opener calls = %d, want zero for materialization rejection", got)
				}
				return
			}
			published, err := runContinuationTransaction(context.Background(), terminal, stream, continuationIntent())
			assertPrePublicationFailure(t, published, err, tc.wantReason, terminal, stream, b1, authority, store)
			if got := openerCalls.Load(); got != int32(tc.wantOpens) {
				t.Fatalf("opener calls = %d, want %d", got, tc.wantOpens)
			}
		})
	}
}

func TestContinuationTransactionPostPublicationSettlementFailureRetainsB2WithoutRollback(t *testing.T) {
	const rawSettlementDetail = "secret-settlement-detail"
	schedule := schedulekit.B2PublishedSettlementSchedule()
	terminal, stream, b1, authority, _ := newContinuationRedHarness(t, nil)
	authority.settleErr = errors.New(rawSettlementDetail)
	stream.recovery.opener = func(_ context.Context, req replacementOpenRequest) (replacementOpenResult, error) {
		schedule.B2Admission.Arrive()
		if authority.settleCalls.Load() != 0 || b1.authority.Settled() {
			t.Fatal("B1 was settled before B2 publication")
		}
		if req.isRetryPath {
			t.Fatal("continuation was opened through retry/replacement mode")
		}
		return continuationOpenResult(t, b1), nil
	}

	published, err := runContinuationTransaction(context.Background(), terminal, stream, continuationIntent())
	if !published {
		t.Fatalf("B2 was not published before B1 settlement failure: err=%v", err)
	}
	if err == nil {
		t.Fatal("post-publication B1 settlement loss was not surfaced as a diagnostic")
	}
	if strings.Contains(err.Error(), rawSettlementDetail) || len(err.Error()) > 256 {
		t.Fatalf("settlement diagnostic leaked/unbounded upstream detail: %v", err)
	}
	current := stream.attempt.snapshot()
	if current == nil || current == b1 {
		t.Fatal("B2 was rolled back after a post-publication settlement failure")
	}
	if authority.settleCalls.Load() != 1 {
		t.Fatalf("B1 settlement calls = %d, want exactly one", authority.settleCalls.Load())
	}
	if schedule.Expected.Terminal != schedulekit.TerminalContinued {
		t.Fatalf("post-publication schedule terminal = %q, want continued", schedule.Expected.Terminal)
	}
}

func assertPrePublicationFailure(t *testing.T, published bool, err error, wantReason string, terminal *turnTerminal, stream *retryRecvStream, b1 *attemptSession, authority *recordingAuthorityService, store *continuationSteeringProbe) {
	t.Helper()
	if published {
		t.Fatalf("published B2 on pre-publication failure (%s), err=%v", wantReason, err)
	}
	if terminal.requestTerminal().Owner().State() == sdkterminal.StateOpen {
		t.Fatalf("original request remained open after %s", wantReason)
	}
	outcome, claimed := terminal.requestTerminal().Owner().Outcome()
	if !claimed || outcome.Command != sdkterminal.CommandNormalFinish {
		t.Fatalf("original request outcome = %#v (claimed=%t), want normal finish after %s", outcome, claimed, wantReason)
	}
	if stream.attempt.snapshot() != b1 {
		t.Fatal("pre-publication failure replaced current B1")
	}
	if authority.settleCalls.Load() != 0 {
		t.Fatalf("B1 settlement calls = %d, want zero before publication", authority.settleCalls.Load())
	}
	if store.deactivateCalls.Load() != 1 {
		t.Fatalf("partial overlay deactivation calls = %d, want exactly one", store.deactivateCalls.Load())
	}
	if err == nil || len(err.Error()) > 256 {
		t.Fatalf("pre-publication error = %v, want bounded diagnostic", err)
	}
}

func continuationIntent() terminaldecision.ContinuationIntent {
	return terminaldecision.ContinuationIntent{
		TrajectoryRef: "trajectory-1",
		ControlRef:    "control-1",
		Instruction:   "continue the already accepted work",
		Provenance:    "internal-control",
		ReasonCode:    "continue",
	}
}

func newContinuationRedHarness(t *testing.T, opener func(context.Context, replacementOpenRequest) (replacementOpenResult, error)) (*turnTerminal, *retryRecvStream, *attemptSession, *recordingAuthorityService, *continuationSteeringProbe) {
	t.Helper()
	b1, authority := newAuthorityTerminalDecisionAttempt(t)
	b1.bleg.ALegID = "a-leg-continuation-red"
	b1.bleg.BLegID = "b1-continuation-red"
	store := &continuationSteeringProbe{ReferenceStore: conversationview.NewReferenceStore()}
	if err := store.CreateALeg(context.Background(), b1.bleg.ALegID); err != nil {
		t.Fatalf("create conversation A-leg: %v", err)
	}
	terminal := newTurnTerminal()
	terminal.supportsContinuation = true
	terminal.markCommitted(nil)
	terminal.steeringWriterFactory = func(ctx context.Context, aLegID string, resolver SteeringWriterResolver) (steering.Writer, error) {
		var trajResolver sdkadapter.TrajectoryResolver
		if resolver != nil {
			trajResolver = sdkadapter.TrajectoryResolver(resolver)
		}
		return sdkadapter.NewWriterWithObserver(store, aLegID, trajResolver, nil)
	}
	terminal.conversationReader = store
	if opener == nil {
		opener = func(context.Context, replacementOpenRequest) (replacementOpenResult, error) {
			return continuationOpenResult(t, b1), nil
		}
	}
	stream := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			traceID:     "trace-continuation-red",
			aLegID:      b1.bleg.ALegID,
			baseline:    continuationUserCall(),
			ingressCall: continuationUserCall(),
		}),
		responsePipeline: newResponsePipeline(),
		terminal:         terminal,
		attempt:          attemptSlot{current: b1},
		recovery:         &recoveryController{opener: opener},
	}
	return terminal, stream, b1, authority, store
}

func continuationOpenResult(t *testing.T, b1 *attemptSession) replacementOpenResult {
	t.Helper()
	b2, _ := newAuthorityTerminalDecisionAttempt(t)
	b2.bleg = b2bua.BLegRecord{ALegID: b1.bleg.ALegID, BLegID: "b2-continuation-red", Seq: b1.bleg.Seq + 1}
	b2.storeInner(aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}))
	ready := newReadyAttempt(b2, pendingSelectionEffects{})
	ready.state = readyStatePrepared
	return replacementOpenResult{opened: true, ready: ready, bleg: b2.bleg, cand: b2.cand}
}

func continuationUserCall() aipapi.Call {
	return aipapi.Call{
		ID: "request-continuation-red",
		Items: []aipapi.Item{{
			Kind:    aipapi.ItemKindMessage,
			Role:    aipapi.RoleUser,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "accepted user task"}},
		}},
	}
}

type continuationSteeringProbe struct {
	*conversationview.ReferenceStore
	putErr          error
	deactivateErr   error
	putCalls        atomic.Int32
	deactivateCalls atomic.Int32
}

func (s *continuationSteeringProbe) PutSteering(ctx context.Context, aLegID string, req conversationview.PutSteeringRequest) (conversationview.SteeringState, error) {
	s.putCalls.Add(1)
	if s.putErr != nil {
		return conversationview.SteeringState{}, s.putErr
	}
	return s.ReferenceStore.PutSteering(ctx, aLegID, req)
}

func (s *continuationSteeringProbe) DeactivateSteering(ctx context.Context, aLegID, overlayID string) (conversationview.SteeringState, error) {
	s.deactivateCalls.Add(1)
	if s.deactivateErr != nil {
		return conversationview.SteeringState{}, s.deactivateErr
	}
	return s.ReferenceStore.DeactivateSteering(ctx, aLegID, overlayID)
}
