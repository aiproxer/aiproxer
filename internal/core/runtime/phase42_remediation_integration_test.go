package runtime

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

func TestPhase42_CloseWinsWhileRecvFinishes_NoAttemptSuccess(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed: true, Reserved: true, ReservationID: "res-close-finish",
			ReservedAmount: authorityInputAmount(5),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	cand := authorityCandidate()
	state := attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(5),
		admissionResult: auth.admitResult,
	}
	rs := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: aipapi.Call{ID: "req-close-finish", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions}},
			traceID:  "trace-close-finish",
			aLegID:   aLegID,
		}),
		attempt:          testAttemptSlot(b2bua.BLegRecord{BLegID: "b-close-finish", Seq: 1}, cand, testAuthorityLifecycle(ex, state, cand), newAttemptAccountingTracker(time.Unix(1, 0))),
		responsePipeline: &responsePipeline{seenEvents: []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "hi"}}},
	}
	rs.terminal.markCommitted(rs.attempt.snapshot())
	installTestTurnTerminal(rs)

	closeDone := make(chan struct{})
	go func() {
		_ = testTerminalizeRequest(context.Background(), rs, sdk.CommandClose, func(context.Context) error {
			rs.terminal.persistCancellationBilling(context.Background(), rs.attempt.snapshot(), "client close", rs.facts.terminalFacts(), rs.responsePipeline)
			rs.terminal.finishResponse(rs.responsePipeline, rs.attempt.snapshot())
			return nil
		})
		close(closeDone)
	}()
	<-closeDone

	finish := aipapi.Event{Kind: aipapi.EventResponseFinished}
	_, ok, err := rs.terminal.finalizeResponseFinishedAuthority(context.Background(), finish, rs.facts.terminalFacts(), rs.attempt.snapshot(), rs.responsePipeline)
	if ok {
		t.Fatal("NormalFinish must not report ok after Close won")
	}
	if err == nil {
		t.Fatal("NormalFinish loser must surface cancel/error")
	}

	_, _, pathErr := rs.terminal.finalizeResponseFinishedAuthority(context.Background(), finish, rs.facts.terminalFacts(), rs.attempt.snapshot(), rs.responsePipeline)
	if pathErr == nil {
		t.Fatal("handleResponseFinishedPath must error when Close already won")
	}
	if !errors.Is(pathErr, leglifecycle.ErrALegCanceled) {
		t.Fatalf("pathErr=%v want ErrALegCanceled (not bare context.Canceled)", pathErr)
	}
	if errors.Is(pathErr, context.Canceled) && !errors.Is(pathErr, leglifecycle.ErrALegCanceled) {
		t.Fatalf("must not surface bare context.Canceled: %v", pathErr)
	}
}

func TestPhase42_CloseThenFinishDelivery_NoBareContextCanceled(t *testing.T) {
	t.Parallel()
	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed: true, Reserved: true, ReservationID: "res-recv-after-close",
			ReservedAmount: authorityInputAmount(5),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	cand := authorityCandidate()
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg("a-recv-after-close")
	rs := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: aipapi.Call{ID: "req-recv-after-close", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions}},
			traceID:  "trace-recv-after-close",
			aLegID:   aLegID,
		}),
		attempt:          testAttemptSlot(b2bua.BLegRecord{BLegID: "b-recv-after-close", Seq: 1}, cand, testAuthorityLifecycle(ex, attemptAuthorityState{admissionInput: testAuthorityAdmissionInput(5), admissionResult: auth.admitResult}, cand), newAttemptAccountingTracker(time.Unix(1, 0))),
		terminal:         newTurnTerminalWithALeg(aScope, aLegEndBase),
		responsePipeline: newResponsePipeline(),
	}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	// Ignore Cancel so Close cannot wake Recv via ctx; only releaseTail delivers finish
	// after Close has already claimed CommandClose (the flaky parallel_race path).
	testStoreInner(rs, &finishAfterReleaseStream{
		entered: entered,
		release: release,
		finish:  aipapi.Event{Kind: aipapi.EventResponseFinished},
	})
	installTestTurnTerminal(rs)

	recvDone := make(chan error, 1)
	go func() {
		_, err := rs.Recv(context.Background())
		recvDone <- err
	}()
	<-entered
	if err := rs.Close(); err != nil {
		t.Fatal(err)
	}
	close(release)
	err := <-recvDone
	if err == nil {
		t.Fatal("expected terminal error after Close")
	}
	if !errors.Is(err, io.EOF) && !errors.Is(err, leglifecycle.ErrALegCanceled) {
		t.Fatalf("got %v want EOF or ErrALegCanceled", err)
	}
	if errors.Is(err, context.Canceled) && !errors.Is(err, leglifecycle.ErrALegCanceled) {
		t.Fatalf("bare context.Canceled is not an allowed Close/Recv race outcome: %v", err)
	}
}

type finishAfterReleaseStream struct {
	entered chan<- struct{}
	release <-chan struct{}
	finish  aipapi.Event
	done    bool
}

func (s *finishAfterReleaseStream) Recv(context.Context) (aipapi.Event, error) {
	if s.done {
		return aipapi.Event{}, io.EOF
	}
	if s.entered != nil {
		select {
		case s.entered <- struct{}{}:
		default:
		}
	}
	<-s.release
	s.done = true
	return s.finish, nil
}

func (s *finishAfterReleaseStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{}
}

func (s *finishAfterReleaseStream) Close() error { return nil }

func TestPhase42_EncoderFailureCompetesBeforeNormalFinish(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed: true, Reserved: true, ReservationID: "res-enc",
			ReservedAmount: authorityInputAmount(5),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	ex.SecureSessionRecordingMandatory = true
	encErr := errors.New("encoder boom")
	ex.SecureSessionRecorder = failingSecureRecorderStub{err: encErr}
	ex.StreamUsage = nil

	cand := authorityCandidate()
	state := attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(5),
		admissionResult: auth.admitResult,
	}
	rs := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline:     aipapi.Call{ID: "req-enc", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions}},
			traceID:      "trace-enc",
			aLegID:       aLegID,
			secureTurnOK: true,
		}),
		attempt:          testAttemptSlot(b2bua.BLegRecord{BLegID: "b-enc", Seq: 1}, cand, testAuthorityLifecycle(ex, state, cand), newAttemptAccountingTracker(time.Unix(1, 0))),
		responsePipeline: &responsePipeline{seenEvents: []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "x"}}},
	}
	rs.terminal.markCommitted(rs.attempt.snapshot())
	installTestTurnTerminal(rs)
	bindTestRuntimeOwners(rs, ex)

	finish := aipapi.Event{Kind: aipapi.EventResponseFinished}
	_, err := testRecvOne(context.Background(), rs, finish)
	if !errors.Is(err, encErr) {
		t.Fatalf("err=%v want encoder", err)
	}
	out, ok := rs.terminal.requestTerminal().Owner().Outcome()
	if !ok || out.Command != sdk.CommandFrontendEncoderFailure {
		t.Fatalf("terminal outcome=%+v ok=%v want frontend_encoder_failure", out, ok)
	}
	if auth.settleCalls.Load() != 0 && out.Command == sdk.CommandNormalFinish {
		t.Fatal("NormalFinish must not win when encoder preflight fails")
	}
	// Encoder must claim before any NormalFinish settlement.
	if out.Command == sdk.CommandNormalFinish {
		t.Fatal("NormalFinish must not be the published command")
	}
}

func TestPhase42_CancelTerminalizesRequest(t *testing.T) {
	t.Parallel()
	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed: true, Reserved: true, ReservationID: "res-cancel",
			ReservedAmount: authorityInputAmount(5),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	cand := authorityCandidate()
	rs := &retryRecvStream{
		responsePipeline: &responsePipeline{bus: hooks.New(hooks.Config{})},
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: aipapi.Call{ID: "req-cancel", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions}},
			traceID:  "trace-cancel",
			aLegID:   aLegID,
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-cancel", Seq: 1}, cand, testAuthorityLifecycle(ex, attemptAuthorityState{admissionInput: testAuthorityAdmissionInput(5), admissionResult: auth.admitResult}, cand), newAttemptAccountingTracker(time.Unix(1, 0))),
	}
	installTestTurnTerminal(rs)
	r := testTerminalizeRequest(context.Background(), rs, sdk.CommandCancel, func(cctx context.Context) error {
		rs.terminal.persistCancellationBilling(cctx, rs.attempt.snapshot(), "context canceled", rs.facts.terminalFacts(), rs.responsePipeline)
		rs.terminal.finishResponse(rs.responsePipeline, rs.attempt.snapshot())
		return nil
	})
	if !r.Won || !rs.terminal.requestTerminal().Owner().State().IsTerminal() {
		t.Fatalf("cancel terminalize: %+v state=%q", r, rs.terminal.requestTerminal().Owner().State())
	}
}

func TestPhase42_NoRetryAfterOutput_GateReplacement(t *testing.T) {
	t.Parallel()
	rs := &retryRecvStream{}
	installTestTurnTerminal(rs)
	rs.terminal.markCommitted(rs.attempt.snapshot())
	r := testTerminalizeRequest(context.Background(), rs, sdk.CommandGateReplacement, nil)
	if r.Won || !errors.Is(r.Err, sdk.ErrOutputCommitted) {
		t.Fatalf("got %+v", r)
	}
	if rs.terminal.requestTerminal().Owner().State() != sdk.StateOpen {
		t.Fatalf("open+committed rejection must leave owner open, state=%q", rs.terminal.requestTerminal().Owner().State())
	}
}

func TestPhase42_ResponsePartHook_RoutesThroughTerminal(t *testing.T) {
	t.Parallel()
	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed: true, Reserved: true, ReservationID: "res-part",
			ReservedAmount: authorityInputAmount(5),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	hookErr := errors.New("part hook boom")
	bus := hooks.New(hooks.Config{
		ResponsePartHooks: []sdkhooks.ResponsePartHook{failingResponsePartHookStub{err: hookErr}},
	})
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	cand := authorityCandidate()
	rs := &retryRecvStream{
		responsePipeline: &responsePipeline{bus: bus},
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: aipapi.Call{ID: "req-part", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions}},
			traceID:  "trace-part",
			aLegID:   aLegID,
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-part", Seq: 1}, cand, testAuthorityLifecycle(ex, attemptAuthorityState{admissionInput: testAuthorityAdmissionInput(5), admissionResult: auth.admitResult}, cand), newAttemptAccountingTracker(time.Unix(1, 0))),
	}
	installTestTurnTerminal(rs)
	_, err := testRecvOne(context.Background(), rs, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"})
	if !errors.Is(err, hookErr) {
		t.Fatalf("err=%v", err)
	}
	if rs.terminal == nil || !rs.terminal.requestTerminal().Owner().State().IsTerminal() {
		t.Fatal("response part failure must terminalize request")
	}
}

func TestPhase42_EventsMu_ClearAndSnapshot(t *testing.T) {
	t.Parallel()
	rs := &retryRecvStream{
		responsePipeline: &responsePipeline{seenEvents: []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "a"}}},
	}
	rs.responsePipeline.visibleText.WriteString("a")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = rs.responsePipeline.seenEventsCopy()
	}()
	go func() {
		defer wg.Done()
		rs.responsePipeline.clearClientAccumulators()
	}()
	wg.Wait()
	if got := rs.responsePipeline.seenEventsCopy(); len(got) != 0 {
		t.Fatalf("cleared accumulators len=%d", len(got))
	}
}
