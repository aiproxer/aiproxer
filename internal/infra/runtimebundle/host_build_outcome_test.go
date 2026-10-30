package runtimebundle

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/configsource"
	"github.com/aiproxer/aiproxer/internal/infra/osenv"
)

// Test-facing HostBuilder outcome types (Task 5.1/5.2 RED matrices).

type hostBuildJournal struct {
	Acquired []string
	Cleaned  []string
	Loads    int
}

type hostBuildOutcome struct {
	Host     *Host
	Journal  hostBuildJournal
	Complete bool
}

func hostIsComplete(out hostBuildOutcome) bool {
	h := out.Host
	return out.Complete && h != nil && h.coordinator != nil && h.manager != nil && h.process != nil && h.effective != nil && h.executor != nil
}

func buildHostOutcome(ctx context.Context, in hostBuildInput, loadEffective bootstrapEffectiveLoader) (hostBuildOutcome, error) {
	loads := 0
	ops := defaultHostBuildOps()
	if loadEffective != nil {
		inner := loadEffective
		ops.load = func(ctx context.Context, path string, cli config.StreamRecoveryOverrides, capabilities config.CapabilitySet) (*config.EffectiveConfig, *configsource.ActiveSourceVersion, *configsource.SourceOwnerSlot, config.StreamRecoveryOverrides, error) {
			loads++
			return inner(ctx, path, cli, capabilities)
		}
	}
	host, err := buildHost(ctx, in, ops, osenv.Process{})
	if err != nil {
		return hostBuildOutcome{Journal: hostBuildJournal{Loads: loads}}, err
	}
	return hostBuildOutcome{
		Host:     host,
		Journal:  hostBuildJournal{Loads: loads},
		Complete: true,
	}, nil
}
