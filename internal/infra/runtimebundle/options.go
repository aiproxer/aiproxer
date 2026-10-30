package runtimebundle

import (
	"context"
	"net/http"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/auth"
	concurrencyapp "github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/snapshotgen"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkfeaturehost "github.com/aiproxer/aiproxer/pkg/aipsdk/featurehost"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

// BuildOptions configures composition-root dependencies for [BuildHost]. Fields are
// grouped by concern to keep the contract readable and prevent the bag from
// becoming a service locator (arch review F-06). PluginRegistry is required; all
// other fields are optional (see [BuildHost] for nil defaults).
type BuildOptions struct {
	// PluginRegistry selects which bundled factories the build uses for backends.
	// Required; nil fails [BuildHost]. Kept top-level because it is set by nearly
	// every call site.
	PluginRegistry *pluginreg.Registry
	// WireModel resolves default upstream model ids when computing the effective
	// default route selector. When nil, Build uses standardplugins.DefaultWireModel
	// (standard distribution).
	WireModel   config.WireModelForBackend
	Startup     StartupOptions
	Infra       InfraOptions
	Auth        AuthOptions
	Policy      PolicyOptions
	Diagnostics DiagnosticsOptions
	Testing     TestingOptions
	// Production holds first-class enterprise injection seams (requirement 12.4).
	// Unlike Testing, these are supported for closed modules via pkg/aipruntime.
	Production ProductionOptions
	// FeatureLifecycles are merged feature plugin lifecycles owned by the candidate
	// resource ledger in serve/compile paths (task 3.2). When non-empty, callers must
	// not also Start/Stop the same instances via runtime.App (singular ownership).
	// Inspect-only bootstrap may keep lifecycles on App instead.
	FeatureLifecycles []aipplugin.Lifecycle
	// FeaturePlanes carries the immutable frozen feature plane set compiled from
	// enabled feature bundles and host contributions.
	FeaturePlanes aipfeature.FrozenPlaneSet
	// ReplaceCandidateSurface, when true, replaces process FeatureLifecycles with
	// this overlay's values even when nil/empty. Used by [CompileGeneration] so a
	// candidate that removes the last feature does not reuse startup-merged
	// lifecycles. Legacy [CompileCandidate] callers leave this false (nil overlay
	// lifecycle fields mean "no override").
	// CorePorts carries minimal fixed consumer-owned core interfaces compiled by featurehost.
	CorePorts               runtime.CorePorts
	HostProjections         featurehost.HostProjections
	ReplaceCandidateSurface bool
}

// StartupOptions carries startup-context configuration.
type StartupOptions struct {
	// StartupContext, when non-nil, is the parent context for bounded startup I/O
	// (e.g. Postgres open and schema migrate for continuity and secure-session
	// stores). When nil, Build uses [context.Background] as the parent. It is not
	// stored or used for per-request paths.
	StartupContext context.Context
}

// InfraOptions carries shared infrastructure (upstream HTTP, tracing).
type InfraOptions struct {
	// HTTPClient is the shared upstream HTTP client for backends that need
	// outbound HTTP (Bedrock, ACP). When nil, Build uses httpclient.Standard().
	HTTPClient *http.Client
	// OutboundTracing when true wraps the upstream transport with OpenTelemetry
	// HTTP propagation. When HTTPClient is non-nil, Build clones the client
	// before wrapping so caller-owned clients are not mutated.
	OutboundTracing bool
	// ProcessTracing carries an already-initialized process tracer shutdown handle
	// from BuildHost. ProcessServices retains it without owning Close (Host /
	// stdhttp remain the shutdown owners).
	ProcessTracing ProcessTracing
}

// AuthOptions carries transport-auth composition-root injection points.
type AuthOptions struct {
	// HTTPAuthProviders runs in [internal/stdhttp] before frontend decode (R4).
	// When nil, empty, or every entry is nil, the build composes providers from
	// validated config instead of using the override (so an accidental
	// []Provider{nil} slice cannot disable authentication). When at least one
	// entry is non-nil, the build uses a clone of this slice only: no config-derived
	// auth is applied. Custom binaries must supply an equivalent policy chain;
	// [cmd/aipstd] does not set this field unless you intentionally replace
	// transport auth at the composition root.
	HTTPAuthProviders []httpauth.Provider
	// AuthEventSink implements [auth.EventSink] when auth.event_delivery is
	// "custom". If set for other delivery modes, [BuildHost] returns
	// [ErrAuthEventSinkDisallowed]. For default/disabled delivery the dispatcher
	// uses an internal slog sink or nil per config.
	AuthEventSink auth.EventSink
	// RemoteDecider is required when the effective auth handler is remote or
	// required_level is api_key_sso. The OSS standard binary does not construct
	// remote transports; inject at the composition root.
	RemoteDecider auth.RemoteDecider
	// OSIdentity supplies OS principal material for local_noop. When nil, [BuildHost]
	// uses the default infra
	// [github.com/aiproxer/aiproxer/internal/infra/osidentity]
	// provider.
	OSIdentity auth.OSIdentityProvider
	// AuthErrorRenderer is optional terminal HTTP mapping for auth failures; nil
	// uses stdhttp defaults.
	AuthErrorRenderer httpauth.AuthErrorRenderer
	// AuthErrorRenderersByFrontend optional per auth-wire-frontend-id renderers
	// (stdhttp/auth DefaultFrontendIDFromRequest vocabulary). Non-nil entries
	// override the same key from [PluginRegistry.AuthErrorRenderers] when [BuildHost]
	// composes HTTP auth providers.
	AuthErrorRenderersByFrontend map[string]httpauth.AuthErrorRenderer
}

// PolicyOptions carries policy-decision observer and budget configuration.
type PolicyOptions struct {
	// PolicyObservers, when non-empty, are chained as the policy decision
	// evidence observer for the runtime snapshot. When nil/empty, the snapshot
	// uses a no-op observer so deployments without policy evidence keep current
	// request outcomes (requirements 7.6, 10.5).
	PolicyObservers []policydecision.Observer
	// PolicyTimeoutBudgetSource, when set, supplies decision-provider evaluation
	// budgets per stage/provider. When nil, the snapshot uses the default
	// zero-budget source so legacy extension behavior is unchanged
	// (requirements 6.3, 10.5).
	PolicyTimeoutBudgetSource extensions.TimeoutBudgetSource
	// PolicyDiagnosticsEnabled controls whether privileged-visibility policy
	// decision records may leave the core through the evidence emitter. Default
	// false withholds privileged records (requirement 7.4).
	PolicyDiagnosticsEnabled bool
}

// DiagnosticsOptions carries operator-diagnostics overrides. Currently a
// single-field group; it can grow as diagnostics overrides accrue.
type DiagnosticsOptions struct {
	// SecureSessionStore is optional; when set on Built, stdhttp may mount
	// secure-session diagnostics.
	SecureSessionStore app.Store
}

// TestingOptions carries test-only overrides. Production leaves these zero so
// Build constructs the real resources from config.
type TestingOptions struct {
	// Clock overrides time sources for the executor and routing-health circuit
	// breaker. Tests only.
	Clock func() time.Time
	// ControlPlaneStoreOverride, when non-nil, replaces the configured
	// control-plane store. When the override implements interface{ Close() error },
	// that Close is used as the closer and is disposed by Build on every error
	// path exactly like the production store closer. Tests only; production
	// leaves this nil so the store is built from config.
	ControlPlaneStoreOverride controlplane.Store
	// ConcurrencyLeaseStoreOverride, when non-nil, replaces the configured
	// concurrency lease store. Tests only; production leaves this nil.
	ConcurrencyLeaseStoreOverride concurrencyapp.LeaseStore
	// PostgresPoolOpener, when non-nil, is used by Build's PoolRegistry instead
	// of the default Postgres opener. Tests only; production leaves this nil.
	PostgresPoolOpener db.PoolOpener
	// SnapshotPublisherOverride, when non-nil, replaces the Build-constructed
	// policy/rating generation publisher (Phase 9.3). Tests only.
	SnapshotPublisherOverride *snapshotgen.Publisher
	// FeatureHostRegistrations carries startup-only host-feature bindings for tests.
	FeatureHostRegistrations []sdkfeaturehost.Registration
}
