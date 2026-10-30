package submitnoop

import (
	"context"
	"sync"
	"sync/atomic"

	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
)

var _ aipplugin.Lifecycle = (*LifecycleProbe)(nil)

var (
	probeFactoryMu sync.Mutex
	probeFactory   func() aipplugin.Lifecycle
)

// SetLifecycleProbeFactoryForTest overrides probe construction used when
// lifecycle_probe is true. Tests may return a shared instance so bootstrap
// merge + CompileGeneration overlay double-registration is observable.
// Pass nil to restore the default per-merge instance.
func SetLifecycleProbeFactoryForTest(fn func() aipplugin.Lifecycle) {
	probeFactoryMu.Lock()
	probeFactory = fn
	probeFactoryMu.Unlock()
}

// NewLifecycleProbeForConfig returns the overlap-safe lifecycle probe for
// submit-noop when lifecycle_probe is enabled.
func NewLifecycleProbeForConfig() aipplugin.Lifecycle {
	probeFactoryMu.Lock()
	fn := probeFactory
	probeFactoryMu.Unlock()
	if fn != nil {
		return fn()
	}
	return &LifecycleProbe{}
}

// LifecycleProbe is an optional no-op lifecycle returned when HookConfig.LifecycleProbe is true.
// It is explicitly safe under candidate overlap (generation-local, no process globals).
type LifecycleProbe struct {
	starts  atomic.Int32
	stops   atomic.Int32
	started atomic.Bool
	stopped atomic.Bool
}

// WasStarted reports whether Start has completed successfully.
func (l *LifecycleProbe) WasStarted() bool { return l.started.Load() }

// WasStopped reports whether Stop has completed successfully.
func (l *LifecycleProbe) WasStopped() bool { return l.stopped.Load() }

// StartCount returns how many times Start has been invoked on this instance.
func (l *LifecycleProbe) StartCount() int {
	if l == nil {
		return 0
	}
	return int(l.starts.Load())
}

// StopCount returns how many times Stop has been invoked on this instance.
func (l *LifecycleProbe) StopCount() int {
	if l == nil {
		return 0
	}
	return int(l.stops.Load())
}

// SafeUnderCandidateOverlap reports that Start/Stop may overlap across generations.
func (l *LifecycleProbe) SafeUnderCandidateOverlap() bool { return true }

func (l *LifecycleProbe) Start(context.Context) error {
	if l == nil {
		return nil
	}
	l.starts.Add(1)
	l.started.Store(true)
	return nil
}

func (l *LifecycleProbe) Stop(context.Context) error {
	if l == nil {
		return nil
	}
	l.stops.Add(1)
	l.stopped.Store(true)
	return nil
}
