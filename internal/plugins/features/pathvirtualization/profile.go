package pathvirtualization

// This file implements the profile half of design.md 197-230: the declared
// tool-profile shape, the closed set of bounded opaque-result modes, the
// compile-time validation of a profile set that carries a mode, and the shipped
// conservative built-in layer.
//
// Two properties decide everything here. First, a profile is exact-name
// authority: it claims one or more whole tool names, compared byte for byte, so
// no prefix, suffix, substring, case-folded, trimmed, or confusable spelling can
// ever reach its selectors (requirement 3.6). Second, a profile can only ever
// NARROW what the feature touches: it names the locations that are proven to be
// filesystem locators, and opaque-result rewriting stays off unless the profile
// explicitly declares a bounded mode (requirements 2.5, 2.6, 3.8).
//
// The built-in layer is shipped data rather than mutable package state. It is
// returned from a function as a fresh copy, carries no init() registration, and
// is versioned with the feature so a change to its contents is always a visible
// decision.

// BuiltinProfileVersion is the version of the built-in profile layer shipped with
// this feature build.
//
// It is a fixed implementation contract, not an operator-configurable value
// (requirement 7.4): the operator layer may add, replace, or withdraw profiles,
// but it can neither move the reserved alias namespace nor change which built-in
// profiles this build ships. The version changes whenever the shipped table does,
// which is what keeps a diagnostics inventory able to explain which exact-name
// surfaces a build resolved without exposing any tool name.
const BuiltinProfileVersion = 1

// OpaqueResultMode is the bounded policy for rewriting an opaque tool result: the
// text payload of a tool result, as opposed to a structured JSON result addressed
// by explicit selectors.
//
// It is a closed set of exactly three values. The disabled value is the zero
// value, so a profile that declares no mode rewrites no opaque result at all, and
// the only way to reach the enabled modes is to name one explicitly. Each enabled
// mode names WHICH bounded recognizer the result handler applies; the recognizer
// itself lives in the canonical rewriter's subpackage, because it is the one place
// that can see an opaque payload, and it fails closed on anything its rule cannot
// prove to be a location.
//
// The two enabled modes are policies, not a ladder, and the line-mode recognizer is
// the strictly narrower of the two: it accepts a line holding exactly one location,
// where the token-mode recognizer accepts a line holding one or more.
//
// The value is an enum because it only ever reaches fixed-count observability
// dimensions: it never carries tool-name, pointer, or payload bytes.
type OpaqueResultMode uint8

const (
	// OpaqueResultModeNone is the disabled default and the zero value. An opaque
	// result is left byte-for-byte unchanged, which is the required behavior of a
	// tool result nothing proved to be path-oriented (requirements 2.5, 3.8).
	OpaqueResultModeNone OpaqueResultMode = iota
	// OpaqueResultModePathTokens marks an opaque result whose path-bearing content
	// is a bounded set of delimited path tokens: a list of locations, one or more
	// per line.
	//
	// The unit this mode's recognizer decides on is the whole line, not the token. A
	// token is a maximal run of bytes that are neither whitespace nor a comma, so an
	// equals sign, a colon, a quote, and a bracket are token bytes rather than
	// boundaries; the recognizer re-spells a line's tokens only when EVERY one of
	// them is a location this mapping accepts, and then replaces exactly those
	// tokens. A single token that is not a location refuses its whole line, so a
	// path beside any other content on its own line is left byte-for-byte unchanged:
	// `level=debug path=<root>/a.go` is one token that is not a location and is
	// refused whole, and so is `paths: <root>/a <root>/b` (requirement 2.6).
	OpaqueResultModePathTokens
	// OpaqueResultModePathLines marks an opaque result whose path-bearing content is
	// whole lines that hold nothing but a location. The recognizer may rewrite such
	// a line and must leave every line that carries any other content unchanged
	// (requirement 2.6). It is the token mode's own whole-line rule narrowed to
	// exactly one location per line, so a line carrying two locations is a list and
	// is left for the token mode.
	OpaqueResultModePathLines
)

// String returns the fixed, low-cardinality label of a mode. It is the same
// spelling the configuration surface accepts, and it is safe for content-free
// observability dimensions: it never contains tool-name, pointer, or payload
// bytes.
func (m OpaqueResultMode) String() string {
	switch m {
	case OpaqueResultModeNone:
		return "none"
	case OpaqueResultModePathTokens:
		return "path_tokens"
	case OpaqueResultModePathLines:
		return "path_lines"
	default:
		return "unknown"
	}
}

// Valid reports whether the value is a member of the closed mode set.
//
// It exists because an integer type can hold a value this build does not define,
// and such a value must be refused rather than treated as the disabled default:
// defaulting an unrecognized mode to "off" would let a typo hide a mode an
// operator meant to enable, while treating it as "on" would let a typo widen
// rewriting.
func (m OpaqueResultMode) Valid() bool {
	switch m {
	case OpaqueResultModeNone, OpaqueResultModePathTokens, OpaqueResultModePathLines:
		return true
	default:
		return false
	}
}

// ParseOpaqueResultMode resolves a configuration spelling to a mode.
//
// Only the exact spelling of a member is accepted. There is no trimming, case
// folding, or separator normalization, because this is a closed enumeration rather
// than a name, and a configuration value that reaches this function is operator
// input: a spelling this build does not define is refused instead of being read as
// the disabled default, which is what keeps requirement 3.7's "validated at
// generation compilation" honest for the mode.
func ParseOpaqueResultMode(spelling string) (OpaqueResultMode, bool) {
	switch spelling {
	case "none":
		return OpaqueResultModeNone, true
	case "path_tokens":
		return OpaqueResultModePathTokens, true
	case "path_lines":
		return OpaqueResultModePathLines, true
	default:
		return OpaqueResultModeNone, false
	}
}

// ToolProfile is one declared tool profile: the exact tool names it claims, the
// JSON Pointers it proves are path-bearing on each side of a call, and the
// bounded opaque-result mode it declares.
//
// It is the profile shape design.md 202-207 fixes, in that order and with no
// other field. The pointers are still operator or built-in text at this point:
// ParseSelector remains the single authority on which spellings exist, and only
// CompileToolProfiles turns them into validated selectors.
//
// A profile carries no reserved alias namespace, no version, and no workspace-tag
// setting. Those are fixed implementation values owned by the mapper, so no profile
// can widen them (requirement 7.4).
type ToolProfile struct {
	// Names are the exact tool names this profile claims. Comparison is byte-exact:
	// a name is never trimmed, case-folded, normalized, or matched as a prefix,
	// suffix, or substring (requirements 3.6, 3.7).
	Names []string
	// ArgPointers are JSON Pointers into a completed tool call's argument JSON.
	ArgPointers []string
	// ResultJSONPointers are JSON Pointers into structured tool-result JSON. An
	// empty list is the common case and is not an error: it declares that this tool
	// has no structured result surface the feature may rewrite.
	ResultJSONPointers []string
	// OpaqueResultMode is the bounded policy for this tool's opaque result text. The
	// zero value disables opaque rewriting.
	OpaqueResultMode OpaqueResultMode
}

// BuiltinToolProfiles returns the built-in profile layer shipped with this feature
// build.
//
// The layer is conservative by construction, which is what design.md 249-250 asks
// of V1 built-ins:
//
//   - only exact tool names from the canonical coding-agent filesystem families are
//     claimed, and each claims the one member its tool contract uses for a
//     filesystem locator;
//   - no built-in names a payload concept, so no built-in can hand requirement 2.4's
//     content/patch/source/script refusal a field to work on;
//   - no built-in publishes a structured-result selector, because the canonical
//     structured-result representation is not stable across the surfaces a tool can
//     arrive from and requirement 3.2 keeps result selectors explicit;
//   - no built-in enables an opaque-result mode, because none of these tool
//     contracts is clearly path-list-only. A deployment that has proved one is
//     declares that in operator configuration, which is the only layer allowed to.
//
// The result is a fresh copy, so a caller may edit or extend its own configuration
// without being able to reach the built-in policy any other caller resolves against.
// Because the layer is fixed data, its exact contents are pinned by test together
// with BuiltinProfileVersion.
func BuiltinToolProfiles() []ToolProfile {
	return []ToolProfile{
		{
			Names: []string{
				"read_file",
				"write_file",
				"edit_file",
				"replace_in_file",
				"delete_file",
				"remove_file",
			},
			ArgPointers: []string{"/file_path"},
		},
		{
			Names:       []string{"notebook_read", "notebook_edit"},
			ArgPointers: []string{"/notebook_path"},
		},
	}
}

// CompileToolProfiles validates one declared profile set and returns it as
// compiled, immutable profiles.
//
// It is the mode-carrying compile path, and it fails closed exactly like the
// mode-free one: an invalid pointer, an ambiguous name, an unusable opaque mode, or
// an over-limit count refuses the entire set and publishes nothing, so no partially
// valid profile can reach a request (requirement 3.7).
//
// Pointer validation is delegated rather than repeated: this function owns only the
// opaque mode, which the mode-free entry point has no field to carry, and every
// name and pointer rule remains the single implementation the 3.1 tests pin.
func CompileToolProfiles(profiles []ToolProfile) ([]CompiledProfile, SelectorReject) {
	if len(profiles) == 0 {
		return nil, SelectorRejectNone
	}
	// A slice of declared profiles is converted to the mode-free compile input first
	// so there is exactly one implementation of the name, count, and pointer rules,
	// and the mode is then validated and attached to each compiled profile.
	inputs := make([]ProfileInput, len(profiles))
	for i, profile := range profiles {
		if !profile.OpaqueResultMode.Valid() {
			return nil, SelectorRejectOpaqueMode
		}
		inputs[i] = ProfileInput{
			Names:              profile.Names,
			ArgPointers:        profile.ArgPointers,
			ResultJSONPointers: profile.ResultJSONPointers,
		}
	}
	compiled, reject := CompileProfiles(inputs)
	if reject != SelectorRejectNone {
		return nil, reject
	}
	for i := range compiled {
		compiled[i].OpaqueResultMode = profiles[i].OpaqueResultMode
	}
	return compiled, SelectorRejectNone
}
