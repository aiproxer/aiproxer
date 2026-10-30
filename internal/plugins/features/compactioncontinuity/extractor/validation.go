package extractor

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/capsule"
)

func sourceRefs(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[strings.TrimSpace(value)] = struct{}{}
	}
	return out
}

func validatePlan(item wirePlan, i int, l Limits, allowed map[string]struct{}) (PlanUpdate, error) {
	if err := validateText(fmt.Sprintf("plan_updates[%d].id", i), item.ID, l.MaxStringBytes, true); err != nil {
		return PlanUpdate{}, err
	}
	if err := validateText(fmt.Sprintf("plan_updates[%d].text", i), item.Text, l.MaxStringBytes, true); err != nil {
		return PlanUpdate{}, err
	}
	status, err := stepStatus(item.Status)
	if err != nil {
		return PlanUpdate{}, fmt.Errorf("%w: %v", ErrInvalidResult, err)
	}
	if err := validateSourceRef(item.SourceRef, l, allowed); err != nil {
		return PlanUpdate{}, fmt.Errorf("%w: plan_updates[%d]: %v", ErrInvalidResult, i, err)
	}
	return PlanUpdate{ID: item.ID, Text: item.Text, Status: status, SourceRef: item.SourceRef}, nil
}

func validateFact(item wireFact, i int, l Limits, allowed map[string]struct{}) (FactUpdate, error) {
	if err := validateText(fmt.Sprintf("facts[%d].kind", i), item.Kind, l.MaxStringBytes, true); err != nil {
		return FactUpdate{}, err
	}
	kind := FactKind(item.Kind)
	if kind != FactConstraint && kind != FactRejectedAlternative && kind != FactOpenQuestion {
		return FactUpdate{}, fmt.Errorf("%w: facts[%d] unknown kind %q", ErrInvalidResult, i, item.Kind)
	}
	if err := validateText(fmt.Sprintf("facts[%d].id", i), item.ID, l.MaxStringBytes, true); err != nil {
		return FactUpdate{}, err
	}
	if err := validateText(fmt.Sprintf("facts[%d].statement", i), item.Statement, l.MaxStringBytes, true); err != nil {
		return FactUpdate{}, err
	}
	status, err := factStatus(item.Status)
	if err != nil {
		return FactUpdate{}, fmt.Errorf("%w: %v", ErrInvalidResult, err)
	}
	if err := validateText(fmt.Sprintf("facts[%d].rationale", i), item.Rationale, l.MaxStringBytes, false); err != nil {
		return FactUpdate{}, err
	}
	if err := validateSourceRef(item.SourceRef, l, allowed); err != nil {
		return FactUpdate{}, fmt.Errorf("%w: facts[%d]: %v", ErrInvalidResult, i, err)
	}
	return FactUpdate{Kind: kind, ID: item.ID, Statement: item.Statement, Status: status, Rationale: item.Rationale, SourceRef: item.SourceRef}, nil
}

func validateDecision(item wireDecision, i int, l Limits, allowed map[string]struct{}, active map[string]capsule.Decision) (DecisionUpdate, error) {
	if err := validateText(fmt.Sprintf("decision_updates[%d].id", i), item.ID, l.MaxStringBytes, true); err != nil {
		return DecisionUpdate{}, err
	}
	if err := validateConflict(item.ConflictKey, l.MaxConflictBytes); err != nil {
		return DecisionUpdate{}, fmt.Errorf("%w: decision_updates[%d]: %v", ErrInvalidResult, i, err)
	}
	if err := validateText(fmt.Sprintf("decision_updates[%d].statement", i), item.Statement, l.MaxStringBytes, true); err != nil {
		return DecisionUpdate{}, err
	}
	status, err := decisionStatus(item.Status)
	if err != nil {
		return DecisionUpdate{}, fmt.Errorf("%w: %v", ErrInvalidResult, err)
	}
	if err := validateText(fmt.Sprintf("decision_updates[%d].rationale", i), item.Rationale, l.MaxStringBytes, false); err != nil {
		return DecisionUpdate{}, err
	}
	if err := validateSourceRef(item.SourceRef, l, allowed); err != nil {
		return DecisionUpdate{}, fmt.Errorf("%w: decision_updates[%d]: %v", ErrInvalidResult, i, err)
	}
	if len(item.Supersedes) > l.MaxSupersedes {
		return DecisionUpdate{}, fmt.Errorf("%w: decision_updates[%d] supersedes count", ErrInvalidResult, i)
	}
	if existing, exists := active[item.ID]; exists {
		// A retry may replay the exact semantic decision already present in the
		// parent capsule. It is a no-op and must bypass the conflict-key rule;
		// any changed field under the stable ID remains rejected.
		if existing.Authority != capsule.AuthoritySemantic || !sameSemanticDecision(existing, item, status) {
			return DecisionUpdate{}, fmt.Errorf("%w: decision_updates[%d] changed or non-semantic parent decision id", ErrInvalidResult, i)
		}
		return semanticDecisionUpdate(item, status), nil
	}
	seen := make(map[string]struct{}, len(item.Supersedes))
	for _, id := range item.Supersedes {
		if err := validateText("supersedes", id, l.MaxStringBytes, true); err != nil {
			return DecisionUpdate{}, err
		}
		if _, duplicate := seen[id]; duplicate {
			return DecisionUpdate{}, fmt.Errorf("%w: duplicate supersedes %q", ErrInvalidResult, id)
		}
		seen[id] = struct{}{}
		parent, ok := active[id]
		if !ok || parent.Status != capsule.DecisionActive {
			return DecisionUpdate{}, fmt.Errorf("%w: supersedes %q is not a known active parent decision", ErrInvalidResult, id)
		}
	}
	if existing, ok := activeByConflict(item.ConflictKey, active); ok && !contains(item.Supersedes, existing.ID) {
		return DecisionUpdate{}, fmt.Errorf("%w: conflict key %q must explicitly supersede %q", ErrInvalidResult, item.ConflictKey, existing.ID)
	}
	return semanticDecisionUpdate(item, status), nil
}

func semanticDecisionUpdate(item wireDecision, status capsule.DecisionStatus) DecisionUpdate {
	return DecisionUpdate{ID: item.ID, ConflictKey: item.ConflictKey, Supersedes: append([]string(nil), item.Supersedes...), Statement: item.Statement, Status: status, Rationale: item.Rationale, SourceRef: item.SourceRef, Authority: capsule.AuthoritySemantic, Source: capsule.SourceSemantic}
}

func sameSemanticDecision(existing capsule.Decision, item wireDecision, status capsule.DecisionStatus) bool {
	if existing.ConflictKey != item.ConflictKey || existing.Statement != item.Statement || existing.Status != status || existing.Rationale != item.Rationale || existing.SourceRef != item.SourceRef {
		return false
	}
	if len(existing.Supersedes) != len(item.Supersedes) {
		return false
	}
	counts := make(map[string]int, len(existing.Supersedes))
	for _, id := range existing.Supersedes {
		counts[id]++
	}
	for _, id := range item.Supersedes {
		counts[id]--
	}
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}
	return true
}

func validateRemoval(item wireRemoval, i int, l Limits, allowed map[string]struct{}, active map[string]capsule.Decision) (Removal, error) {
	if err := validateText(fmt.Sprintf("remove_or_supersede[%d].id", i), item.ID, l.MaxStringBytes, true); err != nil {
		return Removal{}, err
	}
	target, ok := active[item.ID]
	if !ok {
		return Removal{}, fmt.Errorf("%w: remove_or_supersede[%d] unknown active decision %q", ErrInvalidResult, i, item.ID)
	}
	if target.Authority != capsule.AuthoritySemantic {
		return Removal{}, fmt.Errorf("%w: remove_or_supersede[%d] protected decision authority %q", ErrInvalidResult, i, target.Authority)
	}
	status, err := decisionStatus(item.Status)
	if err != nil || status == capsule.DecisionActive {
		return Removal{}, fmt.Errorf("%w: remove_or_supersede[%d] status must be superseded or rejected", ErrInvalidResult, i)
	}
	if err := validateSourceRef(item.SourceRef, l, allowed); err != nil {
		return Removal{}, fmt.Errorf("%w: remove_or_supersede[%d]: %v", ErrInvalidResult, i, err)
	}
	return Removal{ID: item.ID, Status: status, SourceRef: item.SourceRef}, nil
}

func activeByConflict(key string, active map[string]capsule.Decision) (capsule.Decision, bool) {
	for _, decision := range active {
		if decision.ConflictKey == key {
			return decision, true
		}
	}
	return capsule.Decision{}, false
}

func contains(values []string, want string) bool {
	return slices.Contains(values, want)
}

func validateSourceRef(value string, l Limits, allowed map[string]struct{}) error {
	if err := validateText("source_ref", value, l.MaxSourceRefBytes, true); err != nil {
		return err
	}
	if strings.TrimSpace(value) != value || strings.ContainsAny(value, "\t\r\n") {
		return errors.New("source_ref must be a normalized reference")
	}
	if _, ok := allowed[value]; !ok {
		return fmt.Errorf("source_ref %q is not in sanitized source", value)
	}
	return nil
}

func validateConflict(value string, max int) error {
	if err := validateText("conflict_key", value, max, true); err != nil {
		return err
	}
	if strings.TrimSpace(value) != value || !conflictKeyRE.MatchString(value) {
		return errors.New("conflict_key must be normalized lowercase slot syntax")
	}
	return nil
}

func validateText(name, value string, max int, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalidResult, name)
	}
	if len([]byte(value)) > max {
		return fmt.Errorf("%w: %s exceeds %d bytes", ErrInvalidResult, name, max)
	}
	if !utf8.ValidString(value) || strings.IndexFunc(value, func(r rune) bool { return r < 0x20 && r != '\t' }) >= 0 {
		return fmt.Errorf("%w: %s contains invalid control text", ErrInvalidResult, name)
	}
	return nil
}

func stepStatus(value string) (capsule.StepStatus, error) {
	switch value {
	case string(capsule.StepPending):
		return capsule.StepPending, nil
	case string(capsule.StepInProgress):
		return capsule.StepInProgress, nil
	case string(capsule.StepCompleted):
		return capsule.StepCompleted, nil
	case string(capsule.StepCancelled):
		return capsule.StepCancelled, nil
	default:
		return "", fmt.Errorf("unknown plan status %q", value)
	}
}

func factStatus(value string) (capsule.FactStatus, error) {
	switch value {
	case string(capsule.FactActive):
		return capsule.FactActive, nil
	case string(capsule.FactSuperseded):
		return capsule.FactSuperseded, nil
	case string(capsule.FactRejected):
		return capsule.FactRejected, nil
	default:
		return "", fmt.Errorf("unknown fact status %q", value)
	}
}

func decisionStatus(value string) (capsule.DecisionStatus, error) {
	switch value {
	case string(capsule.DecisionActive):
		return capsule.DecisionActive, nil
	case string(capsule.DecisionSuperseded):
		return capsule.DecisionSuperseded, nil
	case string(capsule.DecisionRejected):
		return capsule.DecisionRejected, nil
	default:
		return "", fmt.Errorf("unknown decision status %q", value)
	}
}
