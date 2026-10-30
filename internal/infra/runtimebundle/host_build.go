package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/accessmode"
	"github.com/aiproxer/aiproxer/internal/core/config"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/internal/infra/logging"
	"github.com/aiproxer/aiproxer/internal/infra/osenv"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/infra/tracing"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

type BuildHostInput struct {
	ConfigPath              string
	Mandatory               []aipsdk.Requirement
	LogWriter               io.Writer
	StreamRecoveryOverrides config.StreamRecoveryOverrides
	HandlerComposer         HandlerComposer
	Production              ProductionOptions
	// EnterpriseCapabilities declares provided capabilities; nil fails closed.
	EnterpriseCapabilities  config.CapabilitySet
	EnforceMultiUserCLIGate bool
	MultiUser               *bool
	// RegistrySetup optionally configures plugins in reg before process services build.
	RegistrySetup func(reg *pluginreg.Registry) error
}

func BuildHost(ctx context.Context, in BuildHostInput) (*Host, error) {
	return buildHost(ctx, hostBuildInput(in), defaultHostBuildOps(), osenv.Process{})
}

// cloneCapabilitySet snapshots a caller capability map for the host lifetime.
func cloneCapabilitySet(in config.CapabilitySet) config.CapabilitySet {
	if len(in) == 0 {
		return nil
	}
	out := make(config.CapabilitySet, len(in))
	for name, declared := range in {
		if name = strings.TrimSpace(name); declared && name != "" {
			out[name] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

type hostBuildInput = BuildHostInput

func buildHost(ctx context.Context, in hostBuildInput, ops hostBuildOps, hostEnv featurehost.HostEnvironment) (builtHost *Host, buildErr error) {
	if ctx == nil {
		return nil, fmt.Errorf("runtimebundle: nil context")
	}
	if ops.load == nil || ops.tracing == nil || ops.process == nil || ops.compile == nil || ops.publisher == nil {
		return nil, fmt.Errorf("runtimebundle: incomplete host build operations")
	}
	path := strings.TrimSpace(in.ConfigPath)
	if path == "" {
		return nil, fmt.Errorf("runtimebundle: empty config path")
	}
	if in.HandlerComposer == nil {
		return nil, fmt.Errorf("runtimebundle: BuildHost requires HandlerComposer")
	}
	logOut := in.LogWriter
	if logOut == nil {
		logOut = os.Stdout
	}
	// One immutable capability snapshot at the ownership boundary: startup
	// validation and every reload read this copy, so caller mutation cannot
	// widen admission and concurrent reads never race the caller's map.
	capabilities := cloneCapabilitySet(in.EnterpriseCapabilities)
	// D0: a declared usage-limits capability requires a usable port. Typed-nil
	// is reported as typed-nil first; the pairing refusal otherwise. This is the
	// single chokepoint used by both pkg/aipruntime and cmd/aipstd.
	if capabilities.Has(config.CapabilityUsageLimits) {
		if in.Production.UsageLimits != nil && isNilUsageLimitPort(in.Production.UsageLimits) {
			return nil, fmt.Errorf("runtimebundle: Production.UsageLimits is typed-nil")
		}
		if isNilUsageLimitPort(in.Production.UsageLimits) {
			return nil, fmt.Errorf("runtimebundle: capability %q declared without an injected usage-limit port (Production.UsageLimits)", config.CapabilityUsageLimits)
		}
	}

	// The carried observation-writer declaration is checked before the
	// process acquires anything, so a conflicting writer never reaches an
	// opener, a recovery pass, a worker start, or a published generation.
	if err := validateObservationWriterDeclaration(in.Production); err != nil {
		return nil, err
	}

	effective, activeSource, sourceOwner, fixedStreamRecovery, err := ops.load(ctx, path, in.StreamRecoveryOverrides, capabilities)
	ownerTransferred := false
	defer func() {
		if !ownerTransferred && sourceOwner != nil {
			buildErr = errors.Join(buildErr, sourceOwner.Close(ctx))
			if buildErr != nil {
				builtHost = nil
			}
		}
	}()
	if err != nil {
		return nil, err
	}
	if effective == nil || effective.Config == nil {
		return nil, fmt.Errorf("runtimebundle: nil effective config")
	}
	cfg := effective.Config

	mode, err := cfg.EffectiveAccessMode()
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: access mode: %w", err)
	}
	if in.EnforceMultiUserCLIGate {
		if err := accessmode.ValidateServeModeGate(mode, in.MultiUser); err != nil {
			return nil, err
		}
	}

	traceRes, err := ops.tracing(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: tracing init: %w", err)
	}
	traceShutdown := traceRes.Shutdown
	shutTracing := func(ctx context.Context) error {
		if traceShutdown == nil {
			return nil
		}
		return traceShutdown(context.WithoutCancel(ctx))
	}

	logger, err := logging.NewLogger(cfg.Logging, logOut,
		logging.WithOTELTraceAttrs(cfg.Observability.Tracing.Enabled))
	if err != nil {
		return nil, joinInitialFailureCleanup(ctx, fmt.Errorf("runtimebundle: logger init: %w", err), nil, nil, shutTracing)
	}

	reg, _, err := installRegistryAndRegistrations(cfg, in.Mandatory)
	if err != nil {
		return nil, joinInitialFailureCleanup(ctx, err, nil, nil, shutTracing)
	}

	if in.RegistrySetup != nil {
		if err := in.RegistrySetup(reg); err != nil {
			return nil, joinInitialFailureCleanup(ctx, fmt.Errorf("runtimebundle: registry setup: %w", err), nil, nil, shutTracing)
		}
	}

	discInstall, err := installDiscoveredBackendExports(cfg, reg, hostEnv)
	if err != nil {
		return nil, joinInitialFailureCleanup(ctx, err, nil, nil, shutTracing)
	}
	pInput := processBuildInput{
		Cfg: cfg, Logger: logger, Registry: reg, HostEnv: hostEnv, Production: in.Production,
		Tracing: ProcessTracing{Shutdown: traceShutdown, Active: traceRes.Active},
	}
	if discInstall != nil {
		pInput.PluginResourcePool, pInput.PluginHost = discInstall.ResourcePool, discInstall.Host
		pInput.PluginArtifacts, pInput.PluginStagingDir = discInstall.Artifacts, discInstall.StagingDir
	}
	ps, err := ops.process(ctx, pInput)
	if err != nil {
		return nil, joinInitialFailureCleanup(ctx, fmt.Errorf("runtimebundle: process services: %w", err), nil, nil, shutTracing)
	}
	if !ps.Tracing.Active && traceRes.Active {
		ps.Tracing.Active = true
	}
	closeProcess := ps.Close

	bundle, err := ops.compile(ctx, ps, cfg, in.HandlerComposer)
	if err != nil {
		return nil, joinInitialFailureCleanup(ctx, fmt.Errorf("runtimebundle: compile initial generation: %w", err), nil, closeProcess, shutTracing)
	}

	mgr, _, err := ops.publisher(ctx, initialPublishInput{Process: ps, Bundle: bundle, Effective: effective})
	if err != nil {
		_ = bundle.Quiesce(context.WithoutCancel(ctx))
		_ = bundle.Close()
		return nil, joinInitialFailureCleanup(ctx, err, nil, closeProcess, shutTracing)
	}

	host, err := bindHost(path, bindHostInput{
		Manager:                mgr,
		Process:                ps,
		Compose:                in.HandlerComposer,
		Logger:                 logger,
		Config:                 cfg,
		Effective:              effective,
		ActiveSource:           activeSource,
		ActiveSourceOwner:      sourceOwner,
		FixedStreamRecovery:    fixedStreamRecovery,
		EnterpriseCapabilities: capabilities,
		ShutdownTracing:        traceShutdown,
	})
	if err == nil && ops.afterBind != nil {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					closeHostAfterBindPanic(context.WithoutCancel(ctx), host)
					panic(recovered)
				}
			}()
			err = ops.afterBind()
		}()
	}
	if err != nil {
		if host != nil {
			if cleanupErr := omitSoleAlreadyClosed(host.Close(context.WithoutCancel(ctx))); cleanupErr != nil {
				return nil, errors.Join(err, cleanupErr)
			}
			return nil, err
		}
		return nil, joinInitialFailureCleanup(ctx, err, func() error {
			return mgr.ShutdownDetached(context.WithoutCancel(ctx))
		}, closeProcess, shutTracing)
	}
	ownerTransferred = true
	return host, nil
}

func closeHostAfterBindPanic(ctx context.Context, host *Host) {
	if host != nil {
		_ = host.Close(ctx)
	}
}

func buildHostWithEnv(ctx context.Context, in hostBuildInput, loadEffective bootstrapEffectiveLoader, hostEnv featurehost.HostEnvironment, _ any) (*Host, error) {
	ops := defaultHostBuildOps()
	if loadEffective != nil {
		ops.load = loadEffective
	}
	return buildHost(ctx, in, ops, hostEnv)
}

type (
	effectiveLoader      = bootstrapEffectiveLoader
	tracingInitializer   func(ctx context.Context, cfg *config.Config) (tracing.Result, error)
	processBuilder       func(ctx context.Context, in processBuildInput) (*ProcessServices, error)
	generationCompilerOp func(ctx context.Context, ps *ProcessServices, cfg *config.Config, compose HandlerComposer) (GenerationRuntime, error)
	initialPublisher     func(ctx context.Context, in initialPublishInput) (*runtimehost.Manager, *runtimehost.Generation, error)
	registryInstaller    func(cfg *config.Config, mandatory []aipsdk.Requirement) (*pluginreg.Registry, []aipsdk.Registration, error)
)

type hostBuildOps struct {
	load      effectiveLoader
	tracing   tracingInitializer
	process   processBuilder
	compile   generationCompilerOp
	publisher initialPublisher
	// afterBind is an optional leaf fault hook (no owner authority); nil in production.
	afterBind func() error
}

type processBuildInput struct {
	Cfg                *config.Config
	Logger             *slog.Logger
	Registry           *pluginreg.Registry
	HostEnv            featurehost.HostEnvironment
	Production         ProductionOptions
	Tracing            ProcessTracing
	PluginResourcePool *backendResourcePool
	PluginHost         *processhost.Host
	PluginArtifacts    []*trust.VerifiedArtifact
	PluginStagingDir   string
}
type initialPublishInput struct {
	Process   *ProcessServices
	Bundle    GenerationRuntime
	Effective *config.EffectiveConfig
}

func defaultHostBuildOps() hostBuildOps {
	return hostBuildOps{
		load: LoadBootstrapEffectiveWithSource, tracing: initProcessTracing,
		process: buildProcessServicesOp, compile: compileInitialGenerationOp,
		publisher: publishStartupGenerationOp,
	}
}

func buildProcessServicesOp(ctx context.Context, in processBuildInput) (*ProcessServices, error) {
	return NewProcessServices(ctx, ProcessServicesInput{
		Cfg: in.Cfg, Log: in.Logger,
		Opts: &BuildOptions{
			PluginRegistry: in.Registry,
			Infra:          InfraOptions{OutboundTracing: in.Tracing.Active, ProcessTracing: in.Tracing},
			Production:     in.Production,
		},
		HostEnv:            in.HostEnv,
		Tracing:            in.Tracing,
		PluginResourcePool: in.PluginResourcePool,
		PluginHost:         in.PluginHost,
		PluginArtifacts:    in.PluginArtifacts,
		PluginStagingDir:   in.PluginStagingDir,
	})
}

func compileInitialGenerationOp(ctx context.Context, ps *ProcessServices, cfg *config.Config, compose HandlerComposer) (GenerationRuntime, error) {
	return CompileGeneration(ctx, GenerationCompileInput{Process: ps, Candidate: cfg, Compose: compose})
}

func publishStartupGenerationOp(ctx context.Context, in initialPublishInput) (*runtimehost.Manager, *runtimehost.Generation, error) {
	if in.Process == nil {
		return nil, nil, fmt.Errorf("runtimebundle: nil ProcessServices")
	}
	if in.Bundle == nil {
		return nil, nil, fmt.Errorf("runtimebundle: nil generation bundle")
	}
	mgr := runtimehost.NewManager(DefaultMaxRetainedGenerations, nil)
	gen := mgr.PrepareRequestPlane("startup", in.Bundle)
	hints := runtimehost.MetaHints{TriggerKind: "startup", LoadedAt: time.Now().UTC()}
	if in.Effective != nil {
		hints.PublicFingerprint = in.Effective.Identity.PublicFingerprint
		if !in.Effective.LoadedAt.IsZero() {
			hints.LoadedAt = in.Effective.LoadedAt
		}
	}
	gen.SetMetaHints(hints)
	if err := mgr.Publish(gen); err != nil {
		_ = gen.Discard()
		return nil, nil, fmt.Errorf("runtimebundle: publish initial generation: %w", err)
	}
	if gen.ID() != 1 {
		_ = mgr.ShutdownDetached(context.WithoutCancel(ctx))
		return nil, nil, fmt.Errorf("runtimebundle: initial generation id=%d want 1", gen.ID())
	}
	if in.Process.terminalWorkRT != nil {
		in.Process.terminalWorkRT.BindGenerationManager(mgr)
	}
	return mgr, gen, nil
}

func installRegistryOp(cfg *config.Config, mandatory []aipsdk.Requirement) (*pluginreg.Registry, []aipsdk.Registration, error) {
	return installRegistryAndRegistrations(cfg, mandatory)
}

const DefaultMaxRetainedGenerations = 8

func joinInitialFailureCleanup(ctx context.Context, primary error, genRollback, processClose func() error, traceShutdown func(context.Context) error) error {
	var cleanup error
	if genRollback != nil {
		if err := omitSoleAlreadyClosed(genRollback()); err != nil {
			cleanup = errors.Join(cleanup, err)
		}
	}
	if processClose != nil {
		if err := processClose(); err != nil {
			cleanup = errors.Join(cleanup, err)
		}
	}
	if traceShutdown != nil {
		if err := traceShutdown(context.WithoutCancel(ctx)); err != nil {
			cleanup = errors.Join(cleanup, err)
		}
	}
	if cleanup != nil {
		return errors.Join(primary, cleanup)
	}
	return primary
}

func omitSoleAlreadyClosed(err error) error {
	if err == nil || err == runtimehost.ErrAlreadyClosed {
		return nil
	}
	if !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		return err
	}
	if m, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range m.Unwrap() {
			if omitSoleAlreadyClosed(e) != nil {
				return err
			}
		}
		return nil
	}
	u := errors.Unwrap(err)
	if u == nil || omitSoleAlreadyClosed(u) == nil {
		return nil
	}
	return err
}

// validateObservationWriterDeclaration validates the carried, immutable
// observation-writer selection against the concrete declarations of this
// build. It runs before the process acquires anything, so a conflicting
// writer never reaches an opener, a recovery pass, a worker start, or a
// published generation.
//
// The carried selection is authoritative: the runtime never reselects. The
// pure selector is consulted only to confirm that a carried external pair
// agrees with the concrete declarations.
func validateObservationWriterDeclaration(prod ProductionOptions) error {
	in := observationWriterInput{
		ExplicitRecorder: prod.MeteringRecorder != nil && !coremetering.IsNilObservationPort(prod.MeteringRecorder),
		ExplicitSink:     prod.MeteringObservationSink != nil && !coremetering.IsNilObservationPort(prod.MeteringObservationSink),
	}
	// The declaration check refuses an incomplete pair, a typed-nil port, a
	// conflicting legacy writer, and a recorder/sink pair that belongs to two
	// different durable journals. It inspects declarations only.
	if err := coremetering.ValidateObservationWriterDeclaration(
		prod.MeteringObservationWriterSelection, prod.MeteringRecorder, prod.MeteringObservationSink,
	); err != nil {
		return fmt.Errorf("runtimebundle: %w", err)
	}
	if prod.MeteringObservationWriterSelection != coremetering.ObservationWriterExternal {
		return nil
	}
	selected, err := selectObservationWriter(in)
	if err != nil {
		return fmt.Errorf("runtimebundle: %w", err)
	}
	if selected != coremetering.ObservationWriterExternal {
		return fmt.Errorf("runtimebundle: %w: the carried external observation writer does not match its declarations",
			coremetering.ErrObservationWriterConflict)
	}
	return nil
}
