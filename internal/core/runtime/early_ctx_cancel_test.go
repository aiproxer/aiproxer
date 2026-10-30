package runtime

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
)

type earlyCancelFinishObs struct {
	finishN atomic.Int64
	mu      sync.Mutex
	last    response.StreamOutcome
}

func (o *earlyCancelFinishObs) Observe(context.Context, aipapi.Event) error { return nil }
func (o *earlyCancelFinishObs) Finish(_ context.Context, outcome response.StreamOutcome) error {
	o.finishN.Add(1)
	o.mu.Lock()
	o.last = outcome
	o.mu.Unlock()
	return nil
}

type earlyCancelFinishFactory struct {
	obs *earlyCancelFinishObs
}

func (f earlyCancelFinishFactory) ID() string                      { return "early-cancel-finish" }
func (earlyCancelFinishFactory) Order() int                        { return 0 }
func (earlyCancelFinishFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (f earlyCancelFinishFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return f.obs, nil
}

func TestRecv_earlyCtxCancel_nilExecutorWithInner_noPanic(t *testing.T) {
	t.Parallel()
	inner := &ttftBlockingRecvStream{}
	rs := &retryRecvStream{
		responsePipeline: &responsePipeline{bus: hooks.New(hooks.Config{})},
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: aipapi.Call{ID: "nil-exec", Messages: testMinimalUserMessages()},
			aLegID:   "a1",
			traceID:  "t1",
		}),
		recovery: &recoveryController{budget: &attemptBudget{max: 1}, session: &routing.SessionRoutingState{}, excluded: map[string]struct{}{}}, attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b1", Seq: 1}, routing.AttemptCandidate{Key: "be:m", Primary: routing.Primary{Backend: "be", Model: "m"}}, authorityLifecycle{}),
	}
	testStoreInner(rs, inner)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Recv panicked with nil executor: %v", r)
		}
	}()
	_, err := rs.Recv(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want context.Canceled", err)
	}
	if !inner.closed.Load() {
		t.Fatal("inner stream must be closed on early cancel")
	}
}

func TestRecv_earlyCtxCancel_nilInner_cancelledOutcomeAndAuthorityOnce(t *testing.T) {
	t.Parallel()
	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-swallowed",
			ReservedAmount: authorityInputAmount(5),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)

	obs := &earlyCancelFinishObs{}
	sess := &extensions.FinalStreamObservationSession{}
	if err := sess.Open(context.Background(), []response.StreamObserverFactory{earlyCancelFinishFactory{obs: obs}}, response.StreamMeta{
		TraceID: "t-early", ALegID: aLegID, BLegID: "b-prior",
	}, response.Services{}); err != nil {
		t.Fatalf("open observation session: %v", err)
	}

	sel, err := routing.Parse("backend-1:model-1")
	if err != nil {
		t.Fatal(err)
	}
	bleg, err := ex.Store.NextBLeg(context.Background(), aLegID)
	if err != nil {
		t.Fatalf("NextBLeg: %v", err)
	}
	initial := attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(5),
		admissionResult: auth.admitResult,
	}
	initial.admissionResult.ReservationID = "reservation-swallowed"
	cand := routing.AttemptCandidate{Key: "initial", Primary: routing.Primary{Backend: "initial", Model: "initial"}}
	rs := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: aipapi.Call{ID: "early-nil-inner", Messages: testMinimalUserMessages(), Route: aipapi.RouteIntent{Selector: "backend-1:model-1"}},
			aLegID:   aLegID,
			traceID:  "t-early",
		}),
		recovery: &recoveryController{budget: &attemptBudget{max: 3}, sel: sel, session: &routing.SessionRoutingState{}, excluded: map[string]struct{}{}, rng: routing.NewSeededRng(1)}, attempt: testAttemptSlot(bleg, cand, testAuthorityLifecycle(ex, initial, cand), newAttemptAccountingTracker(time.Unix(1, 0))),
	}
	testAttemptSession(rs).finalStreamObs = sess
	bindTestRuntimeOwners(rs, ex)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = rs.Recv(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want context.Canceled", err)
	}
	if got := obs.finishN.Load(); got != 1 {
		t.Fatalf("Finish calls=%d want 1", got)
	}
	obs.mu.Lock()
	gotOutcome := obs.last
	obs.mu.Unlock()
	if gotOutcome != response.OutcomeCancelled {
		t.Fatalf("Finish outcome=%q want %q", gotOutcome, response.OutcomeCancelled)
	}
	// Opened-attempt lifecycles default backendAttempted=true, so dual-plane Phase 1
	// settles incurred operator liability instead of a pre-work Release.
	if auth.releaseCalls.Load() != 0 {
		t.Fatalf("release calls=%d want 0 (incurred early cancel must settle)", auth.releaseCalls.Load())
	}
	if auth.settleCalls.Load() != 1 {
		t.Fatalf("settle calls=%d want 1", auth.settleCalls.Load())
	}
	settle := auth.lastSettle()
	if settle.Kind != usagelimit.SettlementKindSwallowed {
		t.Fatalf("settle kind=%q want swallowed", settle.Kind)
	}
	if !settle.BackendAttempted {
		t.Fatal("expected incurred early-cancel settle to record backendAttempted=true")
	}

	atts, err := ex.Store.LoadAttempts(context.Background(), aLegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 1 || atts[0].Outcome != aipapi.AttemptCancelled {
		t.Fatalf("attempts=%#v want one AttemptCancelled", atts)
	}

	_, err = rs.Recv(ctx)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("second Recv err=%v want io.EOF", err)
	}
	if got := obs.finishN.Load(); got != 1 {
		t.Fatalf("Finish must be exactly once; got %d", got)
	}
	_ = rs.Close()
	if got := obs.finishN.Load(); got != 1 {
		t.Fatalf("Close must not Finish again; got %d", got)
	}
	if auth.releaseCalls.Load() != 0 {
		t.Fatalf("authority release must stay unused; got %d", auth.releaseCalls.Load())
	}
	if auth.settleCalls.Load() != 1 {
		t.Fatalf("authority settle must stay exactly once; got %d", auth.settleCalls.Load())
	}
}

func TestRecv_earlyCtxCancel_nilInner_deadlineCancelledOutcome(t *testing.T) {
	t.Parallel()
	_, _, aLegID := newAuthorityRuntimeTestExecutor(t, nil)
	obs := &earlyCancelFinishObs{}
	sess := &extensions.FinalStreamObservationSession{}
	if err := sess.Open(context.Background(), []response.StreamObserverFactory{earlyCancelFinishFactory{obs: obs}}, response.StreamMeta{
		TraceID: "t-dl", ALegID: aLegID, BLegID: "b1",
	}, response.Services{}); err != nil {
		t.Fatal(err)
	}
	rs := &retryRecvStream{
		responsePipeline: &responsePipeline{bus: hooks.New(hooks.Config{})},
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: aipapi.Call{ID: "dl", Messages: testMinimalUserMessages()},
			aLegID:   aLegID,
			traceID:  "t-dl",
		}),
		recovery: &recoveryController{budget: &attemptBudget{max: 1}, session: &routing.SessionRoutingState{}, excluded: map[string]struct{}{}}, attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b1", Seq: 1}, routing.AttemptCandidate{Key: "be:m", Primary: routing.Primary{Backend: "be", Model: "m"}}, authorityLifecycle{}, newAttemptAccountingTracker(time.Unix(1, 0))),
	}
	testAttemptSession(rs).finalStreamObs = sess
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err := rs.Recv(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v want DeadlineExceeded", err)
	}
	if obs.finishN.Load() != 1 {
		t.Fatalf("Finish calls=%d want 1", obs.finishN.Load())
	}
	obs.mu.Lock()
	defer obs.mu.Unlock()
	if obs.last != response.OutcomeCancelled {
		t.Fatalf("outcome=%q want cancelled", obs.last)
	}
}
