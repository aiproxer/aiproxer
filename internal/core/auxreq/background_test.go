package auxreq_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/genpin"
)

type backgroundRunner func(context.Context, *aipapi.Call) (aipapi.EventStream, error)

func (r backgroundRunner) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	return r(ctx, call)
}

type countingPin struct{ releases atomic.Int32 }

func (p *countingPin) Kind() genpin.Kind { return genpin.KindAsync }
func (p *countingPin) Release()          { p.releases.Add(1) }

type countingRetainer struct {
	pin     *countingPin
	retains atomic.Int32
	allow   atomic.Bool
}

func (r *countingRetainer) RuntimeInstanceID() string   { return "instance" }
func (r *countingRetainer) RuntimeGenerationID() string { return "generation" }
func (r *countingRetainer) Retain(kind genpin.Kind) (genpin.Pin, bool) {
	if kind != genpin.KindAsync || !r.allow.Load() {
		return nil, false
	}
	r.retains.Add(1)
	return r.pin, true
}

func finishedStream() aipapi.EventStream {
	return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventResponseFinished}})
}

func backgroundRequest() auxiliary.Request {
	return auxiliary.Request{Call: &aipapi.Call{Route: aipapi.RouteIntent{Selector: "local:test"}}}
}

func newBackground(root context.Context, t *testing.T, runner func() auxreq.ExecutorRunner, cfg auxreq.SchedulerConfig) *auxreq.BackgroundScheduler {
	t.Helper()
	s, err := auxreq.NewBackgroundScheduler(root, runner, cfg)
	if err != nil {
		t.Fatalf("NewBackgroundScheduler: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestBackgroundScheduler_CoalescesCommittedKeysAndBoundsResults(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return backgroundRunner(func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
			calls.Add(1)
			return finishedStream(), nil
		})
	}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 1, MaxResults: 1})

	first, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "branch/tx/rev"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "branch/tx/rev"})
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("coalesced id=%q want %q", second, first)
	}
	if _, err := s.Await(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("runner calls=%d want 1", calls.Load())
	}
	if _, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{}); !errors.Is(err, auxreq.ErrInvalidCoalesceKey) {
		t.Fatalf("empty key err=%v want ErrInvalidCoalesceKey", err)
	}
}

func TestBackgroundScheduler_SaturationIsBoundedAndDoesNotFallback(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return backgroundRunner(func(ctx context.Context, _ *aipapi.Call) (aipapi.EventStream, error) {
			calls.Add(1)
			select {
			case <-started:
			default:
				close(started)
			}
			<-release
			return finishedStream(), nil
		})
	}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 1, MaxResults: 4})

	if _, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "one"}); err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "two"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "three"}); !errors.Is(err, auxreq.ErrQueueFull) {
		t.Fatalf("saturated err=%v want ErrQueueFull", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("runner calls=%d want 1 while queue is saturated", calls.Load())
	}
	close(release)
}

func TestBackgroundScheduler_ParentCancellationDoesNotCancelDelayedWorker(t *testing.T) {
	t.Parallel()
	start := make(chan struct{})
	var gotCanceled atomic.Bool
	parent, cancel := context.WithCancel(context.Background())
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return backgroundRunner(func(ctx context.Context, _ *aipapi.Call) (aipapi.EventStream, error) {
			if ctx.Err() != nil {
				gotCanceled.Store(true)
			}
			close(start)
			return finishedStream(), nil
		})
	}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 1})

	id, err := s.SubmitCollect(parent, backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "delayed"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := s.Await(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	<-start
	if gotCanceled.Load() {
		t.Fatal("worker inherited canceled parent context")
	}
}

func TestBackgroundScheduler_DetachedBindingSurvivesDelayedStartAfterParentCancellation(t *testing.T) {
	t.Parallel()
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var calls atomic.Int32
	var binding atomic.Value
	runner := backgroundRunner(func(ctx context.Context, _ *aipapi.Call) (aipapi.EventStream, error) {
		if calls.Add(1) == 1 {
			close(firstStarted)
			<-releaseFirst
			return finishedStream(), nil
		}
		meta, ok := execctx.DetachedSessionFromContext(ctx)
		if ok {
			binding.Store(meta.ParentBranchBinding)
		}
		return finishedStream(), nil
	})
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner { return runner }, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 1})
	if _, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "first"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first worker did not start")
	}
	parent, cancel := context.WithCancel(context.Background())
	second := backgroundRequest()
	second.SessionMode = auxiliary.SessionModeDetached
	second.ParentBranchBinding = "captured-parent-branch"
	id, err := s.SubmitCollect(parent, second, auxiliary.SubmitOptions{CoalesceKey: "second"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	close(releaseFirst)
	if _, err := s.Await(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	got, ok := binding.Load().(string)
	if !ok || got != "captured-parent-branch" {
		t.Fatalf("delayed detached binding=%q ok=%v", got, ok)
	}
}

func TestBackgroundScheduler_PinReleasedOnCompletionAndShutdown(t *testing.T) {
	t.Parallel()
	ret := &countingRetainer{pin: &countingPin{}}
	ret.allow.Store(true)
	ctx := genpin.WithRetainer(context.Background(), ret)
	workerStarted := make(chan struct{})
	cancelObserved := make(chan struct{})
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return backgroundRunner(func(ctx context.Context, _ *aipapi.Call) (aipapi.EventStream, error) {
			return &cancelAwareStream{started: workerStarted, canceled: cancelObserved}, nil
		})
	}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 1})
	if _, err := s.SubmitCollect(ctx, backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "pin"}); err != nil {
		t.Fatal(err)
	}
	if ret.retains.Load() != 1 {
		t.Fatalf("retains=%d want 1 synchronously", ret.retains.Load())
	}
	select {
	case <-workerStarted:
	case <-time.After(time.Second):
		t.Fatal("worker did not start before shutdown")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelObserved:
	case <-time.After(time.Second):
		t.Fatal("worker did not observe scheduler context cancellation")
	}
	if got := ret.pin.releases.Load(); got != 1 {
		t.Fatalf("pin releases=%d want exactly 1", got)
	}
}

func TestBackgroundScheduler_CoalescedSubmissionDoesNotResolveRunnerOrRetain(t *testing.T) {
	t.Parallel()
	ret := &countingRetainer{pin: &countingPin{}}
	ret.allow.Store(true)
	ctx := genpin.WithRetainer(context.Background(), ret)
	var providerCalls atomic.Int32
	var unavailable atomic.Bool
	runner := backgroundRunner(func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
		return finishedStream(), nil
	})
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		providerCalls.Add(1)
		if unavailable.Load() {
			return nil
		}
		return runner
	}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 1})

	id, err := s.SubmitCollect(ctx, backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "existing"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Await(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if got := providerCalls.Load(); got != 1 {
		t.Fatalf("provider calls=%d want 1 after first submission", got)
	}
	ret.allow.Store(false)
	unavailable.Store(true)
	duplicate, err := s.SubmitCollect(ctx, backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "existing"})
	if err != nil {
		t.Fatalf("coalesced submission: %v", err)
	}
	if duplicate != id {
		t.Fatalf("coalesced id=%q want %q", duplicate, id)
	}
	if got := providerCalls.Load(); got != 1 {
		t.Fatalf("provider calls=%d want unchanged at 1", got)
	}
	if got := ret.retains.Load(); got != 1 {
		t.Fatalf("retains=%d want unchanged at 1", got)
	}
}

func TestBackgroundScheduler_PinReleasedOnceOnQueueFullAndClosedAdmission(t *testing.T) {
	t.Parallel()
	ret := &countingRetainer{pin: &countingPin{}}
	ret.allow.Store(true)
	ctx := genpin.WithRetainer(context.Background(), ret)
	workerStarted := make(chan struct{})
	var calls atomic.Int32
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return backgroundRunner(func(ctx context.Context, _ *aipapi.Call) (aipapi.EventStream, error) {
			if calls.Add(1) == 1 {
				return &cancelAwareStream{started: workerStarted, canceled: make(chan struct{})}, nil
			}
			return &cancelOnlyStream{}, nil
		})
	}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 1})

	if _, err := s.SubmitCollect(ctx, backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "active"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-workerStarted:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	if _, err := s.SubmitCollect(ctx, backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "queued"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitCollect(ctx, backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "full"}); !errors.Is(err, auxreq.ErrQueueFull) {
		t.Fatalf("queue-full error=%v want ErrQueueFull", err)
	}
	if got := ret.pin.releases.Load(); got != 1 {
		t.Fatalf("queue-full pin releases=%d want exactly 1", got)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	beforeClosed := ret.pin.releases.Load()
	closedProviderEntered := make(chan struct{})
	closedProviderRelease := make(chan struct{})
	closedScheduler, err := auxreq.NewBackgroundScheduler(context.Background(), func() auxreq.ExecutorRunner {
		close(closedProviderEntered)
		<-closedProviderRelease
		return cancelOnlyRunner{}
	}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 1})
	if err != nil {
		t.Fatal(err)
	}
	submitDone := make(chan error, 1)
	go func() {
		_, submitErr := closedScheduler.SubmitCollect(ctx, backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "closed"})
		submitDone <- submitErr
	}()
	select {
	case <-closedProviderEntered:
	case <-time.After(time.Second):
		_ = closedScheduler.Close()
		t.Fatal("closed submission did not resolve runner")
	}
	if err := closedScheduler.Close(); err != nil {
		t.Fatal(err)
	}
	close(closedProviderRelease)
	select {
	case err := <-submitDone:
		if !errors.Is(err, auxreq.ErrSchedulerClosed) {
			t.Fatalf("closed error=%v want ErrSchedulerClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("closed submission did not return")
	}
	if got := ret.pin.releases.Load(); got != beforeClosed+1 {
		t.Fatalf("closed admission releases=%d want exactly one new release after %d", got, beforeClosed)
	}
}

func TestBackgroundScheduler_RunnerPanicReleasesPinOnce(t *testing.T) {
	t.Parallel()
	ret := &countingRetainer{pin: &countingPin{}}
	ret.allow.Store(true)
	ctx := genpin.WithRetainer(context.Background(), ret)
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return backgroundRunner(func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
			panic("runner panic")
		})
	}, auxreq.SchedulerConfig{})
	id, err := s.SubmitCollect(ctx, backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "panic"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Await(context.Background(), id); err == nil || !strings.Contains(err.Error(), "runner panic") {
		t.Fatalf("panic result error=%v want runner panic", err)
	}
	if got := ret.pin.releases.Load(); got != 1 {
		t.Fatalf("panic pin releases=%d want exactly 1", got)
	}
}

func TestBackgroundScheduler_EvictsResultsByCount(t *testing.T) {
	t.Parallel()
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return backgroundRunner(func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
			return finishedStream(), nil
		})
	}, auxreq.SchedulerConfig{MaxResults: 1})
	first, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "result-one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Await(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "result-two"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Await(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Await(context.Background(), first); !errors.Is(err, auxreq.ErrJobNotFound) {
		t.Fatalf("evicted first result error=%v want ErrJobNotFound", err)
	}
}

func TestBackgroundScheduler_ExpiresResultsByTTL(t *testing.T) {
	t.Parallel()
	clock := newTestClock()
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return backgroundRunner(func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
			return finishedStream(), nil
		})
	}, auxreq.SchedulerConfig{ResultTTL: time.Minute, Now: clock.Now})
	id, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "ttl"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Await(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Minute)
	if _, err := s.Await(context.Background(), id); !errors.Is(err, auxreq.ErrJobNotFound) {
		t.Fatalf("expired result error=%v want ErrJobNotFound", err)
	}
}

func TestBackgroundScheduler_ConcurrentAwaitForgetLateCompletion(t *testing.T) {
	t.Parallel()
	ret := &countingRetainer{pin: &countingPin{}}
	ret.allow.Store(true)
	ctx := genpin.WithRetainer(context.Background(), ret)
	workerStarted := make(chan struct{})
	release := make(chan struct{})
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return backgroundRunner(func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
			return &gatedStream{started: workerStarted, release: release}, nil
		})
	}, auxreq.SchedulerConfig{Workers: 1})
	id, err := s.SubmitCollect(ctx, backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "late"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-workerStarted:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	start := make(chan struct{})
	awaitErr := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, awaitErrValue := s.Await(context.Background(), id)
		awaitErr <- awaitErrValue
	}()
	go func() {
		defer wg.Done()
		<-start
		s.Forget(id)
	}()
	close(start)
	close(release)
	wg.Wait()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-awaitErr; err != nil && !errors.Is(err, auxreq.ErrJobNotFound) {
		t.Fatalf("concurrent Await error=%v", err)
	}
	if got := ret.pin.releases.Load(); got != 1 {
		t.Fatalf("late completion pin releases=%d want exactly 1", got)
	}
}

func TestBackgroundScheduler_ForgetRemovesResult(t *testing.T) {
	t.Parallel()
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return backgroundRunner(func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
			return finishedStream(), nil
		})
	}, auxreq.SchedulerConfig{})
	id, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "forget"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Await(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	s.Forget(id)
	if _, err := s.Await(context.Background(), id); !errors.Is(err, auxreq.ErrJobNotFound) {
		t.Fatalf("Await after Forget=%v want ErrJobNotFound", err)
	}
}

func TestBackgroundScheduler_TimeoutReleasesPin(t *testing.T) {
	t.Parallel()
	ret := &countingRetainer{pin: &countingPin{}}
	ret.allow.Store(true)
	ctx := genpin.WithRetainer(context.Background(), ret)
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return backgroundRunner(func(ctx context.Context, _ *aipapi.Call) (aipapi.EventStream, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		})
	}, auxreq.SchedulerConfig{JobTimeout: 5 * time.Millisecond})
	id, err := s.SubmitCollect(ctx, backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "timeout"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Await(context.Background(), id); err == nil {
		t.Fatal("timeout job unexpectedly succeeded")
	}
	if got := ret.pin.releases.Load(); got != 1 {
		t.Fatalf("pin releases=%d want exactly 1", got)
	}
}

type cancelAwareStream struct {
	started  chan<- struct{}
	canceled chan<- struct{}
	start    sync.Once
	cancel   sync.Once
	emitted  bool
}

func (s *cancelAwareStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if !s.emitted {
		s.emitted = true
		s.start.Do(func() { close(s.started) })
		return aipapi.Event{Kind: aipapi.EventResponseStarted}, nil
	}
	<-ctx.Done()
	s.cancel.Do(func() { close(s.canceled) })
	return aipapi.Event{}, ctx.Err()
}

func (s *cancelAwareStream) Close() error { return nil }

type cancelOnlyStream struct{}

func (*cancelOnlyStream) Recv(ctx context.Context) (aipapi.Event, error) {
	<-ctx.Done()
	return aipapi.Event{}, ctx.Err()
}

func (*cancelOnlyStream) Close() error { return nil }

type cancelOnlyRunner struct{}

func (cancelOnlyRunner) Execute(ctx context.Context, _ *aipapi.Call) (aipapi.EventStream, error) {
	return &cancelOnlyStream{}, nil
}

type gatedStream struct {
	started chan<- struct{}
	release <-chan struct{}
	start   sync.Once
	emitted bool
}

func (s *gatedStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if !s.emitted {
		s.emitted = true
		s.start.Do(func() { close(s.started) })
		return aipapi.Event{Kind: aipapi.EventResponseStarted}, nil
	}
	select {
	case <-s.release:
		return aipapi.Event{Kind: aipapi.EventResponseFinished}, nil
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	}
}

func (s *gatedStream) Close() error { return nil }

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock { return &testClock{now: time.Unix(100, 0)} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}
