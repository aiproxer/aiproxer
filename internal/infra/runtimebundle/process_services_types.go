package runtimebundle

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/aiproxer/aiproxer/internal/core/affinity"
	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	concurrencyapp "github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/ingressdefense"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/policy"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	ssessionapp "github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/snapshotgen"
	terminalworkapp "github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/geoip"
	"github.com/aiproxer/aiproxer/internal/infra/metrics"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	aipstate "github.com/aiproxer/aiproxer/pkg/aipsdk/state"
)

// ProcessTracing holds process-owned tracing shutdown and outbound-propagation state.
type ProcessTracing struct {
	Shutdown func(context.Context) error
	Active   bool
}

// ProcessServices owns process-scoped resources constructed once per process.
// Generation compilation receives a non-owning reference and must not Close it.
type ProcessServices struct {
	Logger         *slog.Logger
	FactoryCatalog *pluginreg.Registry
	Tracing        ProcessTracing
	Metrics        *metrics.Bundle
	// SystemPromptLaunchSeq is the process-owned request launch ordinal source
	// shared by every generation executor. It aliases the metrics bundle's
	// sequence, which is created once with the process and survives reloads and
	// overlapping generation retirement; it is nil when metrics are disabled,
	// where the instruction feed is also off. It carries no request content and
	// needs no closer.
	SystemPromptLaunchSeq *runtime.RequestLaunchSequence
	GeoIP                 *geoip.Service
	// IngressDefense is the single process-lifetime bounded adaptive state for
	// ingress self-defense. It is sized once from the startup-fixed
	// max_entries/state_ttl, survives policy-only generation reloads, and is
	// disposed only with this ProcessServices. A generation borrows it and can
	// never close or resize it.
	IngressDefense     *ingressdefense.State
	DatabasePools      *db.PoolRegistry
	Continuity         b2bua.Store
	RouteOverrideStore routeoverride.Store
	SecureSessions     ssessionapp.Store
	DecodeAdmission    aipsdk.DecodeAdmission
	SpoolLedger        *largebody.SpoolLedger
	ALegLifecycle      *leglifecycle.Coordinator
	AffinityStore      affinity.Store
	CandidateHealth    policy.CandidateHealth
	ExtensionState     aipstate.Store
	MeteringRecorder   metering.Recorder
	UsageAuthority     usagelimit.Authority
	Concurrency        *concurrencyapp.Service
	SnapshotGeneration *snapshotgen.Publisher
	SnapshotController *SnapshotController
	MeteringQuerier    metering.Querier

	TerminalWorkProcessor *terminalworkapp.Processor
	TerminalWorkRegistry  *terminalworkapp.Registry
	TerminalWorkQueries   *terminalworkapp.QueryService
	TerminalWorkMetrics   *terminalworkapp.MetricsObserver
	BackgroundAux         *BackgroundAuxScheduler

	// StandardFeatures owns the single standard-distribution feature host handle (Task 2.3).
	StandardFeatures *featurehost.Runtime

	// Internal handles required by candidate compilation (non-API).
	persistence       *persistenceRuntime
	controlPlane      *controlPlaneRuntime
	concurrencyRT     *concurrencyAuthorityRuntime
	terminalWorkRT    *terminalWorkRuntime
	dualPlaneMigrator *dualPlaneMigrator
	policyObs         policydecision.Observer
	sharedMutable     *sharedMutableRuntime
	accountingStores  *processAccountingStores
	meteringRT        *meteringRuntime
	// observationFence is the process-owned accepted-producer fence over the
	// borrowed observation sink. It is the only observation sink a host-owned
	// producer ever sees, and it is drained before any process closer runs.
	observationFence coremetering.ObservationProducerFence
	cfg              *config.Config
	opts             *BuildOptions
	// hostEnv is the process-environment capability retained for authorized
	// per-candidate captures (static-credential snapshots). It is read only
	// during candidate compilation, never on the request path.
	hostEnv featurehost.HostEnvironment

	closers   []func() error
	closeOnce sync.Once
	closeErr  error
	closed    atomic.Bool
}

// ProcessServicesInput configures [NewProcessServices].
type ProcessServicesInput struct {
	Cfg     *config.Config
	Log     *slog.Logger
	Opts    *BuildOptions
	Tracing ProcessTracing
	// HostEnv is a generic process-environment capability forwarded to the
	// standard feature host, which synthesizes the default env-derived host
	// binding from it when no explicit binding is registered. It names no
	// concrete feature and carries no feature semantics.
	HostEnv featurehost.HostEnvironment
	// PluginResourcePool, PluginHost, PluginArtifacts, and PluginStagingDir are
	// process-owned discovered-plugin resources. When set, NewProcessServices
	// takes sole ownership and disposes them once after generation retirement in
	// reverse acquisition order (pool → host → artifacts → staging), so pooled
	// physical cleanup can still use the live host and Windows releases staged
	// executable handles before staging removal.
	PluginResourcePool *backendResourcePool
	PluginHost         *processhost.Host
	PluginArtifacts    []*trust.VerifiedArtifact
	PluginStagingDir   string
	BackgroundAux      *BackgroundAuxScheduler // transferred and closed with process
}
