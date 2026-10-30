package testkit

import (
	"maps"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// ExecutorOption mutates an [runtime.ExecutorConfig] before construction.
type ExecutorOption func(*runtime.ExecutorConfig)

// WithStore sets the B2BUA continuity store.
func WithStore(store b2bua.Store) ExecutorOption {
	return func(cfg *runtime.ExecutorConfig) {
		cfg.Core.Store = store
	}
}

// WithBus sets the hook bus.
func WithBus(bus *hooks.Bus) ExecutorOption {
	return func(cfg *runtime.ExecutorConfig) {
		cfg.Extension.Bus = bus
	}
}

// WithBackends sets backend adapters keyed by routing primary backend id.
func WithBackends(backends map[string]execbackend.Backend) ExecutorOption {
	return func(cfg *runtime.ExecutorConfig) {
		cfg.Core.Backends = backends
	}
}

// WithRand sets the routing RNG.
func WithRand(rng routing.Rng) ExecutorOption {
	return func(cfg *runtime.ExecutorConfig) {
		cfg.Core.Rand = rng
	}
}

// WithDefaultBackend sets model-only default backend resolution.
func WithDefaultBackend(backend string) ExecutorOption {
	return func(cfg *runtime.ExecutorConfig) {
		cfg.Routing.DefaultBackend = backend
	}
}

// WithSelectorAliases sets selector alias rewriting.
func WithSelectorAliases(ar *routing.AliasResolver) ExecutorOption {
	return func(cfg *runtime.ExecutorConfig) {
		cfg.Routing.SelectorAliases = ar
	}
}

// WithExecutionClasses sets explicit backend execution classes for test backends.
func WithExecutionClasses(classes map[string]aipsdk.BackendExecutionClass) ExecutorOption {
	return func(cfg *runtime.ExecutorConfig) {
		copied := maps.Clone(classes)
		cfg.Routing.BackendExecutionResolver = routing.BackendExecutionResolverFunc(func(id string) (aipsdk.BackendExecutionClass, bool) {
			c, ok := copied[id]
			return c, ok
		})
	}
}

// WithExecutionPolicy sets routing execution composition policy in tests.
func WithExecutionPolicy(policy config.ExecutionCompositionPolicy) ExecutorOption {
	return func(cfg *runtime.ExecutorConfig) {
		cfg.Routing.ExecutionCompositionPolicy = policy
	}
}

// NewTestExecutor constructs an executor from grouped options. Additional promoted
// fields can be set on the returned value when an option does not exist yet.
func NewTestExecutor(tb testing.TB, opts ...ExecutorOption) *runtime.Executor {
	tb.Helper()
	var cfg runtime.ExecutorConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if cfg.Routing.BackendExecutionResolver == nil && len(cfg.Core.Backends) > 0 {
		m := make(map[string]aipsdk.BackendExecutionClass, len(cfg.Core.Backends))
		for k := range cfg.Core.Backends {
			m[k] = aipsdk.BackendExecutionInference
		}
		cfg.Routing.BackendExecutionResolver = routing.BackendExecutionResolverFunc(func(id string) (aipsdk.BackendExecutionClass, bool) {
			c, ok := m[id]
			return c, ok
		})
	}
	return runtime.NewExecutor(cfg)
}

// PatchExecutor applies a mutation to an existing executor in tests (promoted fields).
func PatchExecutor(ex *runtime.Executor, patch func(*runtime.Executor)) *runtime.Executor {
	if ex == nil {
		ex = runtime.TestExecutor()
	}
	if patch != nil {
		patch(ex)
	}
	return ex
}
