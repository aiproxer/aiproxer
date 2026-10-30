package traffic_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

func TestNoopObserver(t *testing.T) {
	t.Parallel()
	var o traffic.Observer = traffic.NoopObserver{}
	if err := o.OnObservation(context.Background(), traffic.Observation{Leg: traffic.LegCTP}); err != nil {
		t.Fatal(err)
	}
}

func TestDisabledRawCapture(t *testing.T) {
	t.Parallel()
	var s traffic.RawCaptureSink = traffic.DisabledRawCapture{}
	err := s.WriteRaw(context.Background(), traffic.LegPTB, traffic.CaptureMeta{}, nil)
	if err != traffic.ErrNotConfigured {
		t.Fatalf("got %v", err)
	}
}
