package standardplugins

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// requireEmptyFeatureYAML rejects non-empty YAML objects for bundled noop features.
// Accepted forms: absent node (Kind 0), null, empty mapping, or document wrapping those.
func requireEmptyFeatureYAML(featureID string, n yaml.Node) error {
	root := n
	switch root.Kind {
	case 0:
		return nil
	case yaml.DocumentNode:
		if len(root.Content) == 0 {
			return nil
		}
		root = *root.Content[0]
	}

	switch root.Kind {
	case 0:
		return nil
	case yaml.ScalarNode:
		if root.Tag == "!!null" {
			return nil
		}
		if root.Value == "" || root.Value == "null" {
			return nil
		}
		return fmt.Errorf("pluginreg: feature %q: config must be null or a mapping", featureID)
	case yaml.MappingNode:
		if len(root.Content) == 0 {
			return nil
		}
		key := root.Content[0].Value
		return fmt.Errorf("pluginreg: feature %q: unsupported config key %q (noop expects empty config)", featureID, key)
	default:
		return fmt.Errorf("pluginreg: feature %q: config must be a mapping or null", featureID)
	}
}
