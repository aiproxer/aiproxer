package runtimebundle

import (
	"context"
	"fmt"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/internal/infra/tracing"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// installRegistryAndRegistrations is the sole production InstallStandardBundleOn
// owner shared by BuildHost, ValidateDistribution, and Inspect entrypoints.
func installRegistryAndRegistrations(cfg *config.Config, mandatory []aipsdk.Requirement) (*pluginreg.Registry, []aipsdk.Registration, error) {
	reg := pluginreg.NewRegistry()
	apiKeys := standardplugins.ResolveUpstreamAPIKeysFromEnv()
	if err := standardplugins.InstallStandardBundleOn(reg, apiKeys); err != nil {
		return nil, nil, fmt.Errorf("runtimebundle: plugin registration: %w", err)
	}
	if len(mandatory) > 0 {
		if err := reg.ValidateBundledFactories(mandatory); err != nil {
			return nil, nil, fmt.Errorf("runtimebundle: registry factory validation: %w", err)
		}
	}
	regs := config.RegistrationsFromConfig(cfg)
	if err := featurehost.ValidateSecretGuardRegistrations(regs); err != nil {
		return nil, nil, err
	}
	return reg, regs, nil
}

// registrySatisfiesEnabledBackendProfiles lets authoritative registries skip
// discovery when every enabled backend already has a security profile.
func registrySatisfiesEnabledBackendProfiles(cfg *config.Config, reg *pluginreg.Registry) bool {
	if cfg == nil || reg == nil {
		return false
	}
	for _, p := range cfg.Plugins.Backends {
		if !p.Enabled {
			continue
		}
		if _, ok := reg.BackendSecurityProfile(p.FactoryID()); !ok {
			return false
		}
	}
	return true
}

// discoveredBackendInstall is the ownership bundle returned by
// installDiscoveredBackendExports. Host/staging transfer to ProcessServices or
// InspectPrepared; Artifacts must be closed before staging removal (Windows
// holds staged executable handles open until VerifiedArtifact.Close).
type discoveredBackendInstall struct {
	ResourcePool *backendResourcePool
	Host         *processhost.Host
	StagingDir   string
	Artifacts    []*trust.VerifiedArtifact
	exports      []ValidatedExport
	provisioning map[string]backendProvisioningInput
}

func (d *discoveredBackendInstall) release() {
	if d == nil {
		return
	}
	if d.ResourcePool != nil {
		_ = d.ResourcePool.Close()
		d.ResourcePool = nil
	}
	if d.Host != nil {
		_ = d.Host.Close()
		d.Host = nil
	}
	for _, a := range d.Artifacts {
		_ = a.Close()
	}
	d.Artifacts = nil
	if d.StagingDir != "" {
		_ = removeAllRetry(d.StagingDir, 8, 25*time.Millisecond)
		d.StagingDir = ""
	}
}

// closeVerifiedArtifacts releases every verified artifact in a resolve result
// (used on error paths where the install bundle never transfers ownership).
func closeVerifiedArtifacts(m map[string]trust.VerifyResult) {
	for _, tr := range m {
		if tr.Artifact != nil {
			_ = tr.Artifact.Close()
		}
	}
}

// installDiscoveredBackendExports discovers/trusts/catalogs optional backend
// exports and registers them on reg before ProcessServices freezes discovery.
//
// Policy (shared by BuildHost, ValidateDistribution, PrepareInspect):
//   - runs only when backend_discovery.enabled;
//   - if every enabled configured kind already has a security profile on reg,
//     treats the registry as authoritative and skips artifact discovery;
//   - otherwise requires configured external kinds to resolve fail-closed and
//     installs them.
//
// Caller owns the returned install until transferred to ProcessServices or
// InspectPrepared.Close.
func installDiscoveredBackendExports(cfg *config.Config, reg *pluginreg.Registry, deliveryEnv ...featurehost.HostEnvironment) (*discoveredBackendInstall, error) {
	if cfg != nil {
		if err := config.ValidateBackendInstanceBindings(cfg); err != nil {
			return nil, err
		}
		for _, binding := range cfg.Plugins.BackendDiscovery.Instances {
			if reg != nil && reg.HasBackend(binding.Kind) {
				return nil, backendProvisioningError("external target required")
			}
		}
	}
	if cfg == nil || !cfg.Plugins.BackendDiscovery.Enabled {
		return nil, nil
	}
	if registrySatisfiesEnabledBackendProfiles(cfg, reg) {
		return nil, nil
	}
	disc, resourcePool, staging, err := prepareDiscoveredPluginInstall(cfg, reg)
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: backend discovery: %w", err)
	}
	if disc == nil {
		return nil, nil
	}
	install := &discoveredBackendInstall{
		ResourcePool: resourcePool,
		Host:         disc.Host,
		StagingDir:   staging,
		Artifacts:    append([]*trust.VerifiedArtifact(nil), disc.Trusted...),
		exports:      disc.Exports,
	}
	provisioning := map[string]backendProvisioningInput{}
	disc.Options.provisioning = provisioning
	install.provisioning = provisioning
	if err := installDiscoveredExportsWithPool(reg, disc.Host, disc.Exports, disc.Options, resourcePool); err != nil {
		install.release()
		return nil, fmt.Errorf("runtimebundle: discovered plugin install: %w", err)
	}
	if len(deliveryEnv) > 0 {
		captured, err := captureBackendProvisioning(cfg, reg, disc.Exports, deliveryEnv[0])
		if err != nil {
			install.release()
			return nil, err
		}
		for id, input := range captured {
			provisioning[id] = input.clone()
		}
	}
	return install, nil
}

func initProcessTracing(ctx context.Context, cfg *config.Config) (tracing.Result, error) {
	return tracing.Init(ctx, cfg)
}
