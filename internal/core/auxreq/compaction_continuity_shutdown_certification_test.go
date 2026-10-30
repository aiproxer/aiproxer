package auxreq_test

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/genpin"
)

// Task 6.3 certification keeps all external work behind deterministic gates.
func TestCompactionContinuityShutdownCertification_SubmitBoundary(t *testing.T) {
	t.Parallel()
	ret := &countingRetainer{pin: &countingPin{}}
	ret.allow.Store(true)
	ctx := genpin.WithRetainer(context.Background(), ret)
	var calls atomic.Int32
	s := newCertificationScheduler(t, auxreq.SchedulerConfig{}, func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
		calls.Add(1)
		return finishedStream(), nil
	})

	// A preview intent has no committed transaction key. It must be rejected
	// before generation retention or provider execution.
	if _, err := s.SubmitCollect(ctx, backgroundRequest(), auxiliary.SubmitOptions{}); !errors.Is(err, auxreq.ErrInvalidCoalesceKey) {
		t.Fatalf("preview submission error=%v want ErrInvalidCoalesceKey", err)
	}
	if got := ret.retains.Load(); got != 0 {
		t.Fatalf("preview submission retained pins=%d want zero", got)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("preview submission provider calls=%d want zero", got)
	}

	id, err := s.SubmitCollect(ctx, backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "branch/committed-tx/rev-1"})
	if err != nil {
		t.Fatalf("committed submission: %v", err)
	}
	if _, err := s.Await(context.Background(), id); err != nil {
		t.Fatalf("committed Await: %v", err)
	}
	if got := ret.retains.Load(); got != 1 {
		t.Fatalf("committed submission retains=%d want one", got)
	}
	if got := ret.pin.releases.Load(); got != 1 {
		t.Fatalf("committed submission releases=%d want one", got)
	}
}

func TestCompactionContinuityShutdownCertification_QueueCloseAndLateCompletion(t *testing.T) {
	t.Parallel()
	ret := &countingRetainer{pin: &countingPin{}}
	ret.allow.Store(true)
	ctx := genpin.WithRetainer(context.Background(), ret)
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var calls atomic.Int32
	s := newCertificationScheduler(t, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 1, MaxResults: 2}, func(ctx context.Context, _ *aipapi.Call) (aipapi.EventStream, error) {
		if calls.Add(1) == 1 {
			return &certificationLateStream{started: firstStarted, release: releaseFirst}, nil
		}
		return finishedStream(), nil
	})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
	// Register after the scheduler cleanup so this gate is opened before
	// Close joins a worker on every test exit path.
	t.Cleanup(releaseAll)

	first, err := s.SubmitCollect(ctx, backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "branch/tx-1"})
	if err != nil {
		t.Fatalf("first submission: %v", err)
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first worker did not start")
	}
	second, err := s.SubmitCollect(ctx, backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "branch/tx-2"})
	if err != nil {
		t.Fatalf("queued submission: %v", err)
	}
	if _, err := s.SubmitCollect(ctx, backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "branch/tx-3"}); !errors.Is(err, auxreq.ErrQueueFull) {
		t.Fatalf("saturated submission error=%v want ErrQueueFull", err)
	}
	// The failed handoff also releases its tentative pin; the two admitted
	// jobs remain owned until terminal completion/cancellation.
	if got := ret.retains.Load(); got != 3 {
		t.Fatalf("retains after saturation=%d want three attempts", got)
	}
	if got := ret.pin.releases.Load(); got != 1 {
		t.Fatalf("releases after saturation=%d want failed handoff only", got)
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- s.Close() }()

	// Observe the exact admission linearization point. Before Close acquires
	// the scheduler lock the bounded queue may still report full; afterwards
	// every new key is rejected as closed.
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		_, submitErr := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "after-close-probe"})
		if errors.Is(submitErr, auxreq.ErrSchedulerClosed) {
			break
		}
		if !errors.Is(submitErr, auxreq.ErrQueueFull) {
			t.Fatalf("close-race probe error=%v want queue-full or closed", submitErr)
		}
		runtime.Gosched()
		select {
		case <-deadline.C:
			t.Fatal("scheduler Close did not linearize")
		default:
		}
	}

	select {
	case <-closeDone:
		t.Fatal("Close returned before late worker completion")
	default:
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("provider calls before late completion=%d want one; queued work started after close", got)
	}
	releaseAll()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not join late completion")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("provider calls after Close=%d want one", got)
	}
	if got := ret.pin.releases.Load(); got != 3 {
		t.Fatalf("releases after shutdown=%d want one per attempted pin", got)
	}
	if _, err := s.Await(context.Background(), first); err != nil {
		t.Fatalf("late completed job Await=%v", err)
	}
	if _, err := s.Await(context.Background(), second); !errors.Is(err, auxreq.ErrSchedulerClosed) {
		t.Fatalf("queued canceled job Await=%v want ErrSchedulerClosed", err)
	}
}

func TestCompactionContinuityShutdownCertification_WorkerTimeoutAndParentCancellation(t *testing.T) {
	t.Parallel()
	t.Run("timeout", func(t *testing.T) {
		t.Parallel()
		ret := &countingRetainer{pin: &countingPin{}}
		ret.allow.Store(true)
		ctx := genpin.WithRetainer(context.Background(), ret)
		s := newCertificationScheduler(t, auxreq.SchedulerConfig{JobTimeout: 10 * time.Millisecond}, func(ctx context.Context, _ *aipapi.Call) (aipapi.EventStream, error) {
			return &cancelOnlyStream{}, nil
		})
		id, err := s.SubmitCollect(ctx, backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "timeout"})
		if err != nil {
			t.Fatalf("timeout submission: %v", err)
		}
		if _, err := s.Await(context.Background(), id); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("timeout Await=%v want context deadline", err)
		}
		if got := ret.pin.releases.Load(); got != 1 {
			t.Fatalf("timeout releases=%d want one", got)
		}
	})

	t.Run("parent-cancellation", func(t *testing.T) {
		t.Parallel()
		ret := &countingRetainer{pin: &countingPin{}}
		ret.allow.Store(true)
		parent, cancel := context.WithCancel(context.Background())
		ctx := genpin.WithRetainer(parent, ret)
		var workerSawCanceled atomic.Bool
		parentCanceled := make(chan struct{})
		s := newCertificationScheduler(t, auxreq.SchedulerConfig{}, func(ctx context.Context, _ *aipapi.Call) (aipapi.EventStream, error) {
			<-parentCanceled
			workerSawCanceled.Store(ctx.Err() != nil)
			return finishedStream(), nil
		})
		var parentReleaseOnce sync.Once
		releaseParent := func() { parentReleaseOnce.Do(func() { close(parentCanceled) }) }
		// Ensure a failed assertion cannot leave the worker blocked ahead of the
		// scheduler cleanup.
		t.Cleanup(releaseParent)
		id, err := s.SubmitCollect(ctx, backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "parent-cancel"})
		if err != nil {
			t.Fatalf("parent cancellation submission: %v", err)
		}
		cancel()
		releaseParent()
		if _, err := s.Await(context.Background(), id); err != nil {
			t.Fatalf("detached worker Await=%v", err)
		}
		if workerSawCanceled.Load() {
			t.Fatal("worker inherited canceled parent context")
		}
		if got := ret.pin.releases.Load(); got != 1 {
			t.Fatalf("parent cancellation releases=%d want one", got)
		}
	})
}

func TestCompactionContinuityShutdownCertification_ProviderCallbackOutsideSchedulerLock(t *testing.T) {
	t.Parallel()
	var scheduler *auxreq.BackgroundScheduler
	var calls atomic.Int32
	nested := make(chan struct {
		id  auxiliary.JobID
		err error
	}, 1)
	runner := func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
		if calls.Add(1) == 1 {
			id, err := scheduler.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "nested"})
			nested <- struct {
				id  auxiliary.JobID
				err error
			}{id: id, err: err}
		}
		return finishedStream(), nil
	}
	scheduler = newCertificationScheduler(t, auxreq.SchedulerConfig{Workers: 2, QueueCapacity: 2}, runner)
	outer, err := scheduler.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "outer"})
	if err != nil {
		t.Fatalf("outer submission: %v", err)
	}
	if _, err := scheduler.Await(context.Background(), outer); err != nil {
		t.Fatalf("outer Await: %v", err)
	}
	select {
	case result := <-nested:
		if result.err != nil {
			t.Fatalf("nested provider callback submission: %v", result.err)
		}
		if _, err := scheduler.Await(context.Background(), result.id); err != nil {
			t.Fatalf("nested Await: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("provider callback could not re-enter scheduler admission")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("provider calls=%d want outer plus nested", got)
	}
}

func TestCompactionContinuityShutdownCertification_ResultRetentionBound(t *testing.T) {
	t.Parallel()
	s := newCertificationScheduler(t, auxreq.SchedulerConfig{Workers: 2, QueueCapacity: 2, MaxResults: 2}, func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
		return finishedStream(), nil
	})
	ids := make([]auxiliary.JobID, 0, 8)
	for i := range 8 {
		id, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "bounded-" + string(rune('a'+i))})
		if err != nil {
			t.Fatalf("submission %d: %v", i, err)
		}
		ids = append(ids, id)
		if _, err := s.Await(context.Background(), id); err != nil {
			t.Fatalf("Await %d: %v", i, err)
		}
	}
	for i := 0; i < len(ids)-2; i++ {
		if _, err := s.Await(context.Background(), ids[i]); !errors.Is(err, auxreq.ErrJobNotFound) {
			t.Fatalf("evicted result %d error=%v want ErrJobNotFound", i, err)
		}
	}
	for i := len(ids) - 2; i < len(ids); i++ {
		if _, err := s.Await(context.Background(), ids[i]); err != nil {
			t.Fatalf("retained result %d: %v", i, err)
		}
	}
}

func newCertificationScheduler(t *testing.T, cfg auxreq.SchedulerConfig, run backgroundRunner) *auxreq.BackgroundScheduler {
	t.Helper()
	s, err := auxreq.NewBackgroundScheduler(context.Background(), func() auxreq.ExecutorRunner { return run }, cfg)
	if err != nil {
		t.Fatalf("NewBackgroundScheduler: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

type certificationLateStream struct {
	started chan<- struct{}
	release <-chan struct{}
	once    atomic.Bool
}

func (s *certificationLateStream) Recv(context.Context) (aipapi.Event, error) {
	if s.once.CompareAndSwap(false, true) {
		close(s.started)
		return aipapi.Event{Kind: aipapi.EventResponseStarted}, nil
	}
	<-s.release
	return aipapi.Event{Kind: aipapi.EventResponseFinished}, nil
}

func (*certificationLateStream) Close() error { return nil }
