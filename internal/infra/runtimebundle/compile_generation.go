package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/configreload"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	terminalworkapp "github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	cpadmin "github.com/aiproxer/aiproxer/internal/stdhttp/admin/controlplane"
	adminaccounting "github.com/aiproxer/aiproxer/internal/stdhttp/admin/tokenaccounting"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkfeaturehost "github.com/aiproxer/aiproxer/pkg/aipsdk/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

func CompileGeneration(ctx context.Context, in GenerationCompileInput) (GenerationRuntime, error) {
	if in.Compose == nil {
		return nil, fmt.Errorf("runtimebundle: nil HandlerComposer")
	}
	gc, err := compileGenerationCandidate(ctx, in, true)
	if err != nil {
		return nil, err
	}
	ps, cand, featOut, frozen, regs, nowFn := in.Process, gc.cand, gc.featOut, gc.frozen, gc.regs, gc.nowFn
	failBeforeTransfer := func(err error) (GenerationRuntime, error) {
		if rollErr := cand.RollbackUnpublished(); rollErr != nil {
			return nil, errors.Join(err, rollErr)
		}
		return nil, err
	}
	if err := injectCandidateFault(in.FaultInject, "handler"); err != nil {
		return failBeforeTransfer(err)
	}
	// The frontends fall back to the configured default route. The executor
	// applies the model aliases to it exactly as it applies them to a client
	// selector, so the route the executor runs is the effective default route
	// the operator check approved.
	route := cand.execution.defaultRoute
	authProviders := append([]httpauth.Provider(nil), cand.security.httpAuth...)
	genCtx, genCancel := context.WithCancel(context.Background())
	if cand.ledger != nil {
		cand.ledger.AddClose("openresponses-generation-lifecycle", PhaseQuiesce, func() error { genCancel(); return nil })
	}
	failWithGenCtx := func(err error) (GenerationRuntime, error) { genCancel(); return failBeforeTransfer(err) }
	adminHandler, err := bindGenerationRouteOverride(ps, frozen, cand.execution.executor, nowFn)
	if err != nil {
		return failWithGenCtx(err)
	}
	httpInput := buildStandardHTTPInput(genCtx, cand, frozen, regs, route)
	httpInput.Operations.RouteOverrideAdmin = adminHandler
	if err := injectCandidateFault(in.FaultInject, "composer-clone"); err != nil {
		return failWithGenCtx(fmt.Errorf("runtimebundle: composer config clone: %w", err))
	}
	composerCfg, err := freezeConfig(frozen)
	if err != nil {
		return failWithGenCtx(fmt.Errorf("runtimebundle: composer config clone: %w", err))
	}
	handler, err := composeStandardHTTPIsolated(ctx, in.Compose, composerCfg, ps.Logger, httpInput)
	if err != nil {
		return failWithGenCtx(fmt.Errorf("runtimebundle: compose request plane: %w", err))
	}
	if handler == nil {
		return failWithGenCtx(fmt.Errorf("runtimebundle: handler composer returned nil handler"))
	}
	if err := injectCandidateFault(in.FaultInject, "ledger-transfer"); err != nil {
		_ = cand.claimLifecycleLedger()
	}
	ledger := cand.transferLedgerOwnership()
	if ledger == nil {
		return failWithGenCtx(fmt.Errorf("runtimebundle: candidate resource ledger unavailable for transfer"))
	}
	// Defer the keep-warm metrics swap to PhasePublish: only an active,
	// published generation may retarget process metrics.
	if swap := featOut.HostProjections.MetricsSwap; swap != nil {
		ledger.AddAction("standard-features-metrics-publish", PhasePublish, func(context.Context) error {
			swap()
			return nil
		}, nil)
	}
	// The continuity store composes its retirement observers, so the
	// generation's cleanup never displaces the process-owned cleanup. The
	// registration waits for PhasePublish: an unpublished candidate must not
	// retarget the active callback.
	if retired, ok := cand.execution.executor.Store.(b2bua.ALegRetirementObserver); ok {
		executor := cand.execution.executor
		ledger.AddAction("aleg-retirement-observer-publish", PhasePublish, func(context.Context) error {
			cleanup := runtime.ALegRetirementCleanup(executor.PromptCacheMaintenance, executor.ConversationViewTagger)
			if keyed, ok := cand.execution.executor.Store.(b2bua.ALegRetirementObservers); ok {
				keyed.SetKeyedALegRetirementObserver(b2bua.RetirementObserverGeneration, cleanup)
			} else {
				retired.SetALegRetirementObserver(cleanup)
			}
			return nil
		}, nil)
	}
	bundle := newGenerationBundle(generationBundleInput{
		handler:             handler,
		executor:            cand.execution.executor,
		routing:             FrozenRoutingView{DefaultRoute: route, RoutePrefixes: append([]string(nil), cand.execution.routePrefixes...)},
		frontends:           frozen.Plugins.Frontends,
		registrations:       regs,
		httpAuth:            authProviders,
		models:              cand.models.registryRuntime,
		catalog:             cand.models.catalog,
		defaultModelBinding: cand.execution.defaultModelBinding,
		backendIDs:          backendIDsOf(cand.execution.executor),
		ledger:              ledger,
		terminalProviders:   terminalworkapp.SnapshotTerminalProviders(cand.operations.terminalRegistry),
		// Publish the facade-composed planes as the generation's canonical
		// frozen surface (Task 2.4, Requirement 8.3): facade-added/replaced
		// planes must be visible to request-time bundle readers.
		frozen:    featOut.Planes,
		readiness: cand.operations.readinessReport,
	})
	return bundle, nil
}

func composeStandardHTTPIsolated(ctx context.Context, compose HandlerComposer, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (handler http.Handler, err error) {
	defer func() {
		if p := recover(); p != nil {
			handler = nil
			err = fmt.Errorf("runtimebundle: compose panic: %s", configreload.SanitizePanicValue(p))
		}
	}()
	return compose(ctx, cfg, log, in)
}

func buildStandardHTTPInput(genCtx context.Context, cand *candidateAssembly, frozen *config.Config, regs []aipsdk.Registration, route string) httpcontract.StandardHTTPInput {
	// One defensive provider copy shared by the transport-auth chain and the
	// ingress self-defense credential probe, so the two can never answer from
	// different active provider sets.
	var authProviders []httpauth.Provider
	// The process logger is the mounted frontends' only diagnostics channel:
	// a frontend without one drops every internal failure record.
	var processLog *slog.Logger
	if cand != nil {
		authProviders = httpcontract.CloneHTTPAuthProviders(cand.security.httpAuth)
		if cand.process.ps != nil {
			processLog = cand.process.ps.Logger
		}
	}
	var (
		maxBody     int64
		preKA       aipsdk.FrontendKeepaliveConfig
		geoInput    httpcontract.GeoIPSecurityInput
		httpHeaders aipsdk.HTTPHeaders
		streamKA    time.Duration
	)
	if frozen != nil {
		maxBody = frozen.Server.EffectiveMaxRequestBodyBytes()
		ka := frozen.Server.EffectivePreRequestKeepalive()
		preKA = aipsdk.FrontendKeepaliveConfig{Enabled: ka.Enabled, Interval: ka.Interval}
		httpHeaders = frozen.HTTPHeaders.Effective()
		if eff, err := config.EffectiveStreamRecoveryAutoResume(frozen, config.StreamRecoveryOverrides{}); err == nil {
			streamKA = eff.KeepaliveInterval
		}
	}
	if cand != nil && cand.security.geoip != nil && cand.security.geoip.Policy() != nil {
		var geoObs httpcontract.GeoIPObserver
		if cand.process.metrics != nil {
			geoObs = cand.process.metrics.GeoIP
		}
		geoInput = httpcontract.GeoIPSecurityInput{
			Policy: cand.security.geoip.Policy(), Lookup: cand.process.geoip, Observer: geoObs,
			Resolver: httpcontract.GeoIPResolverConfig{
				Source:         cand.security.geoip.ClientIPSource(),
				TrustedProxies: cand.security.geoip.TrustedProxies(),
			},
		}
	}
	var plugins []config.PluginConfig
	if frozen != nil {
		plugins = frozen.Plugins.Frontends
	}
	keepwarmAdmin := cand.operations.hostProjections.KeepwarmAdmin
	var terminalPolicy httpcontract.TerminalDecisionPolicyInput
	if project := cand.operations.hostProjections.TerminalPolicyProjection; project != nil {
		terminalPolicy = project(cand.security.runtimeSnapshot, httpHeaders, maxBody, cand.security.secureSessionStore)
	}
	// Assign the narrow view only from a non-nil concrete executor: a nil
	// executor must stay a nil interface, not a non-nil view over a nil pointer.
	var executorView aipsdk.ExecutorView
	var concurrencyProvider authority.ConcurrencyProvider
	var routeTrace *diag.RouteTraceBuffer
	tokenAdmin := adminaccounting.AdaptCountCallService(cand.operations.tokenAccountingAdmin)
	if ex := cand.execution.executor; ex != nil {
		executorView, concurrencyProvider, routeTrace = ex, ex.ConcurrencyProvider, ex.RouteTrace
		if tokenAdmin == nil {
			tokenAdmin = adminaccounting.AdaptCountCallService(ex.AdminCountService)
		}
	}
	return httpcontract.StandardHTTPInput{
		Core: httpcontract.HTTPCoreInput{Executor: executorView},
		Security: httpcontract.HTTPSecurityInput{
			HTTPAuthProviders:    authProviders,
			SecureSessionStore:   cand.security.secureSessionStore,
			UsageAuthority:       adaptAccountingAuthorityQueries(cand.process.usageAuthority),
			ConcurrencyAuthority: cpadmin.AdaptConcurrencyAuthorityQueries(cand.process.concurrencyAuthority),
			GeoIP:                geoInput,
			// Projected independently of the GeoIP gate: self-defense reuses the
			// compiled client-IP trust configuration whether or not fixed
			// country/CIDR enforcement is active, and it shares the auth provider
			// slice with the transport-auth chain.
			SelfDefense: buildSelfDefenseSecurityInput(cand, frozen, authProviders, time.Now),
		},
		Operations: httpcontract.HTTPOperationsInput{
			AdminMounts:          adminMountsForGeneration(cand),
			Metrics:              cand.process.metrics,
			RouteTrace:           routeTrace,
			Store:                cand.process.store,
			SecretGuardInventory: cand.operations.secretGuardInventory,
			ControlPlaneQueries:  cpadmin.AdaptControlPlaneQueries(cand.process.controlPlaneQueries),
			ReadinessReport:      cpadmin.AdaptReadinessReport(cand.operations.readinessReport),
			ConcurrencyProvider:  concurrencyProvider,
			TokenAccountingAdmin: tokenAdmin,
			KeepwarmAdmin:        keepwarmAdmin, KeepwarmAdminEnabled: keepwarmAdmin.Enabled,
			ProxyMemory:            cand.operations.hostProjections.ProxyMemoryHTTP,
			Registrations:          httpcontract.CloneRegistrations(regs),
			TerminalDecisionPolicy: terminalPolicy,
		},
		Models: httpcontract.HTTPModelInput{
			CatalogRuntime: cand.models.catalog, ModelRegistryRuntime: cand.models.registryRuntime,
		},
		Frontends: httpcontract.HTTPFrontendInput{
			Executor:                  executorView,
			Registry:                  cand.process.pluginRegistry,
			DefaultRouteSelector:      route,
			RoutePrefixes:             httpcontract.CloneStrings(cand.execution.routePrefixes),
			Plugins:                   httpcontract.ClonePluginConfigs(plugins),
			MaxRequestBodyBytes:       maxBody,
			DecodeAdmission:           cand.execution.decodeAdmission,
			TrafficPorts:              httpcontract.TrafficPortsFromSnapshot(cand.security.runtimeSnapshot),
			PreRequestKeepalive:       preKA,
			HTTPHeaders:               httpHeaders,
			StreamKeepaliveInterval:   streamKA,
			GenerationContext:         genCtx,
			ContinuationWiringFactory: standardplugins.StandardContinuationWiringFactory(frozen),
			FrontendRouteClaims:       standardplugins.StandardFrontendRouteClaims(),
			LargePayload:              buildStandardLargePayloadConfig(cand, frozen),
			Log:                       processLog,
		},
	}
}

func injectCandidateFault(fi CandidateFaultInject, boundary string) error {
	if fi.After != boundary {
		return nil
	}
	if fi.Hook != nil {
		fi.Hook()
	}
	return fmt.Errorf("%w: after %s", ErrCandidateFaultInjected, boundary)
}

func validateTerminalDecisionProvider(provider terminaldecision.Provider) error {
	if provider != nil {
		_, err := terminaldecision.ProviderIdentity(provider)
		return err
	}
	return nil
}

// generationCandidate is the compiled candidate plus the feature-surface
// outputs the HTTP request plane is composed from.
type generationCandidate struct {
	cand    *candidateAssembly
	featOut featurehost.GenerationOutput
	frozen  *config.Config
	regs    []aipsdk.Registration
	nowFn   func() time.Time
}

// compileGenerationCandidate is the single candidate compile: it freezes the
// candidate config, merges the feature surface, compiles the standard feature
// host for this generation, and assembles the candidate from those outputs.
// CompileGeneration composes the request plane on top; CompileCandidate stops
// here. explicitCandidate selects reload-candidate validation (see
// buildContext.ExplicitCandidate) when the caller supplied no candidate.
func compileGenerationCandidate(ctx context.Context, in GenerationCompileInput, explicitCandidate bool) (generationCandidate, error) {
	if in.Process == nil {
		return generationCandidate{}, fmt.Errorf("runtimebundle: nil ProcessServices")
	}
	ps := in.Process
	if ps.Closed() {
		return generationCandidate{}, fmt.Errorf("runtimebundle: ProcessServices is closed")
	}
	src := in.Candidate
	if src == nil {
		if ps.cfg == nil {
			return generationCandidate{}, fmt.Errorf("runtimebundle: nil candidate config")
		}
		src = ps.cfg
	}
	// Structural guard for every compile path: a candidate that requires
	// accounting-authority evidence needs the captured port to receive it.
	if err := validateRequiredAuthorityEvidenceWiring(src, ps.UsageAuthority); err != nil {
		return generationCandidate{}, err
	}
	frozen, err := freezeConfig(src)
	if err != nil {
		return generationCandidate{}, err
	}
	// Money is process-owned and inherited by every candidate. Check before any
	// generation/HTTP composition, including reload; admin mounts are not evidence.
	if ps.opts != nil && billingCompositionConfigured(ps.opts.Production) {
		if err := standardplugins.ValidateBilledFrontendDeployment(frozen); err != nil {
			return generationCandidate{}, fmt.Errorf("runtimebundle: %w", err)
		}
	}
	var pluginReg *pluginreg.Registry
	if ps.opts != nil {
		pluginReg = ps.opts.PluginRegistry
	}
	if err := validateCandidateManifestOwnership(frozen, pluginReg); err != nil {
		return generationCandidate{}, err
	}
	regs := freezeRegistrations(config.RegistrationsFromConfig(frozen))
	genRunner, boundClient, boundPoller, err := newGenerationAuxiliaryRunner(ps)
	if err != nil {
		return generationCandidate{}, err
	}
	genMerged, err := mergeProcessFeatureSurface(ps, ps.FactoryCatalog, regs)
	if err != nil {
		return generationCandidate{}, err
	}
	if in.CandidateOpts != nil && !in.CandidateOpts.FeaturePlanes.IsZero() {
		genMerged, err = genMerged.MergeCandidatePlanes(in.CandidateOpts.FeaturePlanes)
		if err != nil {
			return generationCandidate{}, fmt.Errorf("runtimebundle: candidate feature planes: %w", err)
		}
	}
	accessMode, err := frozen.EffectiveAccessMode()
	if err != nil {
		return generationCandidate{}, err
	}
	genMerged.Lifecycles = slices.Clone(genMerged.Lifecycles)
	if in.CandidateOpts != nil {
		genMerged.Lifecycles = append(genMerged.Lifecycles, in.CandidateOpts.FeatureLifecycles...)
	}
	nowFn := time.Now
	if ps.opts != nil && ps.opts.Testing.Clock != nil {
		nowFn = ps.opts.Testing.Clock
	}
	var kwAccounting billingport.ProviderMaintenanceUsageObserver
	if ps.opts != nil {
		kwAccounting = ps.opts.Production.MaintenanceAccounting
	}
	if in.CandidateOpts != nil && in.CandidateOpts.Production.MaintenanceAccounting != nil {
		kwAccounting = in.CandidateOpts.Production.MaintenanceAccounting
	}
	var genHostRegs []sdkfeaturehost.Registration
	if in.CandidateOpts != nil && len(in.CandidateOpts.Production.FeatureHostRegistrations) > 0 {
		genHostRegs = in.CandidateOpts.Production.FeatureHostRegistrations
	} else if ps.opts != nil && len(ps.opts.Production.FeatureHostRegistrations) > 0 {
		genHostRegs = ps.opts.Production.FeatureHostRegistrations
	} else if ps.opts != nil && len(ps.opts.Testing.FeatureHostRegistrations) > 0 {
		genHostRegs = ps.opts.Testing.FeatureHostRegistrations
	}
	var convObserver conversationview.Observer
	if ps.Metrics != nil && ps.Metrics.ConversationViewObserver() != nil {
		convObserver = metricsObserverAdapter{ConversationViewObserver: ps.Metrics.ConversationViewObserver()}
	}
	featOut, targetCompiler, err := compileStandardFeatures(ctx, ps.StandardFeatures, featurehost.GenerationInput{
		Registrations:        regs,
		HostRegistrations:    genHostRegs,
		MergeSurface:         genMerged,
		Planes:               genMerged.Frozen,
		Lifecycles:           slices.Clone(genMerged.Lifecycles),
		BackgroundClient:     boundClient,
		BackgroundPoller:     boundPoller,
		AccessMode:           accessMode,
		ConfigInterleaved:    frozen.Interleaved,
		ConfigDir:            frozen.ConfigDir,
		NowFn:                nowFn,
		KeepwarmAccounting:   kwAccounting,
		ConversationObserver: convObserver,
	})
	if err != nil {
		return generationCandidate{}, err
	}
	toolReactorErrorPolicy := config.ParseToolReactorErrorPolicy(frozen.Hooks.ToolReactorErrorPolicy)
	bus := in.Bus
	if bus == nil {
		bus = hooks.New(aipfeature.ProjectHookConfig(featOut.Planes, toolReactorErrorPolicy))
	}
	candidateCfg := frozen
	if !explicitCandidate && in.Candidate == nil {
		candidateCfg = nil
	}
	cand, err := compileCandidate(ctx, GenerationCompileInput{
		Process: ps, Bus: bus, Candidate: candidateCfg,
		FeatureMetadata: featOut.HostProjections.FeatureMetadata,
		CandidateOpts: &BuildOptions{
			FeatureLifecycles:       featOut.Lifecycles,
			FeaturePlanes:           featOut.Planes,
			CorePorts:               featOut.CorePorts,
			HostProjections:         featOut.HostProjections,
			ReplaceCandidateSurface: true,
		},
		LiveFactoryKinds: in.LiveFactoryKinds,
		FaultInject:      in.FaultInject,
		GenerationRunner: genRunner,
	})
	if err != nil {
		return generationCandidate{}, err
	}
	targetCompiler.bind(cand.execution.executor)
	return generationCandidate{cand: cand, featOut: featOut, frozen: frozen, regs: regs, nowFn: nowFn}, nil
}

// mergeProcessFeatureSurface merges the enabled feature bundles for regs with
// the process's host contributions (traffic and usage observers).
func mergeProcessFeatureSurface(ps *ProcessServices, catalog featurebundle.FeatureBundleRegistry, regs []aipsdk.Registration) (featurebundle.GeneratedMergeSurface, error) {
	var host featurebundle.HostContributions
	if ps != nil && ps.opts != nil {
		host = featurebundle.HostContributions{TrafficObservers: slices.Clone(ps.opts.Production.TrafficObservers), UsageObservers: slices.Clone(ps.opts.Production.UsageObservers)}
	}
	merged, err := featurebundle.MergeFeatureSurfacesWithHost(catalog, regs, host)
	if err != nil {
		return featurebundle.GeneratedMergeSurface{}, fmt.Errorf("runtimebundle: feature surface: %w", err)
	}
	return merged, nil
}
