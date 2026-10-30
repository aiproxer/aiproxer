package adapter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

func TestManagedStream_PromptCacheSidebandCommitsOnlyOnSuccessfulTerminal(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		terminal   backendplugin.TerminalStatus
		wantTarget bool
	}{
		{name: "success", terminal: backendplugin.TerminalSuccess, wantTarget: true},
		{name: "failure", terminal: backendplugin.TerminalFailure, wantTarget: false},
		{name: "cancelled", terminal: backendplugin.TerminalCancelled, wantTarget: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			neg := backendplugin.Negotiation{
				Compatible:      true,
				NegotiatedMinor: backendplugin.ProtocolMinorPromptCacheResidency,
				EnabledFeatures: []string{backendplugin.FeaturePromptCacheResidency},
			}
			s := &managedStream{
				ctx: ctx, cancel: cancel, opt: Options{Negotiation: neg, InstanceID: "instance"},
				events: make(chan aipapi.Event, 1), done: make(chan struct{}),
				maxFrame:      int(backendplugin.DefaultMaxStreamFrameBytes),
				lineageALegID: "a", lineageBLegID: "b",
			}
			if err := s.onPluginFrame(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted}); err != nil {
				t.Fatal(err)
			}
			now := time.Unix(1, 0).UTC()
			observation := &promptcache.Observation{
				ALegID: "a", BLegID: "b", BackendInstanceID: "instance",
				TargetID: "target", GenerationID: "generation",
				Lifecycle: promptcache.LifecycleBestEffort,
				Timing:    promptcache.Timing{ObservedAt: now},
			}
			if err := s.onPluginFrame(backendplugin.ServerFrame{Kind: backendplugin.ServerFramePromptCacheObservation, Sequence: 1, PromptCacheObservation: observation}); err != nil {
				t.Fatal(err)
			}
			if err := s.onPluginFrame(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameTerminal, Sequence: 2, Terminal: &backendplugin.Terminal{Status: tc.terminal}}); err != nil {
				t.Fatal(err)
			}
			got := s.DrainPromptCacheObservations()
			if (len(got) == 1) != tc.wantTarget {
				t.Fatalf("observations=%#v, wantTarget=%v", got, tc.wantTarget)
			}
		})
	}
}

// TestManagedStreamPromptCacheObservationRejectsForeignOrigin pins the
// transport boundary: a plugin-side observation may only echo the attempt
// A-leg, the transport leg pseudonym, and the instance identity the host
// supplied in the start invocation. A non-empty foreign claim is an explicit
// protocol violation and never reaches the observation buffer.
func TestManagedStreamPromptCacheObservationRejectsForeignOrigin(t *testing.T) {
	t.Parallel()
	newStream := func() *managedStream {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		return &managedStream{
			ctx: ctx, cancel: cancel,
			opt:    Options{Negotiation: backendplugin.Negotiation{Compatible: true, NegotiatedMinor: backendplugin.ProtocolMinorPromptCacheResidency, EnabledFeatures: []string{backendplugin.FeaturePromptCacheResidency}}, InstanceID: "instance"},
			events: make(chan aipapi.Event, 1), done: make(chan struct{}),
			maxFrame:      int(backendplugin.DefaultMaxStreamFrameBytes),
			lineageALegID: "a-leg", lineageBLegID: "connector:model",
		}
	}
	base := promptcache.Observation{
		ALegID: "a-leg", BLegID: "connector:model", BackendInstanceID: "instance",
		TargetID: "target", GenerationID: "generation",
		Lifecycle: promptcache.LifecycleBestEffort,
		Timing:    promptcache.Timing{ObservedAt: time.Unix(1, 0).UTC()},
	}
	for _, tc := range []struct {
		name   string
		mutate func(*promptcache.Observation)
	}{
		{name: "foreign_backend", mutate: func(o *promptcache.Observation) { o.BackendInstanceID = "other-instance" }},
		{name: "foreign_a_leg", mutate: func(o *promptcache.Observation) { o.ALegID = "other-session" }},
		{name: "foreign_b_leg", mutate: func(o *promptcache.Observation) { o.BLegID = "route:other-model" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newStream()
			if err := s.onPluginFrame(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted}); err != nil {
				t.Fatal(err)
			}
			observation := base
			tc.mutate(&observation)
			err := s.onPluginFrame(backendplugin.ServerFrame{Kind: backendplugin.ServerFramePromptCacheObservation, Sequence: 1, PromptCacheObservation: &observation})
			if !errors.Is(err, execbackend.ErrPromptCacheLineageConflict) {
				t.Fatalf("err=%v, want a lineage conflict protocol violation", err)
			}
			if got := s.DrainPromptCacheObservations(); len(got) != 0 {
				t.Fatalf("foreign observation reached the buffer: %#v", got)
			}
		})
	}
}
