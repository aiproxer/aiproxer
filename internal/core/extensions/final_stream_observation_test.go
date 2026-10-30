package extensions_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

type fsObs struct {
	events []aipapi.Event
	finish response.StreamOutcome
	n      atomic.Int64
	mu     sync.Mutex
}

func (o *fsObs) Observe(_ context.Context, ev aipapi.Event) error {
	o.n.Add(1)
	o.mu.Lock()
	o.events = append(o.events, ev)
	o.mu.Unlock()
	return nil
}

func (o *fsObs) Finish(_ context.Context, outcome response.StreamOutcome) error {
	o.finish = outcome
	return nil
}

type fsFactory struct {
	id      string
	ord     int
	mode    sdkhooks.FailureMode
	obs     response.StreamObserver
	openErr error
}

func (f fsFactory) ID() string                        { return f.id }
func (f fsFactory) Order() int                        { return f.ord }
func (f fsFactory) FailureMode() sdkhooks.FailureMode { return f.mode }
func (f fsFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	return f.obs, nil
}

type mutatingObs struct{}

func (mutatingObs) Observe(_ context.Context, ev aipapi.Event) error {
	if len(ev.UsageScopes) > 0 {
		ev.UsageScopes[0].InputTokens = 99
	}
	return nil
}
func (mutatingObs) Finish(context.Context, response.StreamOutcome) error { return nil }

type mutatingScopeObs struct {
	seen scope.PrincipalScopeView
}

func (m *mutatingScopeObs) Observe(_ context.Context, ev aipapi.Event) error { return nil }
func (m *mutatingScopeObs) Finish(_ context.Context, _ response.StreamOutcome) error {
	return nil
}

func (m *mutatingScopeObs) capture(meta response.StreamMeta) {
	m.seen = meta.Scope
	meta.Scope.PrincipalID = scope.Known("mutated")
}

type scopeCaptureFactory struct {
	obs *mutatingScopeObs
}

func (f scopeCaptureFactory) ID() string                        { return "scope-cap" }
func (f scopeCaptureFactory) Order() int                        { return 0 }
func (f scopeCaptureFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (f scopeCaptureFactory) Open(_ context.Context, meta response.StreamMeta, _ response.Services) (response.StreamObserver, error) {
	f.obs.capture(meta)
	return f.obs, nil
}

type failObs struct{}

func (failObs) Observe(context.Context, aipapi.Event) error { return errors.New("observe fail") }
func (failObs) Finish(context.Context, response.StreamOutcome) error {
	return nil
}

type failClosedObs struct{}

func (failClosedObs) Observe(context.Context, aipapi.Event) error { return errors.New("observe fail") }

func (failClosedObs) Finish(context.Context, response.StreamOutcome) error {
	return nil
}

type countingFinishObs struct {
	finishN  atomic.Int64
	observeN atomic.Int64
	mu       sync.Mutex
	finish   response.StreamOutcome
}

func (c *countingFinishObs) Observe(context.Context, aipapi.Event) error {
	c.observeN.Add(1)
	return nil
}

func (c *countingFinishObs) Finish(_ context.Context, outcome response.StreamOutcome) error {
	c.mu.Lock()
	c.finish = outcome
	c.mu.Unlock()
	c.finishN.Add(1)
	return nil
}

func TestRunFinalStreamObservationStage_defensiveCopyUsageScopes(t *testing.T) {
	t.Parallel()
	sess := &extensions.FinalStreamObservationSession{}
	if err := sess.Open(t.Context(), []response.StreamObserverFactory{
		fsFactory{id: "m", mode: sdkhooks.FailOpen, obs: mutatingObs{}},
	}, response.StreamMeta{BLegID: "b1"}, response.Services{}); err != nil {
		t.Fatal(err)
	}
	ev := aipapi.Event{
		Kind: aipapi.EventUsageDelta,
		UsageScopes: []aipapi.ScopedUsageDelta{{
			InputTokens: 1,
			Accounting:  aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneClientVisible},
		}},
	}
	if err := extensions.RunFinalStreamObservationStage(t.Context(), nil, nil, sess, ev, false); err != nil {
		t.Fatal(err)
	}
	if ev.UsageScopes[0].InputTokens != 1 {
		t.Fatalf("source mutated: %#v", ev.UsageScopes)
	}
}

func TestRunFinalStreamObservationStage_failOpenContinues(t *testing.T) {
	t.Parallel()
	good := &fsObs{}
	sess := &extensions.FinalStreamObservationSession{}
	if err := sess.Open(t.Context(), []response.StreamObserverFactory{
		fsFactory{id: "bad", mode: sdkhooks.FailOpen, obs: failObs{}},
		fsFactory{id: "good", ord: 1, mode: sdkhooks.FailOpen, obs: good},
	}, response.StreamMeta{BLegID: "b1"}, response.Services{}); err != nil {
		t.Fatal(err)
	}
	if err := extensions.RunFinalStreamObservationStage(t.Context(), nil, nil, sess, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}, false); err != nil {
		t.Fatal(err)
	}
	if good.n.Load() != 1 {
		t.Fatal("fail-open must continue chain")
	}
}

func TestRunFinalStreamObservationStage_failClosedPreCommitReturnsError(t *testing.T) {
	t.Parallel()
	sess := &extensions.FinalStreamObservationSession{}
	if err := sess.Open(t.Context(), []response.StreamObserverFactory{
		fsFactory{id: "fc", mode: sdkhooks.FailClosed, obs: failClosedObs{}},
	}, response.StreamMeta{BLegID: "b1"}, response.Services{}); err != nil {
		t.Fatal(err)
	}
	err := extensions.RunFinalStreamObservationStage(t.Context(), nil, nil, sess, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}, false)
	if err == nil {
		t.Fatal("want fail-closed pre-commit observe error")
	}
}

func TestRunFinalStreamObservationStage_failClosedPostCommitIsolates(t *testing.T) {
	t.Parallel()
	sess := &extensions.FinalStreamObservationSession{}
	if err := sess.Open(t.Context(), []response.StreamObserverFactory{
		fsFactory{id: "fc", mode: sdkhooks.FailClosed, obs: failClosedObs{}},
	}, response.StreamMeta{BLegID: "b1"}, response.Services{}); err != nil {
		t.Fatal(err)
	}
	if err := extensions.RunFinalStreamObservationStage(t.Context(), nil, nil, sess, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}, true); err != nil {
		t.Fatalf("post-commit must isolate fail-closed observe: %v", err)
	}
}

func TestRunFinalStreamObservationSession_openFailOpenAndFinishOnce(t *testing.T) {
	t.Parallel()
	obs := &fsObs{}
	sess := &extensions.FinalStreamObservationSession{}
	if err := sess.Open(t.Context(), []response.StreamObserverFactory{
		fsFactory{id: "err", mode: sdkhooks.FailOpen, openErr: errors.New("open fail")},
		fsFactory{id: "ok", ord: 1, mode: sdkhooks.FailOpen, obs: obs},
	}, response.StreamMeta{BLegID: "b1"}, response.Services{}); err != nil {
		t.Fatal(err)
	}
	if err := extensions.RunFinalStreamObservationStage(t.Context(), nil, nil, sess, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "a"}, false); err != nil {
		t.Fatal(err)
	}
	sess.Finish(t.Context(), response.OutcomeSuccessReleased)
	sess.Finish(t.Context(), response.OutcomeFailed)
	if obs.finish != response.OutcomeSuccessReleased {
		t.Fatalf("finish=%q", obs.finish)
	}
	if len(obs.events) != 1 || obs.events[0].Delta != "a" {
		t.Fatalf("events=%#v", obs.events)
	}
}

func TestRunFinalStreamObservationSession_openFailClosed(t *testing.T) {
	t.Parallel()
	sess := &extensions.FinalStreamObservationSession{}
	err := sess.Open(t.Context(), []response.StreamObserverFactory{
		fsFactory{id: "fc", mode: sdkhooks.FailClosed, openErr: errors.New("open fail")},
	}, response.StreamMeta{BLegID: "b1"}, response.Services{})
	if err == nil {
		t.Fatal("want fail-closed open error")
	}
}

func TestRunFinalStreamObservationSession_openFailClosedOrphanCleanup(t *testing.T) {
	t.Parallel()
	opened := &countingFinishObs{}
	sess := &extensions.FinalStreamObservationSession{}
	err := sess.Open(t.Context(), []response.StreamObserverFactory{
		fsFactory{id: "ok", mode: sdkhooks.FailOpen, obs: opened},
		fsFactory{id: "fc", ord: 1, mode: sdkhooks.FailClosed, openErr: errors.New("open fail")},
	}, response.StreamMeta{BLegID: "b1"}, response.Services{})
	if err == nil {
		t.Fatal("want fail-closed open error")
	}
	if opened.finishN.Load() != 1 {
		t.Fatalf("orphan cleanup Finish once with failed; got %d", opened.finishN.Load())
	}
}

type orderedFSObs struct {
	mu                 sync.Mutex
	ops                []string
	observeAfterFinish bool
	finished           bool
	observeEntered     chan struct{}
	releaseObserve     chan struct{}
	enteredOnce        sync.Once
	finish             response.StreamOutcome
}

func (o *orderedFSObs) Observe(_ context.Context, ev aipapi.Event) error {
	if o.observeEntered != nil {
		o.enteredOnce.Do(func() { close(o.observeEntered) })
	}
	if o.releaseObserve != nil {
		<-o.releaseObserve
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.finished {
		o.observeAfterFinish = true
	}
	o.ops = append(o.ops, "observe:"+string(ev.Kind))
	return nil
}

func (o *orderedFSObs) Finish(_ context.Context, outcome response.StreamOutcome) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.finished = true
	o.finish = outcome
	o.ops = append(o.ops, "finish:"+string(outcome))
	return nil
}

func TestFinalStreamObservationSession_concurrentObserveFinish(t *testing.T) {
	t.Parallel()
	obs := &orderedFSObs{
		observeEntered: make(chan struct{}),
		releaseObserve: make(chan struct{}),
	}
	sess := &extensions.FinalStreamObservationSession{}
	if err := sess.Open(t.Context(), []response.StreamObserverFactory{
		fsFactory{id: "ok", mode: sdkhooks.FailOpen, obs: obs},
	}, response.StreamMeta{BLegID: "b1"}, response.Services{}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = extensions.RunFinalStreamObservationStage(t.Context(), nil, nil, sess, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}, false)
	}()
	select {
	case <-obs.observeEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("Observe did not start")
	}
	finishReturned := make(chan struct{})
	go func() {
		defer close(finishReturned)
		sess.Finish(t.Context(), response.OutcomeClosed)
	}()
	// Finish may return before callbacks; callbacks must not run while Observe is in flight.
	select {
	case <-finishReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("Finish did not return")
	}
	time.Sleep(20 * time.Millisecond)
	obs.mu.Lock()
	if obs.finished {
		obs.mu.Unlock()
		t.Fatal("Finish callback must not run until Observe completes")
	}
	obs.mu.Unlock()
	close(obs.releaseObserve)
	<-done
	deadline := time.After(2 * time.Second)
	for {
		obs.mu.Lock()
		doneFinish := obs.finished
		ops := append([]string(nil), obs.ops...)
		after := obs.observeAfterFinish
		finish := obs.finish
		obs.mu.Unlock()
		if doneFinish {
			if after {
				t.Fatalf("Observe after Finish; ops=%v", ops)
			}
			if finish != response.OutcomeClosed {
				t.Fatalf("finish=%q ops=%v", finish, ops)
			}
			if len(ops) != 2 || ops[0] != "observe:text_delta" || ops[1] != "finish:closed" {
				t.Fatalf("want observe then finish:closed; ops=%v", ops)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("Finish callback never ran; ops=%v", ops)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

type blockingOpenFactory struct {
	entered     chan struct{}
	release     chan struct{}
	obs         *countingFinishObs
	enteredOnce sync.Once
}

func (f *blockingOpenFactory) ID() string                        { return "blocking-open" }
func (f *blockingOpenFactory) Order() int                        { return 0 }
func (f *blockingOpenFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (f *blockingOpenFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	f.enteredOnce.Do(func() { close(f.entered) })
	<-f.release
	return f.obs, nil
}

func TestFinalStreamObservationSession_finishDuringOpenFinalizesCreated(t *testing.T) {
	t.Parallel()
	obs := &countingFinishObs{}
	factory := &blockingOpenFactory{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		obs:     obs,
	}
	sess := &extensions.FinalStreamObservationSession{}
	openDone := make(chan error, 1)
	go func() {
		openDone <- sess.Open(t.Context(), []response.StreamObserverFactory{factory}, response.StreamMeta{BLegID: "b1"}, response.Services{})
	}()
	select {
	case <-factory.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Open factory did not start")
	}
	sess.Finish(t.Context(), response.OutcomeClosed)
	close(factory.release)
	select {
	case err := <-openDone:
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Open did not complete")
	}
	if obs.finishN.Load() != 1 {
		t.Fatalf("Finish during Open must finalize created observer once; got %d", obs.finishN.Load())
	}
	obs.mu.Lock()
	got := obs.finish
	obs.mu.Unlock()
	if got != response.OutcomeClosed {
		t.Fatalf("want Finish(closed); got %q", got)
	}
	if err := extensions.RunFinalStreamObservationStage(t.Context(), nil, nil, sess, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}, false); err != nil {
		t.Fatal(err)
	}
	if obs.observeN.Load() != 0 {
		t.Fatalf("no live slots after Finish-during-Open; observes=%d", obs.observeN.Load())
	}
	before := obs.finishN.Load()
	sess.Finish(t.Context(), response.OutcomeFailed)
	if obs.finishN.Load() != before {
		t.Fatalf("second Finish must not re-finalize; got %d", obs.finishN.Load())
	}
}

func TestFinalStreamObservationSession_reentrantFinishFromObserveNoDeadlock(t *testing.T) {
	t.Parallel()
	sess := &extensions.FinalStreamObservationSession{}
	obs := &reentrantFinishObs{sess: sess, outcome: response.OutcomeClosed}
	if err := sess.Open(t.Context(), []response.StreamObserverFactory{
		fsFactory{id: "reentrant", mode: sdkhooks.FailOpen, obs: obs},
	}, response.StreamMeta{BLegID: "b1"}, response.Services{}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- extensions.RunFinalStreamObservationStage(t.Context(), nil, nil, sess, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}, false)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Observe: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock: reentrant Finish from Observe did not complete")
	}
	if obs.finishN.Load() != 1 {
		t.Fatalf("want Finish once; got %d", obs.finishN.Load())
	}
	if obs.finish != response.OutcomeClosed {
		t.Fatalf("finish=%q", obs.finish)
	}
	if obs.observeAfterFinish {
		t.Fatal("Observe body must complete before Finish callback")
	}
}

type reentrantFinishObs struct {
	sess               *extensions.FinalStreamObservationSession
	outcome            response.StreamOutcome
	finishN            atomic.Int64
	finish             response.StreamOutcome
	observeAfterFinish bool
	mu                 sync.Mutex
	finished           bool
}

func (o *reentrantFinishObs) Observe(ctx context.Context, _ aipapi.Event) error {
	o.sess.Finish(ctx, o.outcome)
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.finished {
		o.observeAfterFinish = true
	}
	return nil
}

func (o *reentrantFinishObs) Finish(_ context.Context, outcome response.StreamOutcome) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.finished = true
	o.finish = outcome
	o.finishN.Add(1)
	return nil
}

func TestFinalStreamObservationSession_sequentialOpenWithoutFinishReplaces(t *testing.T) {
	t.Parallel()
	first := &countingFinishObs{}
	second := &countingFinishObs{}
	sess := &extensions.FinalStreamObservationSession{}
	if err := sess.Open(t.Context(), []response.StreamObserverFactory{
		fsFactory{id: "a", mode: sdkhooks.FailOpen, obs: first},
	}, response.StreamMeta{BLegID: "b1"}, response.Services{}); err != nil {
		t.Fatal(err)
	}
	if err := sess.Open(t.Context(), []response.StreamObserverFactory{
		fsFactory{id: "b", mode: sdkhooks.FailOpen, obs: second},
	}, response.StreamMeta{BLegID: "b2"}, response.Services{}); err != nil {
		t.Fatal(err)
	}
	if first.finishN.Load() != 1 {
		t.Fatalf("prior lifecycle must Finish once; got %d", first.finishN.Load())
	}
	first.mu.Lock()
	got := first.finish
	first.mu.Unlock()
	if got != response.OutcomeReplaced {
		t.Fatalf("want Finish(replaced); got %q", got)
	}
	if err := extensions.RunFinalStreamObservationStage(t.Context(), nil, nil, sess, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}, false); err != nil {
		t.Fatal(err)
	}
	if first.observeN.Load() != 0 || second.observeN.Load() != 1 {
		t.Fatalf("only replacement lifecycle observes; first=%d second=%d", first.observeN.Load(), second.observeN.Load())
	}
	sess.Finish(t.Context(), response.OutcomeSuccessReleased)
	if second.finishN.Load() != 1 {
		t.Fatalf("second Finish once; got %d", second.finishN.Load())
	}
}

func TestFinalStreamObservationSession_concurrentDualOpenNoOrphan(t *testing.T) {
	t.Parallel()
	firstObs := &countingFinishObs{}
	secondObs := &countingFinishObs{}
	firstFactory := &blockingOpenFactory{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		obs:     firstObs,
	}
	secondFactory := &blockingOpenFactory{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		obs:     secondObs,
	}
	sess := &extensions.FinalStreamObservationSession{}
	open1 := make(chan error, 1)
	open2 := make(chan error, 1)
	go func() {
		open1 <- sess.Open(t.Context(), []response.StreamObserverFactory{firstFactory}, response.StreamMeta{BLegID: "b1"}, response.Services{})
	}()
	select {
	case <-firstFactory.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first Open did not enter factory")
	}
	go func() {
		open2 <- sess.Open(t.Context(), []response.StreamObserverFactory{secondFactory}, response.StreamMeta{BLegID: "b2"}, response.Services{})
	}()
	// Second must not enter factory until first claims complete (opening ownership).
	select {
	case <-secondFactory.entered:
		t.Fatal("second Open entered factory while first still opening")
	case <-time.After(50 * time.Millisecond):
	}
	close(firstFactory.release)
	select {
	case err := <-open1:
		if err != nil {
			t.Fatalf("open1: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("open1 timeout")
	}
	select {
	case <-secondFactory.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("second Open did not start after first completed")
	}
	close(secondFactory.release)
	select {
	case err := <-open2:
		if err != nil {
			t.Fatalf("open2: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("open2 timeout")
	}
	if firstObs.finishN.Load() != 1 {
		t.Fatalf("first observer must be finalized; got %d", firstObs.finishN.Load())
	}
	firstObs.mu.Lock()
	got := firstObs.finish
	firstObs.mu.Unlock()
	if got != response.OutcomeReplaced {
		t.Fatalf("want replaced; got %q", got)
	}
	if secondObs.finishN.Load() != 0 {
		t.Fatalf("second still live; finishN=%d", secondObs.finishN.Load())
	}
	sess.Finish(t.Context(), response.OutcomeClosed)
	if secondObs.finishN.Load() != 1 {
		t.Fatalf("second Finish(closed) once; got %d", secondObs.finishN.Load())
	}
}

func TestFinalStreamObservationSession_openAfterFinishResetsLifecycle(t *testing.T) {
	t.Parallel()
	first := &fsObs{}
	second := &fsObs{}
	sess := &extensions.FinalStreamObservationSession{}
	if err := sess.Open(t.Context(), []response.StreamObserverFactory{
		fsFactory{id: "a", mode: sdkhooks.FailOpen, obs: first},
	}, response.StreamMeta{BLegID: "b1"}, response.Services{}); err != nil {
		t.Fatal(err)
	}
	sess.Finish(t.Context(), response.OutcomeGateReplaced)
	if err := sess.Open(t.Context(), []response.StreamObserverFactory{
		fsFactory{id: "b", mode: sdkhooks.FailOpen, obs: second},
	}, response.StreamMeta{BLegID: "b2"}, response.Services{}); err != nil {
		t.Fatal(err)
	}
	if err := extensions.RunFinalStreamObservationStage(t.Context(), nil, nil, sess, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "next"}, false); err != nil {
		t.Fatal(err)
	}
	sess.Finish(t.Context(), response.OutcomeSuccessReleased)
	if second.finish != response.OutcomeSuccessReleased {
		t.Fatalf("second finish=%q", second.finish)
	}
	if len(second.events) != 1 {
		t.Fatalf("second events=%#v", second.events)
	}
}

func TestStreamObserverMeta_scopeCloneIsolation(t *testing.T) {
	t.Parallel()
	scopeObs := &mutatingScopeObs{}
	factory := scopeCaptureFactory{obs: scopeObs}
	orig := scope.PrincipalScopeView{PrincipalID: scope.Known("orig")}
	meta := response.StreamMeta{BLegID: "b1", Scope: orig.Clone()}
	sess := &extensions.FinalStreamObservationSession{}
	if err := sess.Open(t.Context(), []response.StreamObserverFactory{factory}, meta, response.Services{}); err != nil {
		t.Fatal(err)
	}
	if orig.PrincipalID.String() != "orig" {
		t.Fatalf("source scope mutated: %#v", orig)
	}
	if scopeObs.seen.PrincipalID.String() != "orig" {
		t.Fatalf("observer saw wrong scope: %#v", scopeObs.seen)
	}
}
