package legacyfeatureconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// CanonicalID is the feature identifier in plugins.features for interleaved-thinking.
const CanonicalID = "interleaved-thinking"

// KeepwarmCanonicalID is the feature identifier in plugins.features for keepwarm.
const KeepwarmCanonicalID = "keepwarm"

// NormalizeYAML takes raw YAML configuration bytes and normalizes any legacy
// top-level 'interleaved:' or 'prompt_cache:' configuration into canonical 'plugins.features' entries.
// If both legacy and canonical entries exist, a deterministic conflict error is returned.
func NormalizeYAML(raw []byte) ([]byte, error) {
	if !bytes.Contains(raw, []byte("interleaved")) && !bytes.Contains(raw, []byte("prompt_cache")) {
		return raw, nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var root yaml.Node
	if err := dec.Decode(&root); err != nil {
		return raw, nil
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		// Either a genuine second document (err == nil) or malformed
		// trailing content (any other decode error): return the original
		// bytes unchanged so StrictDecode can reject/classify them
		// (multiple-documents / trailing-content) instead of silently
		// normalizing just the first document.
		return raw, nil
	}
	doc := &root
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		doc = doc.Content[0]
	}
	hasLegacy := false
	if doc.Kind == yaml.MappingNode {
		for i := 0; i < len(doc.Content); i += 2 {
			if doc.Content[i].Value == "interleaved" || doc.Content[i].Value == "prompt_cache" {
				hasLegacy = true
				break
			}
		}
	}
	if !hasLegacy {
		return raw, nil
	}
	if err := NormalizeNode(&root); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return nil, fmt.Errorf("legacyfeatureconfig: encode yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("legacyfeatureconfig: close encoder: %w", err)
	}
	return buf.Bytes(), nil
}

func nodeTag(node *yaml.Node) string {
	if node == nil {
		return "nil"
	}
	tag := node.Tag
	if tag == "" {
		switch node.Kind {
		case yaml.ScalarNode:
			tag = "scalar"
		case yaml.SequenceNode:
			tag = "sequence"
		case yaml.MappingNode:
			tag = "mapping"
		default:
			tag = fmt.Sprintf("kind %d", node.Kind)
		}
	}
	return tag
}

// NormalizeNode mutates the given YAML root AST in-place, converting any legacy
// top-level 'interleaved:' or 'prompt_cache:' mapping into a canonical 'plugins.features' entry.
// If both are present, a conflict error is returned.
func NormalizeNode(root *yaml.Node) error {
	if root == nil {
		return nil
	}
	doc := root
	if doc.Kind == yaml.DocumentNode {
		if len(doc.Content) == 0 {
			return nil
		}
		doc = doc.Content[0]
	}
	if doc.Kind != yaml.MappingNode {
		return nil
	}

	var pluginsNode *yaml.Node
	var featuresNode *yaml.Node
	hasCanonicalInterleaved := false
	hasCanonicalKeepwarm := false

	for i := 0; i < len(doc.Content); i += 2 {
		if doc.Content[i].Value == "plugins" {
			pluginsNode = doc.Content[i+1]
			break
		}
	}

	if pluginsNode != nil && pluginsNode.Kind == yaml.MappingNode {
		for i := 0; i < len(pluginsNode.Content); i += 2 {
			if pluginsNode.Content[i].Value == "features" {
				featuresNode = pluginsNode.Content[i+1]
				break
			}
		}
	}

	if featuresNode != nil && featuresNode.Kind == yaml.SequenceNode {
		for _, item := range featuresNode.Content {
			if item.Kind == yaml.MappingNode {
				for j := 0; j < len(item.Content); j += 2 {
					if item.Content[j].Value == "id" {
						switch item.Content[j+1].Value {
						case CanonicalID:
							hasCanonicalInterleaved = true
						case KeepwarmCanonicalID:
							hasCanonicalKeepwarm = true
						}
					}
				}
			}
		}
	}

	ensureFeaturesNode := func() {
		if pluginsNode == nil {
			pluginsNode = &yaml.Node{
				Kind:    yaml.MappingNode,
				Tag:     "!!map",
				Content: []*yaml.Node{},
			}
			doc.Content = append(doc.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "plugins"},
				pluginsNode,
			)
		}
		if featuresNode == nil {
			featuresNode = &yaml.Node{
				Kind:    yaml.SequenceNode,
				Tag:     "!!seq",
				Content: []*yaml.Node{},
			}
			pluginsNode.Content = append(pluginsNode.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "features"},
				featuresNode,
			)
		}
	}

	removeDocKey := func(key string) {
		for i := 0; i < len(doc.Content); i += 2 {
			if doc.Content[i].Value == key {
				doc.Content = append(doc.Content[:i], doc.Content[i+2:]...)
				break
			}
		}
	}

	// 1. Process legacy 'interleaved'
	interleavedIdx := -1
	for i := 0; i < len(doc.Content); i += 2 {
		if doc.Content[i].Value == "interleaved" {
			interleavedIdx = i
			break
		}
	}

	if interleavedIdx >= 0 {
		interleavedVal := doc.Content[interleavedIdx+1]
		if isNullNode(interleavedVal) {
			removeDocKey("interleaved")
		} else if interleavedVal.Kind != yaml.MappingNode {
			return fmt.Errorf("legacyfeatureconfig: legacy top-level 'interleaved' must be a mapping, got %s", nodeTag(interleavedVal))
		} else if hasCanonicalInterleaved {
			return fmt.Errorf("legacyfeatureconfig: both legacy top-level 'interleaved' and canonical 'plugins.features' entry for %q are configured", CanonicalID)
		} else {
			enabledVal := "true"
			for i := 0; i < len(interleavedVal.Content); i += 2 {
				if interleavedVal.Content[i].Value == "enabled" {
					enabledVal = interleavedVal.Content[i+1].Value
					break
				}
			}
			canonicalEntry := &yaml.Node{
				Kind: yaml.MappingNode,
				Tag:  "!!map",
				Content: []*yaml.Node{
					{Kind: yaml.ScalarNode, Tag: "!!str", Value: "id"},
					{Kind: yaml.ScalarNode, Tag: "!!str", Value: CanonicalID},
					{Kind: yaml.ScalarNode, Tag: "!!str", Value: "enabled"},
					{Kind: yaml.ScalarNode, Tag: "!!bool", Value: enabledVal},
					{Kind: yaml.ScalarNode, Tag: "!!str", Value: "config"},
					interleavedVal,
				},
			}
			ensureFeaturesNode()
			featuresNode.Content = append(featuresNode.Content, canonicalEntry)
			removeDocKey("interleaved")
		}
	}

	// 2. Process legacy 'prompt_cache'
	promptCacheIdx := -1
	for i := 0; i < len(doc.Content); i += 2 {
		if doc.Content[i].Value == "prompt_cache" {
			promptCacheIdx = i
			break
		}
	}

	if promptCacheIdx >= 0 {
		pcVal := doc.Content[promptCacheIdx+1]
		if isNullNode(pcVal) {
			removeDocKey("prompt_cache")
		} else if pcVal.Kind != yaml.MappingNode {
			return fmt.Errorf("legacyfeatureconfig: legacy top-level 'prompt_cache' must be a mapping, got %s", nodeTag(pcVal))
		} else {
			kwIdx := -1
			for i := 0; i < len(pcVal.Content); i += 2 {
				if pcVal.Content[i].Value == "keepwarm" {
					kwIdx = i
					break
				}
			}
			if kwIdx >= 0 {
				if hasCanonicalKeepwarm {
					return fmt.Errorf("legacyfeatureconfig: both legacy top-level 'prompt_cache.keepwarm' and canonical 'plugins.features' entry for %q are configured", KeepwarmCanonicalID)
				}
				kwVal := pcVal.Content[kwIdx+1]
				if isNullNode(kwVal) {
					// Null keepwarm -> no entry synthesized
				} else if kwVal.Kind != yaml.MappingNode {
					return fmt.Errorf("legacyfeatureconfig: legacy 'prompt_cache.keepwarm' must be a mapping, got %s", nodeTag(kwVal))
				} else {
					enabledVal := "true"
					for i := 0; i < len(kwVal.Content); i += 2 {
						if kwVal.Content[i].Value == "enabled" {
							enabledVal = kwVal.Content[i+1].Value
							break
						}
					}
					canonicalKW := &yaml.Node{
						Kind: yaml.MappingNode,
						Tag:  "!!map",
						Content: []*yaml.Node{
							{Kind: yaml.ScalarNode, Tag: "!!str", Value: "id"},
							{Kind: yaml.ScalarNode, Tag: "!!str", Value: KeepwarmCanonicalID},
							{Kind: yaml.ScalarNode, Tag: "!!str", Value: "enabled"},
							{Kind: yaml.ScalarNode, Tag: "!!bool", Value: enabledVal},
							{Kind: yaml.ScalarNode, Tag: "!!str", Value: "config"},
							kwVal,
						},
					}
					ensureFeaturesNode()
					featuresNode.Content = append(featuresNode.Content, canonicalKW)
				}
			}
			removeDocKey("prompt_cache")
		}
	}

	return nil
}

func isNullNode(node *yaml.Node) bool {
	if node == nil {
		return true
	}
	if node.Tag == "!!null" {
		return true
	}
	if node.Kind == yaml.ScalarNode && node.Tag != "!!str" && (node.Value == "null" || node.Value == "~" || node.Value == "") {
		return true
	}
	return false
}
