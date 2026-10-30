package standardplugins

import (
	"sort"
	"strconv"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/infra/endpoint"
	"github.com/aiproxer/aiproxer/internal/infra/tokenizers"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openresponsescompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const compatibleOriginBuiltIn = "built_in_compatible"

// CompatibleBackendKinds returns the stable built-in compatible factory ids.
func CompatibleBackendKinds() []string {
	views, err := DerivedViews()
	if err != nil {
		return nil
	}
	return append([]string(nil), views.CompatibleIDs...)
}

// ProjectCompatibleBackendRows builds secret-safe diagnostics rows from config only.
// No provider requests, plugin activation, or credential value resolution occur.
func ProjectCompatibleBackendRows(cfg *config.Config) []diag.CompatibleBackendRow {
	if cfg == nil {
		return nil
	}
	out := make([]diag.CompatibleBackendRow, 0)
	for _, row := range cfg.Plugins.Backends {
		kind := row.FactoryID()
		if !IsCustomCompatibleBackendKind(kind) {
			continue
		}
		entry := diag.CompatibleBackendRow{
			Origin:      compatibleOriginBuiltIn,
			InstanceID:  row.InstanceID(),
			FactoryKind: kind,
			Enabled:     row.Enabled,
		}
		if IsOpenResponsesCompatibleBackendKind(kind) {
			projectOpenResponsesCompatibleRow(&entry, row)
			out = append(out, entry)
			continue
		}
		decoded, err := config.DecodeCompatibleModeConfig(row.InstanceID(), kind, row.Config)
		if err != nil {
			entry.ConfigError = err.Error()
			out = append(out, entry)
			continue
		}
		if ep, err := endpoint.ParseBaseURL(decoded.BaseURL); err != nil {
			entry.ConfigError = err.Error()
			out = append(out, entry)
			continue
		} else {
			entry.EndpointIdentity = ep.BaseURL()
		}
		entry.Prefix = strings.TrimSpace(decoded.BackendPrefix)
		entry.AuthConfigured = strings.TrimSpace(decoded.APIKeyEnvVarRoot) != ""
		if _, id, err := tokenizers.ResolveCompatibleID(decoded.TokenizerID); err != nil {
			entry.ConfigError = err.Error()
			out = append(out, entry)
			continue
		} else if id != "" {
			entry.TokenizerID = id
		}
		entry.ConcurrencyPolicy = compatibleConcurrencyPolicy(decoded.MaxConcurrentRequests)
		entry.InventoryState = compatibleInventoryState(decoded.Models)
		out = append(out, entry)
	}
	return out
}

func compatibleConcurrencyPolicy(maxConcurrent int) string {
	if maxConcurrent <= 0 {
		return "default"
	}
	return "limit:" + strconv.Itoa(maxConcurrent)
}

func compatibleInventoryState(models config.CompatibleModeModelsConfig) string {
	switch strings.TrimSpace(strings.ToLower(models.Source)) {
	case "inline":
		if len(models.Items) > 0 {
			return "static_inline"
		}
		return "static_inline_empty"
	case "file":
		if strings.TrimSpace(models.Path) != "" {
			return "static_file"
		}
		return "static_file_unconfigured"
	default:
		return "remote"
	}
}

func projectOpenResponsesCompatibleRow(entry *diag.CompatibleBackendRow, row config.PluginConfig) {
	cfg, err := openresponsescompat.DecodeConfig(row.InstanceID(), row.FactoryID(), row.Config)
	if err != nil {
		entry.ConfigError = err.Error()
		return
	}
	ep, err := endpoint.ParseBaseURL(cfg.BaseURL)
	if err != nil {
		entry.ConfigError = err.Error()
		return
	}
	entry.EndpointIdentity = ep.BaseURL()
	entry.Prefix = strings.TrimSpace(cfg.BackendPrefix)
	entry.AuthConfigured = strings.TrimSpace(cfg.APIKeyEnvVarRoot) != ""
	entry.Profile = strings.TrimSpace(cfg.Profile)
	entry.Capabilities = sanitizedOpenResponsesCapabilities(cfg.Capabilities)
	entry.Conformance = "profile:" + strings.TrimSpace(cfg.Profile)
	entry.InventoryState = compatibleInventoryState(cfg.Models)
}

// sanitizedOpenResponsesCapabilities returns a deterministic, deduplicated,
// sanitized list of declared semantic capabilities for OpenResponses-compatible
// backends.
func sanitizedOpenResponsesCapabilities(in []aipapi.Capability) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, c := range in {
		name := sanitizedOpenResponsesString(string(c))
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// CollectBuiltinCompatibleKinds returns registered factory ids with built-in-compatible provenance.
func CollectBuiltinCompatibleKinds(reg *pluginreg.Registry) []string {
	if reg == nil {
		return append([]string(nil), CompatibleBackendKinds()...)
	}
	out := make([]string, 0, len(CompatibleBackendKinds()))
	for _, kind := range reg.BackendFactoryIDs() {
		source, ok := reg.BackendRegistrationSource(kind)
		if ok && source == pluginreg.BackendSourceBuiltinCompatible {
			out = append(out, kind)
		}
	}
	if len(out) == 0 {
		return append([]string(nil), CompatibleBackendKinds()...)
	}
	return out
}

// ProjectCompatibleBackendInstanceRows projects compatible backend rows as common instance diagnostics.
func ProjectCompatibleBackendInstanceRows(cfg *config.Config) []diag.InstanceDiagnostic {
	return CompatibleBackendInstanceRows(ProjectCompatibleBackendRows(cfg))
}

// CompatibleBackendInstanceRows converts already-projected rows without
// decoding configuration again. Composition roots should use this when they
// expose both compatible_backends and instance_diagnostics.
func CompatibleBackendInstanceRows(rows []diag.CompatibleBackendRow) []diag.InstanceDiagnostic {
	return diag.CompatibleBackendInstanceDiagnostics(rows)
}
