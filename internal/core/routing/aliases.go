package routing

import (
	"fmt"
	"regexp"
	"strings"
)

// ModelAliasRule is one regexp-based rewrite rule for a route selector string.
type ModelAliasRule struct {
	Pattern     string
	Replacement string
}

type AliasResolver struct {
	rules []aliasRule
}

type aliasRule struct {
	re          *regexp.Regexp
	replacement string
}

func ValidateModelAliases(rules []ModelAliasRule) error {
	_, err := compileAliasRules(rules)
	return err
}

func NewAliasResolver(rules []ModelAliasRule) (*AliasResolver, error) {
	return compileAliasRules(rules)
}

func compileAliasRules(rules []ModelAliasRule) (*AliasResolver, error) {
	out := make([]aliasRule, 0, len(rules))
	for i, r := range rules {
		pat := strings.TrimSpace(r.Pattern)
		if pat == "" {
			return nil, fmt.Errorf("model_aliases[%d]: empty pattern", i)
		}
		repl := strings.TrimSpace(r.Replacement)
		if repl == "" {
			return nil, fmt.Errorf("model_aliases[%d]: empty replacement", i)
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, fmt.Errorf("model_aliases[%d].pattern: %w", i, err)
		}
		if _, err := Parse(repl); err != nil {
			return nil, fmt.Errorf("model_aliases[%d].replacement: %w", i, err)
		}
		out = append(out, aliasRule{re: re, replacement: repl})
	}
	return &AliasResolver{rules: out}, nil
}

// Resolve applies the first matching alias rule to s.
//
// An empty selector is "no route", not a model an alias may rewrite: a
// catch-all rule must not turn an absent default route into a route to some
// provider.
func (r *AliasResolver) Resolve(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || r == nil || len(r.rules) == 0 {
		return s
	}
	for _, rule := range r.rules {
		if rule.re.MatchString(s) {
			return rule.re.ReplaceAllString(s, rule.replacement)
		}
	}
	return s
}
