package schemainfer

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
)

// This file reads the conservative subset of a declared JSON Schema that the
// inference step is willing to reason about, and refuses everything else with a
// bounded signal. It is a projection, not a schema implementation: a node records
// only its declared kind, its declared child properties, its declared array item
// schema, and its declared description, plus two failure signals that stop the
// walk (an unreadable structure and a member declared twice).
//
// Reading is deliberately lossy in the safe direction. Keywords the step cannot
// interpret as a single unambiguous shape — every reference, composition,
// conditional, and tuple keyword — mark the node ambiguous instead of being
// skipped, so one unverifiable branch removes that tool from inference rather than
// leaving a partially trusted surface behind. The same applies to arbitrary-key
// channels: `additionalProperties` and `patternProperties` are declared in ways
// this step does not traverse at all, because a member name chosen by an arbitrary
// key or a pattern is not a declared property (design.md 226).
//
// The projection is also a bounded window rather than the whole declaration. The
// walk decides on a declared node only inside that window, so nothing below it is
// read at all; that is what keeps the reader's cost a constant multiple of the
// declared size instead of scaling with nesting depth.

// Structural failures the step can report. They are distinct from a syntax
// failure so a caller can tell "this is not a schema object" from "these bytes are
// not readable", which is the difference between a tool that declares a different
// kind and a tool whose declaration cannot be read at all.
var (
	// errNotObject marks a JSON value that is not a schema object where the step
	// required one.
	errNotObject = errors.New("schema node is not a JSON object")
	// errTrailingBytes marks input holding more than one JSON value.
	errTrailingBytes = errors.New("schema input holds more than one JSON value")
)

// The read window, in levels of declared schema-object nesting below the argument
// root, which is level zero. An `items` keyword nests one level, exactly as a
// `properties` keyword does, because an item schema is itself a schema object.
//
// The window is the walk's own reachable window, read from infer.go's descend:
//   - a node at or below maxSchemaDepth is inspected, so its declared kind,
//     description, properties, item schema, and structural signals are all read;
//   - a node at maxSchemaDepthWindow is the level descend refuses before it
//     inspects anything, having first checked the declared member name against
//     the vocabulary and the payload denylist. Only those two facts are read, and
//     its own children are not;
//   - nothing below that is ever inspected, traversed, or published, so it is not
//     read. Reading it would be work whose result is discarded, and because every
//     level re-scans the bytes below it, doing so would make the read quadratic in
//     declared nesting depth.
//
// Every level the window does admit is read at most once per enclosing node, and
// the subtrees of two nodes at the same level are disjoint, so one read costs at
// most (maxSchemaDepthWindow+1) times the declared size. The declared size is
// itself capped by aipapi.MaxToolParametersBytes before a tool definition reaches
// this step, which is what makes the total bounded; see infer.go for the
// requirement 7.9 contract.
const (
	// maxSchemaDepth is the deepest declared nesting level whose own declared
	// structure is materialized. It is the accepted pointer depth, because a
	// location one level below it is the deepest any accepted pointer can name.
	maxSchemaDepth = pathvirtualization.MaxPointerDepth
	// maxSchemaDepthWindow is the last level the reader materializes anything of:
	// the level the walk decides on from a declared member name alone.
	maxSchemaDepthWindow = maxSchemaDepth + 1
)

// schema type names the step accepts as a proven kind. Every other spelling is
// either a non-candidate or, for a missing or non-string declaration, ambiguous.
const (
	schemaTypeObject = "object"
	schemaTypeArray  = "array"
	schemaTypeString = "string"
)

// ambiguousKeywords are the declared keywords whose presence means the node's
// shape is not a single kind this step can prove. References are not resolved,
// compositions are not merged, conditionals are not applied, and a type union is
// not narrowed, so a node declaring any of them yields no inference at all.
var ambiguousKeywords = map[string]struct{}{
	"$ref":             {},
	"$dynamicRef":      {},
	"$recursiveRef":    {},
	"allOf":            {},
	"anyOf":            {},
	"oneOf":            {},
	"not":              {},
	"if":               {},
	"then":             {},
	"else":             {},
	"dependencies":     {},
	"dependentSchemas": {},
}

// unevenArrayKeywords are the declared keywords that make an array's element
// positions non-uniform. A tuple or a `contains` schema can bind a different kind
// per position, so such an array is not a provable array of strings even when its
// `items` schema says string.
var unevenArrayKeywords = map[string]struct{}{
	"prefixItems":     {},
	"additionalItems": {},
	"contains":        {},
}

// declaredSchema is the conservative projection of one declared schema node.
type declaredSchema struct {
	// typeName is the declared `type` when it is a string. An empty typeName means
	// the node declared no kind, which proves nothing.
	typeName string
	// description is the declared `description` when it is a string. It is used
	// only to suppress inference, never to create it.
	description string
	// properties are the declared child properties, keyed by the declared member
	// name exactly as the schema spelled it. A nil map means the node declares no
	// child properties.
	properties map[string]declaredSchema
	// items is the declared array item schema. A nil pointer means the node
	// declares no item schema, or declared one this step cannot read.
	items *declaredSchema
	// ambiguous marks a node whose declared structure this step cannot reduce to a
	// single proven kind.
	ambiguous bool
	// uneven marks a declared array whose element kinds are not uniform.
	uneven bool
	// duplicate marks a member name declared twice in one object, or a `properties`
	// map holding one name twice.
	duplicate bool
}

// readSchemaNode reads one schema object that sits at nesting depth `depth`,
// where the argument root is depth zero.
//
// `depth` is what bounds the read, and it is threaded through the fold rather than
// checked afterwards, because the only two keywords that can pull the reader deeper
// are the two that nest a child schema. A node whose children would fall past the
// window records the keyword as declared but never reads it, which is what keeps one
// call proportional to the declared size.
//
// The node is a fresh value rather than a reset receiver, so no node can inherit an
// earlier declaration's fields, and it is returned rather than written through a
// pointer so a partially read node is never visible to a caller.
func readSchemaNode(data []byte, depth int) (declaredSchema, error) {
	node := declaredSchema{}
	members, duplicate, err := decodeObjectMembers(data)
	if err != nil {
		return node, err
	}
	node.duplicate = duplicate
	for _, member := range members {
		node.applyKeyword(member.name, member.value, depth)
	}
	return node, nil
}

// applyKeyword folds one declared member into the projection.
//
// Every handler folds into the node, so member declaration order is an input the
// projection must not depend on. That is why the accumulated signals — ambiguous
// and duplicate — are only ever OR-ed here, never assigned from a fresh value: a
// whole-struct assignment would let a later, individually unambiguous member
// erase a reference, composition, or repeated name an earlier one recorded. The
// plain fields (typeName, description, properties, items) are single-valued
// keywords, and a second declaration of any of them is already refused as a
// duplicate before it can be folded twice.
//
// `depth` is the declared nesting level of the node being folded. It gates the two
// nesting keywords only; every other keyword is answered from this node's own
// declared members, so it is read at every level inside the window.
func (s *declaredSchema) applyKeyword(name string, value json.RawMessage, depth int) {
	switch name {
	case "type":
		// `type` is the one handler that can itself report ambiguity (a union of
		// kinds, or a declaration that is not a string at all), so it adds to the
		// signal rather than replacing it. Folding the pair through locals keeps
		// the OR explicit; a direct `s.typeName, s.ambiguous = ...` would let a
		// schema that spells `$ref` before `type` — the order schema generators
		// emit — infer through a branch this step cannot resolve.
		declared, declaredAmbiguous := decodeTypeName(value)
		s.typeName = declared
		s.ambiguous = s.ambiguous || declaredAmbiguous
	case "description":
		s.description = decodeString(value)
	case "properties":
		if depth < maxSchemaDepthWindow {
			s.properties, s.ambiguous, s.duplicate = decodeProperties(value, depth+1, s.ambiguous, s.duplicate)
		}
		// Past the window the declared children are recorded as present but not
		// read. The walk cannot reach them, so reading them would cost a rescan of
		// the whole remaining declaration for a result nothing consumes.
	case "items":
		if depth < maxSchemaDepthWindow {
			s.items = decodeItems(value, depth+1, &s.ambiguous)
		}
	default:
		if _, ambiguous := ambiguousKeywords[name]; ambiguous {
			s.ambiguous = true
		}
		if _, uneven := unevenArrayKeywords[name]; uneven {
			// `uneven` is additive for the same reason as `ambiguous`, and no handler
			// clears it, so a tuple keyword is recorded wherever it is declared.
			s.uneven = true
		}
	}
}

// structuralFailure returns the bounded reason a node's own declared structure
// already proves, or OutcomeNone when the node is structurally clean.
//
// Both signals are order-independent by construction: they are facts about the
// set of declared members, not about one member's value. Reporting them before
// any single-valued field is interpreted is what keeps a first-declaration-wins
// value such as `type` from deciding the reason code.
func (s *declaredSchema) structuralFailure() Outcome {
	switch {
	case s.duplicate:
		return OutcomeSchemaDuplicate
	case s.ambiguous:
		return OutcomeSchemaAmbiguous
	default:
		return OutcomeNone
	}
}

// isContainer reports whether the node declares an object whose declared child
// properties may be traversed. A nested container must declare `type: "object"`:
// a node that lists properties without declaring its kind has not proven that the
// locations behind those names exist in the argument payload at all.
func (s *declaredSchema) isContainer() bool {
	return s.typeName == schemaTypeObject && s.properties != nil
}

// isStringLeaf reports whether the node declares exactly one string.
func (s *declaredSchema) isStringLeaf() bool {
	return s.typeName == schemaTypeString
}

// isStringArrayLeaf reports whether the node declares a homogeneous array of
// strings. Every position must be provably a string, so a tuple schema, a
// `contains` schema, a missing item schema, or an item schema this step cannot
// reduce to a plain string all refuse the whole array rather than partially
// matching it.
func (s *declaredSchema) isStringArrayLeaf() bool {
	return s.typeName == schemaTypeArray && !s.uneven && s.items != nil && s.items.typeName == schemaTypeString
}

// objectMember is one declared member name with its raw value, kept in the order
// the schema declared it.
type objectMember struct {
	name  string
	value json.RawMessage
}

// decodeObjectMembers reads one JSON object into its members.
//
// It is total over arbitrary bytes: a value that is not an object reports
// errNotObject, unreadable bytes report the decoder's own error, bytes trailing the
// closing brace report errTrailingBytes, and a member name that appears twice sets
// the duplicate flag. Members are read through a streaming decoder rather than a
// map so a repeated name stays visible: unmarshalling into a map would silently
// keep one of the two declarations and hide the ambiguity.
//
// A streaming decoder reports "no more elements" for a truncated object as readily
// as for a closed one, so the closing brace and the end of the input are both
// checked explicitly. Without that a half-written object would decode into a
// partial member list, and a schema could hide structure past its own end.
func decodeObjectMembers(data []byte) (members []objectMember, duplicate bool, err error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	head, err := decoder.Token()
	if err != nil {
		return nil, false, err
	}
	if head != json.Delim('{') {
		return nil, false, errNotObject
	}
	seen := make(map[string]struct{}, 8)
	for decoder.More() {
		nameToken, tokenErr := decoder.Token()
		if tokenErr != nil {
			return nil, false, tokenErr
		}
		name, ok := nameToken.(string)
		if !ok {
			// Unreachable while the decoder is positioned on an object member, where
			// every key is a string. It is kept because the alternative is a panic on a
			// decoder invariant this step does not own.
			return nil, false, errNotObject
		}
		var value json.RawMessage
		if decodeErr := decoder.Decode(&value); decodeErr != nil {
			return nil, false, decodeErr
		}
		if _, repeated := seen[name]; repeated {
			// The first declaration is kept so the walk stays deterministic, but the
			// duplicate flag stops the tool before any pointer is published.
			duplicate = true
			continue
		}
		seen[name] = struct{}{}
		members = append(members, objectMember{name: name, value: value})
	}
	closing, closeErr := decoder.Token()
	if closeErr != nil {
		// A truncated object ends here, not at a closing brace.
		return nil, false, closeErr
	}
	if closing != json.Delim('}') {
		// Unreachable while the decoder is positioned on the end of an object, which
		// is what made the loop above stop. Kept so a decoder change surfaces as a
		// bounded reason rather than a misread member list.
		return nil, false, errNotObject
	}
	if _, trailingErr := decoder.Token(); !errors.Is(trailingErr, io.EOF) {
		// Exactly one value was expected, so a second document is a failure rather
		// than a schema this step may read part of.
		return nil, false, errTrailingBytes
	}
	return members, duplicate, nil
}

// decodeTypeName reads a declared `type`. A non-string declaration, including a
// union of kinds, is ambiguous: the step never picks one member of a union.
func decodeTypeName(value json.RawMessage) (string, bool) {
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		// The value arrived from a streaming decode, so it is already one valid JSON
		// value. A failure here is a decoder invariant, and it is treated as ambiguous
		// rather than as a readable kind.
		return "", true
	}
	name, ok := decoded.(string)
	if !ok {
		return "", true
	}
	return name, false
}

// decodeString reads a declared string value, returning the empty string for a
// value that is not one. An unreadable description only ever removes a suppression
// signal, so it can never create an inference.
func decodeString(value json.RawMessage) string {
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		// Unreachable for the same reason as in decodeTypeName.
		return ""
	}
	text, _ := decoded.(string)
	return text
}

// decodeProperties reads a declared `properties` map into its child projections.
//
// A `properties` value that is not an object is a structural failure, so it marks
// the node ambiguous: the node's declared children cannot be enumerated, so
// nothing below it can be trusted. A child that is not a schema object is stored as
// an unreadable projection instead, which is the narrower and equally
// fail-closed reading — that child's kind is simply unproven, while the rest of the
// declared surface stays provable. A `properties` map holding one name twice is
// recorded as a duplicate so the tool is refused instead of resolved by picking one
// of the two declarations.
func decodeProperties(value json.RawMessage, childDepth int, alreadyAmbiguous, alreadyDuplicate bool) (map[string]declaredSchema, bool, bool) {
	members, duplicate, err := decodeObjectMembers(value)
	if err != nil {
		return nil, true, alreadyDuplicate
	}
	properties := make(map[string]declaredSchema, len(members))
	for _, member := range members {
		child, childErr := readSchemaNode(member.value, childDepth)
		if childErr != nil {
			if !errors.Is(childErr, errNotObject) {
				// Unreachable for already-validated bytes; see decodeTypeName. It is
				// kept so a decoder change removes the whole tool from inference instead
				// of silently dropping a declared property.
				return nil, true, alreadyDuplicate
			}
			properties[member.name] = declaredSchema{}
			continue
		}
		properties[member.name] = child
	}
	return properties, alreadyAmbiguous || duplicate, alreadyDuplicate || duplicate
}

// decodeItems reads a declared array item schema, which nests one level exactly as
// a declared property does.
//
// `depth` is the item schema's own nesting level, and it is threaded through for
// the same reason it is on `properties`: the walk inspects an item schema but never
// traverses into one, so the item schema's own children are inside the window's
// tail and are not read.
//
// An item schema that is not a schema object leaves the element kind unproven,
// which makes the array a non-candidate without invalidating the rest of the tool.
func decodeItems(value json.RawMessage, depth int, ambiguous *bool) *declaredSchema {
	items, err := readSchemaNode(value, depth)
	if err != nil {
		if !errors.Is(err, errNotObject) {
			// Unreachable for already-validated bytes; see decodeTypeName.
			*ambiguous = true
		}
		return nil
	}
	return &items
}

// readRootSchema reads the tool's declared argument schema.
//
// It distinguishes the canonical presence semantics of the field rather than
// collapsing them: an unset or whitespace-only message names no schema at all,
// while any present value must be one complete JSON object. Bytes trailing the
// object are a syntax failure rather than a partial read, so a schema cannot
// smuggle a second document past the check.
//
// The read starts at depth zero and is bounded from there by the window documented
// above, so the cost of one call is a constant multiple of the declared size rather
// than a function of how deeply a provider nested its schema. The root's own
// structural signals are settled before its declared kind is interpreted. A repeated
// member name and an unverifiable branch are properties of the whole member set and
// hold in every declaration order, while the retained `type` is whichever
// declaration came first; reading the kind first would let a schema that declares,
// say, `"type":"array"` before `"$ref"` be reported as merely describing the wrong
// payload instead of as the unverifiable structure it also is. Both are bounded
// refusals with zero selectors, so this ordering changes which label is reported,
// never whether the tool is skipped.
//
// This package deliberately does not use internal/core/jsonpresence: a feature
// plugin must not depend on internal/core, and the raw-message length already is
// the presence question here.
func readRootSchema(parameters json.RawMessage) (declaredSchema, Outcome) {
	if len(bytes.TrimSpace(parameters)) == 0 {
		return declaredSchema{}, OutcomeSchemaAbsent
	}
	root, err := readSchemaNode(parameters, 0)
	switch {
	case err == nil:
	case errors.Is(err, errNotObject):
		return declaredSchema{}, OutcomeSchemaNotObject
	default:
		// Truncated, unreadable, or multi-document bytes. The step never publishes a
		// selector set derived from a schema it could not read whole.
		return declaredSchema{}, OutcomeSchemaMalformed
	}
	if failure := root.structuralFailure(); failure != OutcomeNone {
		return declaredSchema{}, failure
	}
	if root.typeName != "" && root.typeName != schemaTypeObject {
		// The canonical argument payload is a JSON object, so a schema declaring any
		// other kind is not describing that payload and nothing below it can be
		// trusted to address it.
		return declaredSchema{}, OutcomeSchemaNotObject
	}
	return root, OutcomeNone
}
