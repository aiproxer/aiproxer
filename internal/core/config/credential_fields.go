package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// forbiddenBackendCredentialKeys is the closed set of secret-bearing YAML
// mapping keys rejected in backend/plugin opaque config subtrees
// (requirements 2.1, 5.4). It covers legacy singular, list and structured
// key fields plus token/PAT/private-key aliases. Matching is exact:
// YAML decoding is case-sensitive, so differently-cased keys never bind to
// credential sinks; they are still dead config, but they cannot deliver a
// secret. The OAuth/workload-classified bedrock factory does not invoke this
// scanner; its access_key_id/secret_access_key/session_token fields stay
// behind that explicit workload mode (requirement 2.4).
var forbiddenBackendCredentialKeys = map[string]struct{}{
	"api_key":               {},
	"api_keys":              {},
	"credentials":           {},
	"credential":            {},
	"access_token":          {},
	"api_token":             {},
	"auth_token":            {},
	"bearer_token":          {},
	"personal_access_token": {},
	"pat":                   {},
	"private_key":           {},
	"client_secret":         {},
}

// secretReferencePrefixes names scalar value shapes that are never valid
// inside a backend config subtree: backend YAML has no secret-path loader,
// so a file:/vault:/dotenv:/env: value is either a misunderstood credential
// alias or a literal bug. Both fail closed with the field path.
var secretReferencePrefixes = []string{"file:", "vault:", "dotenv:", "env:"}

// RejectBackendStaticCredentials scans one backend's opaque config subtree
// before typed decode and delivery, rejecting legacy static-credential fields
// at any nesting depth (including structured credential items and YAML
// merge/alias expansions), dollar-brace interpolation aliases and secret-path
// values. scope identifies the backend (for example "openairesponses backend
// config" or `compatible backend instance "id" (factory "kind")`);
// approvedEnvRoot is the nonsecret environment root the operator must use
// instead (empty when the backend binds no root, e.g. no-auth compatible
// endpoints). Errors report the backend field path plus the env root only and
// never echo configured values.
func RejectBackendStaticCredentials(scope, approvedEnvRoot string, n yaml.Node) error {
	root := n
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return nil
		}
		root = *root.Content[0]
	}
	if root.Kind == 0 || isNullScalar(root) {
		return nil
	}
	return scanCredentialNode(scope, credentialSourceHint(approvedEnvRoot), "config", root)
}

func credentialSourceHint(approvedEnvRoot string) string {
	if strings.TrimSpace(approvedEnvRoot) != "" {
		return "host environment root " + strings.TrimSpace(approvedEnvRoot)
	}
	return "an api_key_env_var_root host environment binding (or omit credentials for no-auth endpoints)"
}

func isNullScalar(n yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && (n.Tag == "!!null" || strings.TrimSpace(n.Value) == "" || n.Value == "null")
}

func scanCredentialNode(scope, hint, path string, n yaml.Node) error {
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			keyNode := n.Content[i]
			valueNode := n.Content[i+1]
			if keyNode.Kind != yaml.ScalarNode {
				continue
			}
			key := keyNode.Value
			if key == "<<" {
				// YAML merge key: expand the aliased/inline mapping so a
				// merged credential field cannot bypass the scan.
				merged := *valueNode
				if valueNode.Kind == yaml.AliasNode && valueNode.Alias != nil {
					merged = *valueNode.Alias
				}
				if merged.Kind == yaml.MappingNode {
					if err := scanCredentialNode(scope, hint, path, merged); err != nil {
						return err
					}
				}
				continue
			}
			fieldPath := path + "." + key
			if _, forbidden := forbiddenBackendCredentialKeys[key]; forbidden {
				return fmt.Errorf("%s: forbidden config key %q (static credentials must come from %s; never put values in config)", scope, fieldPath, hint)
			}
			if err := scanCredentialNode(scope, hint, fieldPath, *valueNode); err != nil {
				return err
			}
		}
		return nil
	case yaml.SequenceNode:
		for i := range n.Content {
			if err := scanCredentialNode(scope, hint, fmt.Sprintf("%s[%d]", path, i), *n.Content[i]); err != nil {
				return err
			}
		}
		return nil
	case yaml.AliasNode:
		if n.Alias == nil {
			return nil
		}
		return scanCredentialNode(scope, hint, path, *n.Alias)
	case yaml.ScalarNode:
		value := n.Value
		if strings.Contains(value, "${") {
			return fmt.Errorf("%s: forbidden secret reference at %q (backend config never interpolates ${...}; static credentials must come from %s)", scope, path, hint)
		}
		trimmed := strings.TrimSpace(value)
		lowered := strings.ToLower(trimmed)
		for _, prefix := range secretReferencePrefixes {
			if strings.HasPrefix(lowered, prefix) {
				return fmt.Errorf("%s: forbidden secret path at %q (backend config never reads %s paths; static credentials must come from %s)", scope, path, prefix, hint)
			}
		}
		return nil
	default:
		return nil
	}
}
