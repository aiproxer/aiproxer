package streampeek

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

type promptCacheManagedStream struct {
	*aipapi.FixedEventStream
	source *promptcache.ObservationBuffer
}

func (s *promptCacheManagedStream) DrainPromptCacheObservations() []promptcache.Observation {
	return s.source.DrainPromptCacheObservations()
}

func (s *promptCacheManagedStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

type economicStateManagedStream struct {
	*aipapi.FixedEventStream
	enabled bool
}

type usageSidebandManagedStream struct {
	*aipapi.FixedEventStream
	evidence []aipapi.Event
}

func (s *usageSidebandManagedStream) DrainUsageEvidence() []aipapi.Event {
	out := append([]aipapi.Event(nil), s.evidence...)
	s.evidence = nil
	return out
}

func (s *usageSidebandManagedStream) AccountingEvidenceEnabled() bool { return true }

func (s *usageSidebandManagedStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func (s *economicStateManagedStream) DrainEconomicObservations() []metering.Observation {
	return nil
}

func (s *economicStateManagedStream) AccountingEvidenceEnabled() bool {
	return s.enabled
}

func (s *economicStateManagedStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func TestManagedPrependForwardsPromptCacheObservations(t *testing.T) {
	t.Parallel()
	now := time.Unix(100, 0).UTC()
	observation := promptcache.Observation{
		ALegID: "a", BLegID: "b", BackendInstanceID: "backend",
		TargetID: "target", GenerationID: "generation",
		Lifecycle: promptcache.LifecycleBestEffort,
		Timing:    promptcache.Timing{ObservedAt: now},
		Renewable: true, Handle: promptcache.Handle("handle"),
	}
	var buffer promptcache.ObservationBuffer
	if err := buffer.Add(observation); err != nil {
		t.Fatal(err)
	}
	buffer.Commit()
	inner := &promptCacheManagedStream{FixedEventStream: aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), source: &buffer}
	wrapped := NewManagedPrependFirst(aipapi.Event{Kind: aipapi.EventResponseStarted}, inner)
	if _, err := wrapped.Recv(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, ok := wrapped.(promptcache.ObservationSource)
	if !ok {
		t.Fatal("managed prepend does not preserve observation source")
	}
	observations := got.DrainPromptCacheObservations()
	if len(observations) != 1 || observations[0].TargetID != observation.TargetID {
		t.Fatalf("observations=%+v", observations)
	}
	if second := got.DrainPromptCacheObservations(); second != nil {
		t.Fatalf("observation sideband drained more than once: %+v", second)
	}
}

func TestManagedPrependForwardsEconomicNegotiationState(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			t.Parallel()
			inner := &economicStateManagedStream{FixedEventStream: aipapi.NewFixedEventStream(nil), enabled: enabled}
			wrapped := NewManagedPrependFirst(aipapi.Event{Kind: aipapi.EventResponseStarted}, inner)
			state, ok := wrapped.(interface{ AccountingEvidenceEnabled() bool })
			if !ok {
				t.Fatal("managed prepend does not preserve economic negotiation state")
			}
			if got := state.AccountingEvidenceEnabled(); got != enabled {
				t.Fatalf("economic sideband state=%v, want %v", got, enabled)
			}
		})
	}
}

func TestManagedPrependForwardsUsageEvidence(t *testing.T) {
	t.Parallel()
	inner := &usageSidebandManagedStream{
		FixedEventStream: aipapi.NewFixedEventStream(nil),
		evidence:         []aipapi.Event{{Kind: aipapi.EventUsageDelta, InputTokens: 3}},
	}
	wrapped := NewManagedPrependFirst(aipapi.Event{Kind: aipapi.EventResponseStarted}, inner)
	source, ok := wrapped.(aipapi.UsageEvidenceSource)
	if !ok {
		t.Fatal("managed prepend does not preserve V1 usage evidence source")
	}
	got := source.DrainUsageEvidence()
	if len(got) != 1 || got[0].InputTokens != 3 {
		t.Fatalf("usage evidence=%+v", got)
	}
	if second := source.DrainUsageEvidence(); second != nil {
		t.Fatalf("usage evidence drained more than once: %+v", second)
	}
}
