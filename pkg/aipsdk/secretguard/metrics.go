package secretguard

// ActionForOutcome maps a decision outcome to the stable action label used by
// metrics and audit events (block/redact/log/pass). Unknown outcomes map to "unknown".
func ActionForOutcome(o Outcome) string {
	switch o {
	case OutcomeBlock:
		return "block"
	case OutcomeRedacted:
		return "redact"
	case OutcomeLog:
		return "log"
	case OutcomePass:
		return "pass"
	default:
		return "unknown"
	}
}

func DecisionMetricLabels(decision Decision) (action, outcome, sourceCategory string) {
	outcome = string(decision.Outcome)
	if outcome == "" {
		outcome = "unknown"
	}
	action = ActionForOutcome(decision.Outcome)
	if len(decision.Findings) == 0 {
		return action, outcome, string(SourceCategoryUnknown)
	}
	seen := make(map[string]struct{}, len(decision.Findings))
	for _, f := range decision.Findings {
		c := string(f.SourceCategory)
		if c == "" {
			c = string(SourceCategoryUnknown)
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		if len(seen) > 1 {
			return action, outcome, "mixed"
		}
		sourceCategory = c
	}
	return action, outcome, sourceCategory
}
