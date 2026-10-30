package traffic_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

func TestChainObservers_emptyIsNoopObserver(t *testing.T) {
	t.Parallel()
	o := traffic.ChainObservers()
	if _, ok := o.(traffic.NoopObserver); !ok {
		t.Fatalf("want NoopObserver, got %T", o)
	}
	if err := o.OnObservation(context.Background(), traffic.Observation{Body: []byte("x")}); err != nil {
		t.Fatal(err)
	}
}

func TestChainObservers_singleReturnsUnderlying(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	inner := appendObserver{&buf}
	chained := traffic.ChainObservers(inner)
	if chained != inner {
		t.Fatal("expected singleton passthrough")
	}
}

func TestChainObservers_nilArgumentsSkipped(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	o := traffic.ChainObservers(nil, appendObserver{&buf}, nil)
	if err := o.OnObservation(context.Background(), traffic.Observation{Body: []byte("y")}); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "y" {
		t.Fatalf("got %q", buf.String())
	}
}
