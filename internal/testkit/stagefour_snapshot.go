package testkit

import (
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/state"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// TestSnapshotOptions configures [NewTestRequestRuntimeSnapshot] for deterministic tests (task 4.3).
type TestSnapshotOptions struct {
	Generation      int64
	State           state.Store
	Aux             auxiliary.Client
	TrafficObserver traffic.Observer
	RawCapture      traffic.RawCaptureSink
	Workspace       workspace.Resolver
}

// NewTestRequestRuntimeSnapshot builds a snapshot for extension and stage tests using the same
// composition rules as production [extensions.NewRequestRuntimeSnapshot].
func NewTestRequestRuntimeSnapshot(bus *hooks.Bus, opts TestSnapshotOptions) *extensions.RequestRuntimeSnapshot {
	return extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Generation:      opts.Generation,
		State:           opts.State,
		Aux:             opts.Aux,
		TrafficObserver: opts.TrafficObserver,
		RawCapture:      opts.RawCapture,
		Workspace:       opts.Workspace,
	})
}
