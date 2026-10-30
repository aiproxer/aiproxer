package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/catalog"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/diagnostics"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/discovery"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	sdkmanifest "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/manifest"
)

// PluginInspectReport is the operator-facing inspect snapshot.
type PluginInspectReport = diagnostics.Report

// PluginDoctorReport is the operator-facing doctor snapshot.
type PluginDoctorReport = diagnostics.DoctorReport

// InspectBackendPlugins runs non-executing discovery/catalog inspect for cfg.
// It uses the same ResolvePluginCatalog path as standard serve bootstrap.
func InspectBackendPlugins(cfg *config.Config, reg *pluginreg.Registry) (PluginInspectReport, error) {
	return InspectBackendPluginsWithContext(context.Background(), cfg, reg)
}

// InspectBackendPluginsWithContext is like InspectBackendPlugins but accepts a context for live inventory projection.
func InspectBackendPluginsWithContext(ctx context.Context, cfg *config.Config, reg *pluginreg.Registry) (PluginInspectReport, error) {
	if cfg == nil {
		return PluginInspectReport{}, fmt.Errorf("runtimebundle: InspectBackendPlugins: nil config")
	}
	staging, err := os.MkdirTemp("", "aip-plugin-inspect-*")
	if err != nil {
		return PluginInspectReport{}, fmt.Errorf("runtimebundle: InspectBackendPlugins: staging: %w", err)
	}
	resolved, err := ResolvePluginCatalog(cfg, reg, staging)
	if err != nil {
		closeVerifiedArtifacts(resolved.TrustBySafe)
		_ = removeAllRetry(staging, 8, 25*time.Millisecond)
		return PluginInspectReport{}, err
	}
	defer func() {
		closeVerifiedArtifacts(resolved.TrustBySafe)
		_ = removeAllRetry(staging, 8, 25*time.Millisecond)
	}()
	compatibleRows := standardplugins.ProjectCompatibleBackendRows(cfg)
	if live, loadErr := tryLoadInventoryLiveSnapshot(ctx, cfg, reg); loadErr == nil {
		defer func() { _ = live.Close(ctx) }()
		compatibleRows = standardplugins.ProjectCompatibleBackendRowsLive(cfg, live.compatibleInputs())
	}
	rep := diagnostics.FormatInspectReportWithCompatible(diagnostics.CatalogResolution{
		Discovered:  resolved.Discovered,
		TrustBySafe: resolved.TrustBySafe,
		Snapshot:    resolved.Snapshot,
		CatalogErr:  resolved.CatalogErr,
	}, collectNativeBuiltinKinds(reg), standardplugins.CollectBuiltinCompatibleKinds(reg), configuredBackends(cfg)).WithCompatibleBackends(compatibleRows)
	return rep, resolved.CatalogErr
}

// doctorStagedArtifact bundles a verified doctor artifact with its private
// staging root so DoctorBackendPlugin can close the staged executable handle
// before removing the root (Windows locks staged executables until
// VerifiedArtifact.Close).
type doctorStagedArtifact struct {
	Artifact   *trust.VerifiedArtifact
	StagingDir string
}

// Close releases the verified handle first, then removes the staging root.
func (d *doctorStagedArtifact) Close() error {
	if d == nil {
		return nil
	}
	var out error
	if d.Artifact != nil {
		out = errors.Join(out, d.Artifact.Close())
		d.Artifact = nil
	}
	if d.StagingDir != "" {
		dir := d.StagingDir
		d.StagingDir = ""
		out = errors.Join(out, removeAllRetry(dir, 8, 25*time.Millisecond))
	}
	return out
}

// DoctorBackendPlugin launches only the selected configured instance id.
func DoctorBackendPlugin(ctx context.Context, cfg *config.Config, reg *pluginreg.Registry, instanceID string, host *processhost.Host) (PluginDoctorReport, error) {
	if ctx == nil {
		return PluginDoctorReport{}, fmt.Errorf("runtimebundle: DoctorBackendPlugin: nil context")
	}
	if cfg == nil {
		return PluginDoctorReport{}, fmt.Errorf("runtimebundle: DoctorBackendPlugin: nil config")
	}
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return PluginDoctorReport{}, fmt.Errorf("runtimebundle: DoctorBackendPlugin: empty instance id")
	}
	if err := config.ValidateBackendInstanceBindings(cfg); err != nil {
		return PluginDoctorReport{}, err
	}
	if _, bound := cfg.Plugins.BackendDiscovery.Instances[instanceID]; bound {
		// Explicit selected-instance doctor grants delivery authority, unlike inventory.
		return doctorProvisionedBackend(ctx, cfg, instanceID)
	}

	for _, b := range cfg.Plugins.Backends {
		if b.InstanceID() != instanceID || !b.Enabled {
			continue
		}
		kind := b.FactoryID()
		if reg != nil && reg.HasBackend(kind) && !isExternalDiscoveryKind(kind, cfg.Plugins.BackendDiscovery, reg) {
			return PluginDoctorReport{Results: []diagnostics.DoctorResult{{
				InstanceID: instanceID,
				Kind:       kind,
				State:      catalog.StateConfigured,
				Reason:     "builtin",
				Guidance:   "built-in backends do not use process doctor; inspect reports registration state without launch",
			}}}, nil
		}
	}

	// Doctor owns the host it creates (never the caller's). Artifact/staging
	// ownership comes back from target resolution and is closed after the
	// operation so Windows handles are released before staging removal.
	ownsHost := host == nil
	if host == nil {
		host = processhost.NewHost(processhost.Config{})
	}

	targets, owned, err := resolveDoctorTargets(cfg, reg, []string{instanceID})
	if err != nil {
		if ownsHost {
			_ = host.Close()
		}
		return PluginDoctorReport{}, err
	}
	defer func() {
		for _, a := range owned {
			_ = a.Close()
		}
		if ownsHost {
			_ = host.Close()
		}
	}()
	return diagnostics.Doctor(ctx, diagnostics.DoctorInput{
		InstanceIDs: []string{instanceID},
		Targets:     targets,
		Host:        host,
	})
}

func collectNativeBuiltinKinds(reg *pluginreg.Registry) []string {
	all := collectBuiltinKinds(reg)
	compatible := map[string]struct{}{}
	for _, k := range standardplugins.CollectBuiltinCompatibleKinds(reg) {
		compatible[k] = struct{}{}
	}
	out := make([]string, 0, len(all))
	for _, k := range all {
		if _, ok := compatible[k]; ok {
			continue
		}
		out = append(out, k)
	}
	return out
}

func collectBuiltinKinds(reg *pluginreg.Registry) []string {
	// Prefer registry provenance so custom composition-root builtins are retained
	// and discovered exports installed later cannot self-classify as builtins.
	if reg != nil {
		if ids := reg.BuiltinBackendFactoryIDs(); len(ids) > 0 {
			return ids
		}
	}
	return append([]string(nil), standardplugins.EssentialBackendKinds()...)
}

func configuredBackends(cfg *config.Config) []diagnostics.ConfiguredBackend {
	out := make([]diagnostics.ConfiguredBackend, 0, len(cfg.Plugins.Backends))
	for _, b := range cfg.Plugins.Backends {
		out = append(out, diagnostics.ConfiguredBackend{
			InstanceID: b.InstanceID(),
			Kind:       b.FactoryID(),
			Enabled:    b.Enabled,
		})
	}
	return out
}

func resolveDoctorTargets(cfg *config.Config, reg *pluginreg.Registry, instanceIDs []string) (map[string]diagnostics.DoctorTarget, []*doctorStagedArtifact, error) {
	want := map[string]struct{}{}
	for _, id := range instanceIDs {
		want[strings.TrimSpace(id)] = struct{}{}
	}
	targets := map[string]diagnostics.DoctorTarget{}
	bd := cfg.Plugins.BackendDiscovery

	var owned []*doctorStagedArtifact
	for _, b := range cfg.Plugins.Backends {
		id := b.InstanceID()
		if _, ok := want[id]; !ok || !b.Enabled {
			continue
		}
		kind := b.FactoryID()
		if reg != nil && reg.HasBackend(kind) && !isExternalDiscoveryKind(kind, bd, reg) {
			// Built-in configured backends are not process-doctor targets.
			targets[id] = diagnostics.DoctorTarget{InstanceID: id, Kind: kind}
			continue
		}
		art, err := resolveConfiguredArtifact(bd, kind)
		if err != nil {
			targets[id] = diagnostics.DoctorTarget{InstanceID: id, Kind: kind}
			continue
		}
		if art != nil {
			owned = append(owned, art)
		}
		raw, _ := encodeOpaqueYAML(b.Config)
		targets[id] = diagnostics.DoctorTarget{
			InstanceID: id,
			Kind:       kind,
			Artifact:   art.Artifact,
			ConfigYAML: raw,
			// Doctor never preloads connector credentials; channel check uses empty secrets.
			Secrets: backendplugin.SecretBundle{},
			Model:   processhost.ProcessModelPerInstance,
		}
	}
	return targets, owned, nil
}

func isExternalDiscoveryKind(kind string, bd config.BackendDiscoveryConfig, reg *pluginreg.Registry) bool {
	if !bd.Enabled {
		return false
	}
	if reg != nil {
		return !slices.Contains(reg.BuiltinBackendFactoryIDs(), kind)
	}
	return !slices.Contains(standardplugins.EssentialBackendKinds(), kind)
}

// resolveConfiguredArtifact returns a verified doctor artifact together with
// its private staging root. The caller owns both and must Close the result so
// the staged executable handle is released before the staging root is removed.
func resolveConfiguredArtifact(bd config.BackendDiscoveryConfig, kind string) (*doctorStagedArtifact, error) {
	if !bd.Enabled {
		return nil, fmt.Errorf("discovery disabled")
	}
	res, err := discovery.Discover(diagnostics.DiscoveryFromConfig(bd))
	if err != nil {
		return nil, err
	}
	staging, err := os.MkdirTemp("", "aip-plugin-doctor-*")
	if err != nil {
		return nil, err
	}
	owned := &doctorStagedArtifact{StagingDir: staging}
	var trustFail error
	for _, d := range res.Descriptors {
		if d.Status != discovery.StatusDiscovered {
			continue
		}
		if !manifestExportsKind(d.Manifest, kind) {
			continue
		}
		tr := trust.Verify(d.Root, d.Manifest, trust.VerifyOptions{StagingDir: staging})
		if tr.Reason == trust.ReasonOK && tr.Artifact != nil {
			owned.Artifact = tr.Artifact
			return owned, nil
		}
		// Failed descriptor loop: close any verified-but-unreturned handle and
		// keep scanning so no staged executable stays locked.
		if tr.Artifact != nil {
			_ = tr.Artifact.Close()
		}
		trustFail = fmt.Errorf("trust %q: %s", d.SafeID, tr.Reason)
	}
	if err := owned.Close(); err != nil {
		return nil, err
	}
	if trustFail != nil {
		return nil, trustFail
	}
	return nil, fmt.Errorf("no trusted artifact for kind %q", kind)
}

func manifestExportsKind(m sdkmanifest.Manifest, kind string) bool {
	for _, e := range m.Exports {
		if e.Kind == kind {
			return true
		}
	}
	return false
}
