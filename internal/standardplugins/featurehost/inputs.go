package featurehost

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/accessmode"
	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/internal/infra/featuredb"
	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	"github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm"
	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	adminkeepwarm "github.com/aiproxer/aiproxer/internal/stdhttp/admin/keepwarm"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkfeaturehost "github.com/aiproxer/aiproxer/pkg/aipsdk/featurehost"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
	aipstate "github.com/aiproxer/aiproxer/pkg/aipsdk/state"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/uptrace/bun"
)

// MetricsRegistry is the narrow generic collector-registration seam owned
// by metrics infrastructure: implementations register opaque prometheus
// collectors, carrying no feature knowledge.
type MetricsRegistry interface {
	Register(prometheus.Collector) error
}

// ProcessInput contains only generic process capabilities required by the standard feature set.
// It MUST NOT accept *runtimebundle.BuildOptions, *ProcessServices, full backend maps,
// database pool registries, or an any services map (Requirement 8.4, Task 2.1).
type ProcessInput struct {
	Logger            *slog.Logger
	ExtensionState    aipstate.Store
	BackgroundAux     *auxreq.BackgroundScheduler
	ContinuityStore   b2bua.Store
	BunDB             *bun.DB
	HostRegistrations []sdkfeaturehost.Registration
	// HostEnv is a generic process-environment capability used when none is registered.
	HostEnv HostEnvironment
	// MetricsRegistry receives feature-owned collectors through the generic metrics seam (nil disables).
	MetricsRegistry MetricsRegistry
	// FeatureDatabases opens the isolated durable databases a feature-owned
	// advisory store uses. The feature host never opens a database path or
	// reads a connection string itself: the composition root owns the
	// connection settings and the handle lifetime (nil disables every
	// feature-owned store).
	FeatureDatabases featuredb.Opener
	// buildSteps carries staged construction actions for package-local tests
	// only. It is unexported so no external caller (including generic
	// runtimebundle) can inject constructors or closers (Tasks 2.1/2.3).
	buildSteps []constructionStep
}

// CorePorts carries the fixed consumer-owned core interfaces.
type CorePorts = runtime.CorePorts

// HostProjections carries opaque admin and operator projections for host composition.
type HostProjections struct {
	// FeatureMetadata describes generation-composed bundles, including
	// host-bound contributions absent from the initial factory merge.
	FeatureMetadata          []diag.InventoryFeatureMetadata
	MetricsSwap              func()
	KeepwarmAdmin            adminkeepwarm.Options
	TerminalPolicyProjection TerminalPolicyProjectionFunc
	// ProxyMemoryHTTP is the projected proxy-memory REST and MCP handler; the
	// standard-HTTP composer mounts it at /v1/aip/memory and its subtree. A nil
	// handler means the feature is disabled for this generation, so nothing is
	// mounted.
	ProxyMemoryHTTP http.Handler
}

// GenerationInput carries inputs for featurehost generation composition.
// It deliberately does NOT accept *runtimebundle.BuildOptions or *runtimebundle.ProcessServices.
type GenerationInput struct {
	Registrations     []aipsdk.Registration
	HostRegistrations []sdkfeaturehost.Registration
	MergeSurface      featurebundle.GeneratedMergeSurface
	Planes            aipfeature.FrozenPlaneSet
	Lifecycles        []aipplugin.Lifecycle
	CandidatePlanes   aipfeature.FrozenPlaneSet
	BackgroundClient  auxiliary.BackgroundClient
	BackgroundPoller  auxiliary.BackgroundPoller
	// ProxyMemoryTargetCompiler is the generation-bound target compiler the
	// proxy-memory feature consumes to compile a phase target into the final
	// compiled actual target (design §1). The composition root binds it from
	// the generation's executor; a nil value leaves the feature without a
	// compiled-target seam, and a phase that declares a model policy then
	// refuses explicitly.
	ProxyMemoryTargetCompiler proxymemory.TargetCompiler
	// ReasoningProdOpts/ReasoningTestOpts carry the raw production and testing
	// reasoning option sources. The facade merges them internally (Task 2.4,
	// Requirement 8.3); generic runtimebundle must never merge or interpret
	// reasoning policy itself, so no merged ReasoningOpts field exists here.
	ReasoningProdOpts    ReasoningCompressionOptions
	ReasoningTestOpts    ReasoningCompressionOptions
	InterleavedConfig    interleavedthinking.Config
	ConfigInterleaved    config.InterleavedConfig
	KeepwarmConfig       keepwarm.Config
	NowFn                func() time.Time
	KeepwarmAccounting   billingport.ProviderMaintenanceUsageObserver
	ConfigDir            string
	AccessMode           accessmode.Mode
	SecretEnv            SecretGuardEnvironment
	SecretInputs         SecretGuardInputs
	DecisionObserver     SecretDecisionObserver
	FaultInject          error
	ConversationObserver conversationview.Observer
}

// GenerationOutput represents the compiled output of standard-distribution features
// for a single generation: the ordinary Bundle/Planes/Lifecycles surface plus the
// minimal fixed CorePorts consumer ports (Task 2.2). Concrete per-feature runtime
// handles (secret-guard plane, keep-warm manager/quiesce) travel inside the frozen
// planes, lifecycles, and opaque ports instead of dedicated fields.
type GenerationOutput struct {
	Bundle          aipfeature.FeatureBundle
	Planes          aipfeature.FrozenPlaneSet
	Lifecycles      []aipplugin.Lifecycle
	CorePorts       CorePorts
	HostProjections HostProjections
}
