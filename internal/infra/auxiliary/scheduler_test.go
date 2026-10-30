package auxiliary

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

func TestProductionSchedulerClosesAtProcessBoundary(t *testing.T) {
	t.Parallel()
	scheduler := NewProductionBackgroundScheduler(context.Background(), auxreq.SchedulerConfig{})
	if scheduler == nil {
		t.Fatal("nil scheduler")
	}
	if err := scheduler.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := scheduler.SubmitCollect(context.Background(), auxiliary.Request{Call: &aipapi.Call{}}, auxiliary.SubmitOptions{CoalesceKey: "closed"})
	if !errors.Is(err, auxreq.ErrSchedulerClosed) {
		t.Fatalf("err=%v", err)
	}
}

func TestSchedulerBindsExactGenerationRunner(t *testing.T) {
	t.Parallel()
	scheduler, err := auxreq.NewBackgroundScheduler(context.Background(), nil, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scheduler.Close() })
	first, second := &schedulerRunner{label: "one"}, &schedulerRunner{label: "two"}
	for i, runner := range []*schedulerRunner{first, second} {
		client := scheduler.BindRunner(runner)
		id, err := client.SubmitCollect(context.Background(), auxiliary.Request{Call: &aipapi.Call{}}, auxiliary.SubmitOptions{CoalesceKey: string(rune('a' + i))})
		if err != nil {
			t.Fatal(err)
		}
		result, err := client.Await(context.Background(), id)
		if err != nil || result.Text.String() != runner.label {
			t.Fatalf("result=%q err=%v", result.Text.String(), err)
		}
	}
	if first.calls.Load() != 1 || second.calls.Load() != 1 {
		t.Fatalf("calls=%d,%d", first.calls.Load(), second.calls.Load())
	}
}

type schedulerRunner struct {
	label string
	calls atomic.Int32
}

func (r *schedulerRunner) Execute(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
	r.calls.Add(1)
	return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}, {Kind: aipapi.EventTextDelta, Delta: r.label}, {Kind: aipapi.EventResponseFinished}}), nil
}
