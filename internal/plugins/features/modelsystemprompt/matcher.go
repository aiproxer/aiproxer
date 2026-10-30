package modelsystemprompt

import (
	"regexp"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/steering"
)

const (
	maxRules          = 64
	maxTotalTextBytes = 256 * 1024
)

type compiledRule struct {
	pattern *regexp.Regexp
	request steering.PutRequest
}

// Matcher is immutable after construction and safe for concurrent matching.
type Matcher struct {
	rules []compiledRule
}

// New validates all configured rules and compiles their Go regexes once, before
// publication. Errors are content-free, including regex and payload failures.
func New(cfg Config) (*Matcher, error) {
	if len(cfg.Rules) > maxRules {
		return nil, configError("too many rules")
	}
	m := &Matcher{rules: make([]compiledRule, 0, len(cfg.Rules))}
	seen := make(map[steering.OverlayID]bool, len(cfg.Rules))
	total := 0
	for _, rule := range cfg.Rules {
		if rule.ID == "" {
			return nil, configError("rule id is required")
		}
		req := steering.PutRequest{
			OverlayID:           steering.OverlayID(ID + "." + rule.ID),
			Message:             steering.Message{Role: aipapi.RoleSystem, Text: rule.Append},
			Placement:           steering.StablePrefix,
			AnchorMissingPolicy: steering.StablePrefixFallback,
			Reason:              "model_system_prompt",
		}
		if err := req.Validate(); err != nil {
			return nil, configError("invalid overlay id or append text")
		}
		if seen[req.OverlayID] {
			return nil, configError("duplicate overlay id")
		}
		seen[req.OverlayID] = true
		total += len(rule.Append)
		if total > maxTotalTextBytes {
			return nil, configError("total append text exceeds limit")
		}
		if rule.ModelPattern == "" {
			return nil, configError("model_pattern is required")
		}
		pattern, err := regexp.Compile(rule.ModelPattern)
		if err != nil {
			return nil, configError("invalid model_pattern")
		}
		m.rules = append(m.rules, compiledRule{pattern: pattern, request: req})
	}
	return m, nil
}

// Match returns independent system steering requests for every matching rule,
// in configuration order. The caller supplies the already-resolved logical
// model; matching performs no routing, rendering, I/O or regex compilation.
func (m *Matcher) Match(model string) []steering.PutRequest {
	var requests []steering.PutRequest
	for _, rule := range m.rules {
		if rule.pattern.MatchString(model) {
			requests = append(requests, rule.request)
		}
	}
	return requests
}
