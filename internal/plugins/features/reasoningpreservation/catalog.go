package reasoningpreservation

import (
	"strings"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation/reasoningreplay"
)

const BuiltinCatalogVersion = reasoningreplay.CatalogVersion

type CandidateIdentity struct {
	BackendID       string
	BackendPrefixes []string
	Model           string
}

type MatchKind string

const (
	MatchNone                    MatchKind = "none"
	MatchExplicitDisabledModel   MatchKind = "explicit_disabled_model"
	MatchExplicitEnabledModel    MatchKind = "explicit_enabled_model"
	MatchExplicitDisabledBackend MatchKind = "explicit_disabled_backend"
	MatchExplicitEnabledBackend  MatchKind = "explicit_enabled_backend"
	MatchBuiltin                 MatchKind = "builtin"
)

type MatchResult struct {
	Kind   MatchKind
	RuleID string
}

type BuiltinCatalogEntry struct {
	ID              string
	BackendPrefixes []string
}

func BuiltinCatalogEntries() ([]BuiltinCatalogEntry, error) {
	return []BuiltinCatalogEntry{{
		ID:              BuiltinCatalogVersion,
		BackendPrefixes: reasoningreplay.BackendPrefixes(),
	}}, nil
}

func ResolveMatch(cfg Config, cand CandidateIdentity) (MatchResult, error) {
	backendID := strings.TrimSpace(cand.BackendID)
	model := strings.TrimSpace(cand.Model)

	if r, ok := firstModelRule(cfg.Rules, backendID, model, false); ok {
		return MatchResult{Kind: MatchExplicitDisabledModel, RuleID: r.ID}, nil
	}
	if r, ok := firstModelRule(cfg.Rules, backendID, model, true); ok {
		return MatchResult{Kind: MatchExplicitEnabledModel, RuleID: r.ID}, nil
	}
	if r, ok := firstBackendWideRule(cfg.Rules, backendID, false); ok {
		return MatchResult{Kind: MatchExplicitDisabledBackend, RuleID: r.ID}, nil
	}
	if r, ok := firstBackendWideRule(cfg.Rules, backendID, true); ok {
		return MatchResult{Kind: MatchExplicitEnabledBackend, RuleID: r.ID}, nil
	}
	if cfg.UseBuiltinCatalog {
		if reasoningreplay.Eligible(model, cand.BackendPrefixes) {
			return MatchResult{Kind: MatchBuiltin, RuleID: BuiltinCatalogVersion}, nil
		}
	}
	return MatchResult{Kind: MatchNone}, nil
}

func MatchEligible(kind MatchKind) bool {
	switch kind {
	case MatchExplicitEnabledModel, MatchExplicitEnabledBackend, MatchBuiltin:
		return true
	default:
		return false
	}
}

func firstModelRule(rules []RuleConfig, backendID, model string, enabled bool) (RuleConfig, bool) {
	for _, r := range rules {
		if r.Enabled == nil || *r.Enabled != enabled {
			continue
		}
		if len(r.ModelKeywords) == 0 {
			continue
		}
		if r.Backend != backendID {
			continue
		}
		if modelMatchesKeywords(model, r.ModelKeywords) {
			return r, true
		}
	}
	return RuleConfig{}, false
}

func firstBackendWideRule(rules []RuleConfig, backendID string, enabled bool) (RuleConfig, bool) {
	for _, r := range rules {
		if r.Enabled == nil || *r.Enabled != enabled {
			continue
		}
		if len(r.ModelKeywords) != 0 {
			continue
		}
		if r.Backend == backendID {
			return r, true
		}
	}
	return RuleConfig{}, false
}

// modelMatchesKeywords keeps contains-semantics for explicit operator keywords.
func modelMatchesKeywords(model string, keywords []string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return false
	}
	for _, kw := range keywords {
		k := strings.ToLower(strings.TrimSpace(kw))
		if k != "" && strings.Contains(m, k) {
			return true
		}
	}
	return false
}
