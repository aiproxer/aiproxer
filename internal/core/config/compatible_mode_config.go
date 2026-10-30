package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// CompatibleModeConfig is the strict, secret-free configuration surface for the
// three built-in compatible backend kinds. Successful decoding never retains
// literal credential values.
type CompatibleModeConfig struct {
	BackendPrefix         string
	BaseURL               string
	APIKeyEnvVarRoot      string
	TokenizerID           string
	MaxConcurrentRequests int
	Models                CompatibleModeModelsConfig
}

// CompatibleModeModelsConfig is the optional static/shared inventory subtree.
type CompatibleModeModelsConfig struct {
	Source string
	Path   string
	Items  []CompatibleModeModelItem
}

// CompatibleModeModelItem is one static inventory row.
type CompatibleModeModelItem struct {
	CanonicalID string
	NativeID    string
	DisplayName string
}

var compatibleModeKnownKeys = map[string]struct{}{
	"backend_prefix":          {},
	"base_url":                {},
	"api_key_env_var_root":    {},
	"tokenizer":               {},
	"max_concurrent_requests": {},
	"models":                  {},
}

var compatibleModeModelsKnownKeys = map[string]struct{}{
	"source": {},
	"path":   {},
	"items":  {},
}

var compatibleModeModelItemKnownKeys = map[string]struct{}{
	"canonical_id": {},
	"native_id":    {},
	"display_name": {},
}

// DecodeCompatibleModeConfig strictly decodes opaque compatible-mode YAML.
// Strictness is scoped to this decoder: it validates the mapping key set before
// typed decode and does not change repository-wide DecodeYAMLNode behavior.
// Errors are instance-scoped and never echo literal secret values.
func DecodeCompatibleModeConfig(instanceID, factoryKind string, n yaml.Node) (CompatibleModeConfig, error) {
	scope := compatibleModeScope(instanceID, factoryKind)
	root := n
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return CompatibleModeConfig{}, fmt.Errorf("%s: config is required", scope)
		}
		root = *root.Content[0]
	}
	if root.Kind == 0 || (root.Kind == yaml.ScalarNode && (root.Tag == "!!null" || strings.TrimSpace(root.Value) == "" || root.Value == "null")) {
		return CompatibleModeConfig{}, fmt.Errorf("%s: config is required", scope)
	}
	if root.Kind != yaml.MappingNode {
		return CompatibleModeConfig{}, fmt.Errorf("%s: config must be a mapping", scope)
	}

	// Central static-credential guard (requirements 2.1, 5.4): reject legacy
	// singular/list/structured/token/PAT/private-key fields at any depth plus
	// interpolation aliases and secret paths before typed decode. The approved
	// env root is probed first so the error names the exact binding to use.
	var probe struct {
		APIKeyEnvVarRoot string `yaml:"api_key_env_var_root"`
	}
	_ = root.Decode(&probe)
	if err := RejectBackendStaticCredentials(scope, strings.TrimSpace(probe.APIKeyEnvVarRoot), root); err != nil {
		return CompatibleModeConfig{}, err
	}

	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i].Value
		if _, ok := compatibleModeKnownKeys[key]; !ok {
			return CompatibleModeConfig{}, fmt.Errorf("%s: unknown config key %q", scope, key)
		}
	}

	var raw struct {
		BackendPrefix         string    `yaml:"backend_prefix"`
		BaseURL               string    `yaml:"base_url"`
		APIKeyEnvVarRoot      string    `yaml:"api_key_env_var_root"`
		Tokenizer             string    `yaml:"tokenizer"`
		MaxConcurrentRequests *int      `yaml:"max_concurrent_requests"`
		Models                yaml.Node `yaml:"models"`
	}
	if err := root.Decode(&raw); err != nil {
		return CompatibleModeConfig{}, fmt.Errorf("%s: %w", scope, err)
	}

	prefix := strings.TrimSpace(raw.BackendPrefix)
	if prefix == "" {
		return CompatibleModeConfig{}, fmt.Errorf("%s: backend_prefix is required", scope)
	}
	baseURL := strings.TrimSpace(raw.BaseURL)
	if baseURL == "" {
		return CompatibleModeConfig{}, fmt.Errorf("%s: base_url is required", scope)
	}
	maxConcurrent := 0
	if raw.MaxConcurrentRequests != nil {
		if *raw.MaxConcurrentRequests < 0 {
			return CompatibleModeConfig{}, fmt.Errorf("%s: max_concurrent_requests must be non-negative", scope)
		}
		maxConcurrent = *raw.MaxConcurrentRequests
	}

	envRoot := strings.TrimSpace(raw.APIKeyEnvVarRoot)
	if envRoot != "" {
		if err := ValidateCredentialEnvRoot(envRoot); err != nil {
			return CompatibleModeConfig{}, fmt.Errorf("%s: api_key_env_var_root: %w", scope, err)
		}
	}

	models, err := decodeCompatibleModeModels(scope, raw.Models)
	if err != nil {
		return CompatibleModeConfig{}, err
	}

	return CompatibleModeConfig{
		BackendPrefix:         prefix,
		BaseURL:               baseURL,
		APIKeyEnvVarRoot:      envRoot,
		TokenizerID:           strings.TrimSpace(raw.Tokenizer),
		MaxConcurrentRequests: maxConcurrent,
		Models:                models,
	}, nil
}

func decodeCompatibleModeModels(scope string, n yaml.Node) (CompatibleModeModelsConfig, error) {
	if n.Kind == 0 || (n.Kind == yaml.ScalarNode && (n.Tag == "!!null" || strings.TrimSpace(n.Value) == "" || n.Value == "null")) {
		return CompatibleModeModelsConfig{}, nil
	}
	if n.Kind != yaml.MappingNode {
		return CompatibleModeModelsConfig{}, fmt.Errorf("%s: models must be a mapping", scope)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i].Value
		if _, ok := compatibleModeModelsKnownKeys[key]; !ok {
			return CompatibleModeModelsConfig{}, fmt.Errorf("%s: unknown config key %q", scope, "models."+key)
		}
		if key == "items" {
			items := n.Content[i+1]
			if items.Kind != yaml.SequenceNode {
				return CompatibleModeModelsConfig{}, fmt.Errorf("%s: models.items must be a sequence", scope)
			}
			for itemIndex, item := range items.Content {
				if item.Kind != yaml.MappingNode {
					return CompatibleModeModelsConfig{}, fmt.Errorf("%s: models.items[%d] must be a mapping", scope, itemIndex)
				}
				for j := 0; j+1 < len(item.Content); j += 2 {
					itemKey := item.Content[j].Value
					if _, ok := compatibleModeModelItemKnownKeys[itemKey]; !ok {
						return CompatibleModeModelsConfig{}, fmt.Errorf("%s: unknown config key %q", scope, fmt.Sprintf("models.items[%d].%s", itemIndex, itemKey))
					}
				}
			}
		}
	}
	var raw struct {
		Source string `yaml:"source"`
		Path   string `yaml:"path"`
		Items  []struct {
			CanonicalID string `yaml:"canonical_id"`
			NativeID    string `yaml:"native_id"`
			DisplayName string `yaml:"display_name"`
		} `yaml:"items"`
	}
	if err := n.Decode(&raw); err != nil {
		return CompatibleModeModelsConfig{}, fmt.Errorf("%s: models: %w", scope, err)
	}
	items := make([]CompatibleModeModelItem, 0, len(raw.Items))
	for _, item := range raw.Items {
		items = append(items, CompatibleModeModelItem{
			CanonicalID: strings.TrimSpace(item.CanonicalID),
			NativeID:    strings.TrimSpace(item.NativeID),
			DisplayName: strings.TrimSpace(item.DisplayName),
		})
	}
	return CompatibleModeModelsConfig{
		Source: strings.TrimSpace(raw.Source),
		Path:   strings.TrimSpace(raw.Path),
		Items:  items,
	}, nil
}

func compatibleModeScope(instanceID, factoryKind string) string {
	id := strings.TrimSpace(instanceID)
	if id == "" {
		id = "<unknown>"
	}
	kind := strings.TrimSpace(factoryKind)
	if kind == "" {
		kind = "<unknown>"
	}
	return fmt.Sprintf("compatible backend instance %q (factory %q)", id, kind)
}
