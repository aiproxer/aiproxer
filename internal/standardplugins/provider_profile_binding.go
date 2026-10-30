package standardplugins

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/anthropic"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/compatmode"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicompat"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openresponsescompat"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/systemonecompat"
	"github.com/aiproxer/aiproxer/internal/providerprofiles"
	"gopkg.in/yaml.v3"
)

const (
	profileAnchorPrefix = "aip_profile_"
	profileTagPrefix    = "aip:provider-profile:"
)

// PreparedProviderProfiles owns the compiled bindings for one candidate. YAML
// contains only family configuration; presentation metadata carries no policy.
type PreparedProviderProfiles struct {
	*config.Config
	bindings map[string]providerprofiles.CompiledProfile
}

// BuildBackendWithLifecycle preserves registry construction for custom rows and
// applies the candidate's immutable profile binding for expanded profile rows.
// A nil receiver means no profile rows were prepared.
func (p *PreparedProviderProfiles) BuildBackendWithLifecycle(reg *pluginreg.Registry, factoryID, instanceID string, node yaml.Node, upstream *http.Client, deps pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
	if p != nil {
		if profile, ok := p.bindings[instanceID]; ok {
			if factoryID != profile.Binding.FactoryKind {
				return pluginreg.BackendBuildResult{}, fmt.Errorf("provider profile %q: family %q requires factory %q, got %q", profile.Profile.ID, profile.Binding.Family, profile.Binding.FactoryKind, factoryID)
			}
			be, err := buildProviderProfileBackendWithNode(profile, instanceID, node, upstream, deps)
			return pluginreg.BackendBuildResult{Backend: be}, err
		}
	}
	return reg.BuildBackendWithLifecycle(factoryID, instanceID, node, upstream, deps)
}

// Old generated markers are rejected, never interpreted as profile authority.
func hasProviderProfileMarker(n yaml.Node) bool {
	if strings.HasPrefix(n.Anchor, profileAnchorPrefix) || strings.HasPrefix(n.HeadComment, profileTagPrefix) {
		return true
	}
	return n.Kind == yaml.DocumentNode && len(n.Content) > 0 && n.Content[0] != nil && hasProviderProfileMarker(*n.Content[0])
}

// ProviderProfileKind is the declarative row kind used by the profile
// contribution. Keeping the identity in the composition source avoids a
// second provider-profile registry.
const ProviderProfileKind = "provider-profile"

// ExpandProviderProfileRows resolves profile references into the existing
// compatible-family factory rows. Only rows explicitly using ProviderProfileKind
// are changed; arbitrary custom-compatible rows retain their exact config.
// The operation is deterministic and performs no provider activation.
func ExpandProviderProfileRows(cfg *config.Config) (*PreparedProviderProfiles, error) {
	catalog, err := ProviderProfileCatalog()
	if err != nil {
		return nil, err
	}
	return ExpandProviderProfileRowsWithCatalog(cfg, catalog)
}

// ExpandProviderProfileRowsWithCatalog resolves profile references against the provided catalog.
func ExpandProviderProfileRowsWithCatalog(cfg *config.Config, catalog *providerprofiles.Catalog) (*PreparedProviderProfiles, error) {
	if cfg == nil {
		return nil, fmt.Errorf("provider profiles: nil config")
	}
	if catalog == nil {
		return nil, fmt.Errorf("provider profiles: nil catalog")
	}
	byID := make(map[string]providerprofiles.Profile, len(catalog.Profiles()))
	for _, profile := range catalog.Profiles() {
		byID[profile.ID] = profile
	}

	bindings := make(map[string]providerprofiles.CompiledProfile)
	rows := make([]config.PluginConfig, len(cfg.Plugins.Backends))
	copy(rows, cfg.Plugins.Backends)
	for i := range rows {
		row := &rows[i]
		if row.FactoryID() != ProviderProfileKind {
			if IsCustomCompatibleBackendKind(row.FactoryID()) {
				if hasProviderProfileMarker(row.Config) {
					return nil, fmt.Errorf("provider profile row %q: custom-compatible config reserves anchor prefix %q and comment prefix %q for expanded provider profiles; remove the marker to keep the row independent of profiles", row.InstanceID(), profileAnchorPrefix, profileTagPrefix)
				}
			}
			continue
		}
		profileID, err := profileReference(row.Config)
		if err != nil {
			return nil, fmt.Errorf("provider profile row %q: %w", row.InstanceID(), err)
		}
		profile, ok := byID[profileID]
		if !ok {
			return nil, fmt.Errorf("provider profile row %q: unknown profile %q", row.InstanceID(), profileID)
		}
		compiled, err := providerprofiles.CompileProfile(profile)
		if err != nil {
			return nil, fmt.Errorf("provider profile %q: %w", profileID, err)
		}
		var node yaml.Node
		if compiled.Binding.Family == providerprofiles.FamilyOpenResponses {
			node, err = openResponsesProfileConfigNode(profile)
		} else {
			node, err = ProfileConfigNode(profile)
		}
		if err != nil {
			return nil, err
		}

		if _, exists := bindings[row.InstanceID()]; exists {
			return nil, fmt.Errorf("provider profile row %q: duplicate instance id", row.InstanceID())
		}
		bindings[row.InstanceID()] = compiled
		row.Kind = compiled.Binding.FactoryKind
		row.Config = node
	}
	clone := *cfg
	clone.Plugins = cfg.Plugins
	clone.Plugins.Backends = rows
	return &PreparedProviderProfiles{Config: &clone, bindings: bindings}, nil
}

func profileReference(node yaml.Node) (string, error) {
	root := node
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) != 1 {
			return "", fmt.Errorf("profile config must be a mapping")
		}
		root = *root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return "", fmt.Errorf("profile config must contain profile")
	}
	var ref struct {
		Profile   string `yaml:"profile"`
		ProfileID string `yaml:"profile_id"`
	}
	if err := root.Decode(&ref); err != nil {
		return "", fmt.Errorf("decode profile reference: %w", err)
	}
	id := strings.TrimSpace(ref.Profile)
	if id == "" {
		id = strings.TrimSpace(ref.ProfileID)
	}
	if id == "" {
		return "", fmt.Errorf("profile reference is required")
	}
	return id, nil
}

// CompileProviderProfile validates and binds one profile to an existing family.
// It is pure data compilation: no credentials, network, process, or goroutine
// is involved. The returned binding is an immutable generation input.
func CompileProviderProfile(profile providerprofiles.Profile) (providerprofiles.CompiledProfile, error) {
	return providerprofiles.CompileProfile(profile)
}

type profileFamilyBuilder func(profile providerprofiles.CompiledProfile, instanceID string, node yaml.Node, upstream *http.Client, snap pluginreg.HostCredentialSnapshot) (execbackend.Backend, error)

var profileFamilyBuilders = map[providerprofiles.Family]profileFamilyBuilder{
	providerprofiles.FamilySystemOne: func(profile providerprofiles.CompiledProfile, instanceID string, node yaml.Node, upstream *http.Client, snap pluginreg.HostCredentialSnapshot) (execbackend.Backend, error) {
		headers := http.Header{}
		for _, header := range profile.Profile.Headers {
			headers.Set(header.Name, header.Value)
		}
		be, err := systemonecompat.BuildCompatibleWithSnapshot(instanceID, node, upstream, headers, snap)
		return applyProfileCapabilities(be, err, profile)
	},
	providerprofiles.FamilyOpenAIChat: func(profile providerprofiles.CompiledProfile, instanceID string, node yaml.Node, upstream *http.Client, snap pluginreg.HostCredentialSnapshot) (execbackend.Backend, error) {
		be, err := openaicompat.BuildCompatibleWithSnapshot(instanceID, profile.Binding.FactoryKind, node, upstream, openaicompat.FlavorChat, openaicompat.CompatibleTransportCaps(openaicompat.FlavorChat), profileHeaders(profile.Profile.Headers), openaicompat.CompatibleQuirks{
			ForwardPromptCacheKey:       slices.Contains(profile.Profile.Quirks, providerprofiles.QuirkOpenAIPromptCacheKey),
			ReasoningContentOnToolCalls: slices.Contains(profile.Profile.Quirks, providerprofiles.QuirkOpenAIChatReasoningContent),
			ToolOutputAdjacency:         false,
		}, snap)
		return applyProfileCapabilities(be, err, profile)
	},
	providerprofiles.FamilyOpenAIResponses: func(profile providerprofiles.CompiledProfile, instanceID string, node yaml.Node, upstream *http.Client, snap pluginreg.HostCredentialSnapshot) (execbackend.Backend, error) {
		be, err := openaicompat.BuildCompatibleWithSnapshot(instanceID, profile.Binding.FactoryKind, node, upstream, openaicompat.FlavorResponses, openaicompat.CompatibleTransportCaps(openaicompat.FlavorResponses), profileHeaders(profile.Profile.Headers), openaicompat.CompatibleQuirks{
			ForwardPromptCacheKey:       slices.Contains(profile.Profile.Quirks, providerprofiles.QuirkOpenAIPromptCacheKey),
			ReasoningContentOnToolCalls: false,
			ToolOutputAdjacency:         slices.Contains(profile.Profile.Quirks, providerprofiles.QuirkOpenAIResponsesToolAdjacency),
		}, snap)
		return applyProfileCapabilities(be, err, profile)
	},
	providerprofiles.FamilyAnthropic: func(profile providerprofiles.CompiledProfile, instanceID string, node yaml.Node, upstream *http.Client, snap pluginreg.HostCredentialSnapshot) (execbackend.Backend, error) {
		modelsPath := ""
		if slices.Contains(profile.Profile.Quirks, providerprofiles.QuirkAnthropicV1Models) {
			modelsPath = profile.Profile.Models.Path
		}
		be, err := anthropic.BuildCompatibleWithSnapshot(instanceID, profile.Binding.FactoryKind, node, upstream, modelsPath, snap)
		return applyProfileCapabilities(be, err, profile)
	},
	providerprofiles.FamilyOpenResponses: func(profile providerprofiles.CompiledProfile, instanceID string, node yaml.Node, upstream *http.Client, snap pluginreg.HostCredentialSnapshot) (execbackend.Backend, error) {
		be, err := openresponsescompat.BuildWithSnapshot(instanceID, node, upstream, snap)
		return applyProfileCapabilities(be, err, profile)
	},
}

// BuildProviderProfileBackend applies one compiled profile through the existing
// family adapter strategy.
func BuildProviderProfileBackend(
	profile providerprofiles.CompiledProfile,
	instanceID string,
	upstream *http.Client,
	deps pluginreg.BackendFactoryDeps,
) (execbackend.Backend, error) {
	return buildProviderProfileBackendWithNode(profile, instanceID, yaml.Node{}, upstream, deps)
}

func buildProviderProfileBackendWithNode(
	profile providerprofiles.CompiledProfile,
	instanceID string,
	node yaml.Node,
	upstream *http.Client,
	deps pluginreg.BackendFactoryDeps,
) (execbackend.Backend, error) {
	if instanceID == "" {
		return execbackend.Backend{}, fmt.Errorf("provider profile %q: empty instance id", profile.Profile.ID)
	}
	if upstream == nil {
		return execbackend.Backend{}, fmt.Errorf("provider profile %q: nil upstream client", profile.Profile.ID)
	}
	if node.Kind == 0 {
		var err error
		if profile.Binding.Family == providerprofiles.FamilyOpenResponses {
			node, err = openResponsesProfileConfigNode(profile.Profile)
		} else {
			node, err = ProfileConfigNode(profile.Profile)
		}
		if err != nil {
			return execbackend.Backend{}, err
		}
	}
	builder, ok := profileFamilyBuilders[profile.Binding.Family]
	if !ok {
		return execbackend.Backend{}, fmt.Errorf("provider profile %q: family %q has no compatible compiler", profile.Profile.ID, profile.Binding.Family)
	}
	return builder(profile, instanceID, node, upstream, deps.Credentials)
}

func rejectProviderProfileMarkers(base pluginreg.LifecycleBackendFactory) pluginreg.LifecycleBackendFactory {
	return func(instanceID string, n yaml.Node, upstream *http.Client, deps pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
		if hasProviderProfileMarker(n) {
			return pluginreg.BackendBuildResult{}, fmt.Errorf("custom-compatible backend %q: reserved provider-profile marker; remove the marker and use a provider-profile row for profile policy", instanceID)
		}
		return base(instanceID, n, upstream, deps)
	}
}

func applyProfileCapabilities(be execbackend.Backend, err error, profile providerprofiles.CompiledProfile) (execbackend.Backend, error) {
	if err != nil {
		return execbackend.Backend{}, err
	}
	return compatmode.ApplyCapabilityCeiling(be, profile.Capabilities), nil
}

func buildOpenResponsesProfile(profile providerprofiles.Profile, instanceID string, upstream *http.Client) (execbackend.Backend, error) {
	node, err := openResponsesProfileConfigNode(profile)
	if err != nil {
		return execbackend.Backend{}, err
	}
	return openresponsescompat.Build(instanceID, node, upstream)
}

func openResponsesProfileConfigNode(profile providerprofiles.Profile) (yaml.Node, error) {
	compiled, err := providerprofiles.CompileProfile(profile)
	if err != nil {
		return yaml.Node{}, err
	}
	caps := make([]string, 0, len(compiled.Capabilities))
	for capability := range compiled.Capabilities {
		caps = append(caps, string(capability))
	}
	slices.Sort(caps)
	value := map[string]any{
		"backend_prefix":       profile.ID,
		"profile":              openresponsescompat.DefaultProfile,
		"base_url":             profile.Endpoint.BaseURL,
		"api_key_env_var_root": profile.Auth.EnvVar,
		"capabilities":         caps,
	}
	if models := staticModelsConfigMap(profile.Models.Static); models != nil {
		value["models"] = models
	}
	if len(profile.Dialects.Item)+len(profile.Dialects.Reasoning)+len(profile.Dialects.Compaction)+len(profile.Dialects.Extensions) > 0 {
		dialects := map[string]any{}
		if len(profile.Dialects.Item) > 0 {
			items := make([]map[string]string, 0, len(profile.Dialects.Item))
			for _, d := range profile.Dialects.Item {
				m := map[string]string{"dialect": d.Dialect}
				if d.Implementor != "" {
					m["implementor"] = d.Implementor
				}
				items = append(items, m)
			}
			dialects["item"] = items
		}
		if len(profile.Dialects.Reasoning) > 0 {
			reasonings := make([]map[string]string, 0, len(profile.Dialects.Reasoning))
			for _, d := range profile.Dialects.Reasoning {
				m := map[string]string{"dialect": d.Dialect}
				if d.Implementor != "" {
					m["implementor"] = d.Implementor
				}
				reasonings = append(reasonings, m)
			}
			dialects["reasoning"] = reasonings
		}
		if len(profile.Dialects.Compaction) > 0 {
			compactions := make([]map[string]string, 0, len(profile.Dialects.Compaction))
			for _, d := range profile.Dialects.Compaction {
				m := map[string]string{"dialect": d.Dialect}
				if d.Implementor != "" {
					m["implementor"] = d.Implementor
				}
				compactions = append(compactions, m)
			}
			dialects["compaction"] = compactions
		}
		if len(profile.Dialects.Extensions) > 0 {
			exts := make([]map[string]string, 0, len(profile.Dialects.Extensions))
			for _, e := range profile.Dialects.Extensions {
				m := map[string]string{"namespace": e.Namespace, "type": e.Type}
				if e.Implementor != "" {
					m["implementor"] = e.Implementor
				}
				exts = append(exts, m)
			}
			dialects["extensions"] = exts
		}
		value["dialects"] = dialects
	}
	var node yaml.Node
	if err := node.Encode(value); err != nil {
		return yaml.Node{}, fmt.Errorf("provider profile %q: encode OpenResponses config: %w", profile.ID, err)
	}
	return node, nil
}

func profileHeaders(headers []providerprofiles.SafeHeader) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for _, header := range headers {
		out[header.Name] = header.Value
	}
	return out
}

func ProfileConfigNode(profile providerprofiles.Profile) (yaml.Node, error) {
	value := map[string]any{
		"backend_prefix":       profile.ID,
		"base_url":             profile.Endpoint.BaseURL,
		"api_key_env_var_root": profile.Auth.EnvVar,
		"tokenizer":            profile.Tokenizer.TokenizerID,
	}
	if models := staticModelsConfigMap(profile.Models.Static); models != nil {
		value["models"] = models
	}
	var node yaml.Node
	if err := node.Encode(value); err != nil {
		return yaml.Node{}, fmt.Errorf("provider profile %q: encode config: %w", profile.ID, err)
	}
	return node, nil
}

func staticModelsConfigMap(static []providerprofiles.Model) map[string]any {
	if len(static) == 0 {
		return nil
	}
	items := make([]map[string]string, 0, len(static))
	for _, model := range static {
		items = append(items, map[string]string{
			"canonical_id": model.CanonicalID,
			"native_id":    model.NativeID,
			"display_name": model.DisplayName,
		})
	}
	return map[string]any{
		"source": "inline",
		"items":  items,
	}
}
