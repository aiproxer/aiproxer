package proxymemory

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// The per-phase model policy of the proxy-memory feature (design §3).
//
// The domain is closed on purpose: two phases, three user-facing fields and
// one allowed inventory per phase. There is no generic policy registry here
// and no fourth user-facing field: the route selector is derived from the
// operator's default route and the effective backend and model, so no
// client-facing field names the target a phase calls.
//
// The operator's allowed inventory is a *policy* statement about what a user
// may choose. It is never presented as provider-verified support: the
// per-provider effort values live in no capability surface today, so the
// feature validates the effort lexically and leaves the provider check to the
// compiled actual target (design §2). The derived selector is the requested
// target; the checks against the served target belong to task 2.2.

// Phase names one background phase of the feature.
type Phase string

const (
	// PhaseExtraction is the phase-1 extraction phase.
	PhaseExtraction Phase = "extraction"
	// PhaseConsolidation is the phase-2 consolidation phase.
	PhaseConsolidation Phase = "consolidation"
)

// The three fields a user may choose for a phase, and the route field the
// derived selector is built from.
const (
	// FieldBackend names the backend field of a phase target.
	FieldBackend = "backend"
	// FieldModel names the model field of a phase target.
	FieldModel = "model"
	// FieldReasoningEffort names the reasoning-effort field of a phase
	// target.
	FieldReasoningEffort = "reasoning_effort"
	// FieldRoute names the operator's default route of a phase.
	FieldRoute = "route"
)

// The bounded spelling of the three target fields. A backend is an
// identifier, a model is a provider model name and a reasoning effort is a
// provider effort token; none of them may carry whitespace or a separator of
// the route grammar, so a value can never smuggle a second target into a
// derived selector.
const (
	maxBackendBytes = 64
	maxModelBytes   = 128
	maxEffortBytes  = 32
)

// defaultPhaseEffort is the reasoning effort each phase used before the
// per-phase target existed. An operator that names no default effort keeps
// today's value (design §3).
func defaultPhaseEffort(phase Phase) string {
	switch phase {
	case PhaseExtraction:
		return "low"
	case PhaseConsolidation:
		return "medium"
	}
	return ""
}

// PhaseTarget is the triple one phase runs with: the backend, the model and
// the reasoning effort. An empty field means the operator named no value for
// it, and the field keeps its default.
type PhaseTarget struct {
	Backend         string
	Model           string
	ReasoningEffort string
}

// PhaseInventory is the per-phase allowed inventory: the backends and the
// models a user may choose, and the per-target effort policy.
//
// The effort values are provider-specific, so they are declared per target:
// the same effort value can be allowed for one target and refused for another
// (design §3). An empty list means "no user-selectable override" for that
// field, not "everything allowed".
type PhaseInventory struct {
	Backends []string
	Models   []string
	Targets  []TargetPolicy
}

// TargetPolicy is the operator's allowed policy for one target: the backend
// and the model that identify it, and the reasoning efforts a user may choose
// for it.
type TargetPolicy struct {
	Backend          string
	Model            string
	ReasoningEfforts []string
}

// AllowedEfforts returns the reasoning efforts the inventory allows for the
// given target. A target the inventory does not name allows none: an empty
// list means "no user-selectable override", not "everything allowed".
func (i PhaseInventory) AllowedEfforts(backend, model string) []string {
	for _, target := range i.Targets {
		if target.Backend == backend && target.Model == model {
			return target.ReasoningEfforts
		}
	}
	return nil
}

// PhasePolicy is the operator's policy for one phase: the defaults the proxy
// applies, the fields the owner may not change, and the inventory an owner
// may choose from.
type PhasePolicy struct {
	Defaults  PhaseTarget
	Locks     TargetLocks
	Inventory PhaseInventory
}

// ModelPolicy is the per-phase model policy of both background phases.
type ModelPolicy struct {
	Extraction    PhasePolicy
	Consolidation PhasePolicy
	// Strict selects the strict policy for the facts the deployment does not
	// prove: the installation of the actual target and the provider's
	// per-value support of a reasoning effort.
	//
	// The default policy is the interoperable one: an unproven fact is
	// served, counted and logged, so a legacy route keeps working. The
	// strict policy refuses instead, which is the operator's explicit
	// choice (VISION priority 1). It is an operator switch, not a
	// fourth owner-selectable field: it adds no target, no model and no
	// effort to the owner-facing surface, and it never widens what an
	// owner may choose.
	Strict bool
}

// For returns the policy of the named phase.
func (p ModelPolicy) For(phase Phase) (PhasePolicy, bool) {
	switch phase {
	case PhaseExtraction:
		return p.Extraction, true
	case PhaseConsolidation:
		return p.Consolidation, true
	}
	return PhasePolicy{}, false
}

// TargetOverrides holds an owner's unprojected per-field overrides. An empty
// field means the owner did not name it, so the field keeps the operator
// default (design §3; Requirement 1.3).
type TargetOverrides struct {
	Backend         string
	Model           string
	ReasoningEffort string
}

// TargetLocks holds the operator's per-field locks for one phase. A locked
// field resolves to the operator's value, so a lock introduced after an
// override existed wins and the owner value is not effective (design §3;
// Requirement 1.2).
type TargetLocks struct {
	Backend         bool
	Model           bool
	ReasoningEffort bool
}

// PolicyError is an explicit refusal of a model-policy value. It names the
// phase and the field and never echoes the offending value, so a refusal can
// be logged without carrying an owner-supplied string.
type PolicyError struct {
	Phase  Phase
	Field  string
	Reason string
}

// ErrReasoningCapabilityRefused classifies the refusal of a phase whose actual
// target does not prove it can carry the requested reasoning effort.
//
// The capability is a tri-state fact: the backend capability set carries a
// boolean reasoning capability and the model catalog a per-model fact. A false
// capability and an unproven one are both refused — an unknown state is never
// treated as support — and the bounded kind lets a caller count the refusal
// without parsing a message.
var ErrReasoningCapabilityRefused = errors.New(ID + ": the actual target does not prove the reasoning capability")

// ErrNegotiatedEffortStrip classifies the refusal of a proxy-memory call whose
// reasoning effort the runtime's negotiated downgrade would strip.
//
// The runtime applies negotiated downgrades at two places: the pre-open
// candidate admission and the post-request transform. For a governed phase
// that carried a requested effort, the strip is converted to this explicit
// refusal instead of a silent downgrade, before any provider I/O.
var ErrNegotiatedEffortStrip = errors.New(ID + ": the runtime negotiated the reasoning effort away")

func (e *PolicyError) Error() string {
	return ID + ": " + string(e.Phase) + ": " + e.Field + ": " + e.Reason
}

// PhaseSettings is the closed policy result of one phase: the effective
// target, the per-field lock flags and the owner's sanitized overrides.
//
// The effective target is the triple the phase runs with, the lock flags say
// which fields the operator holds, and the overrides are the owner's own
// values with every locked field omitted. The three parts travel together
// because the settings projection shows them together: the effective values
// and the per-field lock flags (design §4).
type PhaseSettings struct {
	Phase Phase
	// Defaults are the administrator's baseline for the phase: the target the
	// phase runs with when the owner has no override. It is the operator's
	// declared defaults completed with the operator's default route, so the
	// owner can tell what clearing an override would restore.
	Defaults PhaseTarget
	// Declared is the target as the operator and the owner named it, before
	// the operator's default route completes it: a backend or a model that
	// nobody named stays empty. The route selector is derived from it, so a
	// target nobody selected keeps the operator's own route, and the phase
	// runs with the same route the dispatch path runs with.
	Declared PhaseTarget
	// Effective is the target the phase runs with under the owner's override.
	Effective PhaseTarget
	// Locks are the operator's per-field lock flags.
	Locks TargetLocks
	// Overrides are the owner's own values with every locked field omitted.
	Overrides TargetOverrides
	// DependencyMasked reports that the stored effort override depended on
	// an inherited operator target whose resolved value changed, so this
	// projection uses the operator's current default effort instead. The
	// stored overrides are unchanged; the caller may count and log the
	// bounded masking event.
	DependencyMasked bool
}

// ResolveDeclaredTarget resolves one phase's declared triple: the locked
// operator value, else the permitted owner value, else the operator default.
//
// A locked field resolves to the operator's default, so a lock introduced
// after an override existed wins and the owner value is not effective. An
// owner value is permitted only when the phase's inventory lists it: a value
// outside the inventory is refused explicitly and never silently dropped, and
// an empty inventory permits no override at all (design §3; Requirements 1.2,
// 1.3 and 1.4).
//
// A backend or a model that nobody named stays empty: the operator's default
// route supplies it when the route selector is derived, so the declared
// target is what the operator and the owner named.
//
// inherited is the target the operator's default route names. The allowed
// reasoning efforts are the *effective* target's, because the effort values
// are provider-specific: the same effort value can be allowed for one target
// and refused for another.
func ResolveDeclaredTarget(phase Phase, policy PhasePolicy, inherited PhaseTarget, locks TargetLocks, override TargetOverrides) (PhaseTarget, error) {
	declared, effective, err := resolveTargetFields(phase, policy, inherited, locks, override)
	if err != nil {
		return PhaseTarget{}, err
	}
	effortDefault := policy.Defaults.ReasoningEffort
	if effortDefault == "" {
		effortDefault = defaultPhaseEffort(phase)
	}
	effort, err := resolvePolicyField(phase, FieldReasoningEffort, effortDefault, locks.ReasoningEffort, override.ReasoningEffort, policy.Inventory.AllowedEfforts(effective.Backend, effective.Model))
	if err != nil {
		return PhaseTarget{}, err
	}
	return PhaseTarget{Backend: declared.Backend, Model: declared.Model, ReasoningEffort: effort}, nil
}

// resolveTargetFields resolves the backend and the model of one phase: the
// declared values (what the operator and the owner named) and the effective
// values (the declared values completed with the operator's default route).
//
// The effort is resolved separately because its allowed inventory belongs to
// the effective target, and some callers — notably the dependency-context
// comparison — need the target without resolving an effort that may be invalid.
func resolveTargetFields(phase Phase, policy PhasePolicy, inherited PhaseTarget, locks TargetLocks, override TargetOverrides) (declared, effective PhaseTarget, err error) {
	declared.Backend, err = resolvePolicyField(phase, FieldBackend, policy.Defaults.Backend, locks.Backend, override.Backend, policy.Inventory.Backends)
	if err != nil {
		return PhaseTarget{}, PhaseTarget{}, err
	}
	declared.Model, err = resolvePolicyField(phase, FieldModel, policy.Defaults.Model, locks.Model, override.Model, policy.Inventory.Models)
	if err != nil {
		return PhaseTarget{}, PhaseTarget{}, err
	}
	effective = declared
	if effective.Backend == "" {
		effective.Backend = inherited.Backend
	}
	if effective.Model == "" {
		effective.Model = inherited.Model
	}
	return declared, effective, nil
}

// ownerTargetIdentity resolves the phase's current target without resolving
// its reasoning effort: the dimensions the dependency context compares.
//
// It applies the current operator defaults, locks, default route and inventory
// exactly as the projection does, so a value the current policy refuses is an
// error here too, never a silently repaired target.
func (c Config) ownerTargetIdentity(phase Phase, stored TargetOverrides) (PhaseTarget, error) {
	policy, ok := c.Models.For(phase)
	if !ok {
		return PhaseTarget{}, &PolicyError{Phase: phase, Field: FieldRoute, Reason: "the phase is not part of the model policy"}
	}
	inherited, err := c.inheritedTarget(phase)
	if err != nil {
		return PhaseTarget{}, err
	}
	_, effective, err := resolveTargetFields(phase, policy, inherited, policy.Locks, sanitizeOverrides(policy.Locks, stored))
	if err != nil {
		return PhaseTarget{}, err
	}
	return effective, nil
}

// ResolveEffectiveTarget resolves one phase's effective triple: the declared
// target completed with the operator's default route's target, and the
// reasoning effort that effective target allows.
func ResolveEffectiveTarget(phase Phase, policy PhasePolicy, inherited PhaseTarget, locks TargetLocks, override TargetOverrides) (PhaseTarget, error) {
	declared, err := ResolveDeclaredTarget(phase, policy, inherited, locks, override)
	if err != nil {
		return PhaseTarget{}, err
	}
	if declared.Backend == "" {
		declared.Backend = inherited.Backend
	}
	if declared.Model == "" {
		declared.Model = inherited.Model
	}
	return declared, nil
}

// resolvePolicyField resolves one field of one phase.
func resolvePolicyField(phase Phase, field, operatorDefault string, locked bool, ownerValue string, inventory []string) (string, error) {
	if locked {
		return operatorDefault, nil
	}
	if ownerValue == "" {
		return operatorDefault, nil
	}
	if !slices.Contains(inventory, ownerValue) {
		return "", &PolicyError{
			Phase:  phase,
			Field:  field,
			Reason: "the value is not in the phase's allowed inventory",
		}
	}
	return ownerValue, nil
}

// OwnerSettings projects one phase's policy state for one owner.
//
// The stored overrides are the owner's own unprojected values; the effective
// target is recomputed from the operator's defaults, the operator's locks and
// those overrides, so a changed default or a newly introduced lock takes
// effect at once (design §4). A locked field never carries an owner value: a
// lock that is introduced after an override existed wins, the owner value is
// not effective, and the effective value is the locked operator value
// (Requirement 1.2).
//
// The projection is pure: it reads no store and mutates nothing, and it does
// not change the caller's stored row. The sanitized overrides are the state
// a caller may carry forward: every locked field is omitted, so that state
// stays minimal. The durable handling of a stale override — and its
// reconciliation with the stored, unprojected overrides — is not implemented
// yet and belongs to the settings service, not to this domain.
//
// A stored value the phase's inventory no longer permits is refused by the
// same authority that refuses it at dispatch time, so the caller sees the
// refusal instead of a silently substituted value; how the settings surface
// presents such a stored value belongs to the settings service.
func (c Config) OwnerSettings(phase Phase, stored TargetOverrides) (PhaseSettings, error) {
	policy, ok := c.Models.For(phase)
	if !ok {
		return PhaseSettings{}, &PolicyError{Phase: phase, Field: FieldRoute, Reason: "the phase is not part of the model policy"}
	}
	inherited, err := c.inheritedTarget(phase)
	if err != nil {
		return PhaseSettings{}, err
	}
	operator, err := ResolveEffectiveTarget(phase, policy, inherited, policy.Locks, TargetOverrides{})
	if err != nil {
		return PhaseSettings{}, err
	}
	overrides := sanitizeOverrides(policy.Locks, stored)
	declared, err := ResolveDeclaredTarget(phase, policy, inherited, policy.Locks, overrides)
	if err != nil {
		return PhaseSettings{}, err
	}
	effective, err := ResolveEffectiveTarget(phase, policy, inherited, policy.Locks, overrides)
	if err != nil {
		return PhaseSettings{}, err
	}
	return PhaseSettings{Phase: phase, Defaults: operator, Declared: declared, Effective: effective, Locks: policy.Locks, Overrides: overrides}, nil
}

// ApplyOwnerOverrides validates a proposed owner override set against one
// phase's policy and returns what the owner may keep.
//
// A write that changes a locked field is refused explicitly, naming the
// phase and the field and never the value: the lock holds on the write path,
// so no request path can change a locked field (Requirement 1.2). The refusal
// is atomic: a write that names a permitted field and a locked one is refused
// whole. A write that repeats the locked value changes nothing and is
// idempotent.
//
// The returned overrides are the sanitized set: every locked field is
// omitted, so the state a caller carries forward stays minimal (design §4).
// The proposed set is a replacement, not a patch: a field the caller does not
// name resolves to the operator default, and the set is not merged with a
// previous row. The function is pure: it stores nothing and changes no
// stored row.
func (c Config) ApplyOwnerOverrides(phase Phase, proposed TargetOverrides) (PhaseSettings, error) {
	policy, ok := c.Models.For(phase)
	if !ok {
		return PhaseSettings{}, &PolicyError{Phase: phase, Field: FieldRoute, Reason: "the phase is not part of the model policy"}
	}
	inherited, err := c.inheritedTarget(phase)
	if err != nil {
		return PhaseSettings{}, err
	}
	operator, err := ResolveEffectiveTarget(phase, policy, inherited, policy.Locks, TargetOverrides{})
	if err != nil {
		return PhaseSettings{}, err
	}
	if err := checkLockedWrite(phase, policy.Locks, operator, proposed); err != nil {
		return PhaseSettings{}, err
	}
	overrides := sanitizeOverrides(policy.Locks, proposed)
	declared, err := ResolveDeclaredTarget(phase, policy, inherited, policy.Locks, overrides)
	if err != nil {
		return PhaseSettings{}, err
	}
	effective, err := ResolveEffectiveTarget(phase, policy, inherited, policy.Locks, overrides)
	if err != nil {
		return PhaseSettings{}, err
	}
	return PhaseSettings{Phase: phase, Defaults: operator, Declared: declared, Effective: effective, Locks: policy.Locks, Overrides: overrides}, nil
}

// checkLockedWrite refuses a write that changes a locked field.
//
// The operator's value is the value the lock resolves to, so a write that
// repeats it changes nothing and is not a change.
func checkLockedWrite(phase Phase, locks TargetLocks, operator PhaseTarget, proposed TargetOverrides) error {
	if locks.Backend && proposed.Backend != "" && proposed.Backend != operator.Backend {
		return lockedFieldError(phase, FieldBackend)
	}
	if locks.Model && proposed.Model != "" && proposed.Model != operator.Model {
		return lockedFieldError(phase, FieldModel)
	}
	if locks.ReasoningEffort && proposed.ReasoningEffort != "" && proposed.ReasoningEffort != operator.ReasoningEffort {
		return lockedFieldError(phase, FieldReasoningEffort)
	}
	return nil
}

// lockedFieldError refuses a write to a locked field. The refusal names the
// phase and the field and never the value.
func lockedFieldError(phase Phase, field string) error {
	return &PolicyError{Phase: phase, Field: field, Reason: "the field is locked by the operator"}
}

// sanitizeOverrides drops the owner value of every locked field: a locked
// field carries no owner value, so the owner value of a lock that was
// introduced after the override existed is not effective and is not carried
// in the returned state.
func sanitizeOverrides(locks TargetLocks, override TargetOverrides) TargetOverrides {
	return TargetOverrides{
		Backend:         unlockedValue(locks.Backend, override.Backend),
		Model:           unlockedValue(locks.Model, override.Model),
		ReasoningEffort: unlockedValue(locks.ReasoningEffort, override.ReasoningEffort),
	}
}

// unlockedValue returns the owner value of a field the operator did not lock.
func unlockedValue(locked bool, value string) string {
	if locked {
		return ""
	}
	return value
}

// DeclaredTarget resolves the phase's declared target under the operator's
// policy with no owner override: the operator's default, the phase's own
// fallback where the operator named no default effort, and the reasoning
// effort the effective target allows.
//
// The operator's lock set applies: a locked field resolves to the operator's
// value, so the request path cannot carry an owner value for it (design §3;
// Requirement 1.2).
//
// A backend or a model that nobody named stays empty, because the operator's
// default route supplies it when the route selector is derived.
func (c Config) DeclaredTarget(phase Phase) (PhaseTarget, error) {
	policy, ok := c.Models.For(phase)
	if !ok {
		return PhaseTarget{}, &PolicyError{Phase: phase, Field: FieldRoute, Reason: "the phase is not part of the model policy"}
	}
	inherited, err := c.inheritedTarget(phase)
	if err != nil {
		return PhaseTarget{}, err
	}
	return ResolveDeclaredTarget(phase, policy, inherited, policy.Locks, TargetOverrides{})
}

// EffectiveTarget resolves the phase's effective target under the operator's
// policy with no owner override: the declared target completed with the
// operator's default route's target.
func (c Config) EffectiveTarget(phase Phase) (PhaseTarget, error) {
	policy, ok := c.Models.For(phase)
	if !ok {
		return PhaseTarget{}, &PolicyError{Phase: phase, Field: FieldRoute, Reason: "the phase is not part of the model policy"}
	}
	inherited, err := c.inheritedTarget(phase)
	if err != nil {
		return PhaseTarget{}, err
	}
	return ResolveEffectiveTarget(phase, policy, inherited, policy.Locks, TargetOverrides{})
}

// inheritedTarget returns the target the operator's default route names: the
// first target of a multi-target route, and the model-only target of a route
// that names no backend.
func (c Config) inheritedTarget(phase Phase) (PhaseTarget, error) {
	route := strings.TrimSpace(c.phaseRoute(phase))
	if route == "" {
		return PhaseTarget{}, nil
	}
	shape, err := parseRouteShape(route)
	if err != nil {
		return PhaseTarget{}, &PolicyError{Phase: phase, Field: FieldRoute, Reason: "the operator default route is malformed"}
	}
	return PhaseTarget{Backend: shape.backend, Model: shape.model}, nil
}

// phaseRoute returns the operator's default route of the phase.
func (c Config) phaseRoute(phase Phase) string {
	switch phase {
	case PhaseExtraction:
		return c.ExtractRoute
	case PhaseConsolidation:
		return c.ConsolidateRoute
	}
	return ""
}

// phaseCallTarget returns the route selector and the reasoning effort the
// phase runs with: the selector derived from the operator's default route and
// the phase's declared target, and the phase's effective effort (design §3).
func phaseCallTarget(cfg Config, phase Phase) (route string, effort string, err error) {
	declared, err := cfg.DeclaredTarget(phase)
	if err != nil {
		return "", "", fmt.Errorf("%s: %s target: %w", ID, phase, err)
	}
	route, err = DeriveRouteSelector(phase, cfg.phaseRoute(phase), declared.Backend, declared.Model)
	if err != nil {
		return "", "", fmt.Errorf("%s: %s route: %w", ID, phase, err)
	}
	if route == "" {
		return "", "", fmt.Errorf("%s: %s route is required", ID, phase)
	}
	return route, declared.ReasoningEffort, nil
}

// DeriveRouteSelector builds the phase's route selector from the operator's
// default route and the declared backend and model.
//
// The selector is derived, never chosen: no client-facing field supplies it,
// so a user cannot name a target the operator did not name. The operator's
// default route is kept verbatim while the declared target names neither a
// backend nor a model: the route is then the operator's own, with its own
// failover policy. Otherwise the effective backend and model replace the
// leaf's, and the operator's global block, leaf annotation blocks and leaf
// query are preserved: only the target is replaced. A route that names more
// than one target cannot carry a declared target, so it is refused rather
// than silently reduced.
//
// The derived selector is the *requested* target. The final compiled target,
// the alias resolution and the route parameters the runtime merges per
// attempt are checked against the actual target in task 2.2; this function
// does not establish that the served target is the derived one.
func DeriveRouteSelector(phase Phase, defaultRoute, backend, model string) (string, error) {
	route := strings.TrimSpace(defaultRoute)
	declaredBackend := strings.TrimSpace(backend)
	declaredModel := strings.TrimSpace(model)
	if declaredBackend == "" && declaredModel == "" {
		// The operator named no target: the route is the operator's own.
		return route, nil
	}
	shape, err := parseRouteShape(route)
	if err != nil {
		return "", &PolicyError{Phase: phase, Field: FieldRoute, Reason: "the operator default route is malformed"}
	}
	if shape.multiTarget {
		return "", &PolicyError{Phase: phase, Field: FieldRoute, Reason: "the operator default route must name a single target to derive a per-phase target"}
	}
	effectiveBackend, effectiveModel := declaredBackend, declaredModel
	if effectiveBackend == "" {
		effectiveBackend = shape.backend
	}
	if effectiveModel == "" {
		effectiveModel = shape.model
	}
	var selector string
	if effectiveBackend == "" {
		if strings.Contains(effectiveModel, ":") {
			return "", &PolicyError{Phase: phase, Field: FieldModel, Reason: "a model name with a ':' needs an explicit backend"}
		}
		selector = effectiveModel
	} else {
		selector = effectiveBackend + ":" + effectiveModel
	}
	selector = shape.global + shape.annotations + selector
	if shape.query != "" {
		selector += "?" + shape.query
	}
	return selector, nil
}

// routeShape is the operator's default route reduced to the parts the
// derivation preserves and the part it replaces.
//
// The canonical grammar reads a leading global block, then the leaf
// annotation blocks, then the leaf target and the leaf query. The
// separators that name a second target are read at the top level of the
// whole selector, so a separator behind a leaf query still names one.
type routeShape struct {
	global      string
	annotations string
	query       string
	backend     string
	model       string
	multiTarget bool
}

// parseRouteShape reads the operator's default route with the canonical
// grammar's semantics.
//
// It is a narrow seam: it reads the parts the derivation preserves and the
// target it replaces, and it never compiles a selector. The feature package
// may not import the core routing implementation, so the reading is kept here
// and mirrors the grammar's own rules.
func parseRouteShape(route string) (routeShape, error) {
	rest := strings.TrimSpace(route)
	if rest == "" {
		return routeShape{}, errMalformedRoute
	}
	var shape routeShape
	if strings.HasPrefix(rest, "{") {
		end := strings.Index(rest, "}")
		if end < 0 {
			return routeShape{}, errMalformedRoute
		}
		shape.global = rest[:end+1]
		rest = strings.TrimSpace(rest[end+1:])
	}
	// The failover separator is read at the top level of the whole selector,
	// before any leaf query, so a separator behind a query still names a
	// second target.
	alternatives := splitOutsideBrackets(rest, '|')
	shape.multiTarget = len(alternatives) > 1
	leaf := strings.TrimSpace(alternatives[0])
	if hasTopLevelOperator(leaf, '^') || hasTopLevelOperator(leaf, '!') {
		shape.multiTarget = true
	}
	shape.annotations, leaf = splitSelectorAnnotations(leaf)
	path, query, hasQuery := strings.Cut(leaf, "?")
	if hasQuery {
		shape.query = query
	}
	shape.backend, shape.model = splitSelectorTarget(path)
	if shape.model == "" {
		return routeShape{}, errMalformedRoute
	}
	return shape, nil
}

// errMalformedRoute reports a route the derivation cannot read.
var errMalformedRoute = errors.New(ID + ": malformed route selector")

// splitOutsideBrackets splits on sep at bracket depth zero, the way the
// canonical grammar reads the failover separator.
func splitOutsideBrackets(s string, sep byte) []string {
	var out []string
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '[':
			depth++
		case ']':
			depth--
		case sep:
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// hasTopLevelOperator reports whether s carries the operator outside a
// bracket block, the way the canonical grammar reads the weighted and the
// parallel separators.
func hasTopLevelOperator(s string, op byte) bool {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '[':
			depth++
		case ']':
			depth--
		case op:
			if depth == 0 {
				return true
			}
		}
	}
	return false
}

// splitSelectorTarget splits one selector target into the backend and the
// model it names. The canonical grammar reads a target without a colon as a
// model-only target, whose backend the runtime resolves to the default
// backend; the split keeps that reading, so a derived selector addresses the
// same target the operator wrote.
func splitSelectorTarget(target string) (backend, model string) {
	before, after, hasColon := strings.Cut(target, ":")
	if !hasColon {
		return "", strings.TrimSpace(before)
	}
	return strings.TrimSpace(before), strings.TrimSpace(after)
}

// splitSelectorAnnotations splits the leading annotation blocks of a selector
// path from the target they annotate. The canonical grammar reads a leading
// "[...]" block the same way, so a derived selector keeps the operator's
// annotation.
func splitSelectorAnnotations(path string) (annotations, target string) {
	target = strings.TrimSpace(path)
	for strings.HasPrefix(target, "[") {
		end := strings.Index(target, "]")
		if end < 0 {
			break
		}
		annotations += target[:end+1]
		target = strings.TrimSpace(target[end+1:])
	}
	return annotations, target
}

// policyFieldPath names one field of the per-phase model policy in the
// configuration namespace, so a rejection names the field the operator wrote.
func policyFieldPath(phase Phase, parts ...string) string {
	return strings.Join(append([]string{"models", string(phase)}, parts...), ".")
}

// validate checks the operator's policy for one phase and returns the first
// offending field.
//
// The declared defaults and the inventory entries must be bounded tokens, the
// inventory must list no entry twice, and a phase that names a backend or a
// model default needs a default route that names a single target: a derived
// selector replaces one target, so a failover route has no single default to
// keep. The composed default is derived here as well, so a default that
// cannot address a target is refused at configuration time instead of at the
// first request.
func (p PhasePolicy) validate(phase Phase, routeField, defaultRoute string) error {
	defaults := policyFieldPath(phase, "defaults")
	if p.Defaults.Backend != "" && !validBackend(p.Defaults.Backend) {
		return configFieldError(defaults+"."+FieldBackend, "must be a backend identifier")
	}
	if p.Defaults.Model != "" && !validModel(p.Defaults.Model) {
		return configFieldError(defaults+"."+FieldModel, "must be a model name")
	}
	if p.Defaults.ReasoningEffort != "" && !validEffort(p.Defaults.ReasoningEffort) {
		return configFieldError(defaults+"."+FieldReasoningEffort, "must be a reasoning effort token")
	}
	allowed := policyFieldPath(phase, "allowed")
	if err := validateInventory(allowed+".backends", p.Inventory.Backends, validBackend); err != nil {
		return err
	}
	if err := validateInventory(allowed+".models", p.Inventory.Models, validModel); err != nil {
		return err
	}
	if err := p.Inventory.validateTargets(allowed + ".targets"); err != nil {
		return err
	}
	if p.Defaults.Backend == "" && p.Defaults.Model == "" {
		// The operator named no target: the route is the operator's own and
		// stands as it is.
		return nil
	}
	shape, err := parseRouteShape(defaultRoute)
	if err != nil {
		return configFieldError(routeField, "must be a valid route selector")
	}
	if shape.multiTarget {
		return configFieldError(routeField, "must name a single target when the phase declares a backend or a model default")
	}
	if _, err := DeriveRouteSelector(phase, defaultRoute, p.Defaults.Backend, p.Defaults.Model); err != nil {
		var policyErr *PolicyError
		if !errors.As(err, &policyErr) {
			return configFieldError(routeField, "must be a valid route selector")
		}
		if policyErr.Field == FieldModel {
			return configFieldError(defaults+"."+FieldModel, policyErr.Reason)
		}
		return configFieldError(routeField, policyErr.Reason)
	}
	return nil
}

// validateTargets checks the per-target effort policy: every entry names a
// target, its efforts are bounded tokens, and no target is declared twice.
func (i PhaseInventory) validateTargets(field string) error {
	seen := make(map[string]struct{}, len(i.Targets))
	for _, target := range i.Targets {
		if !validBackend(target.Backend) {
			return configFieldError(field, "a target must name a backend")
		}
		if !validModel(target.Model) {
			return configFieldError(field, "a target must name a model")
		}
		if err := validateInventory(field, target.ReasoningEfforts, validEffort); err != nil {
			return err
		}
		key := target.Backend + ":" + target.Model
		if _, duplicate := seen[key]; duplicate {
			return configFieldError(field, "a target must not be declared twice")
		}
		seen[key] = struct{}{}
	}
	return nil
}

// validateInventory checks one inventory list: every entry is a bounded token
// of the field's own kind and no entry repeats.
func validateInventory(field string, values []string, valid func(string) bool) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !valid(value) {
			return configFieldError(field, "entries must be bounded tokens")
		}
		if _, duplicate := seen[value]; duplicate {
			return configFieldError(field, "entries must not repeat")
		}
		seen[value] = struct{}{}
	}
	return nil
}

// validBackend reports whether s is a usable backend identifier.
func validBackend(s string) bool {
	return validTargetToken(s, maxBackendBytes, "")
}

// validModel reports whether s is a usable model name. A model name may carry
// a provider namespace separator, a provider tag separator and a build
// suffix; the derived selector keeps the model behind an explicit backend,
// where the canonical grammar reads the first colon as the backend separator.
func validModel(s string) bool {
	return validTargetToken(s, maxModelBytes, "/@+:")
}

// validEffort reports whether s is a usable reasoning effort token. The token
// is checked lexically only: the provider's own effort values live in no
// capability surface today, so the feature neither invents a closed list of
// them nor claims that a token is provider-verified (design §2).
func validEffort(s string) bool {
	return validTargetToken(s, maxEffortBytes, "")
}

// validTargetToken reports whether s is a non-empty token of at most max
// bytes whose bytes are identifier bytes or one of the field's own extra
// bytes. A token carries no whitespace and no separator of the route
// grammar, so a value can never smuggle a second target into a derived
// selector.
func validTargetToken(s string, max int, extra string) bool {
	if s == "" || len(s) > max {
		return false
	}
	for i := 0; i < len(s); i++ {
		b := s[i]
		if !isIdentifierByte(b) && !strings.ContainsRune(extra, rune(b)) {
			return false
		}
	}
	return true
}

// isIdentifierByte reports whether b may appear in a target token.
func isIdentifierByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '-', b == '_', b == '.':
		return true
	}
	return false
}
