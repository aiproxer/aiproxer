package runtimebundle

import (
	"context"
	"errors"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/configsource"
	"github.com/aiproxer/aiproxer/internal/infra/tracing"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// Test-facing ValidateDistribution outcome/journal types (Task 5.4 RED matrices).

type validateDistributionJournal struct {
	Acquired []string
	Cleaned  []string
	Loads    int
}

// validateDistributionOutcome invokes the production validateDistribution
// transaction with journaling ops wrappers. It must not reimplement any
// ValidateDistribution stage.
func validateDistributionOutcome(ctx context.Context, in ValidateDistributionInput, loadEffective bootstrapEffectiveLoader) (validateDistributionJournal, error) {
	var journal validateDistributionJournal
	ops := defaultValidateDistributionOps()
	if loadEffective != nil {
		ops.load = loadEffective
	}

	baseLoad := ops.load
	ops.load = func(ctx context.Context, path string, cli config.StreamRecoveryOverrides, capabilities config.CapabilitySet) (*config.EffectiveConfig, *configsource.ActiveSourceVersion, *configsource.SourceOwnerSlot, config.StreamRecoveryOverrides, error) {
		journal.Loads++
		eff, src, owner, fixed, err := baseLoad(ctx, path, cli, capabilities)
		if err != nil {
			return nil, nil, owner, fixed, err
		}
		journal.acquire("loader")
		return eff, src, owner, fixed, nil
	}

	baseTracing := ops.tracing
	ops.tracing = func(ctx context.Context, cfg *config.Config) (tracing.Result, error) {
		res, err := baseTracing(ctx, cfg)
		if err != nil {
			return res, err
		}
		journal.acquire("tracing")
		inner := res.Shutdown
		res.Shutdown = func(ctx context.Context) error {
			journal.clean("tracing_close")
			if inner == nil {
				return nil
			}
			return inner(ctx)
		}
		return res, nil
	}

	baseRegistry := ops.registry
	ops.registry = func(cfg *config.Config, mandatory []aipsdk.Requirement) (*pluginreg.Registry, []aipsdk.Registration, error) {
		reg, regs, err := baseRegistry(cfg, mandatory)
		if err != nil {
			return nil, nil, err
		}
		journal.acquire("registry")
		return reg, regs, nil
	}

	baseProcess := ops.process
	ops.process = func(ctx context.Context, in processBuildInput) (*ProcessServices, error) {
		ps, err := baseProcess(ctx, in)
		if err != nil {
			return nil, err
		}
		journal.acquire("process")
		ps.closers = append([]func() error{func() error {
			journal.clean("process_close")
			return nil
		}}, ps.closers...)
		return ps, nil
	}

	baseCompile := ops.compile
	ops.compile = func(ctx context.Context, ps *ProcessServices, cfg *config.Config, compose HandlerComposer) (GenerationRuntime, error) {
		bundle, err := baseCompile(ctx, ps, cfg, compose)
		if err != nil {
			return nil, err
		}
		journal.acquire("compile")
		return &validateJournalBundle{inner: bundle, journal: &journal}, nil
	}

	err := validateDistribution(ctx, in, nil, ops)
	return journal, err
}

// validateDistributionWithCleanupFaults journals all stages and injects a fault
// on every cleanup stage (for CleanupFaultsJoinedInOrder).
func validateDistributionWithCleanupFaults(ctx context.Context, in ValidateDistributionInput) (validateDistributionJournal, error) {
	var journal validateDistributionJournal
	ops := defaultValidateDistributionOps()

	baseLoad := ops.load
	ops.load = func(ctx context.Context, path string, cli config.StreamRecoveryOverrides, capabilities config.CapabilitySet) (*config.EffectiveConfig, *configsource.ActiveSourceVersion, *configsource.SourceOwnerSlot, config.StreamRecoveryOverrides, error) {
		eff, src, owner, fixed, err := baseLoad(ctx, path, cli, capabilities)
		if err != nil {
			return nil, nil, owner, fixed, err
		}
		journal.acquire("loader")
		return eff, src, owner, fixed, nil
	}

	baseTracing := ops.tracing
	ops.tracing = func(ctx context.Context, cfg *config.Config) (tracing.Result, error) {
		res, err := baseTracing(ctx, cfg)
		if err != nil {
			return res, err
		}
		journal.acquire("tracing")
		inner := res.Shutdown
		res.Shutdown = func(ctx context.Context) error {
			journal.clean("tracing_close")
			shutErr := error(nil)
			if inner != nil {
				shutErr = inner(ctx)
			}
			return errors.Join(shutErr, fmt.Errorf("runtimebundle: validate distribution fault: tracing_close"))
		}
		return res, nil
	}

	baseRegistry := ops.registry
	ops.registry = func(cfg *config.Config, mandatory []aipsdk.Requirement) (*pluginreg.Registry, []aipsdk.Registration, error) {
		reg, regs, err := baseRegistry(cfg, mandatory)
		if err != nil {
			return nil, nil, err
		}
		journal.acquire("registry")
		return reg, regs, nil
	}

	baseProcess := ops.process
	ops.process = func(ctx context.Context, in processBuildInput) (*ProcessServices, error) {
		ps, err := baseProcess(ctx, in)
		if err != nil {
			return nil, err
		}
		journal.acquire("process")
		ps.closers = append([]func() error{func() error {
			journal.clean("process_close")
			return fmt.Errorf("runtimebundle: validate distribution fault: process_close")
		}}, ps.closers...)
		return ps, nil
	}

	baseCompile := ops.compile
	ops.compile = func(ctx context.Context, ps *ProcessServices, cfg *config.Config, compose HandlerComposer) (GenerationRuntime, error) {
		bundle, err := baseCompile(ctx, ps, cfg, compose)
		if err != nil {
			return nil, err
		}
		journal.acquire("compile")
		return &validateJournalBundle{inner: bundle, journal: &journal, faultAt: validateStageRollback}, nil
	}

	err := validateDistribution(ctx, in, nil, ops)
	return journal, err
}
