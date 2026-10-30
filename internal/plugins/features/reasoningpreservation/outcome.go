package reasoningpreservation

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

type SafeOutcome string

const (
	OutcomeObserved            SafeOutcome = "observed"
	OutcomePreserved           SafeOutcome = "preserved"
	OutcomeMissing             SafeOutcome = "missing"
	OutcomeRestored            SafeOutcome = "restored"
	OutcomeAmbiguous           SafeOutcome = "ambiguous"
	OutcomeConflicting         SafeOutcome = "conflicting"
	OutcomeUnmatched           SafeOutcome = "unmatched"
	OutcomeUnrepresentable     SafeOutcome = "unrepresentable"
	OutcomeStateError          SafeOutcome = "state_error"
	OutcomeEvicted             SafeOutcome = "evicted"
	OutcomeOversize            SafeOutcome = "oversize"
	OutcomeBoundedRaw          SafeOutcome = "bounded_raw"
	OutcomeRawOversize         SafeOutcome = "raw_oversize"
	OutcomeRawInvalidChannel   SafeOutcome = "raw_invalid_channel"
	OutcomeRawInvalidLimit     SafeOutcome = "raw_invalid_limit"
	OutcomeDecodeInvalid       SafeOutcome = "decode_invalid"
	OutcomeSchemaInvalid       SafeOutcome = "schema_invalid"
	OutcomeControlInvalid      SafeOutcome = "control_invalid"
	OutcomeSurrogateOversize   SafeOutcome = "surrogate_oversize"
	OutcomeInsufficientSavings SafeOutcome = "insufficient_savings"
	OutcomeStale               SafeOutcome = "stale"
	OutcomeSurrogateAttached   SafeOutcome = "surrogate_attached"
	OutcomeShadowReady         SafeOutcome = "shadow_ready"
	// 5.4 taxonomy additions — content-free, no raw IDs, no reasoning text.
	OutcomeEligible                  SafeOutcome = "eligible"
	OutcomeCompIneligible            SafeOutcome = "ineligible"
	OutcomeExact                     SafeOutcome = "exact"
	OutcomeBelowThreshold            SafeOutcome = "below_threshold"
	OutcomeReservationBudgetExceeded SafeOutcome = "reservation_budget_exceeded"
	OutcomeBudgetPendingPerSession   SafeOutcome = "budget_pending_per_session"
	OutcomeBudgetPendingTotal        SafeOutcome = "budget_pending_total"
	OutcomeBudgetSurrogatePerTurn    SafeOutcome = "budget_surrogate_per_turn"
	OutcomeBudgetSurrogatePerSession SafeOutcome = "budget_surrogate_per_session"
	OutcomeBudgetSurrogateTotal      SafeOutcome = "budget_surrogate_total"
	OutcomeEgressAllow               SafeOutcome = "egress_allow"
	OutcomeEgressRedact              SafeOutcome = "egress_redact"
	OutcomeEgressDeny                SafeOutcome = "egress_deny"
	OutcomeEgressMissingPolicy       SafeOutcome = "egress_missing_policy"
	OutcomeSubmitted                 SafeOutcome = "submitted"
	OutcomeCoalesced                 SafeOutcome = "coalesced"
	OutcomeQueueSaturated            SafeOutcome = "queue_saturated"
	OutcomeAdmissionDenied           SafeOutcome = "admission_denied"
	OutcomeSubmitFailed              SafeOutcome = "submit_failed"
	OutcomePollPending               SafeOutcome = "poll_pending"
	OutcomePollCompleted             SafeOutcome = "poll_completed"
	OutcomePollFailed                SafeOutcome = "poll_failed"
	OutcomePollNotFound              SafeOutcome = "poll_not_found"
	OutcomePollUnavailable           SafeOutcome = "poll_unavailable"
	OutcomePollError                 SafeOutcome = "poll_error"
	OutcomeOriginalFallback          SafeOutcome = "original_fallback"
	OutcomeActiveUsed                SafeOutcome = "active_used"
)

var safeCountKeys = map[string]struct{}{
	"count":                        {},
	"bytes":                        {},
	"rawBytes":                     {},
	"decodedBytes":                 {},
	"savedBytes":                   {},
	"sourceBytes":                  {},
	"restored":                     {},
	"observed":                     {},
	"preserved":                    {},
	"missing":                      {},
	"ambiguous":                    {},
	"conflicting":                  {},
	"unmatched":                    {},
	"unrepresentable":              {},
	"state_error":                  {},
	"evicted":                      {},
	"oversize":                     {},
	"bounded_raw":                  {},
	"raw_oversize":                 {},
	"raw_invalid_channel":          {},
	"raw_invalid_limit":            {},
	"decode_invalid":               {},
	"schema_invalid":               {},
	"control_invalid":              {},
	"surrogate_oversize":           {},
	"insufficient_savings":         {},
	"stale":                        {},
	"surrogate_attached":           {},
	"shadow_ready":                 {},
	"eligible":                     {},
	"ineligible":                   {},
	"exact":                        {},
	"below_threshold":              {},
	"reservation_budget_exceeded":  {},
	"budget_pending_per_session":   {},
	"budget_pending_total":         {},
	"budget_surrogate_per_turn":    {},
	"budget_surrogate_per_session": {},
	"budget_surrogate_total":       {},
	"egress_allow":                 {},
	"egress_redact":                {},
	"egress_deny":                  {},
	"egress_missing_policy":        {},
	"submitted":                    {},
	"coalesced":                    {},
	"queue_saturated":              {},
	"admission_denied":             {},
	"submit_failed":                {},
	"poll_pending":                 {},
	"poll_completed":               {},
	"poll_failed":                  {},
	"poll_not_found":               {},
	"poll_unavailable":             {},
	"poll_error":                   {},
	"original_fallback":            {},
	"active_used":                  {},
}

func FormatSafeDiagnostic(outcome SafeOutcome, ruleID string, counts map[string]int) (string, error) {
	if !isKnownOutcome(outcome) {
		return "", fmt.Errorf("%s: unknown outcome", ID)
	}
	safeRule := sanitizeRuleID(ruleID)
	parts := []string{"outcome=" + string(outcome)}
	if safeRule != "" {
		parts = append(parts, "rule="+safeRule)
	}
	if len(counts) > 0 {
		keys := make([]string, 0, len(counts))
		for k := range counts {
			if _, ok := safeCountKeys[k]; ok {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%d", k, counts[k]))
		}
	}
	return strings.Join(parts, " "), nil
}

func ProjectSafeError(err error) (string, error) {
	if err == nil {
		return "", fmt.Errorf("%s: nil error", ID)
	}
	return ID + ": operation failed", nil
}

func isKnownOutcome(o SafeOutcome) bool {
	switch o {
	case OutcomeObserved, OutcomePreserved, OutcomeMissing, OutcomeRestored,
		OutcomeAmbiguous, OutcomeConflicting, OutcomeUnmatched,
		OutcomeUnrepresentable, OutcomeStateError, OutcomeEvicted, OutcomeOversize,
		OutcomeBoundedRaw, OutcomeRawOversize, OutcomeRawInvalidChannel, OutcomeRawInvalidLimit,
		OutcomeDecodeInvalid, OutcomeSchemaInvalid, OutcomeControlInvalid, OutcomeSurrogateOversize, OutcomeInsufficientSavings,
		OutcomeStale, OutcomeSurrogateAttached, OutcomeShadowReady,
		OutcomeEligible, OutcomeCompIneligible, OutcomeExact, OutcomeBelowThreshold, OutcomeReservationBudgetExceeded,
		OutcomeBudgetPendingPerSession, OutcomeBudgetPendingTotal, OutcomeBudgetSurrogatePerTurn, OutcomeBudgetSurrogatePerSession, OutcomeBudgetSurrogateTotal,
		OutcomeEgressAllow, OutcomeEgressRedact, OutcomeEgressDeny, OutcomeEgressMissingPolicy,
		OutcomeSubmitted, OutcomeCoalesced, OutcomeQueueSaturated, OutcomeAdmissionDenied, OutcomeSubmitFailed,
		OutcomePollPending, OutcomePollCompleted, OutcomePollFailed, OutcomePollNotFound, OutcomePollUnavailable, OutcomePollError,
		OutcomeOriginalFallback, OutcomeActiveUsed:
		return true
	default:
		return false
	}
}

func sanitizeRuleID(ruleID string) string {
	ruleID = strings.TrimSpace(ruleID)
	if ruleID == "" {
		return ""
	}
	if i := strings.IndexByte(ruleID, '/'); i >= 0 {
		ruleID = ruleID[:i]
	}
	if !isSafeLabel(ruleID) {
		return ""
	}
	return ruleID
}

func isSafeLabel(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}
