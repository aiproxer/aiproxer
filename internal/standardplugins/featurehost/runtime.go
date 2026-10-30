package featurehost

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/internal/infra/featuredb"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/state"
	"github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm"
	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/compaction"
	hostclassification "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionclassification"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionpolicy"
	adminkeepwarm "github.com/aiproxer/aiproxer/internal/stdhttp/admin/keepwarm"
	sdkfeaturehost "github.com/aiproxer/aiproxer/pkg/aipsdk/featurehost"
	aipstate "github.com/aiproxer/aiproxer/pkg/aipsdk/state"
	"github.com/uptrace/bun"
)

// Registration aliases the SDK host registration type for convenient consumption.
type Registration = sdkfeaturehost.Registration

// Runtime is the standard-distribution feature host process facade.
// It owns only migrated process feature resources and disposes them
// in reverse acquisition order upon Close. Borrowed resources (BackgroundAux,
// DB pools, etc.) are never closed by Runtime (Requirement 8.5, design §7).
type Runtime struct {
	logger                 *slog.Logger
	extState               aipstate.Store
	bgAux                  *auxreq.BackgroundScheduler // borrowed, never closed
	terminalPolicy         *sessionpolicy.Store
	compactionDetector     runtime.CompactionDetector
	branchCoordinator      *state.BranchCoordinator
	compactionParentPort   *compaction.ParentPort
	conversationStore      conversationview.Store
	conversationStoreStock bool
	bootstrapAuthority     bool
	keepwarmPolicy         *keepwarm.PolicyStore
	keepwarmRegistry       *keepwarm.ManagerRegistry
	keepwarmMetrics        *keepwarm.PrometheusCollector
	proxyMemoryMetrics     *proxymemory.Metrics
	proxyMemoryDecisions   *proxymemory.DecisionCache
	proxyMemoryDatabases   *proxyMemoryDatabasePool
	coreDB                 *bun.DB
	featureDatabases       featuredb.Opener
	sessionClassification  *hostclassification.StateHolder
	hostRegistrations      []sdkfeaturehost.Registration
	boundReasoning         ReasoningCompressionOptions
	boundSecretGuard       SecretGuardHostBinding
	closers                []func() error
	closeOnce              sync.Once
	closeErr               error
	closed                 atomic.Bool
}

// BoundReasoningOptions returns the bound reasoning compression options.
func (r *Runtime) BoundReasoningOptions() ReasoningCompressionOptions {
	if r == nil {
		return ReasoningCompressionOptions{}
	}
	return r.boundReasoning
}

// BoundSecretGuard returns the bound secret guard host options.
func (r *Runtime) BoundSecretGuard() SecretGuardHostBinding {
	if r == nil {
		return SecretGuardHostBinding{}
	}
	return r.boundSecretGuard
}

func (r *Runtime) registerCloser(closer func() error) {
	if closer != nil {
		r.closers = append(r.closers, closer)
	}
}

// CompactionDetector returns the featurehost-owned compaction detector.
// It is the single permitted detector observer: generic runtimebundle wires
// it into candidate executors (candidate_compile.go), and the transition
// guard observes it. Coordinator/parent-port have no such observer by design.
func (r *Runtime) CompactionDetector() runtime.CompactionDetector {
	if r == nil {
		return nil
	}
	return r.compactionDetector
}

// RetireALeg evicts the retired A-leg's proxy-memory replay state. It is a
// bounded process-cache eviction only: the retirement callback never performs
// database I/O. The worker's bounded maintenance removes the retired
// binding's durable row, and only bindings whose A-leg row no longer exists.
func (r *Runtime) RetireALeg(aLegID string) {
	if r == nil || aLegID == "" {
		return
	}
	if r.proxyMemoryDecisions != nil {
		r.proxyMemoryDecisions.RetireALeg(aLegID)
	}
}

// TerminalDecisionPolicy returns the featurehost-owned terminal decision policy store, if owned.
func (r *Runtime) TerminalDecisionPolicy() *sessionpolicy.Store {
	if r == nil {
		return nil
	}
	return r.terminalPolicy
}

// TerminalPolicyReader returns a consumer-owned adapter for the terminal decision policy store.
func (r *Runtime) TerminalPolicyReader() runtime.TerminalPolicyReader {
	if r == nil || r.terminalPolicy == nil {
		return nil
	}
	return NewTerminalPolicyReaderAdapter(r.terminalPolicy)
}

// KeepwarmPolicy returns the featurehost-owned keepwarm policy store, if owned.
func (r *Runtime) KeepwarmPolicy() *keepwarm.PolicyStore {
	if r == nil {
		return nil
	}
	return r.keepwarmPolicy
}

// KeepwarmRegistry returns the featurehost-owned keepwarm manager registry, if owned.
func (r *Runtime) KeepwarmRegistry() *keepwarm.ManagerRegistry {
	if r == nil {
		return nil
	}
	return r.keepwarmRegistry
}

// KeepwarmAdminService returns an admin-facing policy service adapter for the
// process-owned keepwarm policy store and manager registry.
func (r *Runtime) KeepwarmAdminService() adminkeepwarm.Service {
	if r == nil || r.keepwarmPolicy == nil || r.keepwarmRegistry == nil {
		return nil
	}
	return keepwarm.NewPolicyService(r.keepwarmPolicy, r.keepwarmRegistry, keepwarm.ClockFunc(func() time.Time { return time.Now().UTC() }))
}

// KeepwarmAdminProjection exposes the process-owned keep-warm admin projection
// opaquely for stdhttp composition via generation CorePorts. Generic
// runtimebundle copies the value without resolving the admin service itself.
func (r *Runtime) KeepwarmAdminProjection() (adminkeepwarm.Options, bool) {
	if r == nil {
		return adminkeepwarm.Options{}, false
	}
	svc := r.KeepwarmAdminService()
	if svc == nil {
		return adminkeepwarm.Options{}, false
	}
	return adminkeepwarm.Options{
		Enabled:       true,
		Service:       svc,
		ResolveALegID: adminkeepwarm.PathALegID,
	}, true
}

// ClosersCount returns the number of closers registered on this Runtime.
func (r *Runtime) ClosersCount() int {
	if r == nil {
		return 0
	}
	return len(r.closers)
}

// Close disposes all process feature resources owned by this Runtime in reverse
// acquisition order. It is idempotent and aggregates any disposal errors.
// It never closes borrowed generic process resources.
//
// The isolated memory databases are disposed by the process close itself,
// after the registered closers: the closer list of a process is fixed at
// construction, and no registered closer depends on a memory database.
func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		var out error
		// The terminal state of the isolated memory databases is published
		// first: a composition that runs while the process closes is
		// refused instead of receiving a handle that is about to close.
		r.proxyMemoryDatabases.beginClose()
		for _, closer := range slices.Backward(r.closers) {
			if err := closer(); err != nil {
				out = errors.Join(out, fmt.Errorf("featurehost: dispose closer: %w", err))
			}
		}
		if err := r.proxyMemoryDatabases.Close(); err != nil {
			out = errors.Join(out, fmt.Errorf("featurehost: dispose proxy-memory databases: %w", err))
		}
		r.closeErr = out
		r.closed.Store(true)
	})
	return r.closeErr
}

// Closed reports whether [Runtime.Close] has completed.
func (r *Runtime) Closed() bool {
	if r == nil {
		return true
	}
	return r.closed.Load()
}
