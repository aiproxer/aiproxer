package usage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

func TestNoopObserver_dropsEvents(t *testing.T) {
	t.Parallel()
	var o usage.NoopObserver
	if err := o.OnUsage(context.Background(), usage.Event{TraceID: "t"}); err != nil {
		t.Fatal(err)
	}
}

func TestChainObservers_skipsNilAndPreservesOrder(t *testing.T) {
	t.Parallel()
	var calls []int
	one := usage.Observer(usageFunc(func(ctx context.Context, ev usage.Event) error {
		calls = append(calls, 1)
		return nil
	}))
	two := usage.Observer(usageFunc(func(ctx context.Context, ev usage.Event) error {
		calls = append(calls, 2)
		return nil
	}))
	ch := usage.ChainObservers(one, nil, two)
	if err := ch.OnUsage(context.Background(), usage.Event{}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0] != 1 || calls[1] != 2 {
		t.Fatalf("calls=%v", calls)
	}
}

func TestChainObservers_stopsOnError(t *testing.T) {
	t.Parallel()
	boom := errors.New("x")
	var calls int
	ch := usage.ChainObservers(
		usageFunc(func(ctx context.Context, ev usage.Event) error {
			calls++
			return boom
		}),
		usageFunc(func(ctx context.Context, ev usage.Event) error {
			calls++
			return nil
		}),
	)
	err := ch.OnUsage(context.Background(), usage.Event{})
	if !errors.Is(err, boom) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

type usageFunc func(context.Context, usage.Event) error

func (f usageFunc) OnUsage(ctx context.Context, ev usage.Event) error { return f(ctx, ev) }
