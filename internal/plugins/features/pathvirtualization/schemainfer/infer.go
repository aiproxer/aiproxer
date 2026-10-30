package schemainfer

import (
	"maps"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// This file implements the schema-assisted inference step: it turns one tool's
// declared argument schema into the canonical JSON Pointer argument selectors that
// the existing selector resolver can already use, and nothing else.
//
// The step is the third and last entry of the resolution order in design.md
// 210-214, which belongs to the profile-precedence task. What lives here is only
// the conservative candidate search it feeds: the step never decides which
// selector wins, never reads an argument value, and never touches a filesystem.
//
// Every candidate it publishes satisfies all of the following, and each rule is a
// separate refusal rather than a preference:
//
//   - the declared property name, in its normalized spelling, is in the bounded
//     path-key vocabulary (requirement 3.3);
//   - the declared name is not, and the declared description does not describe, a
//     payload concept (requirement 3.4, design.md 221-222);
//   - the declared shape is a string or a homogeneous array of strings
//     (requirement 3.3);
//   - the whole declared structure was readable, unambiguous, and free of a
//     repeated member name;
//   - the pointer is within the depth, pointer-count, and node-count bounds, and
//     the spelling is one the accepted pointer dialect itself accepts.
//
// Anything else yields zero selectors and one bounded reason (requirement 3.5,
// requirement 7.9). There is no partial answer: a tool that trips any bound or
// unverifiable structure publishes nothing at all rather than the subset that
// happened to be provable.

// MaxSchemaNodes bounds how many declared schema objects one inference call may
// inspect.
//
// Depth alone does not bound the work, because a schema can declare an unbounded
// number of sibling properties at one level. This is the same kind of fail-closed
// implementation contract as the selector bounds it complements, and it is what
// keeps one tool's traversal cost independent of how large a provider chose to
// spell its tool schema. A schema past the bound is refused, not truncated.
//
// This bounds the walk only. The read that precedes it is bounded separately, by
// the nesting-depth window in schema.go: the projection materializes only the
// declared levels the walk can still reach, which is at most
// pathvirtualization.MaxPointerDepth+2 levels deep, so the read is a constant
// multiple of the declared size rather than a function of declared nesting depth.
// Both bounds together are this feature's half of requirement 7.9 — the walk may
// visit at most MaxSchemaNodes declared objects, and may only ever visit objects
// within that window — layered on the canonical aipapi.MaxToolParametersBytes
// (256 KiB, pkg/aipapi/limits.go) that refuses an over-large declaration before a
// tool definition reaches this step at all.
const MaxSchemaNodes = 1024

// Outcome is the bounded, content-free reason one inference call produced its
// result. It is an enum because the value only ever reaches fixed-count
// dimensions: it never carries a member name, a pointer, or payload bytes.
type Outcome uint8

const (
	// OutcomeNone marks a step that has not decided yet. It never reaches a caller.
	OutcomeNone Outcome = iota
	// OutcomeInferred marks a tool whose declared structure proves at least one
	// path-bearing location.
	OutcomeInferred
	// OutcomeNoPathKeys marks a readable, unambiguous tool that declares no
	// property the policy accepts. This is an ordinary, expected outcome for most
	// tools, not a failure.
	OutcomeNoPathKeys
	// OutcomeVocabularyEmpty marks inference turned off because no path-key
	// vocabulary is configured, so nothing could ever match.
	OutcomeVocabularyEmpty
	// OutcomeVocabularyRejected marks a vocabulary the shared selector bound
	// refused. Inference stays off; a nil *Inferrer reports this too, so a caller
	// that ignores a reject reason still cannot widen the policy.
	OutcomeVocabularyRejected
	// OutcomeSchemaAbsent marks a tool that declares no argument schema at all.
	OutcomeSchemaAbsent
	// OutcomeSchemaNotObject marks a declared schema that is present but does not
	// describe the canonical argument object, such as a JSON null or a non-object
	// value.
	OutcomeSchemaNotObject
	// OutcomeSchemaMalformed marks declared bytes that are not one complete JSON
	// value. A schema that cannot be read whole is never read partially.
	OutcomeSchemaMalformed
	// OutcomeSchemaAmbiguous marks declared structure whose shape cannot be reduced
	// to one proven kind, such as a reference, a composition, a conditional, or a
	// type union. The whole tool is skipped because one unverifiable branch means
	// the surface cannot be trusted as a whole.
	OutcomeSchemaAmbiguous
	// OutcomeSchemaDuplicate marks a member name declared twice in one object or one
	// `properties` map, so which declaration applies is undecidable.
	OutcomeSchemaDuplicate
	// OutcomeDepthExceeded marks a path key declared deeper than the accepted
	// pointer depth. The depth is refused, never shortened.
	OutcomeDepthExceeded
	// OutcomePointerBudget marks a tool that declares more path keys than one
	// profile may publish. The tool is refused, never truncated to the first N.
	OutcomePointerBudget
	// OutcomeNodeBudget marks a schema with more declared objects than one
	// inference call may inspect.
	OutcomeNodeBudget
	// OutcomePointerRejected marks a declared member name that no pointer in the
	// accepted dialect can address, such as RFC 6901's array-end token. The name is
	// never rewritten into a different location.
	OutcomePointerRejected
)

// String returns the fixed, low-cardinality label for an outcome. It is safe for
// content-free observability dimensions: it never contains member names, pointer
// text, or payload bytes.
func (o Outcome) String() string {
	switch o {
	case OutcomeNone:
		return "none"
	case OutcomeInferred:
		return "inferred"
	case OutcomeNoPathKeys:
		return "no_path_keys"
	case OutcomeVocabularyEmpty:
		return "vocabulary_empty"
	case OutcomeVocabularyRejected:
		return "vocabulary_rejected"
	case OutcomeSchemaAbsent:
		return "schema_absent"
	case OutcomeSchemaNotObject:
		return "schema_not_object"
	case OutcomeSchemaMalformed:
		return "schema_malformed"
	case OutcomeSchemaAmbiguous:
		return "schema_ambiguous"
	case OutcomeSchemaDuplicate:
		return "schema_duplicate"
	case OutcomeDepthExceeded:
		return "depth_exceeded"
	case OutcomePointerBudget:
		return "pointer_budget"
	case OutcomeNodeBudget:
		return "node_budget"
	case OutcomePointerRejected:
		return "pointer_rejected"
	default:
		return "unknown"
	}
}

// Result is the outcome of inferring argument selectors for one tool.
//
// Pointers is empty for every outcome except OutcomeInferred. The two fields are
// never partial: a caller can trust that a non-inferred result selected nothing at
// all, and that an inferred result is the complete proven set.
type Result struct {
	// Pointers are the inferred argument selectors, ordered by canonical spelling
	// so the result is identical for identical declared bytes.
	Pointers pathvirtualization.SelectorSet
	// Outcome is the bounded, content-free reason for the result.
	Outcome Outcome
}

// Inferrer is one compiled vocabulary for schema-assisted inference.
//
// It holds only an immutable set of normalized path keys, so it is safe to build
// once and share across requests and generations. The zero value and a nil pointer
// are both fail-closed: they infer nothing.
type Inferrer struct {
	// vocabulary is the compiled set of normalized path-key names.
	vocabulary map[string]struct{}
}

// New compiles a path-key vocabulary into an immutable inference policy.
//
// The vocabulary is validated by the same fail-closed validator the explicit
// selector configuration uses, so one bounded rule (MaxPathKeys) governs both, and
// an over-limit, empty, or repeated key list refuses the whole policy instead of
// being truncated. Keys are additionally rejected when two of them collide after
// normalization, because one declared name would then match two vocabulary entries
// and membership would stop being a single decision.
//
// A rejected vocabulary returns a nil *Inferrer, and calling a method on that nil
// pointer stays safe and stays off.
func New(pathKeys []string) (*Inferrer, pathvirtualization.SelectorReject) {
	compiled, reject := pathvirtualization.CompilePathKeys(pathKeys)
	if reject != pathvirtualization.SelectorRejectNone {
		return nil, reject
	}
	vocabulary := make(map[string]struct{}, len(compiled))
	for _, key := range compiled {
		normalized := NormalizeKey(key)
		if _, duplicate := vocabulary[normalized]; duplicate {
			return nil, pathvirtualization.SelectorRejectDuplicatePathKey
		}
		vocabulary[normalized] = struct{}{}
	}
	return &Inferrer{vocabulary: vocabulary}, pathvirtualization.SelectorRejectNone
}

// InferArguments returns the argument selectors one tool's declared schema proves
// path-bearing, or zero selectors and one bounded reason.
//
// It reads the canonical declared parameters only. The tool's own name and
// description are not consulted: tool-name matching is exact-name authority held by
// the profile layer, and prose is never a selector signal. The call mutates
// nothing, performs no I/O, and holds no state between calls, so identical declared
// bytes always produce an identical result.
func (r *Inferrer) InferArguments(tool aipapi.ToolDef) Result {
	if r == nil {
		// A nil policy is only reachable from a rejected vocabulary, so the reason is
		// exact rather than a guess.
		return Result{Outcome: OutcomeVocabularyRejected}
	}
	if len(r.vocabulary) == 0 {
		return Result{Outcome: OutcomeVocabularyEmpty}
	}
	root, outcome := readRootSchema(tool.Parameters)
	if outcome != OutcomeNone {
		return Result{Outcome: outcome}
	}
	search := &walker{
		vocabulary: r.vocabulary,
		// One token beyond the depth bound is allocated because the bound is checked
		// only after a name is pushed, which is what turns a too-deep declaration
		// into a refusal instead of a silently shorter path.
		path: make([]string, 0, pathvirtualization.MaxPointerDepth+1),
	}
	search.inspect(&root)
	for _, name := range sortedNames(root.properties) {
		if search.failure != OutcomeNone {
			break
		}
		search.descend(name, root.properties[name])
	}
	if search.failure != OutcomeNone {
		return Result{Outcome: search.failure}
	}
	if len(search.accepted) == 0 {
		return Result{Outcome: OutcomeNoPathKeys}
	}
	// Publication order is canonical rather than declaration order, so the same
	// declared structure yields the same selector set regardless of how a schema
	// happened to spell its members.
	slices.SortFunc(search.accepted, func(a, b pathvirtualization.Selector) int {
		return strings.Compare(a.String(), b.String())
	})
	return Result{Pointers: search.accepted, Outcome: OutcomeInferred}
}

// walker is the mutable state of one inference call. It is created per call and
// never shared, so an *Inferrer stays immutable and concurrency-safe.
type walker struct {
	// vocabulary is the compiled path-key set the parent compiled once.
	vocabulary map[string]struct{}
	// path is the reference-token chain of the property currently being read, used
	// as both the traversal position and the published pointer spelling.
	path []string
	// accepted holds the proven selectors, unsorted until the walk finishes.
	accepted pathvirtualization.SelectorSet
	// visited counts the declared schema objects inspected so far.
	visited int
	// failure is the first bounded reason that stopped the walk. It is set once:
	// the reason depends only on the declared bytes, not on which rule is checked
	// first, so the reported reason is deterministic.
	failure Outcome
}

// inspect reads one declared schema object for failure signals without traversing
// below it. A node's kind, its repeated members, and its composition or reference
// keywords are all decided here, before any pointer can be published.
//
// The budget is charged first so an over-large schema reports the bound it
// actually crossed rather than a structural signal found while looking for it.
func (w *walker) inspect(node *declaredSchema) {
	w.visited++
	if w.visited > MaxSchemaNodes {
		w.fail(OutcomeNodeBudget)
		return
	}
	if failure := node.structuralFailure(); failure != OutcomeNone {
		w.fail(failure)
		return
	}
}

// descend reads one declared property: it decides whether that property is itself a
// path-bearing location, then reads the properties declared below it.
//
// The declared member name is pushed onto the shared path buffer for exactly the
// duration of this property, so a sibling can never see a sibling's depth.
func (w *walker) descend(name string, child declaredSchema) {
	if isBlocked(name, child.description) {
		// A payload concept is closed before it is measured: it is neither inferred
		// nor traversed, so a `file_path` declared inside a `content` object never
		// becomes a selector.
		return
	}
	w.path = append(w.path, name)
	defer func() { w.path = w.path[:len(w.path)-1] }()

	if len(w.path) > pathvirtualization.MaxPointerDepth {
		// Nothing at or below this depth can be addressed by one accepted pointer, and
		// a candidate there would have to be published shortened. Membership is
		// decided from the name alone, so no deeper read is needed to refuse it; a
		// branch that declares no path key at all is simply not traversed.
		if w.isCandidate(name) {
			w.fail(OutcomeDepthExceeded)
		}
		return
	}
	w.inspect(&child)
	if w.failure != OutcomeNone {
		return
	}
	if child.items != nil {
		// An array's item schema is read to prove its element kind, but its own
		// declared properties are not traversed: array elements are addressed by
		// position, so a member name behind an array is not a reachable location.
		w.inspect(child.items)
		if w.failure != OutcomeNone {
			return
		}
	}
	if w.isCandidate(name) && (child.isStringLeaf() || child.isStringArrayLeaf()) {
		if !w.accept() {
			return
		}
	}
	if !child.isContainer() {
		return
	}
	for _, nested := range sortedNames(child.properties) {
		if w.failure != OutcomeNone {
			return
		}
		w.descend(nested, child.properties[nested])
	}
}

// isCandidate reports whether a declared member name is in the path-key vocabulary.
func (w *walker) isCandidate(name string) bool {
	_, listed := w.vocabulary[NormalizeKey(name)]
	return listed
}

// accept publishes the pointer for the property the walker currently sits on. It
// reports false once the walk has failed, so a caller can stop immediately.
func (w *walker) accept() bool {
	if len(w.accepted) >= pathvirtualization.MaxPointersPerProfile {
		w.fail(OutcomePointerBudget)
		return false
	}
	selector, reject := buildSelector(w.path)
	if reject != pathvirtualization.SelectorRejectNone {
		// The accepted pointer dialect is the only authority on pointer spelling, so a
		// name it refuses is refused here too instead of being re-spelled to fit.
		w.fail(OutcomePointerRejected)
		return false
	}
	w.accepted = append(w.accepted, selector)
	return true
}

// fail records the first bounded reason that stopped the walk.
func (w *walker) fail(reason Outcome) {
	if w.failure == OutcomeNone {
		w.failure = reason
	}
}

// isBlocked reports whether a declared property is a payload concept and must never
// be inferred or traversed.
//
// Both signals can only remove a candidate. The name check is byte-exact after
// normalization, and the description check is a bounded pass over declared prose
// that requirement 3.4 asks for ("named or described as"). Neither can create a
// candidate, so prose can never widen what is rewritten.
func isBlocked(name, description string) bool {
	return isPayloadConcept(name) || describesPayloadConcept(description)
}

// sortedNames returns a property map's member names in byte order, so a walk is
// deterministic and the reported reason does not depend on Go's map iteration.
func sortedNames(properties map[string]declaredSchema) []string {
	if len(properties) == 0 {
		return nil
	}
	return slices.Sorted(maps.Keys(properties))
}

// RFC 6901 pointer spelling. The dialect itself, and the only authority on which
// spellings are accepted, lives in the selector package; this encoder exists only to
// turn a chain of declared member names into that one spelling, and every result is
// handed back to the dialect for acceptance.
const (
	// pointerSeparator introduces each reference token of an anchored pointer.
	pointerSeparator = '/'
	// escapePrefix introduces the two RFC 6901 escapes.
	escapePrefix = '~'
	// escapeTilde and escapeSlash are the two escapes, in the order they must be
	// applied: an escaped tilde must not be re-escaped by the slash pass.
	escapeTilde = '0'
	escapeSlash = '1'
)

// buildSelector spells one pointer from its decoded reference tokens and asks the
// accepted dialect to accept it.
//
// Spelling is not a second dialect: the result is parsed by the same validator the
// explicit-selector configuration uses, so an empty token, an array-end token, or
// any other non-canonical form is refused with that validator's own bounded reason.
func buildSelector(tokens []string) (pathvirtualization.Selector, pathvirtualization.SelectorReject) {
	var text strings.Builder
	for _, token := range tokens {
		text.WriteByte(pointerSeparator)
		writeToken(&text, token)
	}
	return pathvirtualization.ParseSelector(text.String())
}

// writeToken writes one reference token with the two RFC 6901 escapes applied.
func writeToken(text *strings.Builder, token string) {
	for i := 0; i < len(token); i++ {
		switch token[i] {
		case escapePrefix:
			text.WriteByte(escapePrefix)
			text.WriteByte(escapeTilde)
		case pointerSeparator:
			text.WriteByte(escapePrefix)
			text.WriteByte(escapeSlash)
		default:
			text.WriteByte(token[i])
		}
	}
}
