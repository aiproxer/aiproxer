package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/metrics"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/decodeqos"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	sdkfeaturehost "github.com/aiproxer/aiproxer/pkg/aipsdk/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// NewProcessServices constructs process-owned stores, pools, metrics, terminal-work,
// limiters, and related dependencies once. Closers are registered immediately;
// partial failures dispose acquired resources in reverse order.
func NewProcessServices(ctx context.Context, in ProcessServicesInput) (*ProcessServices, error) {
	releasePluginOwnership := func() {
		if in.PluginResourcePool != nil {
			_ = in.PluginResourcePool.Close()
			in.PluginResourcePool = nil
		}
		if in.PluginHost != nil {
			_ = in.PluginHost.Close()
			in.PluginHost = nil
		}
		for _, a := range in.PluginArtifacts {
			_ = a.Close()
		}
		in.PluginArtifacts = nil
		if dir := strings.TrimSpace(in.PluginStagingDir); dir != "" {
			_ = removeAllRetry(dir, 8, 25*time.Millisecond)
			in.PluginStagingDir = ""
		}
	}
	reject := func(err error) (*ProcessServices, error) {
		releaseProcessInputOwnership(&in, releasePluginOwnership)
		return nil, err
	}
	if in.Cfg == nil {
		return reject(fmt.Errorf("runtimebundle: nil config"))
	}
	if in.Log == nil {
		return reject(fmt.Errorf("runtimebundle: nil logger"))
	}
	if in.Opts == nil || in.Opts.PluginRegistry == nil {
		return reject(fmt.Errorf("runtimebundle: nil PluginRegistry"))
	}
	if isNilUsageLimitPort(in.Opts.Production.UsageLimits) && in.Opts.Production.UsageLimits != nil {
		return reject(fmt.Errorf("runtimebundle: Production.UsageLimits is typed-nil"))
	}
	if err := validateRequiredAuthorityEvidenceWiring(in.Cfg, in.Opts.Production.UsageLimits); err != nil {
		return reject(err)
	}

	parent := ctx
	if in.Opts.Startup.StartupContext != nil {
		parent = in.Opts.Startup.StartupContext
	} else if parent == nil {
		parent = context.Background()
	}

	ps := &ProcessServices{
		Logger:         in.Log,
		FactoryCatalog: in.Opts.PluginRegistry,
		Tracing:        in.Tracing,
		cfg:            in.Cfg,
		opts:           in.Opts,
		hostEnv:        in.HostEnv,
	}

	register := func(c func() error) {
		if c != nil {
			ps.closers = append(ps.closers, c)
		}
	}
	adoptBackgroundAuxAndDetector(parent, &in, ps, register)
	owner := &processResourceOwner{register: register}
	fail := func(err error) (*ProcessServices, error) {
		return nil, withDisposedClosers(err, ps.closers)
	}
	// Ingress self-defense adaptive state is process-owned and startup-sized, so
	// it is configured before the GeoIP process service: an invalid self-defense
	// configuration then fails startup through the same rollback path that
	// disposes every resource acquired so far.
	if err := configureSelfDefenseProcessService(ps, in.Cfg); err != nil {
		return fail(err)
	}
	if err := configureGeoIPProcessService(parent, ps, in.Cfg, register); err != nil {
		return fail(err)
	}

	// Process-owned pool/host/artifacts/staging: register in acquisition order
	// (staging → artifacts → host → pool) so reverse disposal keeps the pool
	// ahead of Host.Close and artifact/staging teardown. Staging removal must
	// run only after VerifiedArtifact handles are closed so Windows can delete
	// the staged executables.
	if dir := strings.TrimSpace(in.PluginStagingDir); dir != "" {
		stagingDir := dir
		register(func() error {
			return removeAllRetry(stagingDir, 8, 25*time.Millisecond)
		})
		in.PluginStagingDir = ""
	}
	if arts := in.PluginArtifacts; len(arts) > 0 {
		artifacts := make([]*trust.VerifiedArtifact, 0, len(arts))
		artifacts = append(artifacts, arts...)
		register(func() error {
			var out error
			for _, a := range artifacts {
				out = errors.Join(out, a.Close())
			}
			return out
		})
		in.PluginArtifacts = nil
	}
	if in.PluginHost != nil {
		pluginHost := in.PluginHost
		register(pluginHost.Close)
		in.PluginHost = nil
	}
	if in.PluginResourcePool != nil {
		resourcePool := in.PluginResourcePool
		register(resourcePool.Close)
		in.PluginResourcePool = nil
	}

	// Discovery/trust catalog is process-owned and startup-fixed (req 7.3, 8.7).
	ps.FactoryCatalog.FreezeDiscovery()
	if ps.Tracing.Shutdown == nil {
		ps.Tracing.Shutdown = func(context.Context) error { return nil }
	}

	postgresPools := db.NewPoolRegistry(in.Opts.Testing.PostgresPoolOpener)
	ps.DatabasePools = postgresPools
	ps.dualPlaneMigrator = newDualPlaneMigrator(in.Cfg)
	poolsClaimed := false
	defer func() {
		if !poolsClaimed && postgresPools != nil {
			_ = postgresPools.Close(parent)
		}
	}()

	controlPlane, err := buildControlPlaneRuntime(controlPlaneBuildInput{
		StartupContext:    parent,
		Cfg:               in.Cfg,
		Log:               in.Log,
		Clock:             in.Opts.Testing.Clock,
		StoreOverride:     in.Opts.Testing.ControlPlaneStoreOverride,
		PostgresPools:     postgresPools,
		DualPlaneMigrator: ps.dualPlaneMigrator,
	})
	if err != nil {
		return fail(err)
	}
	ps.controlPlane = controlPlane
	if controlPlane != nil {
		register(controlPlane.closer)
	}
	if err := configureExternalBillingLifecycle(owner, in.Opts); err != nil {
		return fail(err)
	}
	policyObs := assemblePolicyObserverChain(in.Opts, controlPlane)
	ps.policyObs = policyObs

	// The usage-limit port is captured once from the caller-owned production
	// options. The runtime opens, migrates, seeds, probes and closes nothing
	// authority-related; the port is borrowed, never registered as a closer.
	ps.UsageAuthority = in.Opts.Production.UsageLimits

	concurrencyRT, err := buildConcurrencyAuthorityRuntime(owner, parent, in.Cfg, in.Opts.Testing, postgresPools, ps.dualPlaneMigrator)
	if err != nil {
		return fail(err)
	}
	ps.concurrencyRT = concurrencyRT
	if concurrencyRT != nil {
		ps.Concurrency = concurrencyRT.Service
	}

	ps.Metrics = buildProcessMetricsBundle(in.Cfg, postgresPools.Stats, ps.IngressDefense)
	// Startup-only registration runs once before publication; a failure
	// discards this process and reloads reuse ps.Metrics without re-running it.
	if err := registerStartupMetricsCollectors(in.Opts.Production.RegisterStartupMetrics, ps.Metrics); err != nil {
		return fail(err)
	}
	if ps.Metrics != nil {
		// The metrics bundle owns the process-lifetime launch chronology; the
		// process container exposes the same instance to composition/tests.
		ps.SystemPromptLaunchSeq = ps.Metrics.SystemPromptLaunchSeq
		// Collapse thresholds are startup-fixed: CompilePromptCacheCollapse
		// already ran in config validation, and a reload that changes any
		// effective threshold is rejected as restart-required, so the
		// process-owned episode history below is never silently reset.
		compiled, err := config.CompilePromptCacheCollapse(in.Cfg.Observability.Metrics.PromptCacheCollapse)
		if err != nil {
			return fail(err)
		}
		ps.Metrics.PromptCacheCollapse = metrics.NewPromptCacheCollapseWithSettings(ps.Metrics.PromptCacheCollapseProm, in.Log, in.Opts.Testing.Clock, metrics.PromptCacheCollapseSettings{
			MinPromptTokens: compiled.MinPromptTokens(),
			HealthyRatio:    compiled.HealthyRatio(),
			ColdRatio:       compiled.ColdRatio(),
			ConsecutiveCold: compiled.ConsecutiveCold(),
			MaxTrackedKeys:  compiled.MaxTrackedKeys(),
			IdleExpiry:      compiled.IdleExpiry(),
		})
		ps.Metrics.PromptCacheWriteChurn = metrics.NewPromptCacheWriteChurn(ps.Metrics.PromptCacheWriteChurnProm, in.Log, in.Opts.Testing.Clock)
	}
	// The seam-digest HMAC key is process-stable: generated once here into
	// the metrics bundle that owns the collapse diagnostic, borrowed by
	// every generation executor, so digests stay stable across rebuilds. A
	// randomness failure fails startup; the key is never logged, persisted,
	// or exposed. A nil bundle (metrics disabled) carries no key, unused
	// because the feed gate requires metrics.
	if ps.Metrics != nil {
		seamKey, err := runtime.NewSeamDigestKey()
		if err != nil {
			return fail(fmt.Errorf("runtimebundle: seam digest key: %w", err))
		}
		ps.Metrics.SetSeamDigestKey(seamKey)
	}
	if ps.Metrics != nil && ps.Metrics.GeoIP != nil && ps.GeoIP != nil {
		status := ps.GeoIP.Status()
		ps.Metrics.GeoIP.SetReady(status.Ready)
		if !status.UpdatedAt.IsZero() {
			ps.Metrics.GeoIP.SetAge(time.Since(status.UpdatedAt))
		}
	}
	// Optional post-construction surfaces are attached once, here, before any
	// authority operation and before generation publication. Setters run only
	// when the port implements them; a nil sink is never attached.
	if ps.UsageAuthority != nil {
		if setter, ok := ps.UsageAuthority.(usagelimit.StageMetricsSetter); ok && ps.Metrics != nil {
			setter.SetStageMetrics(ps.Metrics.AuthorityStageSink())
		}
		if setter, ok := ps.UsageAuthority.(usagelimit.EvidenceSinkSetter); ok {
			if sink := buildAuthorityEvidenceSink(controlPlane, policyObs, in.Opts); sink != nil {
				setter.SetEvidenceSink(sink)
			}
		}
	}

	bctx := buildContext{
		Cfg:               in.Cfg,
		Bus:               nil, // generation-owned; unused by persistence
		Log:               in.Log,
		Opts:              in.Opts,
		Parent:            parent,
		PostgresPools:     postgresPools,
		DualPlaneMigrator: ps.dualPlaneMigrator,
	}
	if ps.persistence, err = buildPersistenceRuntime(owner, bctx, controlPlane, ps.Metrics); err != nil {
		return fail(err)
	}
	persist := ps.persistence
	if persist != nil {
		ps.Continuity = persist.Store
		ps.RouteOverrideStore = persist.OverrideStore
		if persist.SecureSession != nil {
			ps.SecureSessions = persist.SecureSession.appStore
		}
	}
	if in.Cfg.Routing.OverrideAdmin.Enabled && ps.RouteOverrideStore == nil {
		return fail(fmt.Errorf("runtimebundle: routing.override_admin.enabled requires a continuity store that implements routeoverride.Store"))
	}

	nowFn := in.Opts.Testing.Clock
	if nowFn == nil {
		nowFn = time.Now
	}

	if ps.accountingStores, err = buildProcessAccountingStores(parent, in.Cfg, nowFn); err != nil {
		return fail(err)
	}
	// The observation writer is captured once per process. An explicit external
	// pair is the process's only observation writer: the configuration-backed
	// journal is not constructed, and the borrowed pair is fenced by the
	// process-owned producer fence before any producer can reach it. Every
	// other selection keeps the existing configuration-backed behavior exactly.
	if in.Opts.Production.MeteringObservationWriterSelection == coremetering.ObservationWriterExternal {
		if err := ps.installBorrowedObservationPair(in.Opts); err != nil {
			return fail(err)
		}
	} else {
		if ps.meteringRT, err = buildMeteringRuntime(owner, parent, in.Cfg, nowFn, postgresPools, ps.dualPlaneMigrator); err != nil {
			return fail(err)
		}
		if ps.meteringRT != nil {
			ps.MeteringRecorder = ps.meteringRT.Recorder
		}
	}

	shared := buildSharedMutableRuntime(in.Cfg, nowFn)
	if err := bindSharedMutableProcessServices(parent, ps, shared); err != nil {
		return fail(fmt.Errorf("runtimebundle: branch coordinator: %w", err))
	}

	var hostRegs []sdkfeaturehost.Registration
	if in.Opts != nil {
		hostRegs = append(append(hostRegs, in.Opts.Production.FeatureHostRegistrations...), in.Opts.Testing.FeatureHostRegistrations...)
	}
	var metricsRegistry featurehost.MetricsRegistry // generic Prometheus registry contribution
	if ps.Metrics != nil && ps.Metrics.Registry != nil {
		metricsRegistry = ps.Metrics.Registry
	}
	if ps.StandardFeatures, err = featurehost.NewProcess(parent, featurehost.ProcessInput{
		Logger:            in.Log,
		ExtensionState:    ps.ExtensionState,
		BackgroundAux:     ps.BackgroundAux,
		ContinuityStore:   ps.Continuity,
		BunDB:             borrowContinuityDB(ps.Continuity),
		HostRegistrations: hostRegs,
		HostEnv:           in.HostEnv,
		MetricsRegistry:   metricsRegistry,
		FeatureDatabases: db.NewFeatureDatabaseOpener(db.FeatureDatabaseConfig{
			Store:       config.EffectiveContinuityStore(in.Cfg.Continuity),
			SQLitePath:  in.Cfg.Continuity.SQLitePath,
			PostgresDSN: in.Cfg.Continuity.PostgresDSN,
			Pool:        db.PoolSettings{},
		}),
	}); err != nil {
		return fail(fmt.Errorf("runtimebundle: standard features host: %w", err))
	}
	register(ps.StandardFeatures.Close)

	// Snapshot binder before terminal workers so IntentService/reconciler receive
	// executable pending ownership without a post-start setter race.
	ps.SnapshotGeneration, ps.SnapshotController = buildSnapshotGeneration(in.Cfg, in.Opts.Testing, in.Opts.Production)

	if ps.terminalWorkRT, err = buildTerminalWorkWithSetReconcile(owner, parent, in.Opts.Production, nowFn, ps.Metrics, ps.Concurrency, ps.SnapshotGeneration); err != nil {
		return fail(err)
	}
	twRT := ps.terminalWorkRT
	if twRT != nil {
		ps.TerminalWorkProcessor = twRT.Processor
		ps.TerminalWorkRegistry = twRT.Registry
		ps.TerminalWorkQueries = twRT.Queries
		ps.TerminalWorkMetrics = twRT.Metrics
	}

	ps.DecodeAdmission = decodeqos.New(in.Cfg.Server.EffectiveMaxConcurrentDecodes(), in.Cfg.Server.EffectiveMaxInflightDecodeBytes())
	ps.MeteringQuerier = in.Opts.Production.MeteringQuerier

	if ps.SpoolLedger, err = buildProcessSpoolLedger(in.Cfg, ps.Metrics); err != nil {
		return fail(err)
	}

	// One-time prune after all process-owned Open/Claim paths complete. Candidate
	// compilation must remain read-only with respect to the process pool registry.
	if err := postgresPools.PruneUnclaimed(); err != nil {
		return fail(fmt.Errorf("runtimebundle: prune unclaimed postgres pools: %w", err))
	}

	// ProcessServices owns the pool registry on every successful return. Close
	// disposes it after dependent process resources; empty registries are cheap.
	poolsClaimed = true

	// Tracing shutdown remains with the process host (Host.ShutdownTracing /
	// stdhttp). ProcessServices retains the non-owning handle for identity reuse only.

	return ps, nil
}

// installBorrowedObservationPair installs the explicitly borrowed external
// recorder/sink pair as the process's only observation writer.
//
// The pair is borrowed: the process never closes it and never opens a second
// journal. The borrowed sink is wrapped in the process-owned accepted-producer
// fence before any producer is published, so the process can prove that no
// producer is still appending when it tears the borrowed store down. The
// fence keeps the late-economics capability when the borrowed sink declares
// it, so wrapping never removes a capability the owner provided.
func (ps *ProcessServices) installBorrowedObservationPair(opts *BuildOptions) error {
	if opts == nil {
		return fmt.Errorf("runtimebundle: %w: the external observation writer requires build options", coremetering.ErrObservationWriterConflict)
	}
	recorder := opts.Production.MeteringRecorder
	sink := opts.Production.MeteringObservationSink
	if recorder == nil || coremetering.IsNilObservationPort(recorder) {
		return fmt.Errorf("runtimebundle: %w: the external observation writer requires a recorder", coremetering.ErrObservationWriterConflict)
	}
	if sink == nil || coremetering.IsNilObservationPort(sink) {
		return fmt.Errorf("runtimebundle: %w: the external observation writer requires an atomic observation sink", coremetering.ErrObservationWriterConflict)
	}
	// The recorder and the sink must belong to the same durable journal. The
	// pair is proven by the journal identity both halves declare, so two
	// individually valid ports over two different journals are refused before
	// the borrowed pair is installed and before any producer is published.
	if err := coremetering.ValidateObservationWriterPair(recorder, sink); err != nil {
		return fmt.Errorf("runtimebundle: %w", err)
	}
	fence := coremetering.NewObservationProducerFence(sink)
	var exposed metering.AtomicObservationSink = fence
	if economic := coremetering.NewEconomicObservationProducerFence(sink); economic != nil {
		fence = economic
		exposed = economic
	}
	if fence == nil || exposed == nil {
		return fmt.Errorf("runtimebundle: %w: the external observation writer requires a usable atomic observation sink", coremetering.ErrObservationWriterConflict)
	}
	ps.observationFence = fence
	ps.meteringRT = &meteringRuntime{Recorder: recorder, ObservationSink: exposed, StoreBacking: "borrowed"}
	ps.MeteringRecorder = recorder
	// Every generation of this process borrows the fenced sink, never the raw
	// borrowed sink, so the producer fence covers every producer.
	opts.Production.MeteringObservationSink = exposed
	return nil
}

// DrainObservationProducers closes observation admission and waits for every
// accepted append to complete. It runs before any process closer, so no
// producer can still append into a store the process is about to close. A
// process without an observation producer fence is already drained.
func (ps *ProcessServices) DrainObservationProducers(ctx context.Context) error {
	if ps == nil || ps.observationFence == nil {
		return nil
	}
	return ps.observationFence.Drain(ctx)
}

// Close disposes process-owned resources in reverse acquisition order.
// It is idempotent.
func (ps *ProcessServices) Close() error {
	if ps == nil {
		return nil
	}
	// The accepted-producer fence is observed before any closer runs: a
	// producer that is still appending must not have its store closed
	// underneath it. A failed drain leaves every closer untouched, so a later
	// Close retries the same drain.
	if err := ps.DrainObservationProducers(context.Background()); err != nil {
		return fmt.Errorf("runtimebundle: drain observation producers: %w", err)
	}
	ps.closeOnce.Do(func() {
		ps.closeErr = disposeClosers(ps.closers)
		if ps.DatabasePools != nil {
			if err := ps.DatabasePools.Close(context.Background()); err != nil {
				ps.closeErr = errors.Join(ps.closeErr, fmt.Errorf("runtimebundle: close database pools: %w", err))
			}
		}
		ps.closed.Store(true)
	})
	return ps.closeErr
}

// Closed reports whether [ProcessServices.Close] has completed.
func (ps *ProcessServices) Closed() bool {
	if ps == nil {
		return true
	}
	return ps.closed.Load()
}
