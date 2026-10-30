package runtimebundle

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/configreload"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/auxiliary"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	aipstate "github.com/aiproxer/aiproxer/pkg/aipsdk/state"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

type GenerationCompileInput struct {
	Process          *ProcessServices
	Bus              *hooks.Bus
	Candidate        *config.Config
	CandidateOpts    *BuildOptions
	FeatureMetadata  []diag.InventoryFeatureMetadata
	Compose          HandlerComposer
	LiveFactoryKinds map[string]int
	FaultInject      CandidateFaultInject
	GenerationRunner *auxiliary.GenerationExecutorRunner
}

func compileCandidate(ctx context.Context, in GenerationCompileInput) (*candidateAssembly, error) {
	if in.Process == nil {
		return nil, fmt.Errorf("runtimebundle: nil ProcessServices")
	}
	ps := in.Process
	if ps.Closed() {
		return nil, fmt.Errorf("runtimebundle: ProcessServices is closed")
	}
	cfg := in.Candidate
	if cfg == nil {
		cfg = ps.cfg
	}
	opts := mergeCandidateBuildOptions(ps.opts, in.CandidateOpts)
	log := ps.Logger
	if cfg == nil || opts == nil || opts.PluginRegistry == nil {
		return nil, fmt.Errorf("runtimebundle: ProcessServices missing config or PluginRegistry")
	}
	rawCandidate := cfg
	prepared, err := standardplugins.PrepareProviderProfiles(cfg)
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: provider profiles: %w", err)
	}
	cfg = prepared.Config
	if in.Candidate != nil && ps.cfg != nil && rawCandidate != ps.cfg {
		if _, err := configreload.Classify(ps.cfg, rawCandidate); err != nil {
			return nil, err
		}
	}
	if opts.FeaturePlanes.IsZero() {
		// Prebuilt-lifecycle test seam (see CompileCandidate): planes still come
		// from the process feature surface.
		genMerged, err := mergeProcessFeatureSurface(ps, cmp.Or(ps.FactoryCatalog, opts.PluginRegistry), config.RegistrationsFromConfig(cfg))
		if err != nil {
			return nil, err
		}
		opts.FeaturePlanes = genMerged.Frozen
		opts.FeatureLifecycles = prependGeneratedLifecycles(genMerged.Lifecycles, opts.FeatureLifecycles)
		if len(in.FeatureMetadata) == 0 {
			in.FeatureMetadata = featurehost.InventoryMetadataFromMergeSurface(genMerged)
		}
	}
	if err := validateCandidateManifestOwnership(cfg, opts.PluginRegistry); err != nil {
		return nil, err
	}
	compiledGeoIP, err := config.CompileGeoIP(cfg.Access.GeoIP)
	if err != nil {
		return nil, err
	}
	compiledSelfDefense, err := config.CompileSelfDefense(cfg.Access.SelfDefense)
	if err != nil {
		return nil, err
	}
	if compiledGeoIP.Enabled() && compiledGeoIP.Policy() != nil && compiledGeoIP.Policy().NeedsCountryLookup() &&
		(ps.GeoIP == nil || !ps.GeoIP.Ready()) {
		return nil, fmt.Errorf("runtimebundle: GeoIP country lookup is not ready")
	}
	if err := ClassifyFeatureLifecycles(opts.FeatureLifecycles); err != nil {
		return nil, err
	}
	if err := ClassifyBackendOverlap(opts.PluginRegistry, cfg, in.LiveFactoryKinds); err != nil {
		return nil, err
	}
	bus := in.Bus
	if bus == nil {
		bus = hooks.New(hooks.Config{})
	}
	parent := cmp.Or(opts.Startup.StartupContext, ctx)
	if parent == nil {
		return nil, fmt.Errorf("runtimebundle: nil compile context")
	}
	ledger := NewResourceLedger()
	bctx := buildContext{
		Cfg:               cfg,
		Bus:               bus,
		Log:               log,
		Opts:              opts,
		Parent:            parent,
		PostgresPools:     ps.DatabasePools,
		DualPlaneMigrator: ps.dualPlaneMigrator,
		Ledger:            ledger,
		HostEnv:           ps.hostEnv,
		ExplicitCandidate: in.Candidate != nil,
	}
	fail := func(err error) error {
		if rollErr := ledger.Rollback(parent); rollErr != nil {
			return errors.Join(err, rollErr)
		}
		return err
	}
	injectFault := func(boundary string) error {
		if in.FaultInject.After != boundary {
			return nil
		}
		if in.FaultInject.Hook != nil {
			in.FaultInject.Hook()
		}
		return fmt.Errorf("%w: after %s", ErrCandidateFaultInjected, boundary)
	}
	sec, err := buildSecurityRuntime(bctx, ps.controlPlane)
	if err != nil {
		return nil, fail(err)
	}
	// The secret-guard execution posture arrives as ordinary planes; the snapshot
	// plane and inventory read model derive via plane access and featurehost.
	sgPlane, secretGuard := secretGuardFromPlanes(opts.FeaturePlanes)
	featureMetadata := featurehost.InventoryMetadataForConfig(cfg, in.FeatureMetadata, secretGuard)
	var sgInventory *diag.InventoryExtras
	if len(featureMetadata) > 0 {
		sgInventory = &diag.InventoryExtras{FeatureMetadata: featureMetadata}
	}
	obs := buildGenerationObservability(bctx, ps.Metrics)
	model, err := buildModelRuntime(bctx, obs.Upstream, prepared)
	if err != nil {
		return nil, fail(err)
	}
	if err := injectFault("model"); err != nil {
		return nil, fail(err)
	}
	nowFn := time.Now
	if opts.Testing.Clock != nil {
		nowFn = opts.Testing.Clock
	}
	var exec *runtime.Executor
	var extState aipstate.Store
	if ps.sharedMutable != nil {
		extState = ps.sharedMutable.ExtensionState
	}
	var cacheDiagnostic usage.Observer
	if ps.Metrics != nil {
		// Both cache-economics diagnostics are advisory observers on the
		// same usage seam; the chain preserves order and skips nils.
		cacheDiagnostic = usage.ChainObservers(ps.Metrics.PromptCacheCollapse, ps.Metrics.PromptCacheWriteChurn)
	}
	ext := buildExtensionRuntime(bctx, nowFn, func() auxreq.ExecutorRunner { return exec }, ps.controlPlane, ps.policyObs, sgPlane, extState, cacheDiagnostic)
	backendIDs, err := BackendStateIdentitiesFromConfig(cfg)
	if err != nil {
		return nil, fail(err)
	}
	corePorts := opts.CorePorts
	corePorts.CompactionDetector = ps.StandardFeatures.CompactionDetector()
	corePorts.ConversationViewReader, corePorts.ConversationReaderStockOrigin = resolveCandidateConvReader(corePorts, ps.StandardFeatures)
	execRun, err := buildExecutorRuntime(executorBuildInput{
		Bctx:                bctx,
		Ledger:              ledger,
		NowFn:               nowFn,
		Ext:                 ext,
		Model:               model,
		Persistence:         ps.persistence,
		Security:            sec,
		Observability:       &obs,
		ControlPlane:        ps.controlPlane,
		UsageAuthority:      ps.UsageAuthority,
		Concurrency:         ps.concurrencyRT,
		SnapshotGeneration:  ps.SnapshotGeneration,
		TerminalWork:        ps.terminalWorkRT,
		SharedMutable:       ps.sharedMutable,
		AccountingStores:    ps.accountingStores,
		Metering:            ps.meteringRT,
		BackendIdentities:   backendIDs,
		CorePorts:           corePorts,
		BackgroundScheduler: ps.BackgroundAux,
		GenerationRunner:    in.GenerationRunner,
		ConversationStore:   ps.StandardFeatures.ConversationStore(),
	})
	if err != nil {
		return nil, fail(err)
	}
	if err := AdaptOverlapSafeLifecycles(ledger, opts.FeatureLifecycles); err != nil {
		return nil, fail(err)
	}
	if err := injectFault("prepare"); err != nil {
		return nil, fail(err)
	}
	if err := ledger.Prepare(parent); err != nil {
		return nil, fail(err)
	}
	if err := injectFault("activate"); err != nil {
		return nil, fail(err)
	}
	if err := ledger.Activate(parent); err != nil {
		return nil, fail(err)
	}
	exec = execRun.Exec
	// A reloaded candidate is a new config generation: collapses whose healthy
	// sample predates it attribute to the generation change. The initial
	// process build (nil Candidate) establishes the baseline instead.
	if in.Candidate != nil && ps.Metrics != nil && ps.Metrics.PromptCacheCollapse != nil {
		ps.Metrics.PromptCacheCollapse.NoteConfigGeneration()
	}
	return &candidateAssembly{
		execution: candidateExecutionGroup{
			executor:            execRun.Exec,
			routePrefixes:       model.RoutePrefixes,
			defaultRoute:        execRun.DefaultRoute,
			defaultModelBinding: execRun.DefaultModelBinding,
			decodeAdmission:     ps.DecodeAdmission,
			upstreamHTTP:        obs.Upstream,
		},
		security: candidateSecurityGroup{
			httpAuth:           sec.HTTPAuth,
			secureSessionStore: execRun.SecureSessionStore,
			authEvents:         sec.AuthEvents,
			runtimeSnapshot:    ext.Snap,
			geoip:              compiledGeoIP,
			selfDefense:        compiledSelfDefense,
		},
		models: candidateModelGroup{
			catalog:         execRun.CatalogRuntime,
			registry:        model.Registry,
			registryRuntime: model.RegistryRuntime,
		},
		operations: candidateOperationsGroup{
			tokenAccountingAdmin: execRun.TokenAccountingAdmin,
			readinessReport:      execRun.ReadinessReport,
			secretGuardInventory: sgInventory,
			terminalRegistry:     ps.TerminalWorkRegistry,
			hostProjections:      opts.HostProjections,
		},
		process: candidateProcessRefs{
			ps:                   ps,
			store:                ps.Continuity,
			pluginRegistry:       ps.FactoryCatalog,
			metrics:              ps.Metrics,
			controlPlaneQueries:  ps.controlPlane.queriesHandle(),
			usageAuthority:       ps.UsageAuthority,
			concurrencyAuthority: ps.Concurrency,
			meteringQuerier:      ps.MeteringQuerier,
			geoip:                ps.GeoIP,
			ingressDefense:       ps.IngressDefense,
			spoolLedger:          ps.SpoolLedger,
		},
		ledger: ledger,
		terminalWorkReady: func() func(context.Context) error {
			if ps.terminalWorkRT != nil {
				return ps.terminalWorkRT.checkReady
			}
			return nil
		}(),
		terminalWorkRT: ps.terminalWorkRT,
	}, nil
}
