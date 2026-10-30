package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"gopkg.in/yaml.v3"
)

// ValidateStructuralInput configures structural check-config validation: no
// tracing, process services, generation compile, plugin activation, or I/O.
type ValidateStructuralInput struct {
	ConfigPath              string
	Mandatory               []aipsdk.Requirement
	StreamRecoveryOverrides config.StreamRecoveryOverrides
	// EnterpriseCapabilities declares provided capabilities; nil fails closed.
	EnterpriseCapabilities config.CapabilitySet
}

type structuralValidationOps struct {
	load     bootstrapEffectiveLoader
	registry registryInstaller
}

func defaultStructuralValidationOps() structuralValidationOps {
	return structuralValidationOps{
		load:     LoadBootstrapEffectiveWithSource,
		registry: installRegistryOp,
	}
}

// ValidateStructural validates effective configuration for operator check-config.
// Requirements 2.8 and 5.5: structural rules only; no plugin process activation,
// runtime tracing, model-registry refresh, or provider network calls.
//
// It returns the operator advisories of the validation: a configuration without
// a default route is accepted, and its advisory names the remedy.
func ValidateStructural(ctx context.Context, in ValidateStructuralInput) (ValidateStructuralReport, error) {
	return validateStructural(ctx, in, defaultStructuralValidationOps())
}

func validateStructural(ctx context.Context, in ValidateStructuralInput, ops structuralValidationOps) (ValidateStructuralReport, error) {
	if ctx == nil {
		return ValidateStructuralReport{}, fmt.Errorf("runtimebundle: nil context")
	}
	if ops.load == nil || ops.registry == nil {
		return ValidateStructuralReport{}, fmt.Errorf("runtimebundle: incomplete structural validation operations")
	}
	path := strings.TrimSpace(in.ConfigPath)
	if path == "" {
		return ValidateStructuralReport{}, fmt.Errorf("runtimebundle: empty config path")
	}

	effective, _, ownerSlot, _, err := ops.load(ctx, path, in.StreamRecoveryOverrides, in.EnterpriseCapabilities)
	if ownerSlot != nil {
		err = errors.Join(err, ownerSlot.Close(ctx))
	}
	if err != nil {
		return ValidateStructuralReport{}, err
	}
	if effective == nil || effective.Config == nil {
		return ValidateStructuralReport{}, fmt.Errorf("runtimebundle: nil effective config")
	}
	cfg := effective.Config
	if _, err := config.CompileGeoIP(cfg.Access.GeoIP); err != nil {
		return ValidateStructuralReport{}, fmt.Errorf("runtimebundle: structural GeoIP: %w", err)
	}

	reg, regs, err := ops.registry(cfg, in.Mandatory)
	if err != nil {
		return ValidateStructuralReport{}, fmt.Errorf("runtimebundle: structural registry: %w", err)
	}

	if err := registerManifestDiscoveredKinds(cfg, reg); err != nil {
		return ValidateStructuralReport{}, err
	}
	if err := validateCandidateManifestOwnership(cfg, reg); err != nil {
		return ValidateStructuralReport{}, err
	}
	// The route configuration the operator depends on: the effective, alias
	// resolved default route must not name a model no backend serves, and a
	// configuration without one is accepted with the advisory that names the
	// remedy.
	resolution, err := routing.ResolveDefaultRoute(cfg, standardplugins.DefaultWireModel)
	if err != nil {
		return ValidateStructuralReport{}, fmt.Errorf("runtimebundle: structural routes: %w", err)
	}
	report := ValidateStructuralReport{DefaultRouteWarning: resolution.Warning}
	projection := inventoryProjectionForOperator(cfg, nil)
	if _, err := inventorySnapshotForOperatorWithProjection(ctx, cfg, reg, regs, projection); err != nil {
		return report, fmt.Errorf("runtimebundle: structural inventory: %w", err)
	}
	if err := validateProjectedDiagnostics(projection.InstanceDiagnostics); err != nil {
		return report, err
	}
	return report, nil
}

func validateProjectedDiagnostics(rows []diag.InstanceDiagnostic) error {
	for _, row := range rows {
		if strings.TrimSpace(row.ConfigError) == "" {
			continue
		}
		inst := strings.TrimSpace(row.InstanceID)
		if inst == "" {
			inst = strings.TrimSpace(row.ID)
		}
		if inst == "" {
			inst = "<unknown>"
		}
		kind := strings.TrimSpace(row.FactoryKind)
		if kind != "" && inst != kind && inst != "<unknown>" {
			return fmt.Errorf("runtimebundle: extension %q (%s): %s", inst, kind, row.ConfigError)
		}
		return fmt.Errorf("runtimebundle: extension %q: %s", inst, row.ConfigError)
	}
	return nil
}

// registerManifestDiscoveredKinds resolves trusted plugin manifests from disk
// and registers factory kinds on reg for manifest-available ownership checks.
// It never creates a process host, activates plugins, or dials providers.
func registerManifestDiscoveredKinds(cfg *config.Config, reg *pluginreg.Registry) error {
	if cfg == nil || reg == nil || !cfg.Plugins.BackendDiscovery.Enabled {
		return nil
	}
	if registrySatisfiesEnabledBackendProfiles(cfg, reg) {
		return nil
	}
	staging, err := os.MkdirTemp("", "aip-structural-catalog-*")
	if err != nil {
		return fmt.Errorf("runtimebundle: structural catalog staging: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()

	resolved, err := ResolvePluginCatalog(cfg, reg, staging)
	if err != nil {
		closeVerifiedArtifacts(resolved.TrustBySafe)
		return fmt.Errorf("runtimebundle: structural plugin catalog: %w", err)
	}
	if resolved.CatalogErr != nil {
		closeVerifiedArtifacts(resolved.TrustBySafe)
		return fmt.Errorf("runtimebundle: structural plugin catalog: %w", resolved.CatalogErr)
	}
	for _, exp := range resolved.Installable {
		kind := strings.TrimSpace(exp.Kind)
		if kind == "" {
			continue
		}
		if reg.HasBackend(kind) {
			closeVerifiedArtifacts(resolved.TrustBySafe)
			return fmt.Errorf("runtimebundle: structural plugin catalog: duplicate backend registration: %s", kind)
		}
		profile := exp.Profile
		if err := reg.RegisterDiscoveredBackend(
			kind,
			manifestOnlyStubFactory(kind),
			profile,
			pluginreg.BackendReloadPolicy{AllowsCandidateOverlap: true},
		); err != nil {
			closeVerifiedArtifacts(resolved.TrustBySafe)
			return fmt.Errorf("runtimebundle: structural manifest registration: %w", err)
		}
	}
	closeVerifiedArtifacts(resolved.TrustBySafe)
	return nil
}

func manifestOnlyStubFactory(kind string) func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
	kind = strings.TrimSpace(kind)
	return func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{}, fmt.Errorf("runtimebundle: manifest-only kind %q must not build backends during structural validation", kind)
	}
}
