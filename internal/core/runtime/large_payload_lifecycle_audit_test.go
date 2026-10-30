package runtime

// Task 3 (large-payload-lifecycle-correctness) RED tests: the wire turn audit
// must reflect the accepted logical outcome, not the consumer's Close/EOF
// pattern.
//
// Every test drives the REAL Executor.ExecuteLargeBody with the REAL secure
// session audit store and pumps the returned stream to a terminal condition
// (no sleeps; channel rendezvous where concurrency is required). Each test
// asserts the turn_outcome audit row(s) plus attempt/usage/billing
// cardinalities. Tests marked RED-EXPECTATION fail before the fix and pass
// after; the rest guard existing behavior and prove each rejection path stays
// live under the narrowed classifier (mutant: force-success must fail them).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	coreterm "github.com/aiproxer/aiproxer/internal/core/terminal"
	terminalworkapp "github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	accountingstream "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/streamusage"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/infra/terminalwork/workstore"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// lplcFixture wires a real executor to a fresh secure-session audit store, a
// capturing usage sink, and an attempt spy store.
type lplcFixture struct {
	ex    *Executor
	memSS interface {
		Audit(ctx context.Context, sessionID domain.SessionID, opts domain.ReadOptions) ([]domain.AuditItem, error)
	}
	sink *capturingTerminalUsageSink
	spy  *attemptSpyStore
}

func newLPLCFixture(t *testing.T) *lplcFixture {
	t.Helper()
	ex, _, store := setupTestExecutor(t)
	memSS := memory.New(memory.Options{SimulateDurable: true})
	ex.SecureSession = testSecureManager(t, memSS, store)
	sink := &capturingTerminalUsageSink{}
	ex.TerminalUsageSink = sink
	spy := &attemptSpyStore{Store: store}
	ex.Store = spy
	return &lplcFixture{ex: ex, memSS: memSS, sink: sink, spy: spy}
}

func (f *lplcFixture) executeWire(t *testing.T, reqID, traceID string) largebody.ExecutionResult {
	t.Helper()
	src := newTestSource(`{"model":"gpt-4o","prompt":"` + reqID + `"}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)
	acc.WireRequest.CandidateModel = "default:gpt-4o"
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-" + reqID})
	ctx = largebody.WithWireIdentity(ctx, reqID, traceID)
	res, err := f.ex.ExecuteLargeBody(ctx, acc, src)
	require.NoError(t, err)
	require.NotNil(t, res.Stream)
	return res
}

// turnOutcomeResults returns the Result values of every turn_outcome audit
// row for the executed turn, in stored order.
func (f *lplcFixture) turnOutcomeResults(t *testing.T, res largebody.ExecutionResult) []string {
	t.Helper()
	audits, err := f.memSS.Audit(context.Background(), domain.SessionID(res.Facts.SessionID), domain.ReadOptions{})
	require.NoError(t, err)
	var out []string
	for _, a := range audits {
		if a.Action == "turn_outcome" {
			out = append(out, a.Result)
		}
	}
	return out
}

func (f *lplcFixture) counts() (attempts, legs, calls int) {
	f.sink.mu.Lock()
	defer f.sink.mu.Unlock()
	f.spy.mu.Lock()
	defer f.spy.mu.Unlock()
	return len(f.spy.records), len(f.sink.legs), len(f.sink.calls)
}

// pumpWireToEOF Recvs until the stream reports EOF (or a panic escapes),
// returning every delivered event and the first non-EOF error. It never
// sleeps; maxRecvs bounds a non-terminating stream.
func pumpWireToEOF(t *testing.T, stream aipapi.EventStream, maxRecvs int) (got []aipapi.Event, firstErr error) {
	t.Helper()
	for range maxRecvs {
		ev, err := stream.Recv(context.Background())
		if err != nil {
			if errors.Is(err, io.EOF) {
				return got, firstErr
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		got = append(got, ev)
	}
	t.Fatalf("stream did not reach EOF within %d Recvs (got %d events, firstErr=%v)", maxRecvs, len(got), firstErr)
	return got, firstErr
}

// pumpWireToFinish Recvs until the first response_finished event is
// delivered, failing on any terminal error or EOF before it. Unlike
// pumpWireToEOF it never consumes the EOF that would finalize the audit on
// its own, so the test owns the terminal Close/Cancel.
func pumpWireToFinish(t *testing.T, stream aipapi.EventStream, maxRecvs int) []aipapi.Event {
	t.Helper()
	var got []aipapi.Event
	for range maxRecvs {
		ev, err := stream.Recv(context.Background())
		require.NoError(t, err, "stream must not terminate before the accepted finish")
		got = append(got, ev)
		if ev.Kind == aipapi.EventResponseFinished {
			return got
		}
	}
	t.Fatalf("accepted finish not delivered within %d Recvs (got kinds %v)", maxRecvs, eventKinds(got))
	return got
}

func lplcBackend(events ...aipapi.Event) execbackend.Backend {
	return execbackend.Backend{
		OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			return makeStreamWithEvents(events...), nil
		},
	}
}

func lplcFinishEvents() []aipapi.Event {
	return []aipapi.Event{
		{Kind: aipapi.EventTextDelta, Delta: "hello"},
		{Kind: aipapi.EventResponseFinished},
	}
}

func eventKinds(evs []aipapi.Event) []aipapi.EventKind {
	kinds := make([]aipapi.EventKind, 0, len(evs))
	for _, ev := range evs {
		kinds = append(kinds, ev.Kind)
	}
	return kinds
}

// RED-EXPECTATION (fails pre-fix with surfaced_failure): accepted logical
// success followed by Close without an extra Recv for EOF must audit once
// with success (Requirement 3.1).
func TestLargePayloadLifecycle_AcceptedSuccessCloseWithoutEOF_AuditsSuccessOnce(t *testing.T) {
	fx := newLPLCFixture(t)
	fx.ex.Backends = map[string]execbackend.Backend{"default": lplcBackend(lplcFinishEvents()...)}

	res := fx.executeWire(t, "req-lplc-success", "trace-lplc-success")

	ev1, err := res.Stream.Recv(context.Background())
	require.NoError(t, err)
	require.Equal(t, aipapi.EventTextDelta, ev1.Kind)
	ev2, err := res.Stream.Recv(context.Background())
	require.NoError(t, err)
	require.Equal(t, aipapi.EventResponseFinished, ev2.Kind)

	require.NoError(t, res.Stream.Close())
	require.NoError(t, res.Stream.Close())

	assert.Equal(t, []string{"success"}, fx.turnOutcomeResults(t, res))
	attempts, legs, calls := fx.counts()
	assert.Equal(t, 1, attempts, "exactly one attempt record")
	assert.Equal(t, 1, legs, "exactly one usage leg")
	assert.Equal(t, 0, calls, "no billing call closure without stamped identity")
}

// Guard: the same successful evidence followed by EOF must stay success with
// exactly one audit (no relabeling, Requirement 3.1 second row).
func TestLargePayloadLifecycle_AcceptedSuccessThenEOF_AuditsSuccessNoRelabel(t *testing.T) {
	fx := newLPLCFixture(t)
	fx.ex.Backends = map[string]execbackend.Backend{"default": lplcBackend(lplcFinishEvents()...)}

	res := fx.executeWire(t, "req-lplc-eof", "trace-lplc-eof")
	got, firstErr := pumpWireToEOF(t, res.Stream, 16)
	require.NoError(t, firstErr)
	require.Contains(t, eventKinds(got), aipapi.EventResponseFinished)
	require.NoError(t, res.Stream.Close())

	assert.Equal(t, []string{"success"}, fx.turnOutcomeResults(t, res))
	attempts, legs, _ := fx.counts()
	assert.Equal(t, 1, attempts)
	assert.Equal(t, 1, legs)
}

// RED-EXPECTATION: usage-bearing success (synthesized usage delivered first,
// finish via the recovery drain) followed by Close must audit success. This
// exercises the receive-release boundary past the terminal claim.
func TestLargePayloadLifecycle_UsageBearingSuccessCloseWithoutEOF_AuditsSuccess(t *testing.T) {
	fx := newLPLCFixture(t)
	fx.ex.StreamUsage = accountingstream.New(&stubStreamCounter{
		call:   accountingapp.CountResult{InputTokens: 7, TotalTokens: 7},
		output: accountingapp.CountResult{OutputTokens: 3, TotalTokens: 10},
	}, accountingstream.Config{})
	fx.ex.Backends = map[string]execbackend.Backend{"default": lplcBackend(lplcFinishEvents()...)}

	res := fx.executeWire(t, "req-lplc-usage", "trace-lplc-usage")
	got := pumpWireToFinish(t, res.Stream, 16)
	require.Contains(t, eventKinds(got), aipapi.EventResponseFinished)
	require.NoError(t, res.Stream.Close())

	assert.Equal(t, []string{"success"}, fx.turnOutcomeResults(t, res))
}

// Guard: usage-bearing success followed by EOF, a later A-leg cancellation,
// and Close must keep the success outcome without relabeling (evidence-table
// row 2: same successful evidence followed by EOF or later cancellation).
// The cancel races an already-finished stream: the post-success Recv reports
// EOF via the finished gate before any cancellation can surface as a receive
// error, so this test guards no-relabeling rather than exercising a
// post-success cancellation-error veto, which the runtime cannot produce.
func TestLargePayloadLifecycle_SuccessThenCancelWithRecv_AuditsSuccessNoRelabel(t *testing.T) {
	fx := newLPLCFixture(t)
	fx.ex.StreamUsage = accountingstream.New(&stubStreamCounter{
		call:   accountingapp.CountResult{InputTokens: 7, TotalTokens: 7},
		output: accountingapp.CountResult{OutputTokens: 3, TotalTokens: 10},
	}, accountingstream.Config{})
	fx.ex.Backends = map[string]execbackend.Backend{"default": lplcBackend(lplcFinishEvents()...)}

	res := fx.executeWire(t, "req-lplc-cancel-recv", "trace-lplc-cancel-recv")
	got := pumpWireToFinish(t, res.Stream, 16)
	require.Contains(t, eventKinds(got), aipapi.EventResponseFinished)

	cancelCtx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-req-lplc-cancel-recv"})
	require.NoError(t, fx.ex.CancelALeg(cancelCtx, aipapi.ALegCancelRequest{
		ALegID: res.Facts.ALegID,
		Reason: "late cancel after accepted success",
	}))
	_, recvErr := res.Stream.Recv(context.Background())
	require.ErrorIs(t, recvErr, io.EOF, "a finished stream reports EOF; the late cancel cannot surface as a Recv error")
	require.NoError(t, res.Stream.Close())

	assert.Equal(t, []string{"success"}, fx.turnOutcomeResults(t, res))
	attempts, legs, _ := fx.counts()
	assert.Equal(t, 1, attempts)
	assert.Equal(t, 1, legs)
}

// RED-EXPECTATION: success followed by a later cancellation must keep the
// success outcome (no relabeling, Requirement 3.1 second row).
func TestLargePayloadLifecycle_SuccessThenCancel_AuditsSuccessNoRelabel(t *testing.T) {
	fx := newLPLCFixture(t)
	fx.ex.Backends = map[string]execbackend.Backend{"default": lplcBackend(lplcFinishEvents()...)}

	res := fx.executeWire(t, "req-lplc-cancel", "trace-lplc-cancel")
	got := pumpWireToFinish(t, res.Stream, 16)
	require.Contains(t, eventKinds(got), aipapi.EventResponseFinished)

	ms, ok := res.Stream.(aipapi.ManagedEventStream)
	require.True(t, ok, "wire stream must support Cancel")
	_ = ms.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
	require.NoError(t, res.Stream.Close())

	assert.Equal(t, []string{"success"}, fx.turnOutcomeResults(t, res))
}

// RED-EXPECTATION (fails pre-fix with success): EOF without any accepted
// logical success (bare stream, no finish) must audit surfaced_failure, not
// success from the EOF cause alone (Requirement 3.2, CommandEOF row).
func TestLargePayloadLifecycle_EOFWithoutAcceptedSuccess_AuditsFailure(t *testing.T) {
	fx := newLPLCFixture(t)
	fx.ex.Backends = map[string]execbackend.Backend{"default": lplcBackend()}

	res := fx.executeWire(t, "req-lplc-bare-eof", "trace-lplc-bare-eof")
	got, firstErr := pumpWireToEOF(t, res.Stream, 16)
	require.NoError(t, firstErr)
	require.Empty(t, got)
	require.NoError(t, res.Stream.Close())

	assert.Equal(t, []string{"surfaced_failure"}, fx.turnOutcomeResults(t, res))
	attempts, legs, _ := fx.counts()
	assert.Equal(t, 1, attempts)
	assert.Equal(t, 1, legs)
}

// lplcBlockingBackend serves a caller-owned blocking stream: its first Recv
// stays parked until unblockRecv, so the test owns the terminal Close/Cancel
// timing (shared by the early-close and early-cancel guards).
func lplcBlockingBackend(stream *blockingTrackingStream) execbackend.Backend {
	return execbackend.Backend{
		OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			return stream, nil
		},
	}
}

// Guard: early Close before accepted completion stays surfaced_failure with
// exactly one audit (Requirement 3.2).
func TestLargePayloadLifecycle_EarlyClose_AuditsFailure(t *testing.T) {
	fx := newLPLCFixture(t)
	backendStream := newBlockingTrackingStream()
	fx.ex.Backends = map[string]execbackend.Backend{
		"default": lplcBlockingBackend(backendStream),
	}

	res := fx.executeWire(t, "req-lplc-early-close", "trace-lplc-early-close")
	require.NoError(t, res.Stream.Close())
	backendStream.unblockRecv()
	got, firstErr := pumpWireToEOF(t, res.Stream, 16)
	require.NoError(t, firstErr)
	require.Empty(t, got, "no content may surface after an early close")

	assert.Equal(t, []string{"surfaced_failure"}, fx.turnOutcomeResults(t, res))
}

// Guard: A-leg cancellation before accepted completion stays
// surfaced_failure with exactly one terminal leg and one audit.
func TestLargePayloadLifecycle_EarlyCancel_AuditsFailure(t *testing.T) {
	fx := newLPLCFixture(t)
	backendStream := newBlockingTrackingStream()
	fx.ex.Backends = map[string]execbackend.Backend{
		"default": lplcBlockingBackend(backendStream),
	}

	res := fx.executeWire(t, "req-lplc-early-cancel", "trace-lplc-early-cancel")
	recvDone := make(chan error, 1)
	go func() {
		_, err := res.Stream.Recv(context.Background())
		recvDone <- err
	}()
	cancelCtx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-req-lplc-early-cancel"})
	require.NoError(t, fx.ex.CancelALeg(cancelCtx, aipapi.ALegCancelRequest{
		ALegID: res.Facts.ALegID,
		Reason: "test cancel",
	}))
	backendStream.unblockRecv()
	select {
	case <-recvDone:
	case <-time.After(5 * time.Second):
		t.Fatal("pump Recv did not unblock after cancellation")
	}
	require.NoError(t, res.Stream.Close())

	assert.Equal(t, []string{"surfaced_failure"}, fx.turnOutcomeResults(t, res))
	attempts, legs, _ := fx.counts()
	assert.Equal(t, 1, attempts)
	assert.Equal(t, 1, legs)
}

// lplcScriptStream replays fixed events, then a fixed error once, then EOF.
type lplcScriptStream struct {
	mu          sync.Mutex
	events      []aipapi.Event
	err         error
	errReturned bool
	closed      bool
}

func (s *lplcScriptStream) Recv(ctx context.Context) (aipapi.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return aipapi.Event{}, io.EOF
	}
	if len(s.events) > 0 {
		ev := s.events[0]
		s.events = s.events[1:]
		return ev, nil
	}
	if s.err != nil && !s.errReturned {
		s.errReturned = true
		return aipapi.Event{}, s.err
	}
	return aipapi.Event{}, io.EOF
}

func (s *lplcScriptStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

var _ aipapi.ManagedEventStream = (*lplcScriptStream)(nil)

func (s *lplcScriptStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	_ = s.Close()
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

// Guard: partial failure after committed output stays surfaced_failure even
// though a later Recv reports EOF (the EOF must not overwrite the failure).
func TestLargePayloadLifecycle_PartialFailure_AuditsFailure(t *testing.T) {
	fx := newLPLCFixture(t)
	fx.ex.Backends = map[string]execbackend.Backend{
		"default": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				return &lplcScriptStream{
					events: []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "partial"}},
					err:    errors.New("backend boom"),
				}, nil
			},
		},
	}

	res := fx.executeWire(t, "req-lplc-partial", "trace-lplc-partial")
	got, firstErr := pumpWireToEOF(t, res.Stream, 16)
	require.Error(t, firstErr)
	require.NotContains(t, eventKinds(got), aipapi.EventResponseFinished)
	require.NoError(t, res.Stream.Close())

	assert.Equal(t, []string{"surfaced_failure"}, fx.turnOutcomeResults(t, res))
	attempts, legs, _ := fx.counts()
	assert.Equal(t, 1, attempts)
	assert.Equal(t, 1, legs)
}

// Guard: mandatory-recording encoder failure stays surfaced_failure with the
// encoder-failure terminal command (not a success, not a plain partial).
func TestLargePayloadLifecycle_EncoderFailure_AuditsFailure(t *testing.T) {
	fx := newLPLCFixture(t)
	fx.ex.SecureSessionRecordingMandatory = true
	fx.ex.SecureSessionRecorder = failingSecureRecorderStub{err: errors.New("encoder boom")}
	fx.ex.Backends = map[string]execbackend.Backend{"default": lplcBackend(lplcFinishEvents()...)}

	res := fx.executeWire(t, "req-lplc-encoder", "trace-lplc-encoder")
	_, _ = pumpWireToEOF(t, res.Stream, 16)
	require.NoError(t, res.Stream.Close())

	assert.Equal(t, []string{"surfaced_failure"}, fx.turnOutcomeResults(t, res))
	inner, ok := res.Stream.(*wireLifecycleEventStream)
	require.True(t, ok)
	rs, ok := inner.EventStream.(*retryRecvStream)
	require.True(t, ok)
	out, claimed := rs.terminal.requestTerminal().Owner().Outcome()
	require.True(t, claimed)
	assert.Equal(t, sdkterminal.CommandFrontendEncoderFailure, out.Command)
}

// lplcPanicStream panics on its first Recv, then reports EOF.
type lplcPanicStream struct {
	mu       sync.Mutex
	panicked bool
	closed   bool
}

func (s *lplcPanicStream) Recv(ctx context.Context) (aipapi.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return aipapi.Event{}, io.EOF
	}
	if !s.panicked {
		s.panicked = true
		panic("backend recv exploded")
	}
	return aipapi.Event{}, io.EOF
}

func (s *lplcPanicStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

var _ aipapi.ManagedEventStream = (*lplcPanicStream)(nil)

func (s *lplcPanicStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	_ = s.Close()
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

// A backend panic must still finalize exactly one audit with
// surfaced_failure: teardown runs on panic unwinding and the panic
// propagates to the caller.
func TestLargePayloadLifecycle_BackendPanic_AuditsFailureOnce(t *testing.T) {
	fx := newLPLCFixture(t)
	fx.ex.Backends = map[string]execbackend.Backend{
		"default": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				return &lplcPanicStream{}, nil
			},
		},
	}

	res := fx.executeWire(t, "req-lplc-panic", "trace-lplc-panic")
	var panicked any
	func() {
		defer func() { panicked = recover() }()
		_, _ = res.Stream.Recv(context.Background())
	}()
	if panicked == nil {
		// The panic was converted below the wire wrapper; keep pumping to EOF.
		_, _ = pumpWireToEOF(t, res.Stream, 16)
	} else {
		assert.Contains(t, fmt.Sprintf("%v", panicked), "backend recv exploded")
	}
	// Teardown must have run on panic unwinding: exactly one audit already,
	// before any Close.
	assert.Equal(t, []string{"surfaced_failure"}, fx.turnOutcomeResults(t, res))
	require.NoError(t, res.Stream.Close())

	assert.Equal(t, []string{"surfaced_failure"}, fx.turnOutcomeResults(t, res))
}

// lplcFailCallSink records legs but fails every billing call closure.
type lplcFailCallSink struct {
	mu           sync.Mutex
	legs         []usageevidence.CallLegUsageRecord
	callAttempts int
	err          error
}

func (s *lplcFailCallSink) AppendCall(_ context.Context, record usageevidence.CallUsageRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.callAttempts++
	return s.err
}

func (s *lplcFailCallSink) AppendLeg(_ context.Context, record usageevidence.CallLegUsageRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.legs = append(s.legs, record)
	return nil
}

// Guard: a failed terminal effect (billing call-closure append) stays
// surfaced_failure; the failed effect must not become a success.
func TestLargePayloadLifecycle_EffectFailure_AuditsFailure(t *testing.T) {
	fx := newLPLCFixture(t)
	fx.ex.BillingExposureAdmission = &testExposureAdmission{
		admitFunc: func(_ context.Context, in BillingExposureAdmissionInput) (billingport.CallExposure, error) {
			return billingport.CallExposure{AccountID: "acc-effect"}, nil
		},
	}
	failSink := &lplcFailCallSink{err: errors.New("call closure append boom")}
	fx.ex.TerminalUsageSink = failSink
	fx.ex.Backends = map[string]execbackend.Backend{"default": lplcBackend(lplcFinishEvents()...)}

	res := fx.executeWire(t, "req-lplc-effect", "trace-lplc-effect")
	_, firstErr := pumpWireToEOF(t, res.Stream, 16)
	require.Error(t, firstErr, "the failed terminal effect must surface to the consumer")
	require.NoError(t, res.Stream.Close())

	assert.Equal(t, []string{"surfaced_failure"}, fx.turnOutcomeResults(t, res))
	failSink.mu.Lock()
	defer failSink.mu.Unlock()
	assert.Equal(t, 1, len(failSink.legs), "the attempt leg is still recorded")
	assert.Equal(t, 1, failSink.callAttempts, "exactly one call-closure attempt")
	assert.Equal(t, 1, len(fx.spy.Records()), "exactly one attempt record")
}

// Guard: durable-pending terminal effects (WorkPending, not Released) must
// not audit success (Requirement 3.2/3.4, durable-pending row).
func TestLargePayloadLifecycle_DurablePending_AuditsNonSuccess(t *testing.T) {
	fx := newLPLCFixture(t)
	clock := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	state := workstore.NewMemoryState()
	wstore, err := workstore.NewMemoryStore(workstore.MemoryConfig{
		StoreID: "lplc-durable",
		State:   state,
		Now:     func() time.Time { return clock },
	})
	require.NoError(t, err)
	intents := terminalworkapp.NewIntentService(wstore, terminalworkapp.IntentServiceConfig{
		Clock: func() time.Time { return clock },
	})
	prov := &countingSettleProvider{id: "quota", settleErr: errors.New("settle boom")}
	fx.ex.RequestCoordinator = &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{{
			ID: "quota", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: prov, Strength: authority.StrengthRequired,
		}},
	}
	fx.ex.TerminalWork = intents
	fx.ex.Backends = map[string]execbackend.Backend{"default": lplcBackend(lplcFinishEvents()...)}

	res := fx.executeWire(t, "req-lplc-durable", "trace-lplc-durable")
	_, firstErr := pumpWireToEOF(t, res.Stream, 16)
	require.Error(t, firstErr)
	require.ErrorIs(t, firstErr, terminalworkapp.ErrDurablePending)
	require.NoError(t, res.Stream.Close())

	assert.Equal(t, []string{"surfaced_failure"}, fx.turnOutcomeResults(t, res))
	inner, ok := res.Stream.(*wireLifecycleEventStream)
	require.True(t, ok)
	rs, ok := inner.EventStream.(*retryRecvStream)
	require.True(t, ok)
	assert.Equal(t, sdkterminal.StateWorkPending, rs.terminal.requestTerminal().Owner().State())
}

// Guard: when pre-output recovery replaces the attempt, the surviving
// execution determines the outcome; the abandoned attempt leaves no audit.
func TestLargePayloadLifecycle_ReplacedAttempt_SurvivorDeterminesOutcome(t *testing.T) {
	fx := newLPLCFixture(t)
	src := newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"replacement"}]}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, false, "b1:gpt-4o", "b2:gpt-4o")

	be1 := &stubWireBackend{
		openFunc: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			return nil, aipapi.RecoverablePreOutputError(errors.New("b1 connection reset"))
		},
	}
	be2 := &stubWireBackend{
		openFunc: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			return makeStreamWithEvents(lplcFinishEvents()...), nil
		},
	}
	fx.ex.Backends = map[string]execbackend.Backend{
		"b1": {OpenWire: be1.OpenWire},
		"b2": {OpenWire: be2.OpenWire},
	}
	ar, err := routing.NewAliasResolver([]routing.ModelAliasRule{
		{Pattern: "^test-failover$", Replacement: "b1:gpt-4o | b2:gpt-4o"},
	})
	require.NoError(t, err)
	fx.ex.SelectorAliases = ar
	acc.WireRequest.CandidateModel = "test-failover"

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-lplc-replace"})
	ctx = largebody.WithWireIdentity(ctx, "req-lplc-replace", "trace-lplc-replace")
	res, err := fx.ex.ExecuteLargeBody(ctx, acc, src)
	require.NoError(t, err)
	require.NotNil(t, res.Stream)

	got, firstErr := pumpWireToEOF(t, res.Stream, 16)
	require.NoError(t, firstErr)
	require.Contains(t, eventKinds(got), aipapi.EventResponseFinished)
	require.NoError(t, res.Stream.Close())

	assert.Equal(t, []string{"success"}, fx.turnOutcomeResults(t, res))
	attempts, _, _ := fx.counts()
	assert.Equal(t, 2, attempts, "both the abandoned and the surviving attempt are recorded")
}

// lplcRendezvousStream blocks its first Recv until released, then yields one
// finish event.
type lplcRendezvousStream struct {
	entered   chan struct{}
	release   chan struct{}
	closeCh   chan struct{}
	enterOnce sync.Once
	closeOnce sync.Once
	finish    aipapi.Event
}

func (s *lplcRendezvousStream) Recv(ctx context.Context) (aipapi.Event, error) {
	s.enterOnce.Do(func() { close(s.entered) })
	select {
	case <-s.release:
		return s.finish, nil
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	}
}

func (s *lplcRendezvousStream) Close() error {
	s.closeOnce.Do(func() { close(s.closeCh) })
	return nil
}

var _ aipapi.ManagedEventStream = (*lplcRendezvousStream)(nil)

func (s *lplcRendezvousStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	_ = s.Close()
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

// Guard: output withdrawn by Close before delivery (the backend finish lands
// after Close won) must not audit success: the late finish is discarded and
// exactly one surfaced_failure audit remains.
func TestLargePayloadLifecycle_WithdrawnPendingOutput_AuditsFailure(t *testing.T) {
	fx := newLPLCFixture(t)
	inner := &lplcRendezvousStream{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		closeCh: make(chan struct{}),
		finish:  aipapi.Event{Kind: aipapi.EventResponseFinished},
	}
	fx.ex.Backends = map[string]execbackend.Backend{
		"default": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				return inner, nil
			},
		},
	}

	res := fx.executeWire(t, "req-lplc-withdraw", "trace-lplc-withdraw")
	recvDone := make(chan recvOutcome, 1)
	go func() {
		ev, err := res.Stream.Recv(context.Background())
		recvDone <- recvOutcome{event: ev, err: err}
	}()
	select {
	case <-inner.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("backend Recv was not entered")
	}
	require.NoError(t, res.Stream.Close())
	close(inner.release)
	var out recvOutcome
	select {
	case out = <-recvDone:
	case <-time.After(5 * time.Second):
		t.Fatal("pump Recv did not unblock after release")
	}
	require.ErrorIs(t, out.err, io.EOF, "the late finish must be discarded after Close won")
	require.NotEqual(t, aipapi.EventResponseFinished, out.event.Kind)

	got, firstErr := pumpWireToEOF(t, res.Stream, 16)
	require.NoError(t, firstErr)
	require.Empty(t, got)
	require.NoError(t, res.Stream.Close())

	assert.Equal(t, []string{"surfaced_failure"}, fx.turnOutcomeResults(t, res))
	attempts, legs, _ := fx.counts()
	assert.Equal(t, 1, attempts)
	assert.Equal(t, 1, legs)
}

type recvOutcome struct {
	event aipapi.Event
	err   error
}

// TestLargePayloadLifecycle_WireLogicalOutcome_ClassifierTable pins the
// narrowed success predicate on a real terminal: every rejection dimension
// (unclaimed, non-success code, unreleased state, unlatched completion) is
// fault-injected independently, so a broader classifier cannot pass.
func TestLargePayloadLifecycle_WireLogicalOutcome_ClassifierTable(t *testing.T) {
	tests := []struct {
		name    string
		command sdkterminal.Command
		effErr  error
		latch   bool
		claim   bool
		want    bool
	}{
		{name: "success released accepted", command: sdkterminal.CommandNormalFinish, latch: true, claim: true, want: true},
		{name: "claim alone without accepted completion", command: sdkterminal.CommandNormalFinish, claim: true, want: false},
		{name: "latch without claim", latch: true, want: false},
		{name: "open terminal", want: false},
		{name: "failed effects despite latch", command: sdkterminal.CommandNormalFinish, effErr: errors.New("settle boom"), latch: true, claim: true, want: false},
		{name: "durable pending despite latch", command: sdkterminal.CommandNormalFinish, effErr: terminalworkapp.ErrDurablePending, latch: true, claim: true, want: false},
		{name: "eof despite latch", command: sdkterminal.CommandEOF, latch: true, claim: true, want: false},
		{name: "cancel despite latch", command: sdkterminal.CommandCancel, latch: true, claim: true, want: false},
		{name: "close despite latch", command: sdkterminal.CommandClose, latch: true, claim: true, want: false},
		{name: "partial despite latch", command: sdkterminal.CommandPartialError, latch: true, claim: true, want: false},
		{name: "panic despite latch", command: sdkterminal.CommandPanic, latch: true, claim: true, want: false},
		{name: "encoder failure despite latch", command: sdkterminal.CommandFrontendEncoderFailure, latch: true, claim: true, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			term := newTurnTerminal()
			if tt.claim {
				r := term.claimRequestTerminal(context.Background(), tt.command,
					coreterm.NewAccumulatorSnapshot(nil, false),
					func(context.Context, coreterm.Outcome) error { return tt.effErr })
				require.True(t, r.Won, "the test command must win the fresh terminal")
			}
			if tt.latch {
				term.noteAcceptedLogicalCompletion()
			}
			assert.Equal(t, tt.want, term.wireLogicalOutcome().logicallySuccessful())
		})
	}
	t.Run("nil terminal fails closed", func(t *testing.T) {
		var term *turnTerminal
		assert.False(t, term.wireLogicalOutcome().logicallySuccessful())
	})
}

// lplcPanicInnerStream panics on Recv and delegates the rest.
type lplcPanicInnerStream struct {
	aipapi.EventStream
}

func (s *lplcPanicInnerStream) Recv(context.Context) (aipapi.Event, error) {
	panic("inner recv exploded")
}

// The wire wrapper's once-cleanup must run on receive panic unwinding with
// the panic preserved for the caller, and must not run again on a later
// Close.
func TestLargePayloadLifecycle_WireEventStream_Recv_CleanupRunsOnPanic(t *testing.T) {
	var cleanups atomic.Int32
	var cleanupErr atomic.Value
	wles := &wireLifecycleEventStream{
		EventStream: &lplcPanicInnerStream{EventStream: aipapi.NewFixedEventStream(nil)},
		cleanup: func(err error) {
			cleanups.Add(1)
			cleanupErr.Store(err)
		},
	}

	require.PanicsWithValue(t, "inner recv exploded", func() {
		_, _ = wles.Recv(context.Background())
	})
	require.Equal(t, int32(1), cleanups.Load(), "cleanup must run once on panic unwinding")
	if err, ok := cleanupErr.Load().(error); ok {
		require.Error(t, err)
	} else {
		t.Fatal("cleanup must observe the receive failure")
	}

	require.NoError(t, wles.Close())
	assert.Equal(t, int32(1), cleanups.Load(), "later Close must not re-run the spent cleanup")
}

// The wrapper must stop the inner receive lifetime before running the shared
// cleanup (which releases the replay root): Close keeps inner-then-cleanup
// order even with the panic-safe defer.
func TestLargePayloadLifecycle_WireEventStream_Close_CleansUpAfterInnerClose(t *testing.T) {
	var order []string
	var mu sync.Mutex
	record := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, s)
	}
	wles := &wireLifecycleEventStream{
		EventStream: &lplcOrderStream{events: &order, mu: &mu},
		cleanup: func(err error) {
			record("cleanup")
		},
	}

	require.NoError(t, wles.Close())
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"inner_close", "cleanup"}, order)
}

type lplcOrderStream struct {
	events *[]string
	mu     *sync.Mutex
}

func (s *lplcOrderStream) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, io.EOF
}

func (s *lplcOrderStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	*s.events = append(*s.events, "inner_close")
	return nil
}

// lplcPanicCloseStream panics on Close, recording every call so a test can
// prove the wrapper never closes it twice.
type lplcPanicCloseStream struct {
	aipapi.EventStream
	closeCalls atomic.Int32
}

func (s *lplcPanicCloseStream) Close() error {
	s.closeCalls.Add(1)
	panic("inner close exploded")
}

// A panicking inner Close must still run the once-cleanup (source disposal
// and the turn audit) before the close panic propagates, and a later Close
// must not re-close the inner stream or re-run the spent cleanup
// (Requirement 1.3; the Close teardown ordering is stated in
// executor_execute_large_body.go).
func TestLargePayloadLifecycle_WireEventStream_Close_CleanupRunsBeforeInnerClosePanic(t *testing.T) {
	inner := &lplcPanicCloseStream{EventStream: aipapi.NewFixedEventStream(nil)}
	var cleanups atomic.Int32
	wles := &wireLifecycleEventStream{
		EventStream: inner,
		cleanup:     func(error) { cleanups.Add(1) },
	}

	require.PanicsWithValue(t, "inner close exploded", func() { _ = wles.Close() })
	require.Equal(t, int32(1), cleanups.Load(), "the once-cleanup must run before the close panic propagates")
	require.Equal(t, int32(1), inner.closeCalls.Load())

	require.NoError(t, wles.Close(), "a later Close observes the resolved gate")
	require.Equal(t, int32(1), inner.closeCalls.Load(), "the inner stream must not be closed twice")
	require.Equal(t, int32(1), cleanups.Load(), "the spent cleanup must not run again")
}
