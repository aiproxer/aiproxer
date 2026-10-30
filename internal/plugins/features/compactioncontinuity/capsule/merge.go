package capsule

import (
	"fmt"
	"sort"
	"strings"
)

// Merge applies a validated deterministic/semantic delta using compare-and-
// merge semantics. The previous envelope is never modified.
func Merge(previous Envelope, delta Delta) (Envelope, error) {
	if err := previous.Verify(); err != nil {
		return Envelope{}, err
	}
	if delta.SchemaVersion != 0 && delta.SchemaVersion != SchemaVersion {
		return Envelope{}, fmt.Errorf("%w: unsupported delta schema_version %d", ErrInvalidCapsule, delta.SchemaVersion)
	}
	if delta.BaseRevision != previous.Revision {
		return Envelope{}, fmt.Errorf("%w: expected %d got %d", ErrStaleRevision, previous.Revision, delta.BaseRevision)
	}
	if delta.BranchBinding != previous.BranchBinding {
		return Envelope{}, ErrBranchMismatch
	}
	out := previous.Clone()
	out.Revision++
	if delta.SourceHighWatermark != "" {
		out.SourceHighWatermark = delta.SourceHighWatermark
	}
	if delta.Plan != nil {
		if err := mergePlan(&out.Plan, *delta.Plan); err != nil {
			return Envelope{}, err
		}
	}
	if err := mergeDecisions(&out, delta.Decisions, delta.DecisionTransitions); err != nil {
		return Envelope{}, err
	}
	if err := mergeFacts("constraints", &out.Constraints, delta.Constraints); err != nil {
		return Envelope{}, err
	}
	if err := mergeFacts("rejected_alternatives", &out.RejectedAlternatives, delta.RejectedAlternatives); err != nil {
		return Envelope{}, err
	}
	if err := mergeFacts("open_questions", &out.OpenQuestions, delta.OpenQuestions); err != nil {
		return Envelope{}, err
	}
	if err := out.Seal(); err != nil {
		return Envelope{}, err
	}
	return out, nil
}

func mergePlan(dst *Plan, incoming Plan) error {
	if err := validatePlan(incoming); err != nil {
		return err
	}
	if planSourceRank(incoming.Source) < planSourceRank(dst.Source) && len(incoming.Steps) == 0 {
		return nil
	}
	if planStatusRank(incoming.Status) > planStatusRank(dst.Status) {
		dst.Status = incoming.Status
	}
	if planSourceRank(incoming.Source) >= planSourceRank(dst.Source) {
		dst.Source = incoming.Source
	}
	byID := make(map[string]int, len(dst.Steps))
	for i, step := range dst.Steps {
		byID[step.ID] = i
	}
	for _, step := range incoming.Steps {
		if index, exists := byID[step.ID]; exists {
			old := &dst.Steps[index]
			if stepStatusRank(step.Status) > stepStatusRank(old.Status) {
				old.Status = step.Status
			}
			if step.Text != "" && planSourceRank(incoming.Source) >= planSourceRank(dst.Source) {
				old.Text = step.Text
			}
			if old.SourceRef == "" {
				old.SourceRef = step.SourceRef
			}
			continue
		}
		dst.Steps = append(dst.Steps, step)
		byID[step.ID] = len(dst.Steps) - 1
	}
	// Preserve deterministic serialization independent of carrier arrival order.
	sort.SliceStable(dst.Steps, func(i, j int) bool { return dst.Steps[i].ID < dst.Steps[j].ID })
	return validatePlan(*dst)
}

func mergeDecisions(dst *Envelope, incoming []Decision, transitions []DecisionTransition) error {
	if len(incoming) == 0 && len(transitions) == 0 {
		return nil
	}
	byID := make(map[string]int, len(dst.Decisions)+len(incoming))
	active := make(map[string]int)
	for i, d := range dst.Decisions {
		byID[d.ID] = i
		if d.Status == DecisionActive {
			active[d.ConflictKey] = i
		}
	}
	// Validate supersedes against old active facts and all IDs in this delta
	// before changing state, so a malformed result is atomic.
	// Supersedes references are intentionally scoped to the validated parent
	// capsule. A decision introduced by this delta is not yet known and cannot
	// be used as a forward reference from another decision in the same delta.
	known := make(map[string]bool, len(byID))
	activeKnown := make(map[string]bool, len(byID))
	for id, index := range byID {
		known[id] = true
		activeKnown[id] = dst.Decisions[index].Status == DecisionActive
	}
	for i, d := range incoming {
		if err := validateDecision(d, i); err != nil {
			return err
		}
		if index, exists := byID[d.ID]; exists && sameDecision(dst.Decisions[index], d) {
			continue // exact retries may retain historical supersedes references
		}
		for _, id := range d.Supersedes {
			if !known[id] {
				return fmt.Errorf("%w: %q", ErrUnknownSupersede, id)
			}
			if !activeKnown[id] {
				return fmt.Errorf("%w: %q is not active", ErrUnknownSupersede, id)
			}
		}
	}
	if err := validateDecisionTransitions(dst.Decisions, byID, transitions); err != nil {
		return err
	}
	transitionTargets := make(map[string]struct{}, len(transitions))
	for _, transition := range transitions {
		transitionTargets[transition.ID] = struct{}{}
	}
	for _, decision := range incoming {
		for _, targetID := range decision.Supersedes {
			if _, overlaps := transitionTargets[targetID]; overlaps {
				return fmt.Errorf("%w: target %q is both superseded and transitioned", ErrInvalidDecisionTransition, targetID)
			}
		}
	}
	for _, d := range incoming {
		if index, exists := byID[d.ID]; exists {
			if !sameDecision(dst.Decisions[index], d) {
				return fmt.Errorf("%w: decision id %q", ErrFactConflict, d.ID)
			}
			continue // deterministic retry/dedupe
		}
		if d.Status == DecisionActive {
			// Preflight every target before changing any one of them. A candidate
			// that loses to one higher-authority target must not partially
			// supersede lower-authority targets named in the same delta.
			targets := activeDecisionTargets(d, byID, active, dst.Decisions)
			for _, targetIndex := range targets {
				if decisionRank(d.Authority) < decisionRank(dst.Decisions[targetIndex].Authority) {
					d.Status = DecisionSuperseded
					break
				}
			}
			if d.Status == DecisionActive {
				for _, targetIndex := range targets {
					dst.Decisions[targetIndex].Status = DecisionSuperseded
				}
				active[d.ConflictKey] = len(dst.Decisions)
			}
		}
		// A lower-precedence candidate that named an existing active decision is
		// retained as history, but cannot reactivate or displace it.
		dst.Decisions = append(dst.Decisions, d)
		byID[d.ID] = len(dst.Decisions) - 1
	}
	for _, transition := range transitions {
		index := byID[transition.ID]
		dst.Decisions[index].Status = transition.Status
		dst.Decisions[index].StatusSourceRef = transition.SourceRef
	}
	sort.SliceStable(dst.Decisions, func(i, j int) bool { return dst.Decisions[i].ID < dst.Decisions[j].ID })
	return dst.validate(false)
}

func validateDecisionTransitions(decisions []Decision, byID map[string]int, transitions []DecisionTransition) error {
	seen := make(map[string]struct{}, len(transitions))
	for i, transition := range transitions {
		if err := validateDecisionTransition(transition, i); err != nil {
			return err
		}
		if _, exists := seen[transition.ID]; exists {
			return fmt.Errorf("%w: duplicate target %q", ErrInvalidDecisionTransition, transition.ID)
		}
		seen[transition.ID] = struct{}{}
		index, exists := byID[transition.ID]
		if !exists || index < 0 || index >= len(decisions) {
			return fmt.Errorf("%w: unknown target %q", ErrInvalidDecisionTransition, transition.ID)
		}
		target := decisions[index]
		if target.Status != DecisionActive {
			return fmt.Errorf("%w: target %q is not active", ErrInvalidDecisionTransition, transition.ID)
		}
		if target.Authority != AuthoritySemantic {
			return fmt.Errorf("%w: target %q authority %q is protected", ErrInvalidDecisionTransition, transition.ID, target.Authority)
		}
	}
	return nil
}

func validateDecisionTransition(transition DecisionTransition, index int) error {
	if err := validateString(fmt.Sprintf("decision_transitions[%d].id", index), transition.ID, 256, true); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidDecisionTransition, err)
	}
	if err := validateString(fmt.Sprintf("decision_transitions[%d].source_ref", index), transition.SourceRef, 512, true); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidDecisionTransition, err)
	}
	if transition.Authority != AuthoritySemantic {
		return fmt.Errorf("%w: authority must be %q", ErrInvalidDecisionTransition, AuthoritySemantic)
	}
	switch transition.Status {
	case DecisionSuperseded, DecisionRejected:
		return nil
	default:
		return fmt.Errorf("%w: status must be superseded or rejected", ErrInvalidDecisionTransition)
	}
}

func activeDecisionTargets(d Decision, byID, active map[string]int, decisions []Decision) []int {
	seen := make(map[int]struct{}, len(d.Supersedes)+1)
	add := func(index int) {
		if index >= 0 && index < len(decisions) && decisions[index].Status == DecisionActive {
			if _, exists := seen[index]; !exists {
				seen[index] = struct{}{}
			}
		}
	}
	for _, id := range d.Supersedes {
		if index, exists := byID[id]; exists {
			add(index)
		}
	}
	if index, exists := active[d.ConflictKey]; exists {
		add(index)
	}
	targets := make([]int, 0, len(seen))
	for index := range seen {
		targets = append(targets, index)
	}
	sort.Ints(targets)
	return targets
}

func sameDecision(a, b Decision) bool {
	a.Supersedes = uniqueSorted(a.Supersedes)
	b.Supersedes = uniqueSorted(b.Supersedes)
	return a.ConflictKey == b.ConflictKey && a.Statement == b.Statement && a.Status == b.Status && a.Authority == b.Authority && a.Rationale == b.Rationale && a.SourceRef == b.SourceRef && a.StatusSourceRef == b.StatusSourceRef && strings.Join(a.Supersedes, "\x00") == strings.Join(b.Supersedes, "\x00")
}

func mergeFacts(name string, dst *[]Fact, incoming []Fact) error {
	if len(incoming) == 0 {
		return nil
	}
	byID := make(map[string]int, len(*dst))
	for i, f := range *dst {
		byID[f.ID] = i
	}
	for _, f := range incoming {
		if err := validateFacts(name, []Fact{f}); err != nil {
			return err
		}
		if index, exists := byID[f.ID]; exists {
			if !sameFact((*dst)[index], f) {
				return fmt.Errorf("%w: %s id %q", ErrFactConflict, name, f.ID)
			}
			continue
		}
		*dst = append(*dst, f)
		byID[f.ID] = len(*dst) - 1
	}
	sort.SliceStable(*dst, func(i, j int) bool { return (*dst)[i].ID < (*dst)[j].ID })
	return nil
}

func sameFact(a, b Fact) bool {
	return a.ID == b.ID && a.Statement == b.Statement && a.Status == b.Status && a.Authority == b.Authority && a.Rationale == b.Rationale && a.SourceRef == b.SourceRef
}

func decisionRank(a Authority) int {
	switch a {
	case AuthorityUserExplicit:
		return 4
	case AuthorityUserAcceptance:
		return 3
	case AuthorityStructured:
		return 2
	case AuthoritySemantic:
		return 1
	default:
		return 0
	}
}

func planSourceRank(s Source) int {
	switch s {
	case SourceUserExplicit:
		return 4
	case SourceUserAcceptance:
		return 3
	case SourceStructured:
		return 2
	case SourceSemantic:
		return 1
	default:
		return 0
	}
}

func planStatusRank(s PlanStatus) int {
	switch s {
	case PlanDraft:
		return 1
	case PlanAccepted:
		return 2
	case PlanCompleted:
		return 3
	default:
		return 0
	}
}

func stepStatusRank(s StepStatus) int {
	switch s {
	case StepPending:
		return 1
	case StepInProgress:
		return 2
	case StepCompleted, StepCancelled:
		return 3
	default:
		return 0
	}
}
