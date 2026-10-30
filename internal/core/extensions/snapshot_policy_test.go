package extensions_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

type snapCaptureObserver struct {
	called bool
}

func (o *snapCaptureObserver) OnPolicyDecision(context.Context, policydecision.Record) error {
	o.called = true
	return nil
}

func TestRequestRuntimeSnapshot_PolicyObserverDefaultsToNoop(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{})

	obs := snap.PolicyObserver()
	if obs == nil {
		t.Fatal("default policy observer must be non-nil (disabled no-op)")
	}
	// Default observer is callable and isolated from request execution.
	if err := obs.OnPolicyDecision(context.Background(), policydecision.Record{
		Stage: feature.StageIDPreRequest, Outcome: policydecision.OutcomeDeny, Effect: policydecision.EffectNone,
	}); err != nil {
		t.Fatalf("default noop observer must not error: %v", err)
	}
}

func TestRequestRuntimeSnapshot_PolicyObserverPreservesConfigured(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	obs := &snapCaptureObserver{}
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{PolicyObserver: obs})

	got := snap.PolicyObserver()
	if err := got.OnPolicyDecision(context.Background(), policydecision.Record{
		Stage: feature.StageIDPreRequest, Outcome: policydecision.OutcomeDeny, Effect: policydecision.EffectNone,
	}); err != nil {
		t.Fatalf("OnPolicyDecision returned error: %v", err)
	}
	if !obs.called {
		t.Fatal("configured policy observer must be invoked through the snapshot accessor")
	}
}

func TestRequestRuntimeSnapshot_TimeoutBudgetSourceDefaultsToZero(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{})

	src := snap.TimeoutBudgetSource()
	if src == nil {
		t.Fatal("default timeout budget source must be non-nil")
	}
	if got := src.TimeoutFor(feature.StageIDPreRequest, "p1"); got != 0 {
		t.Fatalf("default timeout budget must be zero (legacy), got %v", got)
	}
}

func TestRequestRuntimeSnapshot_TimeoutBudgetSourcePreservesConfigured(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	src := extensions.StaticTimeoutBudgetSource{Budget: 123 * time.Millisecond}
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{TimeoutBudgetSource: src})

	got := snap.TimeoutBudgetSource()
	if got == nil {
		t.Fatal("timeout budget source must be non-nil")
	}
	if g := got.TimeoutFor(feature.StageIDPreRequest, "p1"); g != 123*time.Millisecond {
		t.Fatalf("configured timeout budget not preserved: got %v", g)
	}
}

func TestRequestRuntimeSnapshot_NilAccessorsReturnSafeDefaults(t *testing.T) {
	t.Parallel()
	var snap *extensions.RequestRuntimeSnapshot

	if obs := snap.PolicyObserver(); obs == nil {
		t.Fatal("nil snapshot PolicyObserver must return non-nil default")
	} else if err := obs.OnPolicyDecision(context.Background(), policydecision.Record{}); err != nil {
		t.Fatalf("nil snapshot default observer must be safe to call: %v", err)
	}
	if src := snap.TimeoutBudgetSource(); src == nil {
		t.Fatal("nil snapshot TimeoutBudgetSource must return non-nil default")
	} else if got := src.TimeoutFor(feature.StageIDPreRequest, "p1"); got != 0 {
		t.Fatalf("nil snapshot default timeout must be zero, got %v", got)
	}
}

func TestRequestRuntimeSnapshot_DefaultsDoNotChangeRequestOutcomes(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{})
	// With no concrete policy observer or timeout budget configured, the snapshot must keep
	// legacy behavior: default observer is no-op and default timeout is zero (requirement 10.5).
	if snap.PolicyObserver() == nil || snap.TimeoutBudgetSource() == nil {
		t.Fatal("defaults must be present so deployments without policy evidence keep current outcomes")
	}
	if snap.TimeoutBudgetSource().TimeoutFor(feature.StageIDRequestWide, "rtx") != 0 {
		t.Fatal("default timeout must be zero to preserve legacy request-transform behavior")
	}
}
