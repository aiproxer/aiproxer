package auxreq_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/genpin"
)

type boundGenerationRunner struct {
	name  string
	start chan<- struct{}
	gate  <-chan struct{}
	calls atomic.Int32
}

func (r *boundGenerationRunner) Execute(ctx context.Context, _ *aipapi.Call) (aipapi.EventStream, error) {
	r.calls.Add(1)
	if r.start != nil {
		close(r.start)
	}
	if r.gate != nil {
		select {
		case <-r.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: r.name},
		{Kind: aipapi.EventResponseFinished},
	}), nil
}

func TestBackgroundScheduler_BindRunnerKeepsEachGenerationImmutable(t *testing.T) {
	t.Parallel()
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	first := &boundGenerationRunner{name: "first", start: firstStarted, gate: releaseFirst}
	second := &boundGenerationRunner{name: "second"}
	var active atomic.Value
	active.Store(auxreq.ExecutorRunner(first))
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		runner, ok := active.Load().(auxreq.ExecutorRunner)
		if !ok {
			panic("active runner has unexpected type")
		}
		return runner
	}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 2})
	firstRunner, ok := active.Load().(auxreq.ExecutorRunner)
	if !ok {
		t.Fatal("active runner has unexpected type")
	}
	firstClient := s.BindRunner(firstRunner)

	firstID, err := firstClient.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "bound-first"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first bound runner did not start")
	}
	// Simulate the active generation changing after the first view was bound.
	active.Store(auxreq.ExecutorRunner(second))
	secondRunner, ok := active.Load().(auxreq.ExecutorRunner)
	if !ok {
		t.Fatal("active runner has unexpected type")
	}
	secondClient := s.BindRunner(secondRunner)
	secondID, err := secondClient.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "bound-second"})
	if err != nil {
		t.Fatal(err)
	}
	close(releaseFirst)

	firstResult, err := firstClient.Await(context.Background(), firstID)
	if err != nil {
		t.Fatal(err)
	}
	secondResult, err := secondClient.Await(context.Background(), secondID)
	if err != nil {
		t.Fatal(err)
	}
	if got := firstResult.Text.String(); got != "first" {
		t.Fatalf("first result=%q want first", got)
	}
	if got := secondResult.Text.String(); got != "second" {
		t.Fatalf("second result=%q want second", got)
	}
	if first.calls.Load() != 1 || second.calls.Load() != 1 {
		t.Fatalf("runner calls first=%d second=%d want one each", first.calls.Load(), second.calls.Load())
	}
}

func TestBackgroundScheduler_BoundRunnerRetainsAndReleasesOneAsyncPin(t *testing.T) {
	t.Parallel()
	ret := &countingRetainer{pin: &countingPin{}}
	ret.allow.Store(true)
	ctx := genpin.WithRetainer(context.Background(), ret)
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return nil
	}, auxreq.SchedulerConfig{})
	client := s.BindRunner(backgroundRunner(func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
		return finishedStream(), nil
	}))

	id, err := client.SubmitCollect(ctx, backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "bound-pin"})
	if err != nil {
		t.Fatal(err)
	}
	if got := ret.retains.Load(); got != 1 {
		t.Fatalf("retains=%d want one synchronous retain", got)
	}
	if _, err := client.Await(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if got := ret.pin.releases.Load(); got != 1 {
		t.Fatalf("releases=%d want one release", got)
	}
}

func TestBackgroundScheduler_BoundCoalescedSubmissionSkipsRunnerAndPin(t *testing.T) {
	t.Parallel()
	ret := &countingRetainer{pin: &countingPin{}}
	ret.allow.Store(true)
	ctx := genpin.WithRetainer(context.Background(), ret)
	var firstCalls atomic.Int32
	var secondCalls atomic.Int32
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return nil
	}, auxreq.SchedulerConfig{})
	firstClient := s.BindRunner(backgroundRunner(func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
		firstCalls.Add(1)
		return finishedStream(), nil
	}))
	secondClient := s.BindRunner(backgroundRunner(func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
		secondCalls.Add(1)
		return finishedStream(), nil
	}))

	id, err := firstClient.SubmitCollect(ctx, backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "bound-coalesce"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := firstClient.Await(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	ret.allow.Store(false)
	duplicate, err := secondClient.SubmitCollect(ctx, backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "bound-coalesce"})
	if err != nil {
		t.Fatal(err)
	}
	if duplicate != id {
		t.Fatalf("coalesced id=%q want %q", duplicate, id)
	}
	if got := firstCalls.Load(); got != 1 {
		t.Fatalf("first runner calls=%d want one", got)
	}
	if got := secondCalls.Load(); got != 0 {
		t.Fatalf("second runner calls=%d want zero", got)
	}
	if got := ret.retains.Load(); got != 1 {
		t.Fatalf("retains=%d want one", got)
	}
}

func TestBackgroundScheduler_NilBoundRunnerFailsWithoutAdmission(t *testing.T) {
	t.Parallel()
	ret := &countingRetainer{pin: &countingPin{}}
	ret.allow.Store(true)
	ctx := genpin.WithRetainer(context.Background(), ret)
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return backgroundRunner(func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
			return finishedStream(), nil
		})
	}, auxreq.SchedulerConfig{})
	client := s.BindRunner(nil)
	if client == nil {
		t.Fatal("BindRunner(nil) returned nil client")
	}
	if _, err := client.SubmitCollect(ctx, backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "nil-bound"}); !errors.Is(err, auxiliary.ErrNotConfigured) {
		t.Fatalf("nil bound error=%v want ErrNotConfigured", err)
	}
	if got := ret.retains.Load(); got != 0 {
		t.Fatalf("retains=%d want no admission", got)
	}
	id, err := s.SubmitCollect(ctx, backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "nil-bound"})
	if err != nil {
		t.Fatalf("direct submission after nil binding: %v", err)
	}
	if _, err := s.Await(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}

var _ auxiliary.BackgroundClient = (*auxreq.BackgroundScheduler)(nil)
