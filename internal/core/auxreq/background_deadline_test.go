package auxreq_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/genpin"
	"github.com/stretchr/testify/require"
)

// A memory lease cannot cover a detached job whose execution timeout starts
// after an arbitrary queue wait (review F10). An absolute opt-in deadline must
// stop both queued admission to the runner and already-running work.
func TestBackgroundScheduler_DeadlineIncludesQueueWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var calls atomic.Int32
		s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
			return backgroundRunner(func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
				calls.Add(1)
				<-release
				return finishedStream(), nil
			})
		}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 1})
		first, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "deadline-blocker"})
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		queued, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{
			CoalesceKey: "expires-in-queue", Deadline: time.Now().Add(time.Second), Timeout: time.Hour,
		})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second) // virtual elapsed time, not synchronization
		close(release)
		if _, err := s.Await(context.Background(), first); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Await(context.Background(), queued); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("queued result = %v, want deadline exceeded", err)
		}
		if got := calls.Load(); got != 1 {
			t.Fatalf("runner calls = %d, want only the blocker", got)
		}
	})
}

func TestBackgroundScheduler_DeadlineCancelsExecution(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deadline := time.Now().Add(time.Second)
		observed := make(chan time.Time, 1)
		s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
			return backgroundRunner(func(ctx context.Context, _ *aipapi.Call) (aipapi.EventStream, error) {
				got, _ := ctx.Deadline()
				observed <- got
				return &cancelOnlyStream{}, nil
			})
		}, auxreq.SchedulerConfig{Workers: 1})
		id, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{
			CoalesceKey: "deadline-running", Deadline: deadline, Timeout: time.Hour,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := <-observed; !got.Equal(deadline) {
			t.Errorf("execution deadline = %v, want %v", got, deadline)
		}
		if _, err := s.Await(context.Background(), id); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("execution result = %v, want deadline exceeded", err)
		}
		if got := time.Now(); got.After(deadline) {
			t.Fatalf("execution stopped at %v, after absolute deadline %v", got, deadline)
		}
	})
}

// A queued job must expire on its own deadline, independently of worker
// availability: a worker busy for longer than the deadline must not keep the
// expired job alive, hold its queue slot or keep the queue saturated for
// healthy submissions.
func TestBackgroundScheduler_QueuedJobExpiresWithoutWorker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		started := make(chan struct{}, 2)
		s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
			return backgroundRunner(func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
				started <- struct{}{}
				<-release
				return finishedStream(), nil
			})
		}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 1})
		_, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "expiry-blocker"})
		require.NoError(t, err)
		synctest.Wait()
		queued, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "expires-queued", Deadline: time.Now().Add(time.Second), Timeout: time.Hour})
		require.NoError(t, err)
		time.Sleep(2 * time.Second) // virtual elapsed time, not synchronization

		// The worker is still blocked on the first job: the queued job must
		// have expired on its own deadline.
		_, err = s.Await(context.Background(), queued)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Len(t, started, 1, "the expired queued job must not invoke the runner")

		// The expired job must not keep the queue slot: a healthy submission
		// is admitted instead of being refused as saturated.
		_, err = s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "after-expiry"})
		require.NoError(t, err, "an expired queued job must not saturate the queue")

		close(release)
		synctest.Wait()
	})
}

// An expired queued job must release its generation pin exactly once, and a
// job the worker already owns must release its own pin exactly once when it
// completes: neither path may underflow or leak the generation.
func TestBackgroundScheduler_ExpiredQueuedJobReleasesGenerationPinOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ret := &countingRetainer{pin: &countingPin{}}
		ret.allow.Store(true)
		ctx := genpin.WithRetainer(context.Background(), ret)
		release := make(chan struct{})
		s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
			return backgroundRunner(func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
				<-release
				return finishedStream(), nil
			})
		}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 2})
		_, err := s.SubmitCollect(ctx, backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "pin-blocker"})
		require.NoError(t, err)
		synctest.Wait()
		queued, err := s.SubmitCollect(ctx, backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "pin-expires", Deadline: time.Now().Add(time.Second), Timeout: time.Hour})
		require.NoError(t, err)
		time.Sleep(2 * time.Second) // virtual elapsed time, not synchronization

		_, err = s.Await(context.Background(), queued)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Equal(t, int32(1), ret.pin.releases.Load(), "the expired queued job must release its generation pin exactly once")

		close(release)
		synctest.Wait()
		require.Equal(t, int32(2), ret.pin.releases.Load(), "the completed job must release its generation pin exactly once")
	})
}

// A submission that misses its deadline while it is prepared must be refused
// at the publication boundary: resolving the runner, retaining the generation
// pin and cloning the request all happen after the preflight check.
func TestBackgroundScheduler_ExpiredSubmissionRefusedAtPublication(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		factoryEntered := make(chan struct{})
		releaseFactory := make(chan struct{})
		s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
			close(factoryEntered)
			<-releaseFactory
			return backgroundRunner(func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
				return finishedStream(), nil
			})
		}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 1})
		type outcome struct {
			id  auxiliary.JobID
			err error
		}
		result := make(chan outcome, 1)
		go func() {
			id, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{
				CoalesceKey: "expires-during-preparation",
				Deadline:    time.Now().Add(time.Second),
				Timeout:     time.Hour,
			})
			result <- outcome{id: id, err: err}
		}()
		<-factoryEntered
		time.Sleep(2 * time.Second) // the deadline passes while the runner is resolved
		close(releaseFactory)
		got := <-result
		require.ErrorIs(t, got.err, context.DeadlineExceeded)
		require.Empty(t, got.id, "an expired submission must not be admitted")
	})
}

// The coalescing path is a publication boundary of its own: a submission whose
// deadline passed must be refused even when it would coalesce onto a live job.
func TestBackgroundScheduler_ExpiredSubmissionRefusedOnCoalescingPath(t *testing.T) {
	t.Parallel()
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return backgroundRunner(func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
			return finishedStream(), nil
		})
	}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 1})
	_, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "coalesced"})
	require.NoError(t, err)
	id, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{
		CoalesceKey: "coalesced",
		Deadline:    time.Now().Add(-time.Second),
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Empty(t, id, "an expired submission must not be admitted")
}

// An expired context must not reach the runner: the auxiliary client checks
// the bound immediately before it invokes the executor, because the request
// preparation in between can outlive the caller's deadline.
func TestAuxiliaryClient_RefusesExpiredContextBeforeRunner(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client := auxreq.NewClient(func() auxreq.ExecutorRunner {
		return backgroundRunner(func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
			calls.Add(1)
			return finishedStream(), nil
		})
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.Stream(ctx, auxiliary.Request{Call: &aipapi.Call{Route: aipapi.RouteIntent{Selector: "local:test"}}})
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, calls.Load(), "an expired context must not invoke the runner")
}
