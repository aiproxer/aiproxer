package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// InspectInput configures [InspectRoutes] / [InspectInventory] / [PrepareInspect]
// (no logger/tracing/runtime serve Host).
type InspectInput struct {
	ConfigPath              string
	Mandatory               []aipsdk.Requirement
	StreamRecoveryOverrides config.StreamRecoveryOverrides
	// EnterpriseCapabilities declares provided capabilities; nil fails closed.
	EnterpriseCapabilities config.CapabilitySet
}

// InspectPrepared is a one-shot inspect/doctor preparation: effective config,
// registry with optional discovered exports installed, and process-owned
// temporary host/staging that must be released via [InspectPrepared.Close].
type InspectPrepared struct {
	Config   *config.Config
	Registry *pluginreg.Registry

	pluginHost *processhost.Host
	stagingDir string
	artifacts  []*trust.VerifiedArtifact
	closeOnce  sync.Once
	closeErr   error
}

// PluginHost returns the process host used for discovered-export install, if any.
func (p *InspectPrepared) PluginHost() *processhost.Host {
	if p == nil {
		return nil
	}
	return p.pluginHost
}

// Close releases temporary discovery host/staging exactly once.
func (p *InspectPrepared) Close() error {
	if p == nil {
		return nil
	}
	p.closeOnce.Do(func() {
		for i, a := range p.artifacts {
			p.closeErr = errors.Join(p.closeErr, a.Close())
			p.artifacts[i] = nil
		}
		p.artifacts = nil
		if p.pluginHost != nil {
			p.closeErr = errors.Join(p.closeErr, p.pluginHost.Close())
			p.pluginHost = nil
		}
		if p.stagingDir != "" {
			dir := p.stagingDir
			p.stagingDir = ""
			p.closeErr = errors.Join(p.closeErr, removeAllRetry(dir, 8, 25*time.Millisecond))
		}
	})
	return p.closeErr
}

// removeAllRetry deletes path, retrying briefly so Windows releases staged
// plugin binaries after verified-artifact and process-host close.
func removeAllRetry(path string, attempts int, delay time.Duration) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if attempts < 1 {
		attempts = 1
	}
	var last error
	for i := 0; i < attempts; i++ {
		last = os.RemoveAll(path)
		if last == nil {
			if _, err := os.Stat(path); os.IsNotExist(err) {
				return nil
			}
			last = fmt.Errorf("runtimebundle: staging %q still present after RemoveAll", path)
		}
		if i+1 < attempts && delay > 0 {
			time.Sleep(delay)
		}
	}
	return last
}

// PrepareInspect loads one effective snapshot, installs the standard registry,
// and registers optional discovered backend exports for inspect/doctor.
// Callers must Close the result; it never constructs a serve Host or tracing.
func PrepareInspect(ctx context.Context, in InspectInput) (*InspectPrepared, error) {
	return prepareInspectSession(ctx, in, LoadBootstrapEffectiveWithSource)
}

func prepareInspectSession(ctx context.Context, in InspectInput, loadEffective bootstrapEffectiveLoader) (*InspectPrepared, error) {
	cfg, reg, _, err := prepareInspect(ctx, in, loadEffective)
	if err != nil {
		return nil, err
	}
	disc, err := installDiscoveredBackendExports(cfg, reg)
	if err != nil {
		return nil, err
	}
	prep := &InspectPrepared{Config: cfg, Registry: reg}
	if disc != nil {
		prep.pluginHost = disc.Host
		prep.stagingDir = disc.StagingDir
		prep.artifacts = disc.Artifacts
	}
	return prep, nil
}

// InspectRoutes loads one effective snapshot and projects a routes-only read model.
func InspectRoutes(ctx context.Context, in InspectInput) (RoutesSnapshot, error) {
	return inspectRoutes(ctx, in, LoadBootstrapEffectiveWithSource)
}

func inspectRoutes(ctx context.Context, in InspectInput, loadEffective bootstrapEffectiveLoader) (RoutesSnapshot, error) {
	cfg, reg, _, err := prepareInspect(ctx, in, loadEffective)
	if err != nil {
		return RoutesSnapshot{}, err
	}
	var live *inventoryLiveSnapshot
	if snap, loadErr := tryLoadInventoryLiveSnapshot(ctx, cfg, reg); loadErr == nil {
		live = snap
		defer func() { _ = live.Close(ctx) }()
	}
	return routesSnapshotFrom(cfg, reg, live)
}

// ValidateStructuralReport carries the operator-facing advisories of a
// structural validation. An advisory is not a refusal: the configuration is
// accepted, and the operator is told what to watch.
type ValidateStructuralReport struct {
	// DefaultRouteWarning explains a configuration with no default route: a
	// client that omits an explicit route has no fallback.
	DefaultRouteWarning string
}

// RoutesSnapshot is a config-derived operator read model (no provider I/O).
type RoutesSnapshot struct {
	EffectiveDefaultRoute string `json:"effective_default_route"`
	// EffectiveDefaultRouteNote explains an absent default route: the frontends
	// have no route to fall back to, and the operator is told how to name one.
	EffectiveDefaultRouteNote string `json:"effective_default_route_note,omitempty"`
	// EffectiveDefaultRouteSource names where the effective default route came
	// from: "configured" (routing.default_route), "canonical_default_model" (the
	// compile-time default of the first enabled backend kind), or
	// "discovered_inventory" (the model the backend instance published at
	// runtime). It is empty when there is no default route.
	EffectiveDefaultRouteSource string `json:"effective_default_route_source,omitempty"`
	// EffectiveDefaultRouteModel is the model the effective default route names.
	// It is empty when there is no default route.
	EffectiveDefaultRouteModel string                      `json:"effective_default_route_model,omitempty"`
	Backends                   []RouteBackend              `json:"backends"`
	CompatibleBackends         []diag.CompatibleBackendRow `json:"compatible_backends,omitempty"`
	InstanceDiagnostics        []diag.InstanceDiagnostic   `json:"instance_diagnostics,omitempty"`
	OpenResponsesFrontends     []diag.InstanceDiagnostic   `json:"openresponses_frontends,omitempty"`
	ModelAliases               []RouteAlias                `json:"model_aliases"`
	// CredentialPosture is derived from enabled backend factory ids only: all_local_stub when every
	// enabled backend uses factory kind local-stub; live_provider when any enabled backend is not
	// local-stub; no_enabled_backends when there are no enabled backend rows.
	CredentialPosture string `json:"credential_posture"`
}

// RouteBackend is one configured backend row for route inspection.
type RouteBackend struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Enabled bool   `json:"enabled"`
}

// RouteAlias is one model alias rule from configuration.
type RouteAlias struct {
	Pattern     string `json:"pattern"`
	Replacement string `json:"replacement"`
}

// InspectInventory loads one effective snapshot and projects an inventory-only read model.
func InspectInventory(ctx context.Context, in InspectInput) (diag.InventorySnapshot, error) {
	return inspectInventory(ctx, in, LoadBootstrapEffectiveWithSource)
}

func inspectInventory(ctx context.Context, in InspectInput, loadEffective bootstrapEffectiveLoader) (diag.InventorySnapshot, error) {
	cfg, reg, regs, err := prepareInspect(ctx, in, loadEffective)
	if err != nil {
		return diag.InventorySnapshot{}, err
	}
	return InventorySnapshotForOperator(ctx, cfg, reg, regs)
}

// prepareInspect is the private shared inspect pipeline. loadEffective must be
// supplied by the caller (never a package-global fallback) so config_load sees
// no new startup-load alias.
func prepareInspect(ctx context.Context, in InspectInput, loadEffective bootstrapEffectiveLoader) (*config.Config, *pluginreg.Registry, []aipsdk.Registration, error) {
	if ctx == nil {
		return nil, nil, nil, fmt.Errorf("runtimebundle: nil context")
	}
	if loadEffective == nil {
		return nil, nil, nil, fmt.Errorf("runtimebundle: nil effective loader")
	}
	path := strings.TrimSpace(in.ConfigPath)
	if path == "" {
		return nil, nil, nil, fmt.Errorf("runtimebundle: empty config path")
	}
	effective, _, ownerSlot, _, err := loadEffective(ctx, path, in.StreamRecoveryOverrides, in.EnterpriseCapabilities)
	if ownerSlot != nil {
		err = errors.Join(err, ownerSlot.Close(ctx))
	}
	if err != nil {
		return nil, nil, nil, err
	}
	if effective == nil || effective.Config == nil {
		return nil, nil, nil, fmt.Errorf("runtimebundle: nil effective config")
	}
	reg, regs, err := installRegistryAndRegistrations(effective.Config, in.Mandatory)
	if err != nil {
		return nil, nil, nil, err
	}
	return effective.Config, reg, regs, nil
}
