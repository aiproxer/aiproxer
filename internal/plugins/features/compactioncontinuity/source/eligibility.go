package source

import "strings"

// EligibilitySignal explains the bounded, local reason a semantic call may
// add information. It is diagnostic classification, never accepted intent.
type EligibilitySignal string

const (
	SignalNone                 EligibilitySignal = "none"
	SignalExplicitUserDecision EligibilitySignal = "explicit_user_decision"
	SignalPlanAccepted         EligibilitySignal = "plan_accepted_or_corrected"
	SignalStaleCapsule         EligibilitySignal = "stale_or_missing_capsule"
)

// EligibilityInput contains only sanitized source and local state. Untrusted
// records are deliberately ignored as semantic triggers.
type EligibilityInput struct {
	Entries                []Entry
	DeterministicSatisfied bool
	CapsuleAbsent          bool
	CapsuleStale           bool
	// OnlyNew restricts evaluation to entries marked New. It is useful to
	// evaluate an incremental delta while direct callers can evaluate a full
	// bounded window without setting New on every fixture.
	OnlyNew bool
}

// Eligibility is a cost gate only. Eligible means "a semantic extraction may
// be worth paying for"; it does not assert that any user decision is valid or
// accepted.
type Eligibility struct {
	Eligible       bool
	Signal         EligibilitySignal
	CandidateCount int
}

// EvaluateEligibility applies narrow explicit-language and source-shape
// heuristics. Generic words such as "plan" by themselves never trigger.
func EvaluateEligibility(in EligibilityInput) Eligibility {
	if in.DeterministicSatisfied {
		return Eligibility{}
	}

	var assistantPlan bool
	var planningMarker bool
	var explicitUser bool
	var acceptedPlan bool
	count := 0
	for i, entry := range in.Entries {
		if entry.Untrusted || entry.Kind == EntryUntrustedTool || (in.OnlyNew && !entry.New) {
			continue
		}
		if entry.DecisionRelevant || entry.Kind == EntryUserDecision {
			explicitUser = true
			count++
		}
		if entry.PlanningRelevant || entry.Kind == EntryAssistantPlan {
			assistantPlan = true
			planningMarker = true
		}
		if (entry.Role == "user" || entry.Kind == EntryUserText || entry.Kind == EntryUserDecision) && assistantPlan && affirmativeOrCorrection(entry.Text) {
			acceptedPlan = true
			count++
		}
		// A user entry may be the first record in a preparation result but
		// should still be paired with an earlier assistant plan in the same
		// bounded window. The index guard excludes same-item artifacts.
		if i > 0 && (entry.Role == "user" || entry.Kind == EntryUserText || entry.Kind == EntryUserDecision) && affirmativeOrCorrection(entry.Text) {
			for _, prior := range in.Entries[:i] {
				if prior.Untrusted || prior.Kind == EntryUntrustedTool || !prior.PlanningRelevant {
					continue
				}
				acceptedPlan = true
				count++
				break
			}
		}
	}
	if explicitUser {
		return Eligibility{Eligible: true, Signal: SignalExplicitUserDecision, CandidateCount: count}
	}
	if acceptedPlan {
		return Eligibility{Eligible: true, Signal: SignalPlanAccepted, CandidateCount: count}
	}
	if (in.CapsuleAbsent || in.CapsuleStale) && planningMarker {
		return Eligibility{Eligible: true, Signal: SignalStaleCapsule, CandidateCount: count + 1}
	}
	return Eligibility{CandidateCount: count}
}

func explicitDecision(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	patterns := []string{
		"i choose ", "we choose ", "i decided ", "we decided ", "we'll use ", "we will use ", "let's use ",
		"go with ", "must ", "require ", "constraint:", "the requirement is ", "prefer ", "avoid ",
		"instead ", "change ", "switch ", "correction:", "actually,", "do not ", "don't ", "never ",
		"approved", "looks good", "go ahead", "proceed with ", "yes, ", "yes ", "correct, ",
	}
	for _, pattern := range patterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	for _, prefix := range []string{"use ", "keep ", "change ", "switch "} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func planningSignal(text string) bool {
	lower := strings.ToLower(text)
	patterns := []string{
		"plan:", "implementation plan", "make a plan", "plan to ", "next steps", "proposal:", "propose ", "we will ", "we'll ",
		"milestone", "trade-off", "tradeoff", "constraint", "architecture", "decision record", "open question",
		"todo", "in progress", "remaining work", "acceptance criteria",
	}
	for _, pattern := range patterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

func affirmativeOrCorrection(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	for _, pattern := range []string{
		"yes", "yep", "correct", "approved", "looks good", "go ahead", "proceed", "do that",
		"actually", "correction", "instead", "change ", "switch ", "not ",
	} {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}
