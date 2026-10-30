package capsule

import (
	"slices"
	"sort"
)

// Limits bounds the serialized capsule. MaxBytes and MaxTokens are independent
// dimensions; a zero value leaves that dimension unbounded. MaxTokens uses the
// conservative provider-neutral convention of one token-equivalent per UTF-8
// byte in the canonical JSON.
type Limits struct {
	MaxBytes  int
	MaxTokens int
}

// TokenEquivalent returns the deterministic provider-neutral token estimate
// used for capsule bounds. Counting UTF-8 bytes one-for-one is intentionally
// conservative and avoids depending on a provider tokenizer.
func TokenEquivalent(data []byte) int { return len(data) }

// Prune preserves the original byte-only API. New callers that have both
// configured dimensions should use PruneWithLimits.
func Prune(input Envelope, maxBytes int) (Envelope, error) {
	return PruneWithLimits(input, Limits{MaxBytes: maxBytes})
}

// PruneWithLimits removes complete facts, then historical facts, as whole
// values until the sealed canonical envelope satisfies every configured
// dimension. Active decisions, constraints, and pending/in-progress plan
// steps are retained whenever possible.
func PruneWithLimits(input Envelope, limits Limits) (Envelope, error) {
	if limits.MaxBytes < 0 || limits.MaxTokens < 0 || (limits.MaxBytes == 0 && limits.MaxTokens == 0) {
		return Envelope{}, ErrCapsuleTooLarge
	}
	if err := input.Verify(); err != nil {
		return Envelope{}, err
	}
	out := input.Clone()
	fits := func(candidate Envelope) bool {
		serialized := mustMarshal(candidate)
		if limits.MaxBytes > 0 && len(serialized) > limits.MaxBytes {
			return false
		}
		return limits.MaxTokens <= 0 || TokenEquivalent(serialized) <= limits.MaxTokens
	}
	if fits(out) {
		return out, nil
	}

	// Lowest-priority removable groups come first. Sorting by ID makes pruning
	// independent of source arrival order and avoids syntactic truncation.
	removePlan := orderedStepIDs(out.Plan.Steps, func(s PlanStep) bool { return s.Status == StepCompleted || s.Status == StepCancelled })
	for _, id := range removePlan {
		out.Plan.Steps = removeStepID(out.Plan.Steps, id)
		if fits(out) {
			return reseal(out)
		}
	}
	removeDecisions := orderedDecisionIDs(out.Decisions, func(d Decision) bool { return d.Status != DecisionActive })
	for _, id := range removeDecisions {
		if decisionReferenced(out.Decisions, id) {
			continue
		}
		out.Decisions = removeDecisionID(out.Decisions, id)
		if fits(out) {
			return reseal(out)
		}
	}
	for _, group := range []*[]Fact{&out.RejectedAlternatives, &out.OpenQuestions} {
		ids := orderedFactIDs(*group, func(f Fact) bool { return f.Status != FactActive })
		for _, id := range ids {
			*group = removeFactID(*group, id)
			if fits(out) {
				return reseal(out)
			}
		}
	}
	// Active facts are the final removable group. If one active fact itself is
	// larger than the configured bound, fail rather than corrupting it.
	for _, group := range []*[]Fact{&out.Constraints} {
		ids := orderedFactIDs(*group, func(f Fact) bool { return f.Status != FactActive })
		for _, id := range ids {
			*group = removeFactID(*group, id)
			if fits(out) {
				return reseal(out)
			}
		}
	}
	return Envelope{}, ErrCapsuleTooLarge
}

func reseal(e Envelope) (Envelope, error) {
	e.ContentDigest = ""
	if err := e.Seal(); err != nil {
		return Envelope{}, err
	}
	return e, nil
}
func mustMarshal(e Envelope) []byte { b, _ := marshalCanonical(e); return b }

func orderedStepIDs(items []PlanStep, predicate func(PlanStep) bool) []string {
	var out []string
	for _, item := range items {
		if predicate(item) {
			out = append(out, item.ID)
		}
	}
	sort.Strings(out)
	return out
}

func orderedDecisionIDs(items []Decision, predicate func(Decision) bool) []string {
	var out []string
	for _, item := range items {
		if predicate(item) {
			out = append(out, item.ID)
		}
	}
	sort.Strings(out)
	return out
}

func orderedFactIDs(items []Fact, predicate func(Fact) bool) []string {
	var out []string
	for _, item := range items {
		if predicate(item) {
			out = append(out, item.ID)
		}
	}
	sort.Strings(out)
	return out
}

func removeStepID(items []PlanStep, id string) []PlanStep {
	for i, item := range items {
		if item.ID == id {
			return append(items[:i:i], items[i+1:]...)
		}
	}
	return items
}

func removeDecisionID(items []Decision, id string) []Decision {
	for i, item := range items {
		if item.ID == id {
			return append(items[:i:i], items[i+1:]...)
		}
	}
	return items
}

func removeFactID(items []Fact, id string) []Fact {
	for i, item := range items {
		if item.ID == id {
			return append(items[:i:i], items[i+1:]...)
		}
	}
	return items
}

func decisionReferenced(items []Decision, id string) bool {
	for _, item := range items {
		if slices.Contains(item.Supersedes, id) {
			return true
		}
	}
	return false
}
