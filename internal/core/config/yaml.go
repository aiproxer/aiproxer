package config

import "gopkg.in/yaml.v3"

// DecodeYAMLNode decodes a YAML node into a typed struct when the node is present.
func DecodeYAMLNode(n yaml.Node, into any) error {
	if n.Kind == 0 {
		return nil
	}
	return n.Decode(into)
}
