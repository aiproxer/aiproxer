package feature

import (
	"fmt"
)

// SourceKind identifies the composition source contributing to an extension plane.
type SourceKind uint8

const (
	// SourceUnknown is the uninitialized or unknown source sentinel.
	SourceUnknown SourceKind = iota
	// SourceFeature represents contributions from feature plugins.
	SourceFeature
	// SourceHost represents contributions or overlays injected by the host runtime.
	SourceHost
	// SourceGenerationBinder represents generation-scoped binder contributions.
	SourceGenerationBinder
)

func (s SourceKind) String() string {
	switch s {
	case SourceFeature:
		return "feature"
	case SourceHost:
		return "host"
	case SourceGenerationBinder:
		return "generation_binder"
	default:
		return fmt.Sprintf("SourceKind(%d)", s)
	}
}

// Multiplicity defines whether a plane accepts multiple ordered contributions or a single exclusive slot.
type Multiplicity uint8

const (
	// MultUnknown is the uninitialized multiplicity sentinel.
	MultUnknown Multiplicity = iota
	// MultOrdered allows multiple contributions combined in registration order.
	MultOrdered
	// MultExclusive permits at most one contribution, failing fast on conflict.
	MultExclusive
)

func (m Multiplicity) String() string {
	switch m {
	case MultOrdered:
		return "ordered"
	case MultExclusive:
		return "exclusive"
	default:
		return fmt.Sprintf("Multiplicity(%d)", m)
	}
}

// Combination specifies how contributions are combined for a particular source.
type Combination uint8

const (
	// CombUnsupported is the zero value and the only unsupported-source sentinel.
	CombUnsupported Combination = iota
	// CombConcatenate appends incoming contributions to existing ones.
	CombConcatenate
	// CombExclusive allows exactly one contribution and rejects conflicts.
	CombExclusive
	// CombReduce deterministically folds incoming contributions in order.
	CombReduce
	// CombReplaceByIdentity replaces matching identity entries or appends new ones.
	CombReplaceByIdentity
)

func (c Combination) String() string {
	switch c {
	case CombUnsupported:
		return "unsupported"
	case CombConcatenate:
		return "concatenate"
	case CombExclusive:
		return "exclusive"
	case CombReduce:
		return "reduce"
	case CombReplaceByIdentity:
		return "replace_by_identity"
	default:
		return fmt.Sprintf("Combination(%d)", c)
	}
}

// SourceRules defines per-source combination rules for an extension plane.
type SourceRules struct {
	Feature          Combination
	Host             Combination
	GenerationBinder Combination
}

// RuleFor returns the combination rule declared for source.
func (r SourceRules) RuleFor(source SourceKind) Combination {
	switch source {
	case SourceFeature:
		return r.Feature
	case SourceHost:
		return r.Host
	case SourceGenerationBinder:
		return r.GenerationBinder
	default:
		return CombUnsupported
	}
}

// NilPolicy defines how typed-nil interface values are handled before validation and combination.
type NilPolicy uint8

const (
	// NilNotApplicable indicates nil policy is not applicable (e.g. non-pointer/non-interface types).
	NilNotApplicable NilPolicy = iota
	// NilReject causes typed nil contributions to be rejected with an error before candidate mutation.
	NilReject
	// NilSkip causes typed nil contributions to be silently ignored/skipped.
	NilSkip
)

func (n NilPolicy) String() string {
	switch n {
	case NilNotApplicable:
		return "not_applicable"
	case NilReject:
		return "reject"
	case NilSkip:
		return "skip"
	default:
		return fmt.Sprintf("NilPolicy(%d)", n)
	}
}

// RequestBodyAccess classifies how a plane's content relates to the request
// body for large-payload wire eligibility (Task 3.1 authority classification).
// The zero value Unclassified fails generation and census CI so a new or
// unannotated plane fails closed; Task 3.5+ consumes the classification in the
// generation-frozen eligibility summary.
type RequestBodyAccess uint8

const (
	// RequestBodyAccessUnclassified is the zero value: the plane has no wire
	// classification and fails generation/CI.
	RequestBodyAccessUnclassified RequestBodyAccess = iota
	// RequestBodyCanonicalRequired marks planes that receive or mutate the
	// full canonical request and block the wire lane when occupied.
	RequestBodyCanonicalRequired
	// RequestBodyMetadataOnly marks planes carrying only bounded
	// session/workspace/int metadata.
	RequestBodyMetadataOnly
	// RequestBodyResponseOnly marks planes operating on canonical output
	// events only.
	RequestBodyResponseOnly
	// RequestBodyWireContract marks planes carrying an explicit certified
	// wire contract.
	RequestBodyWireContract
)

func (a RequestBodyAccess) String() string {
	switch a {
	case RequestBodyAccessUnclassified:
		return "unclassified"
	case RequestBodyCanonicalRequired:
		return "canonical_required"
	case RequestBodyMetadataOnly:
		return "metadata_only"
	case RequestBodyResponseOnly:
		return "response_only"
	case RequestBodyWireContract:
		return "wire_contract"
	default:
		return fmt.Sprintf("RequestBodyAccess(%d)", uint8(a))
	}
}

// DiagnosticOccupant represents an occupant in the diagnostics inventory.
type DiagnosticOccupant struct {
	Label      string
	PluginID   string
	Privileges []string
}

// PrivilegeProjection represents privilege metadata projected from a plane contribution.
type PrivilegeProjection struct {
	Flags []string
}

// Canonical privilege flag constants.
const (
	PrivilegeRawCapture        = "raw_capture"
	PrivilegeAuxiliaryRequests = "auxiliary_requests"
	PrivilegeAuthProvider      = "auth_provider"
	PrivilegeCompletionGate    = "completion_gate"
)

func validatePrivilegeFlag(flag string) error {
	switch flag {
	case PrivilegeRawCapture, PrivilegeAuxiliaryRequests, PrivilegeAuthProvider, PrivilegeCompletionGate:
		return nil
	default:
		return fmt.Errorf("%w: unknown privilege flag %q", ErrInvalidPlane, flag)
	}
}

// DiagnosticPlaneProjection represents the type-erased diagnostic projection of a single plane.
type DiagnosticPlaneProjection struct {
	PlaneID       string
	StageID       string
	CoalesceGroup string
	Order         int
	Occupants     []DiagnosticOccupant
	Privileges    PrivilegeProjection
}

// DiagnosticDescriptor describes how a plane's frozen values are materialized for diagnostics.
type DiagnosticDescriptor[T any] struct {
	StageID       string
	CoalesceGroup string
	Order         int
	Materialize   func(T) []DiagnosticOccupant
	Privileges    func(T) PrivilegeProjection
}

type generatedPolicy[T any] struct {
	planeID                string
	rules                  SourceRules
	nilPolicy              NilPolicy
	isNil                  func(T) bool
	validate               func(T) error
	validateIdentity       func(string) error
	combine                func(SourceKind, T, T) (T, error)
	identity               func(T) (string, bool)
	exclusiveConflictError error
	requestMaterializer    func(T) T
	requestBorrow          bool
	hookTarget             HookTarget
	requestAccess          RequestBodyAccess
	diagStageID            string
	diagCoalesceGroup      string
	diagOrder              int
	diagMaterialize        func(T) []DiagnosticOccupant
	diagPrivileges         func(T) PrivilegeProjection
}

func (gp *generatedPolicy[T]) materializeOccupants(v T) []DiagnosticOccupant {
	if gp == nil || gp.diagMaterialize == nil {
		return nil
	}
	return gp.diagMaterialize(v)
}

func (gp *generatedPolicy[T]) projectPrivileges(v T) PrivilegeProjection {
	if gp == nil || gp.diagPrivileges == nil {
		return PrivilegeProjection{}
	}
	return gp.diagPrivileges(v)
}

type generatedAccess[T any] struct {
	contribute func(*generatedContributions, SourceKind, string, T) error
	get        func(*generatedFrozen) T
	identity   func(*generatedFrozen) (string, bool)
	policy     *generatedPolicy[T]
}

// HookTarget defines the target field name in generated hook configuration.
type HookTarget string

const (
	// HookTargetSubmitHooks targets generated HookConfig.SubmitHooks.
	HookTargetSubmitHooks HookTarget = "SubmitHooks"
	// HookTargetRequestPartHooks targets generated HookConfig.RequestPartHooks.
	HookTargetRequestPartHooks HookTarget = "RequestPartHooks"
	// HookTargetResponsePartHooks targets generated HookConfig.ResponsePartHooks.
	HookTargetResponsePartHooks HookTarget = "ResponsePartHooks"
	// HookTargetToolReactors targets generated HookConfig.ToolReactors.
	HookTargetToolReactors HookTarget = "ToolReactors"
)

// validateHookTarget returns an error if target is non-empty and not one of the canonical hook targets.
func validateHookTarget(target HookTarget) error {
	switch target {
	case "", HookTargetSubmitHooks, HookTargetRequestPartHooks, HookTargetResponsePartHooks, HookTargetToolReactors:
		return nil
	default:
		return fmt.Errorf("%w: unknown hook target %q", ErrInvalidPlane, target)
	}
}

// Plane declares an extension plane contract with multiplicity, per-source combination rules,
// validation, nil policy, and diagnostics metadata.
//
// In v1, Plane descriptors represent published standard planes in the closed canonical manifest.
// Production contribution policy (source combination rules, nil handling, validation, identity,
// combination) is authoritatively enforced by the canonical generated binding. Copying or mutating
// exported fields on a Plane descriptor does not redefine the plane; production contribution always
// executes the canonical generated policy. If an altered copy has a modified plane ID or is unbound,
// contribution is rejected with [ErrUngeneratedPlane]. Adding a new extension plane is an upstream
// platform change requiring a manifest declaration in plane_manifest.go and code regeneration.
type Plane[T any] struct {
	ID           string
	Multiplicity Multiplicity
	Rules        SourceRules
	// NilPolicy defines how nil contributions are handled before validation and combination.
	// NilReject fails before candidate mutation; NilSkip silently skips the contribution;
	// NilNotApplicable proceeds to validation and combination.
	NilPolicy NilPolicy
	// HookTarget defines the optional generated hook configuration target for this plane.
	HookTarget HookTarget
	// RequestAccess declares the request-body access class for large-payload
	// wire eligibility. The zero value Unclassified is rejected by plane
	// generation and census CI; declaration validation accepts it so
	// synthetic test planes stay focused on the rule under test.
	RequestAccess RequestBodyAccess
	// IsNil is an optional predicate used to detect nil values for NilPolicy checks.
	// For interface-valued planes (e.g. Plane[MyInterface]), Go's untyped nil check (anyVal == nil)
	// returns false for typed nil pointers boxed in an interface (e.g. (*ConcreteStub)(nil)).
	// Because reflection is banned on composition paths per architecture, detecting typed-nil
	// values in interface-valued planes requires supplying this IsNil predicate.
	// When IsNil is omitted, untyped nil checks (anyVal == nil) serve as the fast path.
	IsNil func(v T) bool
	// Validate validates incoming contribution values.
	Validate func(v T) error
	// ValidateIdentity validates a cached identity string (e.g. for exclusive planes).
	ValidateIdentity func(string) error
	// Combine combines an incoming contribution with current accumulated state for a source,
	// returning the retained value for the specified source rule. Combiners must not mutate
	// caller-owned or current stored state on failure (fail-before-mutate).
	Combine func(source SourceKind, current, incoming T) (T, error)
	// Identity extracts the stable identity string for an exclusive or replace-by-identity plane value.
	Identity func(v T) (string, bool)
	// ExclusiveConflictError is an optional legacy compatibility error joined with ErrExclusiveConflict on conflict.
	ExclusiveConflictError error
	// RequestMaterializer is an optional function that materializes/sorts the plane value
	// for request-scoped snapshot execution. When nil, the value is preserved as-is.
	RequestMaterializer func(v T) T
	// RequestBorrow indicates whether this plane exposes a narrow, immutable borrowed execution accessor
	// on RequestExecutionView for the runtime executor hot path.
	RequestBorrow bool
	// Diagnostics configures operator inventory and privilege projection metadata for this plane.
	Diagnostics DiagnosticDescriptor[T]
	generated   generatedAccess[T]
}

// MaterializeOccupants materializes diagnostic occupants for value v using the plane's descriptor.
// If Materialize is nil, it returns nil.
func (p Plane[T]) MaterializeOccupants(v T) []DiagnosticOccupant {
	if p.Diagnostics.Materialize == nil {
		return nil
	}
	return p.Diagnostics.Materialize(v)
}

// ProjectPrivileges projects privilege flags for value v using the plane's descriptor.
// If Privileges is nil, it returns an empty PrivilegeProjection.
func (p Plane[T]) ProjectPrivileges(v T) PrivilegeProjection {
	if p.Diagnostics.Privileges == nil {
		return PrivilegeProjection{}
	}
	return p.Diagnostics.Privileges(v)
}

// PlaneDeclaration allows non-generic validation of plane declarations across a manifest.
type PlaneDeclaration interface {
	PlaneID() string
	ValidateDeclaration() error
	DiagnosticOrder() int
	DiagnosticStageID() string
	DiagnosticCoalesceGroup() string
}

// declaredHookTarget returns the optional generated hook configuration target of the plane.
func (p Plane[T]) declaredHookTarget() HookTarget {
	return p.HookTarget
}

// declaredRequestAccess returns the request-body access class of the plane.
func (p Plane[T]) declaredRequestAccess() RequestBodyAccess {
	return p.RequestAccess
}

// PlaneID returns the stable ID of the plane.
func (p Plane[T]) PlaneID() string {
	return p.ID
}

// DiagnosticOrder returns the explicit diagnostic order of the plane.
func (p Plane[T]) DiagnosticOrder() int {
	return p.Diagnostics.Order
}

// DiagnosticStageID returns the diagnostics stage ID of the plane.
func (p Plane[T]) DiagnosticStageID() string {
	return p.Diagnostics.StageID
}

// DiagnosticCoalesceGroup returns the diagnostics coalesce group of the plane.
func (p Plane[T]) DiagnosticCoalesceGroup() string {
	return p.Diagnostics.CoalesceGroup
}

// ValidateDeclaration verifies that the plane declaration is well-formed, complete,
// and that multiplicity, nil policy, diagnostics metadata, and source rules are mutually consistent.
// For exclusive and replace-by-identity rules, p.Identity is required.
// When generated access closures are attached (e.g. via test-local bindings or generated adapters),
// p.generated.identity is also required. Unbound declarations rely on manifest-collection validation
// in code generation tooling (task 2.3).
func (p Plane[T]) ValidateDeclaration() error {
	if p.ID == "" {
		return fmt.Errorf("%w: plane ID must not be empty", ErrInvalidPlane)
	}
	if p.Multiplicity != MultOrdered && p.Multiplicity != MultExclusive {
		return fmt.Errorf("%w: plane %q: invalid multiplicity %v", ErrInvalidPlane, p.ID, p.Multiplicity)
	}
	if p.NilPolicy != NilNotApplicable && p.NilPolicy != NilReject && p.NilPolicy != NilSkip {
		return fmt.Errorf("%w: plane %q: invalid nil policy %v", ErrInvalidPlane, p.ID, p.NilPolicy)
	}

	sources := []SourceKind{SourceFeature, SourceHost, SourceGenerationBinder}
	hasSupportedSource := false
	hasExclusiveRule := false
	requiresIdentity := (p.Multiplicity == MultExclusive)

	for _, src := range sources {
		rule := p.Rules.RuleFor(src)
		switch rule {
		case CombUnsupported:
			continue
		case CombConcatenate, CombExclusive, CombReduce, CombReplaceByIdentity:
			// valid combination rule
		default:
			return fmt.Errorf("%w: plane %q: invalid combination rule %v on source %v", ErrInvalidPlane, p.ID, rule, src)
		}
		hasSupportedSource = true

		if rule == CombExclusive {
			hasExclusiveRule = true
		}

		if p.Multiplicity == MultExclusive {
			if rule == CombConcatenate {
				return fmt.Errorf("%w: plane %q: exclusive plane cannot use concatenate rule on source %v", ErrInvalidPlane, p.ID, src)
			}
			if rule == CombReduce {
				return fmt.Errorf("%w: plane %q: exclusive plane cannot use reduce rule on source %v", ErrInvalidPlane, p.ID, src)
			}
		}
		if p.Multiplicity == MultOrdered {
			if rule == CombExclusive {
				return fmt.Errorf("%w: plane %q: ordered plane cannot use exclusive rule on source %v", ErrInvalidPlane, p.ID, src)
			}
		}
		if rule == CombExclusive || rule == CombReplaceByIdentity {
			requiresIdentity = true
			if p.Identity == nil {
				return fmt.Errorf("%w: plane %q: identity extractor required for %v rule on source %v", ErrInvalidPlane, p.ID, rule, src)
			}
		}
	}

	if !hasSupportedSource {
		return fmt.Errorf("%w: plane %q: at least one source rule must be supported", ErrInvalidPlane, p.ID)
	}

	if p.ExclusiveConflictError != nil && !hasExclusiveRule {
		return fmt.Errorf(
			"%w: plane %q: ExclusiveConflictError requires at least one exclusive source rule",
			ErrInvalidPlane,
			p.ID,
		)
	}

	if requiresIdentity {
		if p.Identity == nil {
			return fmt.Errorf("%w: plane %q: identity extractor required for exclusive plane", ErrInvalidPlane, p.ID)
		}
		if p.ValidateIdentity == nil {
			return fmt.Errorf("%w: plane %q: cached identity validator required", ErrInvalidPlane, p.ID)
		}
		if (p.generated.contribute != nil || p.generated.get != nil) && p.generated.identity == nil {
			return fmt.Errorf("%w: plane %q: generated identity accessor required when generated access is bound", ErrInvalidPlane, p.ID)
		}
	}

	if p.Combine == nil {
		return fmt.Errorf("%w: plane %q: Combine function must not be nil", ErrInvalidPlane, p.ID)
	}

	if p.RequestBorrow {
		if p.RequestMaterializer == nil {
			return fmt.Errorf("%w: plane %q: RequestBorrow requires non-nil RequestMaterializer", ErrInvalidPlane, p.ID)
		}
	}

	if p.Diagnostics.StageID != "" {
		if p.Diagnostics.Materialize == nil {
			return fmt.Errorf("%w: plane %q: diagnostics StageID is set but Materialize function is nil", ErrInvalidPlane, p.ID)
		}
		if p.Diagnostics.Order <= 0 {
			return fmt.Errorf("%w: plane %q: diagnostics StageID is set but Order must be > 0 (got %d)", ErrInvalidPlane, p.ID, p.Diagnostics.Order)
		}
		if !ValidateStageID(p.Diagnostics.StageID) {
			return fmt.Errorf("%w: plane %q: invalid diagnostics stage ID %q", ErrInvalidPlane, p.ID, p.Diagnostics.StageID)
		}
	} else {
		if p.Diagnostics.Materialize != nil || p.Diagnostics.Privileges != nil || p.Diagnostics.CoalesceGroup != "" || p.Diagnostics.Order != 0 {
			return fmt.Errorf("%w: plane %q: diagnostics StageID must not be empty when diagnostics metadata is provided", ErrInvalidPlane, p.ID)
		}
	}

	if err := validateHookTarget(p.HookTarget); err != nil {
		return fmt.Errorf("%w: plane %q: unknown hook target %q", ErrInvalidPlane, p.ID, p.HookTarget)
	}

	if p.RequestAccess > RequestBodyWireContract {
		return fmt.Errorf("%w: plane %q: invalid request access class %v", ErrInvalidPlane, p.ID, p.RequestAccess)
	}

	return nil
}

type hookTargetProvider interface {
	declaredHookTarget() HookTarget
}

type requestAccessProvider interface {
	declaredRequestAccess() RequestBodyAccess
}

// ValidateManifest checks that a collection of plane declarations contains no duplicate IDs,
// consistent diagnostic orders and coalesce groups, no duplicate hook targets, and that every plane declaration is valid.
func ValidateManifest(declarations ...PlaneDeclaration) error {
	seen := make(map[string]struct{}, len(declarations))
	seenOrders := make(map[int]PlaneDeclaration)
	seenHookTargets := make(map[HookTarget]string)
	coalesceStages := make(map[string]string)

	for _, decl := range declarations {
		if decl == nil {
			return fmt.Errorf("%w: nil plane declaration in manifest", ErrInvalidPlane)
		}
		if err := decl.ValidateDeclaration(); err != nil {
			return err
		}
		id := decl.PlaneID()
		if _, exists := seen[id]; exists {
			return fmt.Errorf("%w: duplicate plane ID %q in manifest", ErrInvalidPlane, id)
		}
		seen[id] = struct{}{}

		if htp, ok := decl.(hookTargetProvider); ok {
			ht := htp.declaredHookTarget()
			if ht != "" {
				if prevID, exists := seenHookTargets[ht]; exists {
					return fmt.Errorf("%w: duplicate hook target %q between plane %q and %q", ErrInvalidPlane, ht, prevID, id)
				}
				seenHookTargets[ht] = id
			}
		}

		order := decl.DiagnosticOrder()
		if order > 0 {
			if prev, exists := seenOrders[order]; exists {
				// duplicate order values are allowed only for declarations sharing the same non-empty CoalesceGroup and StageID
				if decl.DiagnosticCoalesceGroup() == "" || prev.DiagnosticCoalesceGroup() == "" ||
					decl.DiagnosticCoalesceGroup() != prev.DiagnosticCoalesceGroup() ||
					decl.DiagnosticStageID() != prev.DiagnosticStageID() {
					return fmt.Errorf("%w: duplicate diagnostic order %d between plane %q and %q with different coalesce groups", ErrInvalidPlane, order, decl.PlaneID(), prev.PlaneID())
				}
			} else {
				seenOrders[order] = decl
			}
		}

		cg := decl.DiagnosticCoalesceGroup()
		if cg != "" {
			stage := decl.DiagnosticStageID()
			if prevStage, exists := coalesceStages[cg]; exists {
				if prevStage != stage {
					return fmt.Errorf("%w: mismatching stage IDs %q and %q for coalesce group %q", ErrInvalidPlane, prevStage, stage, cg)
				}
			} else {
				coalesceStages[cg] = stage
			}
		}
	}
	return nil
}
