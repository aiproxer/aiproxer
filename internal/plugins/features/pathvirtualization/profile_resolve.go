package pathvirtualization

// This file implements the selector resolution order of design.md 210-214: one
// exact operator profile by tool name, then one exact built-in profile, then the
// optional schema-assisted inference step, and otherwise no selector at all.
//
// The order is a safety order, not a preference. An exact profile is a decision
// somebody already made about which locations of one tool are filesystem
// locators, so it is authoritative and the step after it never runs. Schema
// inference is the fallback because it can only prove locations from a tool's own
// declared structure, and its own refusal rules already keep it conservative. The
// fourth answer is the required one for an unknown tool: no selector, not a guess
// (requirement 3.5).
//
// Two decisions make the order deterministic. A profile's pointer order is the
// order it declared, never the order a lookup table happens to iterate, so the same
// inputs always publish the same selectors. And a claimed tool name is answered
// from exactly one layer: the operator layer replaces the built-in layer for a name
// it claims rather than merging with it, so which layer supplied a selector can
// never depend on configuration order, pointer order, or map layout.

// ArgumentInference is the optional third entry of the resolution order: the
// step that answers which argument locations a tool's own declared schema proves to
// be filesystem locators.
//
// It is declared here, by the consumer of the step, and it takes only the declared
// argument-schema bytes. That signature is deliberate:
//
//   - keeping the canonical tool contract out of this signature is what lets the
//     lexical core keep no repository import at all, which its host-authority guard
//     proves;
//   - taking bytes rather than a tool definition means prose cannot reach the
//     inference step through this port. A tool's own name and description are never
//     a selector signal, because exact-name authority belongs to the profile layers
//     and description prose can only ever suppress a candidate anyway.
//
// A nil implementation, or a resolver built without one, turns the step off: it then
// publishes nothing, and a tool with no exact profile resolves to no selector.
type ArgumentInference interface {
	// InferArgumentSelectors returns the argument selectors the declared schema
	// proves, or nil when it proves none. It must be deterministic for identical
	// bytes and must never enable opaque-result rewriting.
	InferArgumentSelectors(declaredSchema []byte) SelectorSet
}

// ProfileSource names the single layer a resolution answered from.
//
// It is an enum because the value only ever reaches fixed-count observability
// dimensions: it never carries a tool name, a pointer, or payload bytes. Reporting
// the source is what makes the operator-over-built-in precedence observable, so a
// deployment can account for how many resolutions an operator profile answered
// without any of them being named (requirement 7.6).
type ProfileSource uint8

const (
	// ProfileSourceNone marks a resolution that selected nothing: no exact profile
	// claimed the tool name and the optional inference step did not run or proved
	// nothing.
	ProfileSourceNone ProfileSource = iota
	// ProfileSourceOperator marks a resolution answered by an exact operator
	// profile. It includes a profile that declares no selectors at all, which is
	// how an operator withdraws a built-in surface.
	ProfileSourceOperator
	// ProfileSourceBuiltin marks a resolution answered by an exact built-in profile
	// no operator profile claims.
	ProfileSourceBuiltin
	// ProfileSourceInference marks a resolution answered by the optional
	// schema-assisted inference step.
	ProfileSourceInference
)

// String returns the fixed, low-cardinality label of a source. It is safe for
// content-free observability dimensions: it never contains tool-name, pointer, or
// payload bytes.
func (s ProfileSource) String() string {
	switch s {
	case ProfileSourceNone:
		return "none"
	case ProfileSourceOperator:
		return "operator"
	case ProfileSourceBuiltin:
		return "builtin"
	case ProfileSourceInference:
		return "inference"
	default:
		return "unknown"
	}
}

// Resolved is the outcome of resolving one tool's path-bearing surfaces: which
// selectors apply to its arguments, which apply to its structured results, and
// which bounded opaque-result mode it declared.
//
// It is the whole decision a caller needs. An absent result selector set and an
// absent argument selector set are both ordinary answers, and the zero value is a
// complete one: nothing is selected and opaque rewriting is off.
type Resolved struct {
	// ArgPointers are the argument selectors to apply, in declared or inferred
	// order.
	ArgPointers SelectorSet
	// ResultJSONPointers are the structured-result selectors to apply. Inference
	// never contributes any: declared argument structure can prove an argument
	// location, and nothing else.
	ResultJSONPointers SelectorSet
	// OpaqueResultMode is the bounded opaque-result policy. It is disabled unless an
	// exact profile declared a mode.
	OpaqueResultMode OpaqueResultMode
	// Source is the single layer this resolution answered from.
	Source ProfileSource
}

// Resolver answers the resolution order for one generation's policy. It holds two
// exact-name lookup tables and an optional inference step, all immutable after
// construction, so one resolver may be shared by every request without
// synchronization.
type Resolver struct {
	// operator is the exact operator layer, keyed by claimed tool name.
	operator map[string]CompiledProfile
	// builtin is the exact built-in layer, keyed by claimed tool name.
	builtin map[string]CompiledProfile
	// inference is the optional schema-assisted step, or nil when it is off.
	inference ArgumentInference
}

// NewResolver binds two compiled profile layers and an optional inference step
// into one resolver.
//
// It fails closed. An over-limit layer, an empty or duplicated exact name, an
// over-limit pointer count, or an unusable opaque mode refuses the whole binding,
// so a request can never resolve against a partially valid policy. The two layers
// are validated independently because a name may legitimately be claimed by both:
// that is precisely the override case, and which layer wins must be a rule rather
// than an accident of declaration order.
func NewResolver(operator, builtin []CompiledProfile, inference ArgumentInference) (*Resolver, SelectorReject) {
	operatorIndex, reject := indexProfiles(operator)
	if reject != SelectorRejectNone {
		return nil, reject
	}
	builtinIndex, reject := indexProfiles(builtin)
	if reject != SelectorRejectNone {
		return nil, reject
	}
	return &Resolver{operator: operatorIndex, builtin: builtinIndex, inference: inference}, SelectorRejectNone
}

// indexProfiles builds one exact-name lookup table from a compiled layer.
//
// A caller may assemble compiled profiles directly instead of going through the
// compiler, so the rules compilation enforces are enforced here as well. Each name
// maps to its whole profile, which keeps resolution O(1) in the number of profiles
// and O(len(name)) in one exact comparison.
func indexProfiles(profiles []CompiledProfile) (map[string]CompiledProfile, SelectorReject) {
	if len(profiles) == 0 {
		return nil, SelectorRejectNone
	}
	if len(profiles) > MaxProfiles {
		return nil, SelectorRejectProfileCount
	}
	index := make(map[string]CompiledProfile, len(profiles))
	for _, profile := range profiles {
		if len(profile.Names) == 0 {
			return nil, SelectorRejectEmptyToolName
		}
		if !profile.OpaqueResultMode.Valid() {
			return nil, SelectorRejectOpaqueMode
		}
		if len(profile.ArgPointers)+len(profile.ResultJSONPointers) > MaxPointersPerProfile {
			return nil, SelectorRejectPointerCount
		}
		for _, name := range profile.Names {
			if name == "" {
				return nil, SelectorRejectEmptyToolName
			}
			if _, duplicate := index[name]; duplicate {
				return nil, SelectorRejectDuplicateToolName
			}
			index[name] = profile
		}
	}
	return index, SelectorRejectNone
}

// Resolve returns the selectors and opaque mode that apply to one tool.
//
// The declared schema bytes are read only when neither exact layer claims the name,
// so an explicit profile both wins and costs nothing to look up. The lookup is
// exact: a name is equal or it is a different tool, and no other name shape can
// reach a profile (requirements 3.6, 3.7).
//
// A name the operator layer claims is answered from that layer alone. The built-in
// profile for the same name is replaced, not merged, which is the determinism rule
// requirement 3.7 asks for: an operator who publishes a profile for a tool owns
// that tool's whole selector surface, so it can widen the built-in by declaring the
// superset and withdraw a built-in selector it finds wrong by declaring the profile
// without it. Requirement 3.8's refusal is preserved either way, because a profile
// that omits the mode leaves opaque rewriting disabled and a profile that omits the
// pointers selects nothing.
//
// The returned selector sets are private copies, so a caller cannot corrupt the
// policy through the result.
func (r *Resolver) Resolve(toolName string, declaredSchema []byte) Resolved {
	if r == nil {
		// An absent policy is the same answer as a tool no layer claims: no selector.
		return Resolved{Source: ProfileSourceNone}
	}
	if profile, claimed := r.operator[toolName]; claimed {
		return resolvedFrom(profile, ProfileSourceOperator)
	}
	if profile, claimed := r.builtin[toolName]; claimed {
		return resolvedFrom(profile, ProfileSourceBuiltin)
	}
	if r.inference == nil {
		return Resolved{Source: ProfileSourceNone}
	}
	// The inference step is the fallback only, and it can prove argument locations
	// and nothing else. Its own step stays off when the declared bytes are absent, so
	// no selector can be invented for a tool that declares no schema.
	inferred := r.inference.InferArgumentSelectors(declaredSchema)
	if len(inferred) == 0 {
		return Resolved{Source: ProfileSourceNone}
	}
	// Opaque rewriting stays off for an inferred tool: nothing has explicitly marked
	// its result as path-oriented (requirement 3.8).
	return Resolved{ArgPointers: copySelectorSet(inferred), Source: ProfileSourceInference}
}

// resolvedFrom publishes one compiled profile as a resolution, copying its selector
// sets so the resolver's own tables stay immutable.
func resolvedFrom(profile CompiledProfile, source ProfileSource) Resolved {
	return Resolved{
		ArgPointers:        copySelectorSet(profile.ArgPointers),
		ResultJSONPointers: copySelectorSet(profile.ResultJSONPointers),
		OpaqueResultMode:   profile.OpaqueResultMode,
		Source:             source,
	}
}

// copySelectorSet returns a private copy of a compiled selector set. A compiled set
// is never empty once it exists, so the empty case yields nil rather than an empty
// non-nil slice.
func copySelectorSet(set SelectorSet) SelectorSet {
	if len(set) == 0 {
		return nil
	}
	return append(SelectorSet(nil), set...)
}
