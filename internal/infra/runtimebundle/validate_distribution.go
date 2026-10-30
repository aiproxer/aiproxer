package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/internal/infra/logging"
	"github.com/aiproxer/aiproxer/internal/infra/osenv"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

type ValidateDistributionInput struct {
	ConfigPath              string
	Mandatory               []aipsdk.Requirement
	StreamRecoveryOverrides config.StreamRecoveryOverrides
	Production              ProductionOptions
	HandlerComposer         HandlerComposer
	// EnterpriseCapabilities declares provided capabilities; nil fails closed.
	EnterpriseCapabilities config.CapabilitySet
}

type validateDistributionOps struct {
	hostBuildOps
	registry registryInstaller
}

func defaultValidateDistributionOps() validateDistributionOps {
	return validateDistributionOps{hostBuildOps: defaultHostBuildOps(), registry: installRegistryOp}
}

func ValidateDistribution(ctx context.Context, in ValidateDistributionInput) error {
	return validateDistribution(ctx, in, osenv.Process{}, defaultValidateDistributionOps())
}

func validateDistribution(
	ctx context.Context,
	in ValidateDistributionInput,
	hostEnv featurehost.HostEnvironment,
	ops validateDistributionOps,
) error {
	if ctx == nil {
		return fmt.Errorf("runtimebundle: nil context")
	}
	if ops.load == nil || ops.tracing == nil || ops.registry == nil || ops.process == nil || ops.compile == nil {
		return fmt.Errorf("runtimebundle: incomplete validate distribution operations")
	}
	path := strings.TrimSpace(in.ConfigPath)
	if path == "" {
		return fmt.Errorf("runtimebundle: empty config path")
	}
	if in.HandlerComposer == nil {
		return fmt.Errorf("runtimebundle: ValidateDistribution requires HandlerComposer")
	}

	effective, _, ownerSlot, _, err := ops.load(ctx, path, in.StreamRecoveryOverrides, in.EnterpriseCapabilities)
	if ownerSlot != nil {
		err = errors.Join(err, ownerSlot.Close(ctx))
	}
	if err != nil {
		return err
	}
	if effective == nil || effective.Config == nil {
		return fmt.Errorf("runtimebundle: nil effective config")
	}
	cfg := effective.Config

	var shutTracing func(context.Context) error
	fail := func(err error, rollback, closeProcess func() error) error {
		return joinInitialFailureCleanup(ctx, err, rollback, closeProcess, shutTracing)
	}

	traceRes, err := ops.tracing(ctx, cfg)
	if err != nil {
		return fmt.Errorf("runtimebundle: tracing init: %w", err)
	}
	traceShutdownRaw := traceRes.Shutdown
	shutTracing = func(ctx context.Context) error {
		if traceShutdownRaw == nil {
			return nil
		}
		return traceShutdownRaw(ctx)
	}

	reg, _, err := ops.registry(cfg, in.Mandatory)
	if err != nil {
		return fail(err, nil, nil)
	}

	discInstall, err := installDiscoveredBackendExports(cfg, reg)
	if err != nil {
		return fail(err, nil, nil)
	}
	var pluginHost *processhost.Host
	var pluginResourcePool *backendResourcePool
	var pluginStaging string
	var pluginArtifacts []*trust.VerifiedArtifact
	if discInstall != nil {
		pluginResourcePool = discInstall.ResourcePool
		pluginHost, pluginStaging = discInstall.Host, discInstall.StagingDir
		pluginArtifacts = discInstall.Artifacts
	}

	logger, err := logging.NewLogger(cfg.Logging, io.Discard,
		logging.WithOTELTraceAttrs(cfg.Observability.Tracing.Enabled))
	if err != nil {
		if discInstall != nil {
			discInstall.release()
		}
		return fail(fmt.Errorf("runtimebundle: logger init: %w", err), nil, nil)
	}

	ps, err := ops.process(ctx, processBuildInput{
		Cfg: cfg, Logger: logger, Registry: reg, HostEnv: hostEnv, Production: in.Production,
		Tracing:            ProcessTracing{Shutdown: traceShutdownRaw, Active: traceRes.Active},
		PluginResourcePool: pluginResourcePool,
		PluginHost:         pluginHost,
		PluginArtifacts:    pluginArtifacts,
		PluginStagingDir:   pluginStaging,
	})
	if err != nil {
		// NewProcessServices adopts (or releases) host/staging on entry.
		return fail(fmt.Errorf("runtimebundle: process services: %w", err), nil, nil)
	}
	closeProcess := ps.Close

	bundle, err := ops.compile(ctx, ps, cfg, in.HandlerComposer)
	var rollback func() error
	if bundle != nil {
		b := bundle
		rollback = func() error {
			return errors.Join(b.Quiesce(context.WithoutCancel(ctx)), omitSoleAlreadyClosed(b.Close()))
		}
	}
	if err != nil {
		return fail(fmt.Errorf("runtimebundle: compile validation generation: %w", err), rollback, closeProcess)
	}
	return fail(nil, rollback, closeProcess)
}
