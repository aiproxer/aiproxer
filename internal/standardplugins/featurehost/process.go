package featurehost

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	compactiondetect "github.com/aiproxer/aiproxer/internal/infra/compactiondetect"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/state"
	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	"github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm"
	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/compaction"
	hostclassification "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionclassification"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionpolicy"
	"github.com/uptrace/bun"
)

// constructionStep represents a staged feature construction action during NewProcess.
// It is unexported: only package-local _test.go seams may stage construction;
// external callers cannot inject constructors (Tasks 2.1/2.3, Requirements 2.5/8.4).
type constructionStep struct {
	Name      string
	Construct func(r *Runtime) error
}

// Package-local constructor seams for construction-counting tests.
// These variables alias the production constructors; package-local _test.go
// files may substitute counting delegates. They are unexported, so no external
// caller can substitute factories (Tasks 2.1/2.3, Requirements 2.5/8.4).
var (
	newCompactionDetector      = compactiondetect.New
	newBranchCoordinator       = state.NewBranchCoordinator
	newCompactionParentPort    = compaction.NewParentPort
	newInterleavedProcessor    = interleavedthinking.NewProcessor
	newKeepwarmPolicyStore     = keepwarm.NewPolicyStore
	newKeepwarmManagerRegistry = keepwarm.NewManagerRegistry
	newSessionPolicyStore      = sessionpolicy.NewStore
)

// NewProcess constructs the standard-distribution featurehost process facade.
// Every successfully constructed owned feature resource is recorded before later
// construction can fail; on failure, already-registered closers are unwound in reverse order.
func NewProcess(ctx context.Context, in ProcessInput) (*Runtime, error) {
	if in.Logger == nil {
		return nil, fmt.Errorf("featurehost: nil logger")
	}
	r := &Runtime{
		logger:               in.Logger,
		extState:             in.ExtensionState,
		bgAux:                in.BackgroundAux,
		proxyMemoryDecisions: proxymemory.NewDecisionCache(0),
		// The isolated memory databases are process-owned: the pool is
		// created with the process and disposed by the process close, so
		// the closer list of a process never grows while a generation
		// compiles.
		proxyMemoryDatabases: newProxyMemoryDatabasePool(),
	}

	// Fail-before-escape: if any step fails, unwind already-registered closers in reverse order.
	rollback := func(err error) (*Runtime, error) {
		var unwindErr error
		for _, closer := range slices.Backward(r.closers) {
			if cErr := closer(); cErr != nil {
				unwindErr = errors.Join(unwindErr, fmt.Errorf("featurehost: rollback closer: %w", cErr))
			}
		}
		if cErr := r.proxyMemoryDatabases.Close(); cErr != nil {
			unwindErr = errors.Join(unwindErr, fmt.Errorf("featurehost: rollback proxy-memory databases: %w", cErr))
		}
		if unwindErr != nil {
			return nil, errors.Join(err, unwindErr)
		}
		return nil, err
	}

	hostRegs := withDefaultEnvBinding(in.HostRegistrations, in.HostEnv)
	if len(hostRegs) > 0 {
		bound, err := bindHostRegistrations(hostRegs)
		if err != nil {
			return nil, err
		}
		r.boundReasoning = bound.reasoning
		r.boundSecretGuard = bound.secretGuard
		r.hostRegistrations = slices.Clone(hostRegs)
	}

	for _, step := range in.buildSteps {
		if step.Construct != nil {
			if err := step.Construct(r); err != nil {
				return rollback(fmt.Errorf("featurehost: construct %s: %w", step.Name, err))
			}
		}
	}

	// Compaction process ownership (Task 3.3): detector, coordinator, parent port.
	// None of these resources implement io.Closer; no closer registration required.
	r.compactionDetector = newCompactionDetector(compactiondetect.Config{})

	coord, err := newBranchCoordinator(ctx, state.Config{Store: in.ExtensionState})
	if err != nil {
		return rollback(fmt.Errorf("featurehost: branch coordinator: %w", err))
	}
	r.branchCoordinator = coord

	port, err := newCompactionParentPort(coord)
	if err != nil {
		return rollback(fmt.Errorf("featurehost: parent port: %w", err))
	}
	r.compactionParentPort = port

	// Conversation view process ownership (Task 4.3): store.
	bunDB := in.BunDB
	if bunDB == nil && in.ContinuityStore != nil {
		for s := any(in.ContinuityStore); s != nil; {
			if provider, ok := s.(interface{ DB() *bun.DB }); ok {
				bunDB = provider.DB()
				break
			}
			if u, ok := s.(interface{ Unwrap() any }); ok {
				s = u.Unwrap()
			} else {
				break
			}
		}
	}
	classificationMetrics := hostclassification.NewPrometheusCollector()
	r.sessionClassification = hostclassification.NewStateHolder(bunDB, classificationMetrics)
	r.registerCloser(r.sessionClassification.Close)
	r.coreDB = bunDB
	r.featureDatabases = in.FeatureDatabases
	if bunDB != nil {
		if err := conversationview.EnsureSchema(ctx, bunDB); err != nil {
			return rollback(fmt.Errorf("featurehost: conversationview ensure schema: %w", err))
		}
		r.conversationStore = wrapConversationStore(conversationview.NewBunStore(bunDB))
	} else {
		r.conversationStore = newConversationStore()
	}
	r.conversationStoreStock = true
	r.bindBootstrapAuthority(in.ContinuityStore)
	if in.ContinuityStore != nil {
		for s := any(in.ContinuityStore); s != nil; {
			if retired, ok := s.(b2bua.ALegRetirementObserver); ok {
				// The continuity store composes its retirement observers: the
				// process registers the cleanup it owns (the conversation view
				// and the proxy-memory replay cache) under its own key, and the
				// published generation registers the prompt-cache cleanup under
				// another one. Neither registration displaces the other, so
				// publishing a generation never kills the process cleanup.
				deleter, _ := r.conversationStore.(interface {
					DeleteALeg(context.Context, string) error
				})
				cleanup := func(aLegID string) {
					if deleter != nil {
						_ = deleter.DeleteALeg(context.Background(), aLegID)
					}
					r.RetireALeg(aLegID)
				}
				if keyed, ok := s.(b2bua.ALegRetirementObservers); ok {
					keyed.SetKeyedALegRetirementObserver(b2bua.RetirementObserverProcess, cleanup)
				} else {
					retired.SetALegRetirementObserver(cleanup)
				}
				break
			}
			if u, ok := s.(interface{ Unwrap() any }); ok {
				s = u.Unwrap()
			} else {
				break
			}
		}
	}

	// Keepwarm process ownership (Task 6.3): policy store and manager registry.
	// Neither implements io.Closer; no closer registration required.
	kwPolicy, err := newKeepwarmPolicyStore(keepwarm.DefaultMaxPolicyEntries)
	if err != nil {
		return rollback(fmt.Errorf("featurehost: keepwarm policy store: %w", err))
	}
	r.keepwarmPolicy = kwPolicy
	r.keepwarmRegistry = newKeepwarmManagerRegistry()

	// Feature-owned keep-warm collector, registered through the generic
	// metrics registry (lifetime stays with metrics infrastructure).
	if in.MetricsRegistry != nil {
		mc := keepwarm.NewPrometheusCollector()
		if err := in.MetricsRegistry.Register(mc); err != nil {
			return rollback(fmt.Errorf("featurehost: keepwarm metrics collector: %w", err))
		}
		r.keepwarmMetrics = mc
	}

	// Feature-owned proxy-memory collector (design §14.2): constructed
	// unconditionally so the memory service and HTTP handler always have a
	// usable counter surface, and registered with the generic metrics seam when
	// one exists. With no registry it stays unregistered but usable.
	r.proxyMemoryMetrics = proxymemory.NewPrometheusCollector()
	if in.MetricsRegistry != nil {
		if err := in.MetricsRegistry.Register(r.proxyMemoryMetrics); err != nil {
			return rollback(fmt.Errorf("featurehost: proxy-memory metrics collector: %w", err))
		}
	}

	// Terminal decision policy process ownership (Task 7.3): bounded session policy store.
	// Bounded in-memory store with client/operator overrides; closed on process shutdown.
	policyStore := newSessionPolicyStore(sessionpolicy.Config{})
	r.terminalPolicy = policyStore
	r.registerCloser(policyStore.Close)
	if in.MetricsRegistry != nil {
		if err := in.MetricsRegistry.Register(classificationMetrics); err != nil {
			return rollback(fmt.Errorf("featurehost: session classification metrics collector: %w", err))
		}
	}

	return r, nil
}
