package standardplugins

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/identity"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/httpidentity"
	"gopkg.in/yaml.v3"
)

// decodeIdentityOverride strictly decodes the backend `identity` subtree.
// Unknown keys (including flat app_url/app_title) fail with field-qualified errors.
// The rest of the backend YAML remains non-strict.
func decodeIdentityOverride(n yaml.Node) (*identity.BackendOverride, error) {
	root := yamlRootMapping(&n)
	if root == nil {
		return nil, nil
	}
	child, ok := yamlMappingChild(root, "identity")
	if !ok {
		return nil, nil
	}
	return decodeBackendOverrideStrict(child, "identity")
}

func decodeBackendOverrideStrict(n *yaml.Node, path string) (*identity.BackendOverride, error) {
	if n == nil || isYAMLNull(n) {
		return nil, nil
	}
	if n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: want mapping", path)
	}
	var ov identity.BackendOverride
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i].Value
		val := n.Content[i+1]
		switch key {
		case "user_agent":
			fp, err := decodeFieldPolicyStrict(val, path+".user_agent")
			if err != nil {
				return nil, err
			}
			ov.UserAgent = &fp
		case "openrouter":
			or, err := decodeOpenRouterOverrideStrict(val, path+".openrouter")
			if err != nil {
				return nil, err
			}
			ov.OpenRouter = or
		default:
			return nil, fmt.Errorf("%s.%s: unknown key; allowed: user_agent, openrouter", path, key)
		}
	}
	return &ov, nil
}

func decodeOpenRouterOverrideStrict(n *yaml.Node, path string) (*identity.OpenRouterOverride, error) {
	if n == nil || isYAMLNull(n) {
		return nil, nil
	}
	if n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: want mapping", path)
	}
	var or identity.OpenRouterOverride
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i].Value
		val := n.Content[i+1]
		switch key {
		case "app_url":
			fp, err := decodeFieldPolicyStrict(val, path+".app_url")
			if err != nil {
				return nil, err
			}
			or.AppURL = &fp
		case "app_title":
			fp, err := decodeFieldPolicyStrict(val, path+".app_title")
			if err != nil {
				return nil, err
			}
			or.AppTitle = &fp
		default:
			return nil, fmt.Errorf("%s.%s: unknown key; allowed: app_url, app_title", path, key)
		}
	}
	return &or, nil
}

func decodeFieldPolicyStrict(n *yaml.Node, path string) (identity.FieldPolicy, error) {
	if n == nil || isYAMLNull(n) {
		return identity.FieldPolicy{}, fmt.Errorf("%s: want mapping", path)
	}
	if n.Kind != yaml.MappingNode {
		return identity.FieldPolicy{}, fmt.Errorf("%s: want mapping", path)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i].Value
		switch key {
		case "mode", "value":
		default:
			return identity.FieldPolicy{}, fmt.Errorf("%s.%s: unknown key; allowed: mode, value", path, key)
		}
	}
	var fp identity.FieldPolicy
	if err := n.Decode(&fp); err != nil {
		return identity.FieldPolicy{}, fmt.Errorf("%s: %w", path, err)
	}
	return fp, nil
}

func yamlRootMapping(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	root := n
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return nil
		}
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return nil
	}
	return root
}

func yamlMappingChild(n *yaml.Node, key string) (*yaml.Node, bool) {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil, false
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1], true
		}
	}
	return nil, false
}

func isYAMLNull(n *yaml.Node) bool {
	if n == nil || n.Kind == 0 {
		return true
	}
	if n.Kind == yaml.ScalarNode && (n.Tag == "!!null" || strings.TrimSpace(n.Value) == "" || n.Value == "null") {
		return true
	}
	return false
}

// resolveIdentityHTTP validates an optional backend override, validates a copy of
// the proxy-wide policy (caller's global is never mutated), merges, and wraps
// upstream with the final-wire User-Agent transport.
func resolveIdentityHTTP(upstream *http.Client, global identity.Config, n yaml.Node, errPrefix string) (*http.Client, error) {
	ov, err := decodeIdentityOverride(n)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", errPrefix, err)
	}
	if err := identity.ValidateBackendOverride(ov); err != nil {
		return nil, fmt.Errorf("%s: %w", errPrefix, err)
	}
	g := global
	if err := identity.Validate(&g); err != nil {
		return nil, fmt.Errorf("%s: %w", errPrefix, err)
	}
	eff := identity.MergeUpstream(g, ov)
	return httpidentity.WrapClient(resolveUpstreamHTTP(upstream), eff.UserAgent), nil
}
