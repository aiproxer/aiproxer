package proxymemory

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// The dispatch-time enforcement of the per-phase model policy against the
// final compiled actual target (design §2 and §3; Requirements 1.4 and 1.5).
//
// The checks run against the compiled actual target — the target the runtime
// addresses after the model-alias resolution and the route-parameter merge —
// and not against the requested target, so neither an alias nor a query
// parameter can point the call at a target the inventory, a lock or the
// capability check does not cover.
//
// Every candidate the compiled route can address is checked, not only the
// first one: the runtime may fail over to a later candidate or run a parallel
// leg, so a candidate that violates the policy would otherwise serve the
// call unchecked.
//
// The operator's inventory is a **policy** statement, never provider-verified
// support: the backend capability set carries a boolean reasoning capability
// and the model catalog a tri-state per-model fact, and neither states which
// effort *values* a provider supports (design §2). The inventory therefore
// constrains what the phase may use, and the capability check refuses an
// effort the actual target cannot carry at all. Neither check ever states
// that a provider supports an effort value.

// CheckCompiledTarget runs the phase's inventory, lock and capability checks
// against the final compiled actual target.
//
// The refusal is explicit and names the phase and the offending field; it
// never echoes a value, so a refusal can be logged without carrying an
// owner-supplied or operator-supplied string. A phase that is refused is
// never dispatched (Requirement 1.4).
func (c Config) CheckCompiledTarget(ctx context.Context, phase Phase, call aipapi.Call, target CompiledTarget) error {
	policy, ok := c.Models.For(phase)
	if !ok {
		return &PolicyError{Phase: phase, Field: FieldRoute, Reason: "the phase is not part of the model policy"}
	}
	effective, err := c.EffectiveTarget(phase)
	if err != nil {
		return err
	}
	reference, hasReference, err := c.compiledLockReference(ctx, phase, policy, effective)
	if err != nil {
		return err
	}
	for _, candidate := range target.Candidates {
		if err := checkCompiledCandidate(phase, policy, effective, reference, hasReference, call, candidate); err != nil {
			return err
		}
	}
	return nil
}

// compiledLockReference compiles the operator's own locked reference into the
// compiled identity of the operator's effective target.
//
// The reference is the operator's **effective** value: the operator's
// declared value completed by the operator's default route. A lock on a field
// the operator named no value for therefore pins the value the operator's
// route supplies, and it pins nothing only when neither the declaration nor
// the route names one.
//
// The reference keeps **every** leaf the operator's reference compiles to:
// the operator's own unchanged alias may legitimately resolve to several
// leaves, and each of them is the operator's own identity. The comparison
// against the reference set is made leaf by leaf, on the locked fields only.
//
// The compilation is identity-only: it resolves no capability, so it performs
// no provider I/O and no goroutine. The reference is compiled independently
// of the phase's own compile, so a later divergence between the operator's
// reference and the phase's route cannot make the lock tautological.
func (c Config) compiledLockReference(ctx context.Context, phase Phase, policy PhasePolicy, effective PhaseTarget) ([]CompiledIdentity, bool, error) {
	needBackend := policy.Locks.Backend && effective.Backend != ""
	needModel := policy.Locks.Model && effective.Model != ""
	if !needBackend && !needModel {
		return nil, false, nil
	}
	selector, err := c.referenceSelector(phase, policy, effective)
	if err != nil {
		return nil, false, err
	}
	// The synthetic call carries the reference selector and no content: the
	// compilation reads only the selector, the options and the shape.
	call := aipapi.Call{Route: aipapi.RouteIntent{Selector: selector}}
	reference, err := CompileIdentity(ctx, c.TargetCompiler, phase, call)
	if err != nil {
		return nil, false, err
	}
	return reference, true, nil
}

// referenceSelector returns the selector the operator's own reference is
// compiled from.
//
// The operator's route is kept verbatim when the operator named no target:
// the route is then the operator's own, with its own failover policy, and
// the derivation would refuse a multi-target route. Otherwise the reference
// selector is derived with the same function the phase uses to build the
// selector it runs with, from the operator's *effective* target, so the
// operator's global block, leaf annotations and leaf query are preserved.
// There is no second route parser: the derivation is the existing one.
func (c Config) referenceSelector(phase Phase, policy PhasePolicy, effective PhaseTarget) (string, error) {
	route := c.phaseRoute(phase)
	if policy.Defaults.Backend == "" && policy.Defaults.Model == "" {
		return strings.TrimSpace(route), nil
	}
	return DeriveRouteSelector(phase, route, effective.Backend, effective.Model)
}

// checkCompiledCandidate checks one compiled candidate of one phase.
func checkCompiledCandidate(
	phase Phase,
	policy PhasePolicy,
	effective PhaseTarget,
	reference []CompiledIdentity,
	hasReference bool,
	call aipapi.Call,
	candidate CompiledCandidate,
) error {
	if err := checkLockedIdentity(phase, policy, effective, candidate, reference, hasReference); err != nil {
		return err
	}
	effort := candidate.Options.ReasoningEffort
	if err := checkLockedField(phase, FieldReasoningEffort, policy.Locks.ReasoningEffort, effective.ReasoningEffort, effort); err != nil {
		return err
	}
	if err := checkInventoryField(phase, FieldBackend, policy.Inventory.Backends, candidate.Backend); err != nil {
		return err
	}
	if err := checkInventoryField(phase, FieldModel, policy.Inventory.Models, candidate.Model); err != nil {
		return err
	}
	if err := checkInventoryEffort(phase, policy.Inventory, candidate.Backend, candidate.Model, effort); err != nil {
		return err
	}
	return checkEffortCapability(phase, call, candidate, effort)
}

// checkLockedField refuses a candidate whose locked field carries a value
// other than the operator's own.
//
// The lock is checked against the final compiled actual target, so neither an
// alias nor a query parameter can move a locked field (Requirement 1.2;
// design §3). A field the operator named no value for pins nothing: there is
// no operator value to protect, and the deployment's own default serves it.
//
// The reasoning effort is never alias-resolved — an effort token names a
// level, not a target — so the effort lock is compared directly and a route
// parameter that moves a locked effort is a bypass of the operator's hold.
func checkLockedField(phase Phase, field string, locked bool, effective, actual string) error {
	if !locked || effective == "" {
		return nil
	}
	if actual != effective {
		return &PolicyError{Phase: phase, Field: field, Reason: "the field is locked by the operator"}
	}
	return nil
}

// checkLockedIdentity refuses a candidate whose locked backend or model is
// not the operator's own, compared in the compiled domain.
//
// The backend and the model are alias-resolved, so the operator's reference
// is compared with the compiled identity of the candidate and not with the
// raw string the operator wrote. The operator's own unchanged alias
// therefore compiles to the candidate's identity and is not refused, while a
// candidate that addresses a different target still is.
//
// The comparison is made against the operator's **whole** compiled reference
// set, and the leaf is the unit of identity: a candidate matches the
// reference when **one** leaf carries the candidate's value for **every**
// locked field. A reference that resolves to several leaves therefore locks
// every one of them, and a candidate that combines the backend of one leaf
// with the model of another is not the operator's own.
//
// Only the locked fields are compared, and the runtime's opaque candidate
// key is never parsed and never compared: the key carries the route
// parameters, so a full-key match would over-lock an unlocked effort that a
// route parameter legitimately moved.
func checkLockedIdentity(
	phase Phase,
	policy PhasePolicy,
	effective PhaseTarget,
	candidate CompiledCandidate,
	reference []CompiledIdentity,
	hasReference bool,
) error {
	if !policy.Locks.Backend && !policy.Locks.Model {
		return nil
	}
	if !hasReference {
		// The operator named no value for the locked field: the lock pins
		// nothing and the deployment's own default serves it.
		return nil
	}
	for _, leaf := range reference {
		if policy.Locks.Backend && leaf.Backend != candidate.Backend {
			continue
		}
		if policy.Locks.Model && leaf.Model != candidate.Model {
			continue
		}
		return nil
	}
	// No leaf carries the whole locked identity. The refusal names the
	// first locked field that no leaf carries; when every locked field is
	// carried by some leaf but not by a common one, the model names the
	// combination that is not the operator's own.
	if policy.Locks.Backend && !referenceCarries(reference, FieldBackend, candidate.Backend) {
		return &PolicyError{Phase: phase, Field: FieldBackend, Reason: "the field is locked by the operator"}
	}
	if policy.Locks.Model && !referenceCarries(reference, FieldModel, candidate.Model) {
		return &PolicyError{Phase: phase, Field: FieldModel, Reason: "the field is locked by the operator"}
	}
	return &PolicyError{Phase: phase, Field: FieldModel, Reason: "the field is locked by the operator"}
}

// referenceCarries reports whether any leaf of the operator's reference
// carries the candidate's value for one locked field.
func referenceCarries(reference []CompiledIdentity, field, value string) bool {
	for _, leaf := range reference {
		if field == FieldBackend {
			if leaf.Backend == value {
				return true
			}
			continue
		}
		if leaf.Model == value {
			return true
		}
	}
	return false
}

// checkInventoryField refuses a candidate whose field the phase's allowed
// inventory does not cover.
//
// An inventory the operator did not declare constrains nothing: the phase
// keeps the operator's own route, and no owner may select a value for that
// field (design §3: an empty inventory means "no user-selectable value", not
// "everything allowed"). A declared inventory is the closed set of values the
// phase may use, so a candidate outside it is refused (Requirement 1.4).
func checkInventoryField(phase Phase, field string, inventory []string, actual string) error {
	if len(inventory) == 0 {
		return nil
	}
	if slices.Contains(inventory, actual) {
		return nil
	}
	return &PolicyError{Phase: phase, Field: field, Reason: "the value is not in the phase's allowed inventory"}
}

// checkInventoryEffort refuses a candidate whose reasoning effort the
// phase's per-target effort policy does not cover.
//
// The allowed efforts are provider-specific, so they are declared per phase
// and per target, and the target they are read for is the actual one the
// candidate addresses. A per-target policy the operator did not declare
// constrains nothing: the phase keeps the operator's own effort, and no owner
// may select a value for it (design §3: an empty inventory means "no
// user-selectable value", not "everything allowed"). A declared policy is the
// closed set of efforts the phase may use, so an effort outside it is
// refused (Requirement 1.4).
func checkInventoryEffort(phase Phase, inventory PhaseInventory, backend, model, effort string) error {
	if len(inventory.Targets) == 0 {
		return nil
	}
	allowed := inventory.AllowedEfforts(backend, model)
	if slices.Contains(allowed, effort) {
		return nil
	}
	return &PolicyError{Phase: phase, Field: FieldReasoningEffort, Reason: "the value is not in the phase's allowed inventory"}
}

// checkEffortCapability refuses a phase whose reasoning effort the actual
// target cannot carry.
//
// The runtime strips a downgraded reasoning effort in two places: the
// pre-open candidate admission and the post-request transform. A stripped
// effort would silently violate the phase's declared effort and the
// operator's lock, so the phase is refused before the dispatch instead of
// being degraded afterwards (design §2; Requirement 1.4, U8).
//
// The capability facts are coarse: the backend capability set carries a
// boolean reasoning capability and the model catalog a tri-state per-model
// fact. Neither states which effort values a provider supports, so this
// check refuses an effort the target cannot carry at all and never claims
// that a provider supports an effort value. The three states never collapse:
// an unknown model fact is not support, so the policy's compiled target
// reports the capability as absent and this check refuses it exactly like an
// explicit denial. The per-value support is a provider-owned fact that the
// pre-open guard applies at the provider open, where the provider's own answer
// is available.
func checkEffortCapability(phase Phase, call aipapi.Call, candidate CompiledCandidate, effort string) error {
	if effort == "" {
		return nil
	}
	negotiation := candidate.Negotiation(call)
	if !slices.Contains(negotiation.Downgraded, aipapi.CapabilityReasoning) {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrReasoningCapabilityRefused, &PolicyError{
		Phase:  phase,
		Field:  FieldReasoningEffort,
		Reason: "the actual target does not prove the reasoning capability",
	})
}
