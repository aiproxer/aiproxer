package reasoningpreservation

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	companionRuleMaxIDLength = 64
)

// NewCompanionConfig returns generic feature config for backend instance IDs.
// Provider-specific eligibility and trust issuance remain composition policy.
func NewCompanionConfig(backendIDs []string, rulePrefix string) (yaml.Node, error) {
	node := mappingNode(
		mappingEntry{"action", stringNode(ActionRestore)},
		mappingEntry{"use_builtin_catalog", boolNode(true)},
		mappingEntry{"rules", sequenceNode(companionRules(backendIDs, rulePrefix))},
		mappingEntry{"on_ambiguous", stringNode(PolicyLogSkip)},
		mappingEntry{"on_unrepresentable", stringNode(PolicyReject)},
		mappingEntry{"on_state_error", stringNode(PolicyReject)},
		mappingEntry{"state", mappingNode(
			mappingEntry{"ttl", stringNode("24h")},
			mappingEntry{"max_turns_per_session", intNode(16)},
			mappingEntry{"max_reasoning_bytes_per_turn", intNode(65536)},
			mappingEntry{"max_session_bytes", intNode(262144)},
		)},
	)
	if _, err := DecodeConfig(*node); err != nil {
		return yaml.Node{}, fmt.Errorf("%s defaults: %w", ID, err)
	}
	return *node, nil
}

// EnsureCompanionRules appends missing backend-only rules while
// retaining every existing config node and its ordering. DecodeConfig performs
// validation before mutation, so malformed or unknown feature config is never
// silently repaired by composition.
func EnsureCompanionRules(n yaml.Node, backendIDs []string, rulePrefix string) (yaml.Node, error) {
	decoded, err := DecodeConfig(n)
	if err != nil {
		return yaml.Node{}, err
	}
	missing := missingCompanionBackends(decoded.Rules, backendIDs)
	if len(missing) == 0 {
		return n, nil
	}

	root := yamlRootNode(&n)
	if root == nil || root.Kind != yaml.MappingNode {
		return yaml.Node{}, fmt.Errorf("%s: config must be a mapping", ID)
	}
	rules := mappingValue(root, "rules")
	if rules == nil {
		rules = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		root.Content = append(root.Content, stringNode("rules"), rules)
	}
	if rules.Kind != yaml.SequenceNode {
		return yaml.Node{}, fmt.Errorf("%s: rules must be a sequence", ID)
	}

	usedIDs := ruleIDs(rules)
	for _, backendID := range missing {
		ruleID := uniqueCompanionRuleID(backendID, usedIDs, rulePrefix)
		usedIDs[ruleID] = struct{}{}
		rules.Content = append(rules.Content, companionRuleNode(ruleID, backendID))
	}
	return n, nil
}

func missingCompanionBackends(rules []RuleConfig, backendIDs []string) []string {
	missing := make([]string, 0, len(backendIDs))
	for _, backendID := range backendIDs {
		var hasEnabled, hasDisabled bool
		for _, rule := range rules {
			if strings.TrimSpace(rule.Backend) != backendID || len(rule.ModelKeywords) != 0 {
				continue
			}
			if rule.Enabled != nil && *rule.Enabled {
				hasEnabled = true
			} else {
				hasDisabled = true
			}
		}
		if !hasEnabled && !hasDisabled {
			missing = append(missing, backendID)
		}
	}
	return missing
}

func companionRules(backendIDs []string, rulePrefix string) []*yaml.Node {
	used := make(map[string]struct{}, len(backendIDs))
	rules := make([]*yaml.Node, 0, len(backendIDs))
	for _, backendID := range backendIDs {
		id := uniqueCompanionRuleID(backendID, used, rulePrefix)
		used[id] = struct{}{}
		rules = append(rules, companionRuleNode(id, backendID))
	}
	return rules
}

func companionRuleNode(id, backendID string) *yaml.Node {
	enabled := boolNode(true)
	return mappingNode(
		mappingEntry{"id", stringNode(id)},
		mappingEntry{"backend", stringNode(backendID)},
		mappingEntry{"model_keywords", sequenceNode(nil)},
		mappingEntry{"enabled", enabled},
	)
}

func uniqueCompanionRuleID(backendID string, used map[string]struct{}, rulePrefix string) string {
	base := companionRuleID(backendID, rulePrefix)
	if _, exists := used[base]; !exists {
		return base
	}
	for suffix := 2; ; suffix++ {
		marker := fmt.Sprintf("-%d", suffix)
		keep := companionRuleMaxIDLength - len(marker)
		candidate := base
		if len(candidate) > keep {
			candidate = candidate[:keep]
		}
		candidate += marker
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
}

func companionRuleID(backendID, rulePrefix string) string {
	normalized := strings.ToLower(strings.TrimSpace(backendID))
	var b strings.Builder
	lastDash := false
	for _, r := range normalized {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	sanitized := strings.Trim(b.String(), "-")
	if sanitized == "" {
		sanitized = "backend"
	}
	base := strings.TrimSpace(rulePrefix) + sanitized
	if len(base) > companionRuleMaxIDLength {
		hash := sha256.Sum256([]byte(base))
		suffix := "-" + hex.EncodeToString(hash[:])[:8]
		keep := companionRuleMaxIDLength - len(suffix)
		if len(sanitized) > keep {
			sanitized = sanitized[:keep]
		}
		base = sanitized + suffix
	}
	return base
}

func ruleIDs(rules *yaml.Node) map[string]struct{} {
	used := make(map[string]struct{}, len(rules.Content))
	for _, rule := range rules.Content {
		if id := mappingValue(rule, "id"); id != nil {
			used[id.Value] = struct{}{}
		}
	}
	return used
}

func yamlRootNode(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 {
			return nil
		}
		return n.Content[0]
	}
	return n
}

func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

type mappingEntry struct {
	key   string
	value *yaml.Node
}

func mappingNode(entries ...mappingEntry) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, entry := range entries {
		node.Content = append(node.Content, stringNode(entry.key), entry.value)
	}
	return node
}

func sequenceNode(items []*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: items}
}

func stringNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func boolNode(value bool) *yaml.Node {
	text := "false"
	if value {
		text = "true"
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: text}
}

func intNode(value int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprintf("%d", value)}
}
