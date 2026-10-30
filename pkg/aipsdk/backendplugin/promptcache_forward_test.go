package backendplugin_test

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

func TestForwardExecute_PromptCacheObservationIsHostOnlyAndSuccessGated(t *testing.T) {
	t.Parallel()
	buffer := &promptcache.ObservationBuffer{}
	observation := promptcache.Observation{ALegID: "a", BLegID: "b", BackendInstanceID: "i", TargetID: "t", GenerationID: "g", Lifecycle: promptcache.LifecycleBestEffort, Timing: promptcache.Timing{ObservedAt: time.Unix(1, 0)}, Renewable: false}
	if err := buffer.Add(observation); err != nil {
		t.Fatal(err)
	}
	buffer.Commit()
	stream := newFakeExecuteStream(context.Background(), validStartFrame(t))
	managed := &observationManaged{source: buffer}
	if err := backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
		return managed, nil
	}); err != nil {
		t.Fatal(err)
	}
	var sideband, canonical int
	for _, frame := range stream.sent {
		switch frame.Kind {
		case backendplugin.ServerFramePromptCacheObservation:
			sideband++
		case backendplugin.ServerFrameEvent:
			canonical++
		}
	}
	if sideband != 1 || canonical != 0 {
		t.Fatalf("sideband=%d canonical=%d frames=%#v", sideband, canonical, stream.sent)
	}
	if got := buffer.DrainPromptCacheObservations(); got != nil {
		t.Fatalf("observation was not drained by forwarding: %#v", got)
	}
}

type observationManaged struct {
	source *promptcache.ObservationBuffer
}

func (m *observationManaged) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, io.EOF
}

func (m *observationManaged) Close() error { return nil }

func (m *observationManaged) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func (m *observationManaged) DrainPromptCacheObservations() []promptcache.Observation {
	return m.source.DrainPromptCacheObservations()
}
