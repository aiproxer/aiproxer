package runtimebundle

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/aiproxer/aiproxer/internal/core/auth"
	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	concurrencyapp "github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/continuity"
	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	ssessionapp "github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/snapshotgen"
	terminalworkapp "github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

// WrapProcessContinuityForTest replaces the process-owned continuity store that
// generations bind their executors to.
func WrapProcessContinuityForTest(ps *ProcessServices, wrap func(b2bua.Store) b2bua.Store) {
	ps.persistence.Store = wrap(ps.persistence.Store)
	ps.Continuity = ps.persistence.Store
}

func GenerationExecutorOf(g GenerationRuntime) *runtime.Executor {
	b, ok := g.(*GenerationBundle)
	if !ok || b == nil {
		return nil
	}
	return b.execution.executor
}

// ProxyMemoryGenerationForTest runs the production candidate compile and
// returns the proxy-memory feature's worker of that generation together with
// the generation's executor, so the external test package observes the
// feature's own consumption of the bound port instead of the compiler the
// composition root bound.
//
// The third result is the candidate's own cleanup: the candidate started the
// feature's worker and capture lifecycles, and only the candidate's resource
// ledger owns their stop actions. The caller runs the cleanup before the
// process and the storage teardown; the cleanup joins every loop the
// candidate started.
func ProxyMemoryGenerationForTest(ctx context.Context, in GenerationCompileInput) (*proxymemory.Worker, *runtime.Executor, func() error, error) {
	gc, err := compileGenerationCandidate(ctx, in, true)
	if err != nil {
		return nil, nil, nil, err
	}
	dispose := func() error { return gc.cand.RollbackUnpublished() }
	var worker *proxymemory.Worker
	for _, lifecycle := range gc.featOut.Lifecycles {
		if candidate, ok := lifecycle.(*proxymemory.Worker); ok {
			worker = candidate
			break
		}
	}
	if worker == nil {
		// The candidate composed no worker, but its ledger still owns the
		// lifecycles it started: the error path disposes them too.
		_ = dispose()
		return nil, nil, nil, fmt.Errorf("runtimebundle: the candidate composed no proxy-memory worker")
	}
	return worker, gc.cand.execution.executor, dispose, nil
}

func GenerationSelectorValidatorForTest(aliases *routing.AliasResolver, defaultBackend string, knownBackends map[string]struct{}) routeoverride.SelectorValidator {
	return routing.NewGenerationSelectorValidator(
		aliases,
		defaultBackend,
		knownBackends,
		nil,
		config.ExecutionCompositionSafe,
	)
}

func GenerationSelectorValidatorWithExecutionForTest(
	aliases *routing.AliasResolver,
	defaultBackend string,
	knownBackends map[string]struct{},
	execResolver routing.BackendExecutionResolver,
	policy config.ExecutionCompositionPolicy,
) routeoverride.SelectorValidator {
	return routing.NewGenerationSelectorValidator(
		aliases,
		defaultBackend,
		knownBackends,
		execResolver,
		policy,
	)
}

func NewGenerationBundleForTest(models *modelregistry.Runtime, catalog *modelcatalog.CatalogRuntime) *GenerationBundle {
	return &GenerationBundle{
		models: generationModelViews{
			models:  models,
			catalog: catalog,
		},
	}
}

func NewGenerationBundleWithLedgerForTest(ledger *ResourceLedger) *GenerationBundle {
	return &GenerationBundle{ledger: ledger}
}

func NewGenerationBundleWithPublicationForTest(auth []httpauth.Provider, regs []aipsdk.Registration) *GenerationBundle {
	return newGenerationBundle(generationBundleInput{
		httpAuth:      auth,
		registrations: regs,
	})
}

func TransferLedgerOwnershipForTest(c *CandidateHTTPCompile) *ResourceLedger {
	if c == nil || c.assem == nil {
		return nil
	}
	return c.assem.transferLedgerOwnership()
}

func NewCandidateAssemblyForTest(ledger *ResourceLedger) *candidateAssembly {
	return &candidateAssembly{ledger: ledger}
}

// NewCandidateRuntimeForTest constructs an opaque candidate handle around a ledger.
func NewCandidateRuntimeForTest(ledger *ResourceLedger) *CandidateHTTPCompile {
	return &CandidateHTTPCompile{assem: &candidateAssembly{ledger: ledger}}
}

// Ledger exposes the candidate ledger only in test builds.
func (c *CandidateHTTPCompile) Ledger() *ResourceLedger {
	if a := c.a(); a != nil {
		return a.ledger
	}
	return nil
}

func CandidateAssemblyOf(c *CandidateHTTPCompile) *candidateAssembly {
	if c == nil {
		return nil
	}
	return c.assem
}

// CompileCandidateAssembly exposes compileCandidate to runtimebundle_test.
func CompileCandidateAssembly(ctx context.Context, in GenerationCompileInput) (*candidateAssembly, error) {
	return compileCandidate(ctx, in)
}

func HostManager(h *Host) *runtimehost.Manager {
	if h == nil {
		return nil
	}
	return h.manager
}

func HostProcess(h *Host) *ProcessServices {
	if h == nil {
		return nil
	}
	return h.process
}

func HostCoordinator(h *Host) *runtimehost.Coordinator {
	if h == nil {
		return nil
	}
	return h.coordinator
}

func HostGenerationExecutor(h *Host) *runtimehost.GenerationExecutor {
	if h == nil {
		return nil
	}
	return h.executor
}

func HostShutdownTracing(h *Host) func(context.Context) error {
	if h == nil {
		return nil
	}
	return h.shutdownTracing
}

func SetHostShutdownTracing(h *Host, fn func(context.Context) error) {
	if h == nil {
		return
	}
	h.shutdownTracing = fn
}

func HostFixedStreamRecovery(h *Host) config.StreamRecoveryOverrides {
	if h == nil {
		return config.StreamRecoveryOverrides{}
	}
	return h.fixedStreamRecovery
}

// HostCloseTestInput constructs a Host for close-ordering unit tests.
type HostCloseTestInput struct {
	Coordinator     *runtimehost.Coordinator
	Manager         *runtimehost.Manager
	Process         *ProcessServices
	Executor        *runtimehost.GenerationExecutor
	ShutdownTracing func(context.Context) error
	Dispatcher      *runtimehost.GenerationDispatcher
	Logger          *slog.Logger
	Config          *config.Config
	Effective       *config.EffectiveConfig
}

func NewHostForCloseTest(in HostCloseTestInput) *Host {
	return &Host{
		coordinator:     in.Coordinator,
		manager:         in.Manager,
		process:         in.Process,
		executor:        in.Executor,
		shutdownTracing: in.ShutdownTracing,
		dispatcher:      in.Dispatcher,
		logger:          in.Logger,
		config:          in.Config,
		effective:       in.Effective,
	}
}

// Test-only candidate field accessors (PR B2: no production getter wall).
func CandidateHTTPAuthProviders(c *CandidateHTTPCompile) []httpauth.Provider {
	if a := CandidateAssemblyOf(c); a != nil {
		return append([]httpauth.Provider(nil), a.security.httpAuth...)
	}
	return nil
}

func CandidateAuthEventDispatcher(c *CandidateHTTPCompile) *auth.EventDispatcher {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.security.authEvents
	}
	return nil
}

func CandidateReadinessReport(c *CandidateHTTPCompile) *controlplane.ReadinessReportService {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.operations.readinessReport
	}
	return nil
}

func CandidateCatalogRuntime(c *CandidateHTTPCompile) *modelcatalog.CatalogRuntime {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.models.catalog
	}
	return nil
}

func CandidateModelRegistryRuntime(c *CandidateHTTPCompile) *modelregistry.Runtime {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.models.registryRuntime
	}
	return nil
}

func CandidateSecureSessionStore(c *CandidateHTTPCompile) ssessionapp.Store {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.security.secureSessionStore
	}
	return nil
}

func CandidateConcurrencyAuthority(c *CandidateHTTPCompile) *concurrencyapp.Service {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.process.concurrencyAuthority
	}
	return nil
}

func CandidateControlPlaneQueries(c *CandidateHTTPCompile) *controlplane.QueryService {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.process.controlPlaneQueries
	}
	return nil
}

func CandidateControlPlaneStatus(c *CandidateHTTPCompile) *controlplane.Status {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.process.ps.controlPlane.statusHandle()
	}
	return nil
}

func CandidateControlPlaneRetention(c *CandidateHTTPCompile) *controlplane.RetentionController {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.process.ps.controlPlane.retentionHandle()
	}
	return nil
}

func CandidateSnapshotGeneration(c *CandidateHTTPCompile) *snapshotgen.Publisher {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.process.ps.SnapshotGeneration
	}
	return nil
}

func CandidateSnapshotController(c *CandidateHTTPCompile) *SnapshotController {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.process.ps.SnapshotController
	}
	return nil
}

func CandidateDatabasePools(c *CandidateHTTPCompile) *db.PoolRegistry {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.process.ps.DatabasePools
	}
	return nil
}

func CandidateMeteringQuerier(c *CandidateHTTPCompile) metering.Querier {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.process.meteringQuerier
	}
	return nil
}

func CandidateTokenAccountingAdmin(c *CandidateHTTPCompile) *accountingapp.Service {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.operations.tokenAccountingAdmin
	}
	return nil
}

func CandidateSecretGuardInventory(c *CandidateHTTPCompile) *diag.InventoryExtras {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.operations.secretGuardInventory
	}
	return nil
}

func CandidateTerminalWorkProcessor(c *CandidateHTTPCompile) *terminalworkapp.Processor {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.process.ps.TerminalWorkProcessor
	}
	return nil
}

func CandidateTerminalWorkRegistry(c *CandidateHTTPCompile) *terminalworkapp.Registry {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.operations.terminalRegistry
	}
	return nil
}

func CandidateTerminalWorkQueries(c *CandidateHTTPCompile) *terminalworkapp.QueryService {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.process.ps.TerminalWorkQueries
	}
	return nil
}

func CandidateTerminalWorkMetrics(c *CandidateHTTPCompile) *terminalworkapp.MetricsObserver {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.process.ps.TerminalWorkMetrics
	}
	return nil
}

func CandidateUpstreamHTTP(c *CandidateHTTPCompile) *http.Client {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.execution.upstreamHTTP
	}
	return nil
}

// White-box method aliases for package runtimebundle tests (not exported to other packages).
func (c *CandidateHTTPCompile) HTTPAuthProviders() []httpauth.Provider {
	return CandidateHTTPAuthProviders(c)
}

func (c *CandidateHTTPCompile) AuthEventDispatcher() *auth.EventDispatcher {
	return CandidateAuthEventDispatcher(c)
}

func (c *CandidateHTTPCompile) ReadinessReport() *controlplane.ReadinessReportService {
	return CandidateReadinessReport(c)
}

func (c *CandidateHTTPCompile) CatalogRuntime() *modelcatalog.CatalogRuntime {
	return CandidateCatalogRuntime(c)
}

func (c *CandidateHTTPCompile) ModelRegistryRuntime() *modelregistry.Runtime {
	return CandidateModelRegistryRuntime(c)
}

func (c *CandidateHTTPCompile) SecureSessionStore() ssessionapp.Store {
	return CandidateSecureSessionStore(c)
}

func (c *CandidateHTTPCompile) ConcurrencyAuthority() *concurrencyapp.Service {
	return CandidateConcurrencyAuthority(c)
}

func (c *CandidateHTTPCompile) ControlPlaneQueries() *controlplane.QueryService {
	return CandidateControlPlaneQueries(c)
}

func (c *CandidateHTTPCompile) ControlPlaneStatus() *controlplane.Status {
	return CandidateControlPlaneStatus(c)
}

func (c *CandidateHTTPCompile) ControlPlaneRetention() *controlplane.RetentionController {
	return CandidateControlPlaneRetention(c)
}

func (c *CandidateHTTPCompile) SnapshotGeneration() *snapshotgen.Publisher {
	return CandidateSnapshotGeneration(c)
}

func (c *CandidateHTTPCompile) SnapshotController() *SnapshotController {
	return CandidateSnapshotController(c)
}

func (c *CandidateHTTPCompile) DatabasePools() *db.PoolRegistry { return CandidateDatabasePools(c) }

func (c *CandidateHTTPCompile) MeteringQuerier() metering.Querier { return CandidateMeteringQuerier(c) }

func (c *CandidateHTTPCompile) TokenAccountingAdmin() *accountingapp.Service {
	return CandidateTokenAccountingAdmin(c)
}

func (c *CandidateHTTPCompile) SecretGuardInventory() *diag.InventoryExtras {
	return CandidateSecretGuardInventory(c)
}

func (c *CandidateHTTPCompile) TerminalWorkProcessor() *terminalworkapp.Processor {
	return CandidateTerminalWorkProcessor(c)
}

func (c *CandidateHTTPCompile) TerminalWorkRegistry() *terminalworkapp.Registry {
	return CandidateTerminalWorkRegistry(c)
}

func (c *CandidateHTTPCompile) TerminalWorkQueries() *terminalworkapp.QueryService {
	return CandidateTerminalWorkQueries(c)
}

func (c *CandidateHTTPCompile) TerminalWorkMetrics() *terminalworkapp.MetricsObserver {
	return CandidateTerminalWorkMetrics(c)
}
func (c *CandidateHTTPCompile) UpstreamHTTP() *http.Client { return CandidateUpstreamHTTP(c) }

func NewCompositionGenIDForTest() string {
	return newCompositionGenID()
}

// compositionTestBackendRow returns the minimal eligible backend row the
// composition assessor sentinels build through the real production path.
func compositionTestBackendRow() (map[string]execbackend.Backend, routing.BackendExecutionResolver) {
	backends := map[string]execbackend.Backend{
		"test-backend": {
			ResolveWireRequest: func(context.Context, largebody.WireRequestFacts, routing.AttemptCandidate) largebody.WireRequestSupport {
				return largebody.WireRequestSupport{
					Compatible: true,
					Reason:     largebody.WireSupportReasonNone,
				}
			},
			ResolveWireDomain: func(context.Context, largebody.WireDomainFacts) largebody.WireDomainSupport {
				return largebody.WireDomainSupport{
					Compatible:       true,
					AnyAcceptedModel: true,
					Reason:           largebody.WireSupportReasonNone,
				}
			},
		},
	}
	execResolver := routing.BackendExecutionResolverFunc(func(string) (aipsdk.BackendExecutionClass, bool) {
		return aipsdk.BackendExecutionInference, true
	})
	return backends, execResolver
}

// BuildCompositionAssessorForTest builds one production large-body assessor
// composition through the real buildLargeBodyAssessor path with a minimal
// eligible backend row. snapGen travels the same input production uses;
// composition identity must not derive from it, so two builds sharing one
// populated (or absent) publisher must still disagree.
func BuildCompositionAssessorForTest(snapGen *snapshotgen.Publisher) (*runtime.ProductionLargeBodyAssessor, string, error) {
	backends, execResolver := compositionTestBackendRow()
	return buildLargeBodyAssessor(largeBodyAssessorInput{
		Cfg: &config.Config{},
		In: executorBuildInput{
			Model:              &modelRuntime{Backends: backends},
			SnapshotGeneration: snapGen,
		},
		DefBE:        "test-backend",
		ExecResolver: execResolver,
		ExecPolicy:   config.ExecutionCompositionSafe,
	})
}

// OccupancyAssessorTestInput drives the real buildLargeBodyAssessor production
// path with caller-supplied frozen plane contributions, hook-bus occupancy,
// production ports and core ports. Zero values select the clean baseline.
type OccupancyAssessorTestInput struct {
	Frozen    aipfeature.FrozenPlaneSet
	Bus       *hooks.Bus
	Prod      ProductionOptions
	CorePorts runtime.CorePorts
}

// BuildOccupancyAssessorForTest builds one production large-body assessor
// composition through the real buildLargeBodyAssessor path with the same
// minimal eligible backend row as BuildCompositionAssessorForTest, plus the
// given occupancy inputs. It exercises the derived-catalog census seeding and
// frozen/bus/port occupancy projection without replacing the builder.
func BuildOccupancyAssessorForTest(in OccupancyAssessorTestInput) (*runtime.ProductionLargeBodyAssessor, string, error) {
	backends, execResolver := compositionTestBackendRow()
	return buildLargeBodyAssessor(largeBodyAssessorInput{
		Cfg:  &config.Config{},
		Bctx: buildContext{Bus: in.Bus},
		Opts: &BuildOptions{FeaturePlanes: in.Frozen},
		In: executorBuildInput{
			Model:     &modelRuntime{Backends: backends},
			CorePorts: in.CorePorts,
		},
		Prod:         in.Prod,
		DefBE:        "test-backend",
		ExecResolver: execResolver,
		ExecPolicy:   config.ExecutionCompositionSafe,
	})
}

func ResolveCandidateConvReaderForTest(ports featurehost.CorePorts, sf *featurehost.Runtime) (conversationprojection.Reader, bool) {
	return resolveCandidateConvReader(ports, sf)
}

func (c *CandidateHTTPCompile) Quiesce(ctx context.Context) error {
	if a := c.a(); a != nil {
		return a.Quiesce(ctx)
	}
	return nil
}

func (c *CandidateHTTPCompile) ModelRegistry() *modelregistry.Registry {
	if a := c.a(); a != nil {
		return a.models.registry
	}
	return nil
}

// NewTestGenerationPresentResolver exposes generationPresentResolver for tests.
func NewTestGenerationPresentResolver(lookup func(instanceID string, id int64) *runtimehost.Generation) terminalworkapp.GenerationBoundResolver {
	r := &generationPresentResolver{}
	r.SetLookup(lookup)
	return r
}

// NewMemoryContinuityStore builds the in-memory continuity store for tests.
func NewMemoryContinuityStore(cfg config.ContinuityConfig) (b2bua.Store, error) {
	cfg.InMemory = true
	return continuity.NewMemoryStoreFromConfig(cfg)
}

// HostModelRegistryRuntime returns the model registry runtime of the host's
// active generation.
func HostModelRegistryRuntime(h *Host) *modelregistry.Runtime {
	if h == nil || h.manager == nil {
		return nil
	}
	gen := h.manager.Active()
	if gen == nil {
		return nil
	}
	bundle, ok := gen.RequestPlane().(*GenerationBundle)
	if !ok || bundle == nil {
		return nil
	}
	return bundle.models.models
}

// RoutesSnapshotFrom builds a deterministic route read model from config and the standard registry
// wire model (same default-route resolution as serving).
func RoutesSnapshotFrom(cfg *config.Config, reg *pluginreg.Registry) (RoutesSnapshot, error) {
	return routesSnapshotFromCompatible(cfg, reg, standardplugins.ProjectCompatibleBackendRows(cfg), nil)
}

// RoutesSnapshotFromWithInventory builds a route read model with the models a
// backend instance published in its runtime inventory.
func RoutesSnapshotFromWithInventory(
	cfg *config.Config,
	reg *pluginreg.Registry,
	discovered config.DiscoveredModelForBackend,
) (RoutesSnapshot, error) {
	return routesSnapshotFromCompatible(cfg, reg, standardplugins.ProjectCompatibleBackendRows(cfg), discovered)
}
