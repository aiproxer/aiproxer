package runtimebundle

import (
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/affinity"
	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/capabilities"
	concurrencyapp "github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/config"
	corecp "github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/policy"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	ssessionapp "github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/snapshotgen"
	"github.com/aiproxer/aiproxer/internal/core/streamrecovery"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	infraaux "github.com/aiproxer/aiproxer/internal/infra/auxiliary"
	"github.com/aiproxer/aiproxer/internal/infra/billingbinding"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview/sdkadapter"
	"github.com/aiproxer/aiproxer/internal/infra/metrics"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/steering"
)

// executorRuntime holds the assembled executor and the values the candidate compile needs from
// the executor unit: the effective default route, the secure-session store (after
// the optional BuildOptions override), the model-catalog runtime attached to the
// executor, and the token-accounting admin service.
type executorRuntime struct {
	Exec *runtime.Executor
	// DefaultRoute is the configured default route. The frontends fall back to
	// it and the executor applies the model aliases to it, exactly as it does
	// to a client selector, so the route the executor runs is the effective
	// default route the operator check approved.
	DefaultRoute string
	// DefaultModelBinding is the generation-owned canonical-to-native binding of
	// a default route derived from a discovered model. It is nil for every
	// other default route.
	DefaultModelBinding  *defaultModelBinding
	SecureSessionStore   ssessionapp.Store
	CatalogRuntime       *modelcatalog.CatalogRuntime
	TokenAccountingAdmin *accountingapp.Service
	ReadinessReport      *corecp.ReadinessReportService
	// Production is the candidate-local effective production wiring. It is
	// returned instead of mutating shared BuildOptions during concurrent builds.
	Production ProductionOptions
}

// executorBuildInput groups the upstream unit results consumed by
// [buildExecutorRuntime].
type executorBuildInput struct {
	Bctx                buildContext
	Ledger              *ResourceLedger
	NowFn               func() time.Time
	Ext                 *extensionRuntime
	Model               *modelRuntime
	Persistence         *persistenceRuntime
	Security            *securityRuntime
	Observability       *observabilityRuntime
	ControlPlane        *controlPlaneRuntime
	UsageAuthority      usagelimit.Authority
	Concurrency         *concurrencyAuthorityRuntime
	SnapshotGeneration  *snapshotgen.Publisher
	TerminalWork        *terminalWorkRuntime
	SharedMutable       *sharedMutableRuntime
	AccountingStores    *processAccountingStores
	Metering            *meteringRuntime
	BackendIdentities   map[string]BackendStateIdentity
	CorePorts           runtime.CorePorts
	BackgroundScheduler *auxreq.BackgroundScheduler
	GenerationRunner    *infraaux.GenerationExecutorRunner
	ConversationStore   conversationview.Store
}

// buildExecutorRuntime runs the executor-assembly sequence: routing resolution,
// capability map, RNG seed, stream-recovery config, token-accounting runtime,
// interleaved-thinking config, price catalog, auth/session/metrics/secure-session
// wiring, synthetic-local-principal flag, and model-catalog resolver attachment.
// All values are computed before [runtime.NewExecutor] so NewExecutor is a strong
// invariant boundary: no post-construction field mutation occurs.
// Accounting ledger and metering stores are process-owned; this bind only
// attaches generation backends to those shared identities.
func buildExecutorRuntime(in executorBuildInput) (*executorRuntime, error) {
	bctx := in.Bctx
	cfg, log, opts := bctx.Cfg, bctx.Log, bctx.Opts
	// A backend kind that discovers its models at runtime has no canonical
	// default model, so the default model the instance designated is the
	// default route. The inventory is this generation's own: the route the
	// frontends fall back to is the route the executor runs.
	var discovered config.DiscoveredModelForBackend
	if in.Model != nil {
		discovered = discoveredModelResolver(in.Model.RegistryRuntime)
	}
	resolution, defBE, err := resolveRouting(cfg, opts.WireModel, discovered)
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: %w", err)
	}
	// A default route derived from a discovered model keeps the mapping it was
	// compiled with: a later inventory refresh can withdraw the model, and the
	// generation's route must still address the model the backend published.
	var defaultBinding *defaultModelBinding
	if resolution.Source == config.DefaultRouteSourceDiscoveredInventory && in.Model != nil {
		bindings := frozenDefaultRouteBindings(in.Model.Registry, resolution.Leaves)
		if len(bindings) > 0 {
			defaultBinding = &defaultModelBinding{bindings: bindings}
		}
	}
	effectiveRoute, aliasResolver := resolution.Selector, resolution.Aliases
	if resolution.Warning != "" {
		// The frontends have no route to fall back to. The serving runtime keeps
		// working for clients that route explicitly, so the missing default is
		// reported rather than silently assumed.
		log.Warn("no default route configured", "detail", resolution.Warning)
	}
	execResolver := buildBackendExecutionResolver(cfg, opts.PluginRegistry)
	execPolicy := cfg.Routing.EffectiveExecutionCompositionPolicy()
	if bctx.ExplicitCandidate {
		if err := validateRouteSelectorsAgainstBackends(cfg, effectiveRoute, cfg.ModelAliases, cfg.Plugins.Backends, execResolver, execPolicy); err != nil {
			return nil, err
		}
	}
	capMap := make(capabilities.MapResolver, len(in.Model.Backends))
	for id, be := range in.Model.Backends {
		capMap[id] = func(ctx context.Context, cand routing.AttemptCandidate, call aipapi.Call) aipapi.BackendCaps {
			return execbackend.EffectiveCaps(ctx, be, call, cand)
		}
	}
	var seed int64
	if err := binary.Read(crand.Reader, binary.LittleEndian, &seed); err != nil {
		seed = time.Now().UnixNano()
	}
	streamRecovery, err := streamRecoveryConfigFromConfig(cfg)
	if err != nil {
		return nil, err
	}
	tokenAccounting, err := bindTokenAccountingRuntime(in.AccountingStores, cfg, in.Model.Backends)
	if err != nil {
		return nil, err
	}
	meteringRT := in.Metering
	var prod ProductionOptions
	if in.Bctx.Opts != nil {
		prod = in.Bctx.Opts.Production
		if prod.MeteringRecorder != nil {
			// Recorder and sink stay paired: the generation writes the same
			// durable journal, behind the process-owned producer fence.
			meteringRT = &meteringRuntime{Recorder: prod.MeteringRecorder, ObservationSink: prod.MeteringObservationSink, StoreBacking: "injected"}
		}
	}
	var quotaStore coremetering.AccountWindowStore
	if prod.MeteringAccountWindowStore != nil {
		quotaStore = prod.MeteringAccountWindowStore
	} else if meteringRT != nil {
		quotaStore, _ = meteringRT.Recorder.(coremetering.AccountWindowStore)
	}
	quotaReg, err := quotaRequestRegistration(cfg, quotaStore, in.NowFn)
	if err != nil {
		return nil, err
	}
	if quotaReg != nil {
		// Allocate a new slice so a candidate-local quota registration never
		// mutates the caller-owned production options backing array.
		prod.RequestRegistrations = append([]authority.RequestRegistration{*quotaReg}, prod.RequestRegistrations...)
	}
	// Compute accounting runtime fields.
	accountingRT := runtime.AccountingRuntime{}
	if tokenAccounting != nil {
		accountingRT.Preflight = tokenAccounting.Preflight
		accountingRT.StreamUsage = tokenAccounting.StreamUsage
		accountingRT.TokenAccountingObservability = tokenAccounting.Observability
		// Request metering admission needs the same configured counter regardless
		// of whether the optional public/admin count endpoint is mounted.
		accountingRT.AdminCountService = tokenAccounting.Counter
	}
	if meteringRT != nil {
		// Recorder and sink are process-owned and borrowed by this generation;
		// keep them paired so the sink always writes the same durable journal.
		accountingRT.MeteringRecorder = meteringRT.Recorder
		accountingRT.MeteringObservationSink = meteringRT.ObservationSink
	}
	if in.UsageAuthority != nil {
		accountingRT.UsageAuthority = in.UsageAuthority
		cleanupTimeout, err := cfg.Accounting.Authority.CleanupTimeoutDuration()
		if err != nil {
			return nil, err
		}
		accountingRT.UsageAuthorityCleanupTimeout = cleanupTimeout
	}
	attachConcurrencyToAccounting(&accountingRT, in.Concurrency)
	if err := attachCompatibleAdmission(&prod, cfg); err != nil {
		return nil, err
	}
	if accountingRT.UsageAuthority != nil || accountingRT.ConcurrencyProvider != nil || prod.HasAuthorityOverrides() {
		if err := attachAuthorityCoordinators(&accountingRT, prod); err != nil {
			return nil, err
		}
	}
	accountingRT.SnapshotGeneration = in.SnapshotGeneration
	if in.TerminalWork != nil {
		accountingRT.TerminalWork = in.TerminalWork.Intents
	}
	// Compute security runtime from secure-session + auth.
	securityRT := securityRuntimeFromSecureSession(in.Persistence.SecureSession)
	securityRT.AuthEvents = in.Security.AuthEvents
	securityRT.SessionAuditPolicy = in.Security.SAP
	ssStore := strings.TrimSpace(cfg.SecureSession.Store)
	if ssStore == "" {
		ssStore = "memory"
	}
	securityRT.SyntheticLocalPrincipal = cfg.SingleUserLocalMode() && strings.EqualFold(ssStore, "memory")
	policyDiagEnabled := false
	maxArgsBytes := 0
	if opts != nil {
		policyDiagEnabled = opts.Policy.PolicyDiagnosticsEnabled
		maxArgsBytes = aipfeature.Get(opts.FeaturePlanes, aipfeature.PlaneToolCallFinalizationMaxArgsBytes)
	}
	// Compute observability runtime with metrics sinks.
	obsRT := runtime.ObservabilityRuntime{
		Log:                      log,
		PolicyDiagnosticsEnabled: policyDiagEnabled,
	}
	// The seam-digest feed hashes the model-bound request per B-leg. It stays
	// off unless the operator enables it and the metrics diagnostic can
	// consume it: a reload that flips the flag is restart-required via the
	// whole-block comparison. Gating on the metrics switch as well avoids
	// paying the hash pass when no collapse detector runs.
	obsRT.SeamDigestsEnabled = config.EffectiveSeamDigestsEnabled(cfg)
	if obsRT.SeamDigestsEnabled && in.Observability.Bundle != nil {
		obsRT.SeamDigestKey = in.Observability.Bundle.SeamDigestKey()
	}
	// The instruction-identity feed hashes only the provider-bound
	// system/developer instruction prefix, so it runs whenever the collapse
	// detector exists: the identity is what attributes a cross-turn steering
	// change to system-prompt. With metrics disabled no detector consumes it
	// and no hashing runs.
	obsRT.SystemPromptDigestEnabled = cfg.Observability.Metrics.Enabled
	if obsRT.SystemPromptDigestEnabled && in.Observability.Bundle != nil {
		obsRT.SystemPromptDigestKey = in.Observability.Bundle.SeamDigestKey()
	}
	// Borrow the process-owned launch sequence from the metrics bundle so every
	// generation, including an older one still draining, draws from one
	// chronology across reloads.
	if in.Observability != nil && in.Observability.Bundle != nil {
		obsRT.SystemPromptLaunchSeq = in.Observability.Bundle.SystemPromptLaunchSeq
	}
	// The composition root owns the buffer installed at construction.
	if strings.TrimSpace(cfg.Diagnostics.RouteTracePath) != "" {
		obsRT.RouteTrace = diag.NewRouteTraceBuffer(diag.DefaultRouteTraceCapacity)
	}
	if in.Observability.Bundle != nil {
		obsRT.Metrics = in.Observability.Bundle.ExecutorSink()
		obsRT.ExtensionMetrics = in.Observability.Bundle.ExtensionStageSink()
		obsRT.SecretGuardDecisionMetrics = in.Observability.Bundle.SecretGuardDecisionSink()
		obsRT.ConversationViewObserver = in.Observability.Bundle.ConversationViewObserver()
		securityRT.SecureSessionMetrics = in.Observability.Bundle.SecureSessionMetricsSink()
		if tokenAccounting != nil && tokenAccounting.Observability != nil {
			tokenAccounting.Observability.SetSink(in.Observability.Bundle.TokenAccountingObservabilitySink())
		}
	}
	// Build routing runtime; model catalog resolvers are attached before construction.
	var affStore affinity.Store
	var candHealth policy.CandidateHealth
	aLeg := (*leglifecycle.Coordinator)(nil)
	if in.SharedMutable != nil {
		affStore, candHealth = in.SharedMutable.candidateRoutingViews(in.BackendIdentities, cfg, in.NowFn)
		aLeg = in.SharedMutable.ALegLifecycle
	}
	var overrideReader routeoverride.Reader
	if in.Persistence != nil && in.Persistence.OverrideStore != nil {
		overrideReader = in.Persistence.OverrideStore
	}
	routingRT := runtime.RoutingRuntime{
		MaxAttempts:                cfg.Routing.MaxAttempts,
		DefaultBackend:             defBE,
		SelectorAliases:            aliasResolver,
		CapsResolver:               capMap,
		CandidateHealth:            candHealth,
		RouteObserver:              routeObserverFor(log),
		AffinityStore:              affStore,
		AffinityMissingIdentity:    affinity.MissingIdentityPolicy(strings.TrimSpace(cfg.Routing.Affinity.MissingIdentity)),
		TransportFallbackPolicy:    config.EffectiveTransportFallbackPolicy(cfg),
		RouteOverrideReader:        overrideReader,
		ExecutionCompositionPolicy: execPolicy,
		BackendExecutionResolver:   execResolver,
		AttemptPolicy:              cfg.Routing.EffectiveAttemptPolicy(),
	}
	routingRT, catalogRuntime := attachModelCatalog(routingRT, in.Model.StartedCatalog, cfg)
	genRunner := in.GenerationRunner
	if genRunner == nil {
		genRunner = infraaux.NewGenerationExecutorRunner()
	}
	var compactionBackground auxiliary.BackgroundClient
	if in.BackgroundScheduler != nil {
		compactionBackground = in.BackgroundScheduler.BindRunner(genRunner)
	}
	var convObs conversationview.Observer
	if in.Observability != nil && in.Observability.Bundle != nil && in.Observability.Bundle.ConversationViewObserver() != nil {
		convObs = metricsObserverAdapter{ConversationViewObserver: in.Observability.Bundle.ConversationViewObserver()}
	}
	convStore := in.ConversationStore
	largeBodyAssessor, largeBodyGenID, err := buildLargeBodyAssessor(largeBodyAssessorInput{
		Cfg:                           cfg,
		Bctx:                          bctx,
		Opts:                          opts,
		In:                            in,
		RoutingRT:                     routingRT,
		AccountingRT:                  accountingRT,
		Prod:                          prod,
		DefBE:                         defBE,
		AliasResolver:                 aliasResolver,
		ExecResolver:                  execResolver,
		ExecPolicy:                    execPolicy,
		OverrideReader:                overrideReader,
		CapsResolverWireProofSubsumed: capMap != nil,
		DefaultRouteBindings:          defaultRouteBindings(defaultBinding),
	})
	if err != nil {
		return nil, err
	}
	// The generic binder carries the generation-resolved backend-kind inventory
	// into the public-binding adapter so an admission can stamp the planned
	// route facts of its route plan. It is metadata carriage only: the runtime
	// applies no admission policy on the kinds, and the shared process
	// admission port is never mutated — each generation publishes its own copy.
	prod.BillingExposureAdmission = billingbinding.BindBackendKinds(prod.BillingExposureAdmission, in.Model.BackendKinds)
	exec := runtime.NewExecutor(runtime.ExecutorConfig{
		Core: runtime.CoreRuntime{
			CorePorts:                          in.CorePorts,
			Store:                              in.Persistence.Store,
			Backends:                           in.Model.Backends,
			ALegLifecycle:                      aLeg,
			Rand:                               routing.NewSeededRng(seed),
			Now:                                in.NowFn,
			MaxPendingWireEvents:               cfg.Server.EffectiveMaxPendingWireEvents(),
			StreamRecovery:                     streamRecovery,
			EmptyVisibleTurnRetry:              runtime.EmptyVisibleTurnRetryPolicy{Enabled: config.EffectiveEmptyVisibleTurnRetry(cfg).Enabled},
			ConversationViewTagger:             newConversationViewTaggerAdapter(convStore),
			LargeBodyAssessor:                  largeBodyAssessor,
			LargeBodyGenerationID:              largeBodyGenID,
			LargeBodyCandidateDomainGeneration: largeBodyGenID,
			SteeringWriterFactory: func(ctx context.Context, aLegID string, resolver runtime.SteeringWriterResolver) (steering.Writer, error) {
				if convStore == nil {
					return nil, errors.New("runtimebundle: conversation store unavailable")
				}
				return sdkadapter.NewWriterWithObserver(convStore, aLegID, sdkadapter.TrajectoryResolver(resolver), convObs)
			},
			BoundRegistry: proxyMemoryBoundRegistry(in.Model),
		},
		Billing: runtime.BillingRuntime{
			BillingCreditGate:        prod.BillingCreditGate,
			BillingExposureAdmission: prod.BillingExposureAdmission,
			TerminalUsageSink:        prod.BillingTerminalUsageSink,
			BillingIdentity:          prod.BillingIdentity,
		},
		Routing:       routingRT,
		Security:      securityRT,
		Accounting:    accountingRT,
		Observability: obsRT,
		Extension: runtime.ExtensionRuntime{
			Bus:                              bctx.Bus,
			RuntimeSnapshot:                  in.Ext.Snap,
			ToolCallFinalizationMaxArgsBytes: maxArgsBytes,
		},
		Compaction: runtime.CompactionRuntime{BackgroundAux: compactionBackground},
	})
	genRunner.Bind(exec)
	secureSessionStore := in.Persistence.SecureSession.appStore
	if opts.Diagnostics.SecureSessionStore != nil {
		secureSessionStore = opts.Diagnostics.SecureSessionStore
	}
	var concurrencySvc *concurrencyapp.Service
	if in.Concurrency != nil {
		concurrencySvc = in.Concurrency.Service
	}
	var tokenAccountingAdminSvc *accountingapp.Service
	if tokenAccounting != nil {
		tokenAccountingAdminSvc = tokenAccounting.Admin
	}
	readiness := buildReadinessReportService(readinessReportBuildInput{
		Cfg:                cfg,
		ControlPlaneStatus: in.ControlPlane.statusHandle(),
		UsageAuthority:     in.UsageAuthority,
		Concurrency:        concurrencySvc,
		Metering:           meteringRT,
		SnapshotGeneration: in.SnapshotGeneration,
		Executor:           exec,
		Production:         prod,
		TerminalWork:       in.TerminalWork,
	})
	return &executorRuntime{
		Exec:                 exec,
		DefaultRoute:         resolution.RawSelector,
		DefaultModelBinding:  defaultBinding,
		SecureSessionStore:   secureSessionStore,
		CatalogRuntime:       catalogRuntime,
		TokenAccountingAdmin: tokenAccountingAdminSvc,
		ReadinessReport:      readiness,
		Production:           prod,
	}, nil
}

func streamRecoveryConfigFromConfig(cfg *config.Config) (streamrecovery.Config, error) {
	eff, err := config.EffectiveStreamRecoveryAutoResume(cfg, config.StreamRecoveryOverrides{})
	if err != nil {
		return streamrecovery.Config{}, fmt.Errorf("runtimebundle: stream recovery config: %w", err)
	}
	return streamrecovery.Config{
		Enabled:     eff.Enabled,
		IdleTimeout: eff.IdleTimeout,
		GracePeriod: eff.GracePeriod,
		EmitWarning: eff.EmitWarning,
	}, nil
}

// validateRouteSelectorsAgainstBackends fails compile when explicit default_route or alias
// replacements reference backends absent from the candidate backend row set (req 9.2) or
// violate execution composition safety under the active policy.
func validateRouteSelectorsAgainstBackends(
	cfg *config.Config,
	effectiveRoute string,
	aliases []config.ModelAliasConfig,
	backendRows []config.PluginConfig,
	execResolver routing.BackendExecutionResolver,
	policy config.ExecutionCompositionPolicy,
) error {
	configured := make(map[string]struct{}, len(backendRows))
	for _, p := range backendRows {
		if id := p.InstanceID(); id != "" {
			configured[id] = struct{}{}
		}
	}
	if strings.TrimSpace(cfg.Routing.DefaultRoute) != "" {
		if err := validateSelectorTextAgainstBackends("routing default_route", effectiveRoute, configured); err != nil {
			return err
		}
		if sel, err := routing.Parse(effectiveRoute); err == nil {
			if err := routing.ValidateExecutionComposition(sel, execResolver, policy); err != nil {
				return fmt.Errorf("runtimebundle: routing default_route: %w", err)
			}
		}
	}
	for i, a := range aliases {
		repl := strings.TrimSpace(a.Replacement)
		if repl == "" {
			continue
		}
		label := fmt.Sprintf("model_aliases[%d].replacement", i)
		if err := validateSelectorTextAgainstBackends(label, repl, configured); err != nil {
			return err
		}
		if sel, err := routing.Parse(repl); err == nil {
			if err := routing.ValidateExecutionComposition(sel, execResolver, policy); err != nil {
				return fmt.Errorf("runtimebundle: %s: %w", label, err)
			}
		}
	}
	return nil
}

func buildBackendExecutionResolver(cfg *config.Config, reg *pluginreg.Registry) routing.BackendExecutionResolver {
	if cfg == nil {
		return routing.BackendExecutionResolverFunc(func(string) (aipsdk.BackendExecutionClass, bool) {
			return aipsdk.BackendExecutionUnknown, false
		})
	}
	classes := make(map[string]aipsdk.BackendExecutionClass, len(cfg.Plugins.Backends))
	for _, p := range cfg.Plugins.Backends {
		if !p.Enabled {
			continue
		}
		iid := p.InstanceID()
		if iid == "" {
			continue
		}
		fid := p.FactoryID()
		cls := aipsdk.BackendExecutionUnknown
		if reg != nil {
			if prof, ok := reg.BackendExecutionProfile(fid); ok {
				cls = prof.EffectiveClass()
			}
		}
		classes[iid] = cls
	}
	return routing.BackendExecutionResolverFunc(func(id string) (aipsdk.BackendExecutionClass, bool) {
		c, ok := classes[id]
		return c, ok
	})
}

func validateSelectorTextAgainstBackends(label, text string, configured map[string]struct{}) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	sel, err := routing.Parse(text)
	if err != nil {
		return nil // malformed selectors rejected by config.Validate
	}
	for _, id := range routing.BackendIDsReferenced(sel) {
		if _, ok := configured[id]; !ok {
			return fmt.Errorf("runtimebundle: %s references unconfigured backend %q", label, id)
		}
	}
	return nil
}

func resolveRouting(
	cfg *config.Config,
	wireModel config.WireModelForBackend,
	discovered config.DiscoveredModelForBackend,
) (routing.DefaultRouteResolution, string, error) {
	if wireModel == nil {
		wireModel = standardplugins.DefaultWireModel
	}
	resolution, err := routing.ResolveDefaultRouteWithInventory(cfg, wireModel, discovered)
	if err != nil {
		return routing.DefaultRouteResolution{}, "", err
	}
	return resolution, resolution.DefaultBackend, nil
}

type conversationViewTaggerAdapter struct{ store conversationview.Store }

func newConversationViewTaggerAdapter(s conversationview.Store) runtime.ConversationViewTagger {
	if s == nil {
		return nil
	}
	return &conversationViewTaggerAdapter{store: s}
}

func (a *conversationViewTaggerAdapter) TagNeverBackend(ctx context.Context, aLegID string, tags []runtime.TagRequest) (runtime.TagResult, error) {
	if a == nil || a.store == nil {
		return runtime.TagResult{}, errors.New("runtimebundle: conversation store unavailable")
	}
	cvTags := make([]conversationview.TagRequest, len(tags))
	for i, t := range tags {
		cvTags[i] = conversationview.TagRequest{Identity: t.Identity, Reason: t.Reason}
	}
	res, err := a.store.TagNeverBackend(ctx, aLegID, cvTags)
	if err != nil {
		return runtime.TagResult{}, err
	}
	return runtime.TagResult{StateRevision: res.StateRevision, Tags: res.Tags}, nil
}

func (a *conversationViewTaggerAdapter) DeleteALeg(ctx context.Context, aLegID string) error {
	if a != nil && a.store != nil {
		if d, ok := a.store.(interface {
			DeleteALeg(context.Context, string) error
		}); ok {
			return d.DeleteALeg(ctx, aLegID)
		}
	}
	return nil
}

func (a *conversationViewTaggerAdapter) ConversationViewStore() conversationview.Store {
	if a == nil {
		return nil
	}
	return a.store
}

type metricsObserverAdapter struct {
	metrics.ConversationViewObserver
}

func (a metricsObserverAdapter) OnSteeringMutation(k conversationview.CacheDiscontinuityKind, p conversationprojection.PlacementKind) {
	a.ConversationViewObserver.OnSteeringMutation(metrics.CacheDiscontinuityKind(k), p)
}
