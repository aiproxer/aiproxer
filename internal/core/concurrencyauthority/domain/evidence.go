package domain

import "strconv"

// DecisionKind is the domain admit/renew outcome.
type DecisionKind string

const (
	DecisionAllow    DecisionKind = "allow"
	DecisionDeny     DecisionKind = "deny"
	DecisionAdvisory DecisionKind = "advisory"
)

// IsKnown reports whether k is a documented decision kind.
func (k DecisionKind) IsKnown() bool {
	switch k {
	case DecisionAllow, DecisionDeny, DecisionAdvisory:
		return true
	default:
		return false
	}
}

// EvidenceCategoryConcurrencyLimit is the stable client-safe denial category
// (requirement 10.11).
const EvidenceCategoryConcurrencyLimit = "concurrency_limit"

// Evidence is client-safe denial/allow evidence without other principals or
// internal lease identifiers in Message.
type Evidence struct {
	Category string
	Code     string
	Message  string
	RuleID   string
	Attrs    map[string]string
}

// DenialEvidence builds concurrency-limit denial evidence.
func DenialEvidence(ruleID string, remainingSlots int) Evidence {
	ev := Evidence{
		Category: EvidenceCategoryConcurrencyLimit,
		Code:     "concurrency_limit_exceeded",
		Message:  "active request limit exceeded",
		RuleID:   ruleID,
	}
	if remainingSlots >= 0 {
		ev.Attrs = map[string]string{
			"remaining_slots": strconv.Itoa(remainingSlots),
		}
	}
	return ev
}
