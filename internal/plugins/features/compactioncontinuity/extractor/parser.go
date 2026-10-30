package extractor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/capsule"
)

var (
	ErrInvalidResult = errors.New("extractor: invalid result")
	conflictKeyRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9._:/-]{0,127}$`)
)

// ParseResult accepts exactly one strict JSON object and validates it against
// the locally verified parent capsule. It never trusts child authority fields.
func ParseResult(data []byte, opts ParseOptions) (Result, error) {
	limits := opts.Limits.normalized()
	if len(bytes.TrimSpace(data)) == 0 || len(data) > limits.MaxBytes {
		return Result{}, fmt.Errorf("%w: byte bound", ErrInvalidResult)
	}
	if err := checkDepth(data, limits.MaxDepth); err != nil {
		return Result{}, err
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return Result{}, err
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return Result{}, fmt.Errorf("%w: result must be one JSON object", ErrInvalidResult)
	}
	if err := requireFields(trimmed, []string{"schema_version", "base_revision", "facts", "plan_updates", "decision_updates", "remove_or_supersede"}); err != nil {
		return Result{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	var wire wireResult
	if err := dec.Decode(&wire); err != nil {
		return Result{}, fmt.Errorf("%w: malformed JSON: %v", ErrInvalidResult, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return Result{}, fmt.Errorf("%w: trailing JSON", ErrInvalidResult)
	}
	if wire.SchemaVersion != capsule.SchemaVersion {
		return Result{}, fmt.Errorf("%w: schema_version %d", ErrInvalidResult, wire.SchemaVersion)
	}
	if err := validateParent(opts, wire.BaseRevision); err != nil {
		return Result{}, err
	}
	if wire.Facts == nil || wire.PlanUpdates == nil || wire.DecisionUpdates == nil || wire.RemoveOrSupersede == nil {
		return Result{}, fmt.Errorf("%w: result arrays must be present and non-null", ErrInvalidResult)
	}
	if len(wire.Facts)+len(wire.PlanUpdates)+len(wire.DecisionUpdates)+len(wire.RemoveOrSupersede) > limits.MaxItems {
		return Result{}, fmt.Errorf("%w: item count exceeds bound", ErrInvalidResult)
	}
	active := activeParentDecisions(opts.Previous)
	allowed := sourceRefs(opts.AllowedSourceRefs)
	result := Result{SchemaVersion: wire.SchemaVersion, BaseRevision: wire.BaseRevision}
	seenFactIDs := make(map[string]struct{}, len(wire.Facts))
	for i, item := range wire.Facts {
		fact, err := validateFact(item, i, limits, allowed)
		if err != nil {
			return Result{}, err
		}
		if _, exists := seenFactIDs[fact.ID]; exists {
			return Result{}, fmt.Errorf("%w: duplicate fact id %q", ErrInvalidResult, fact.ID)
		}
		seenFactIDs[fact.ID] = struct{}{}
		result.Facts = append(result.Facts, fact)
	}
	seenPlanIDs := make(map[string]struct{}, len(wire.PlanUpdates))
	for i, item := range wire.PlanUpdates {
		plan, err := validatePlan(item, i, limits, allowed)
		if err != nil {
			return Result{}, err
		}
		if _, exists := seenPlanIDs[plan.ID]; exists {
			return Result{}, fmt.Errorf("%w: duplicate plan update id %q", ErrInvalidResult, plan.ID)
		}
		seenPlanIDs[plan.ID] = struct{}{}
		result.PlanUpdates = append(result.PlanUpdates, plan)
	}
	seenDecisionIDs := make(map[string]struct{}, len(wire.DecisionUpdates))
	seenConflictKeys := make(map[string]struct{}, len(wire.DecisionUpdates))
	for i, item := range wire.DecisionUpdates {
		decision, err := validateDecision(item, i, limits, allowed, active)
		if err != nil {
			return Result{}, err
		}
		if _, exists := seenDecisionIDs[decision.ID]; exists {
			return Result{}, fmt.Errorf("%w: duplicate decision update id %q", ErrInvalidResult, decision.ID)
		}
		if decision.Status == capsule.DecisionActive {
			if _, exists := seenConflictKeys[decision.ConflictKey]; exists {
				return Result{}, fmt.Errorf("%w: multiple active decisions for conflict key %q", ErrInvalidResult, decision.ConflictKey)
			}
			seenConflictKeys[decision.ConflictKey] = struct{}{}
		}
		seenDecisionIDs[decision.ID] = struct{}{}
		result.Decisions = append(result.Decisions, decision)
	}
	seenRemovalIDs := make(map[string]struct{}, len(wire.RemoveOrSupersede))
	for i, item := range wire.RemoveOrSupersede {
		removal, err := validateRemoval(item, i, limits, allowed, active)
		if err != nil {
			return Result{}, err
		}
		if _, exists := seenRemovalIDs[removal.ID]; exists {
			return Result{}, fmt.Errorf("%w: duplicate removal id %q", ErrInvalidResult, removal.ID)
		}
		if _, exists := seenDecisionIDs[removal.ID]; exists {
			return Result{}, fmt.Errorf("%w: decision/removal duplicate id %q", ErrInvalidResult, removal.ID)
		}
		seenRemovalIDs[removal.ID] = struct{}{}
		result.Removals = append(result.Removals, removal)
	}
	return result, nil
}

// Delta converts validated semantic additions and terminal decision changes
// into the capsule's authority-safe delta. The parser has already established
// that every transition targets an active semantic parent decision.
func (r Result) Delta(branchBinding, sourceHighWatermark string) capsule.Delta {
	delta := capsule.Delta{SchemaVersion: r.SchemaVersion, BaseRevision: r.BaseRevision, BranchBinding: branchBinding, SourceHighWatermark: sourceHighWatermark}
	if len(r.PlanUpdates) > 0 {
		delta.Plan = &capsule.Plan{Status: capsule.PlanAccepted, Source: capsule.SourceSemantic, Steps: make([]capsule.PlanStep, 0, len(r.PlanUpdates))}
		for _, update := range r.PlanUpdates {
			delta.Plan.Steps = append(delta.Plan.Steps, capsule.PlanStep{ID: update.ID, Text: update.Text, Status: update.Status, SourceRef: update.SourceRef})
		}
	}
	for _, update := range r.Decisions {
		update.Authority = capsule.AuthoritySemantic
		update.Source = capsule.SourceSemantic
		delta.Decisions = append(delta.Decisions, capsule.Decision{ID: update.ID, ConflictKey: update.ConflictKey, Supersedes: append([]string(nil), update.Supersedes...), Statement: update.Statement, Status: update.Status, Authority: update.Authority, Rationale: update.Rationale, SourceRef: update.SourceRef})
	}
	for _, removal := range r.Removals {
		delta.DecisionTransitions = append(delta.DecisionTransitions, capsule.DecisionTransition{
			ID: removal.ID, Status: removal.Status, Authority: capsule.AuthoritySemantic, SourceRef: removal.SourceRef,
		})
	}
	for _, fact := range r.Facts {
		value := capsule.Fact{ID: fact.ID, Statement: fact.Statement, Status: fact.Status, Authority: capsule.AuthoritySemantic, Rationale: fact.Rationale, SourceRef: fact.SourceRef}
		switch fact.Kind {
		case FactConstraint:
			delta.Constraints = append(delta.Constraints, value)
		case FactRejectedAlternative:
			delta.RejectedAlternatives = append(delta.RejectedAlternatives, value)
		case FactOpenQuestion:
			delta.OpenQuestions = append(delta.OpenQuestions, value)
		}
	}
	return delta
}

func validateParent(opts ParseOptions, base uint64) error {
	if opts.Previous.SchemaVersion == 0 {
		if base != 0 {
			return fmt.Errorf("%w: base_revision %d without parent capsule", ErrInvalidResult, base)
		}
		return nil
	}
	expected := strings.TrimSpace(opts.ExpectedBranch)
	if expected == "" {
		expected = opts.Previous.BranchBinding
	}
	if err := opts.Previous.VerifyBranch(expected); err != nil {
		return fmt.Errorf("%w: parent capsule: %v", ErrInvalidResult, err)
	}
	if base != opts.Previous.Revision {
		return fmt.Errorf("%w: base_revision %d want %d", ErrInvalidResult, base, opts.Previous.Revision)
	}
	return nil
}

func activeParentDecisions(previous capsule.Envelope) map[string]capsule.Decision {
	out := make(map[string]capsule.Decision)
	for _, decision := range previous.Decisions {
		if decision.Status == capsule.DecisionActive {
			out[decision.ID] = decision
		}
	}
	return out
}
