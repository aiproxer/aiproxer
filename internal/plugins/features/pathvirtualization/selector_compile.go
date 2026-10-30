package pathvirtualization

// This file compiles operator selector configuration into validated, immutable
// values. It owns the fail-closed part of requirement 7.5: an invalid selector,
// an ambiguous tool profile, or an over-limit configuration is refused as a whole
// at configuration compile time, so no partially valid selector set is ever
// published and no per-request path can reach an unvalidated location.
//
// The compiled shapes here carry only what selector resolution needs. Profile
// precedence between operator and built-in profiles is a separate policy decision
// and lives in profile_resolve.go; this file validates the parts a profile shares
// with the built-in layer, and the mode-carrying declared shape lives in
// profile.go.

// ProfileInput is the raw, unvalidated form of one operator tool profile as it
// arrives from feature configuration. Pointers are still the operator's text at
// this point; only CompileProfiles turns them into validated selectors.
//
// This is the compile-time input shape, not the feature's configuration surface:
// it carries no mode, key, or tuning fields.
type ProfileInput struct {
	// Names are the exact tool names this profile claims. Selection is exact-name
	// authority, so a name is compared byte-exactly and never trimmed, folded, or
	// matched as a prefix or substring.
	Names []string
	// ArgPointers are JSON Pointers into a completed tool call's argument JSON.
	ArgPointers []string
	// ResultJSONPointers are JSON Pointers into structured tool-result JSON.
	ResultJSONPointers []string
}

// CompiledProfile is one validated profile: exact tool names plus immutable
// selector sets for arguments and structured results, and the bounded opaque-result
// mode it declared. It holds no mutable state and no reference to the configuration
// it came from.
type CompiledProfile struct {
	// Names are the exact tool names this profile claims, byte-exact.
	Names []string
	// ArgPointers are the validated argument selectors, in configured order.
	ArgPointers SelectorSet
	// ResultJSONPointers are the validated structured-result selectors, in
	// configured order.
	ResultJSONPointers SelectorSet
	// OpaqueResultMode is the bounded opaque-result policy this profile declared.
	// The zero value is the disabled default, so the mode-free compile input used by
	// operator profiles without a mode field resolves to a profile that rewrites no
	// opaque result.
	OpaqueResultMode OpaqueResultMode
}

// CompileProfiles validates one operator profile set and returns it as compiled,
// immutable profiles.
//
// It fails closed. Any invalid pointer, ambiguous name, duplicate pointer, or
// over-limit count refuses the entire set: the returned slice is empty and the
// bounded reason says which rule was broken, so no partially valid profile can be
// published. The same exact name may not appear in two profiles, because which
// selectors would win would then depend on ordering rather than configuration.
//
// Every compiled profile carries the disabled opaque mode. A caller that has a mode
// to declare compiles the mode-carrying declared shape instead, which delegates
// here and then attaches the validated mode.
func CompileProfiles(inputs []ProfileInput) ([]CompiledProfile, SelectorReject) {
	if len(inputs) > MaxProfiles {
		return nil, SelectorRejectProfileCount
	}
	claimed := make(map[string]struct{}, len(inputs))
	compiled := make([]CompiledProfile, 0, len(inputs))
	for _, input := range inputs {
		if reject := checkProfileNames(input.Names, claimed); reject != SelectorRejectNone {
			return nil, reject
		}
		// Both lists count against one budget, so a profile cannot be made
		// arbitrarily large by moving pointers between them. The count is checked
		// before any pointer is parsed, so an over-limit profile is refused without
		// being compiled first.
		if len(input.ArgPointers)+len(input.ResultJSONPointers) > MaxPointersPerProfile {
			return nil, SelectorRejectPointerCount
		}
		arguments, reject := compileSelectorList(input.ArgPointers)
		if reject != SelectorRejectNone {
			return nil, reject
		}
		results, reject := compileSelectorList(input.ResultJSONPointers)
		if reject != SelectorRejectNone {
			return nil, reject
		}
		compiled = append(compiled, CompiledProfile{
			Names:              append([]string(nil), input.Names...),
			ArgPointers:        arguments,
			ResultJSONPointers: results,
		})
	}
	return compiled, SelectorRejectNone
}

// checkProfileNames validates one profile's tool names and records them as
// claimed, refusing an empty, nameless, or duplicated exact name.
func checkProfileNames(names []string, claimed map[string]struct{}) SelectorReject {
	if len(names) == 0 {
		return SelectorRejectEmptyToolName
	}
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name == "" {
			return SelectorRejectEmptyToolName
		}
		if _, duplicate := seen[name]; duplicate {
			return SelectorRejectDuplicateToolName
		}
		if _, duplicate := claimed[name]; duplicate {
			// Two profiles claiming one exact tool name would make the selected
			// pointers depend on ordering, so the configuration is ambiguous.
			return SelectorRejectDuplicateToolName
		}
		seen[name] = struct{}{}
		claimed[name] = struct{}{}
	}
	return SelectorRejectNone
}

// compileSelectorList validates one profile's pointer list, refusing an invalid
// pointer and a pointer repeated inside the same list.
func compileSelectorList(pointers []string) (SelectorSet, SelectorReject) {
	if len(pointers) == 0 {
		return nil, SelectorRejectNone
	}
	set := make(SelectorSet, 0, len(pointers))
	seen := make(map[string]struct{}, len(pointers))
	for _, pointer := range pointers {
		selector, reject := ParseSelector(pointer)
		if reject != SelectorRejectNone {
			return nil, reject
		}
		if _, duplicate := seen[selector.text]; duplicate {
			// The canonical spelling is unique per pointer, so an equal spelling is
			// the same configured location declared twice.
			return nil, SelectorRejectDuplicatePointer
		}
		seen[selector.text] = struct{}{}
		set = append(set, selector)
	}
	return set, SelectorRejectNone
}

// CompilePathKeys validates the schema path-key vocabulary against MaxPathKeys
// and returns it as an immutable list.
//
// It is the fail-closed bound on the vocabulary the schema-assisted inference
// step compares declared property names against. Keys are kept byte-exact and
// unnormalized here: whether a declared name normalizes into a vocabulary key is
// inference policy, and this function only guarantees that the vocabulary is a
// finite, unambiguous set of non-empty names. An empty or repeated key is refused
// because it could make a name ambiguous, and an over-limit vocabulary is refused
// rather than truncated.
func CompilePathKeys(keys []string) ([]string, SelectorReject) {
	if len(keys) > MaxPathKeys {
		return nil, SelectorRejectPathKeyCount
	}
	if len(keys) == 0 {
		return nil, SelectorRejectNone
	}
	seen := make(map[string]struct{}, len(keys))
	compiled := make([]string, 0, len(keys))
	for _, key := range keys {
		if key == "" {
			return nil, SelectorRejectEmptyPathKey
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, SelectorRejectDuplicatePathKey
		}
		seen[key] = struct{}{}
		compiled = append(compiled, key)
	}
	return compiled, SelectorRejectNone
}
