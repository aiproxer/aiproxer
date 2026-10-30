package config

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/accessmode"
	"gopkg.in/yaml.v3"
)

var backendDiscoveryKnownKeys = map[string]struct{}{
	"enabled":                  {},
	"paths":                    {},
	"strict":                   {},
	"development_mode":         {},
	"instances":                {},
	"economic_evidence_policy": {},
}

// UnmarshalYAML rejects unknown keys under plugins.backend_discovery.
func (c *BackendDiscoveryConfig) UnmarshalYAML(value *yaml.Node) error {
	decoded, err := DecodeBackendDiscovery(*value)
	if err != nil {
		return err
	}
	*c = decoded
	return nil
}

// DecodeBackendDiscovery strictly decodes a backend_discovery mapping and rejects unknown keys.
func DecodeBackendDiscovery(n yaml.Node) (BackendDiscoveryConfig, error) {
	root := n
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return BackendDiscoveryConfig{}, nil
		}
		root = *root.Content[0]
	}
	if root.Kind == 0 || (root.Kind == yaml.ScalarNode && (root.Tag == "!!null" || strings.TrimSpace(root.Value) == "" || root.Value == "null")) {
		return BackendDiscoveryConfig{}, nil
	}
	if root.Kind != yaml.MappingNode {
		return BackendDiscoveryConfig{}, fmt.Errorf("plugins.backend_discovery: must be a mapping")
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(root.Content); i += 2 {
		k := root.Content[i].Value
		if root.Content[i].Kind != yaml.ScalarNode || root.Content[i].Tag != "!!str" || seen[k] {
			return BackendDiscoveryConfig{}, provisioningError()
		}
		seen[k] = true
		if _, ok := backendDiscoveryKnownKeys[k]; !ok {
			return BackendDiscoveryConfig{}, fmt.Errorf("plugins.backend_discovery: unknown config key")
		}
	}
	var instances map[string]BackendInstanceProvisioning
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "instances" {
			var err error
			instances, err = decodeBackendInstances(root.Content[i+1])
			if err != nil {
				return BackendDiscoveryConfig{}, err
			}
		}
	}
	var raw struct {
		Enabled                bool     `yaml:"enabled"`
		Paths                  []string `yaml:"paths"`
		Strict                 bool     `yaml:"strict"`
		DevelopmentMode        bool     `yaml:"development_mode"`
		EconomicEvidencePolicy string   `yaml:"economic_evidence_policy"`
	}
	if err := root.Decode(&raw); err != nil {
		return BackendDiscoveryConfig{}, fmt.Errorf("plugins.backend_discovery: invalid field type")
	}
	policy, err := parseConnectorEconomicEvidencePolicy(raw.EconomicEvidencePolicy, backendDiscoveryKeyPresent(root, "economic_evidence_policy"))
	if err != nil {
		return BackendDiscoveryConfig{}, fmt.Errorf("plugins.backend_discovery.economic_evidence_policy: %w", err)
	}
	return BackendDiscoveryConfig{
		Enabled:                raw.Enabled,
		Paths:                  append([]string(nil), raw.Paths...),
		Strict:                 raw.Strict,
		DevelopmentMode:        raw.DevelopmentMode,
		Instances:              instances,
		EconomicEvidencePolicy: policy,
	}, nil
}

// connectorEconomicEvidencePolicies is the closed set of accepted values of
// the plugins.backend_discovery.economic_evidence_policy key. The canonical
// typed policy lives in internal/core/execbackend; this package cannot import
// it without an import cycle, so the closed set is mirrored here and the two
// sets are pinned by a guard test in internal/core/execbackend.
var connectorEconomicEvidencePolicies = map[string]struct{}{
	"degrade": {},
	"strict":  {},
}

// parseConnectorEconomicEvidencePolicy validates one configured policy value.
// An absent key selects the default; a present key must name an exact member
// of the closed set.
func parseConnectorEconomicEvidencePolicy(raw string, present bool) (string, error) {
	value := strings.TrimSpace(raw)
	if !present {
		return "degrade", nil
	}
	if _, ok := connectorEconomicEvidencePolicies[value]; !ok {
		return "", fmt.Errorf("want degrade or strict, got %q", raw)
	}
	return value, nil
}

// backendDiscoveryKeyPresent reports whether a key is present in the decoded
// mapping, so an absent key and an explicitly empty value stay distinguishable.
func backendDiscoveryKeyPresent(root yaml.Node, key string) bool {
	if root.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			return true
		}
	}
	return false
}

// ValidateBackendDiscovery enforces path and production development_mode rules.
func ValidateBackendDiscovery(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config: nil")
	}
	bd := cfg.Plugins.BackendDiscovery
	if err := ValidateBackendInstanceBindings(cfg); err != nil {
		return err
	}
	for i, p := range bd.Paths {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("plugins.backend_discovery.paths[%d]: empty path", i)
		}
	}
	if !bd.DevelopmentMode {
		return nil
	}
	if len(bd.Paths) == 0 {
		return fmt.Errorf("plugins.backend_discovery.paths: required when development_mode is true")
	}
	mode, err := cfg.EffectiveAccessMode()
	if err != nil {
		return fmt.Errorf("plugins.backend_discovery: %w", err)
	}
	if mode == accessmode.ModeMultiUser {
		return fmt.Errorf("plugins.backend_discovery.development_mode: not allowed when access.mode is multi_user")
	}
	return nil
}

var (
	backendBindingName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	backendEnvRefName  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
)

// BackendExecutionEnvName is the fixed non-secret execution-context allowlist.
func BackendExecutionEnvName(name string) bool {
	switch name {
	case "HOME", "PATH", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "SYSTEMROOT", "TEMP", "TMP", "TMPDIR", "LANG", "LC_ALL", "XDG_CONFIG_HOME", "ANTHROPIC_CONFIG_DIR", "CLAUDE_CONFIG_DIR":
		return true
	}
	return false
}

func provisioningError() error {
	return fmt.Errorf("plugins.backend_discovery.instances: invalid schema or binding")
}

// checkProvisioningNode prevents YAML coercion, aliases, merges and duplicates.
func checkProvisioningNode(n *yaml.Node) error {
	switch n.Kind {
	case yaml.MappingNode:
		if len(n.Content)%2 != 0 {
			return provisioningError()
		}
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode || k.Tag != "!!str" || k.Value == "<<" || seen[k.Value] {
				return provisioningError()
			}
			seen[k.Value] = true
			if err := checkProvisioningNode(n.Content[i+1]); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for _, v := range n.Content {
			if err := checkProvisioningNode(v); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		if n.Tag != "!!str" || strings.ContainsRune(n.Value, 0) {
			return provisioningError()
		}
	default:
		return provisioningError()
	}
	return nil
}

func decodeBackendInstances(n *yaml.Node) (map[string]BackendInstanceProvisioning, error) {
	if n.Kind != yaml.MappingNode || len(n.Content)/2 > 64 {
		return nil, provisioningError()
	}
	if err := checkProvisioningNode(n); err != nil {
		return nil, err
	}
	for i := 1; i < len(n.Content); i += 2 {
		binding := n.Content[i]
		if binding.Kind != yaml.MappingNode {
			return nil, provisioningError()
		}
		for j := 0; j < len(binding.Content); j += 2 {
			v := binding.Content[j+1]
			switch binding.Content[j].Value {
			case "kind":
				if v.Kind != yaml.ScalarNode {
					return nil, provisioningError()
				}
			case "execution_env":
				if v.Kind != yaml.SequenceNode {
					return nil, provisioningError()
				}
				for _, name := range v.Content {
					if name.Kind != yaml.ScalarNode {
						return nil, provisioningError()
					}
				}
			case "secret_refs":
				if v.Kind != yaml.MappingNode {
					return nil, provisioningError()
				}
				for k := 1; k < len(v.Content); k += 2 {
					ref := v.Content[k]
					if ref.Kind != yaml.MappingNode || len(ref.Content) != 2 || ref.Content[0].Value != "env" || ref.Content[1].Kind != yaml.ScalarNode {
						return nil, provisioningError()
					}
				}
			default:
				return nil, provisioningError()
			}
		}
	}
	var out map[string]BackendInstanceProvisioning
	if err := n.Decode(&out); err != nil {
		return nil, provisioningError()
	}
	if err := validateBackendInstanceNames(out); err != nil {
		return nil, err
	}
	return out, nil
}

func validateBackendInstanceNames(bindings map[string]BackendInstanceProvisioning) error {
	if len(bindings) > 64 {
		return provisioningError()
	}
	for id, b := range bindings {
		if !backendBindingName.MatchString(id) || !backendBindingName.MatchString(b.Kind) || len(b.SecretRefs) > 32 || len(b.ExecutionEnv) > 16 {
			return provisioningError()
		}
		for name, ref := range b.SecretRefs {
			if !backendBindingName.MatchString(name) || !backendEnvRefName.MatchString(ref.Env) {
				return provisioningError()
			}
		}
		seen := map[string]bool{}
		for _, name := range b.ExecutionEnv {
			if !BackendExecutionEnvName(name) || seen[strings.ToUpper(name)] {
				return provisioningError()
			}
			seen[strings.ToUpper(name)] = true
		}
	}
	return nil
}

// ValidateBackendInstanceBindings also validates disabled rows without resolving inputs.
func ValidateBackendInstanceBindings(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config: nil")
	}
	bindings := cfg.Plugins.BackendDiscovery.Instances
	if err := validateBackendInstanceNames(bindings); err != nil {
		return err
	}
	for id, binding := range bindings {
		matches := 0
		for _, b := range cfg.Plugins.Backends {
			if b.InstanceID() == id {
				if b.FactoryID() != binding.Kind {
					return provisioningError()
				}
				matches++
			}
		}
		if matches != 1 {
			return provisioningError()
		}
	}
	return nil
}
