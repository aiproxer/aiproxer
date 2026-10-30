package largebody_test

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
)

// classificationCarrierFieldName is the single bounded classification field a
// large-body proof/wire-fact carrier exposes. The stock classifier must be able
// to run on the wire path without any canonical materialization, so the carrier
// is the provider-neutral SDK evidence value itself and nothing else
// (requirements 5.2, 5.3, 5.5, 12.7).
const classificationCarrierFieldName = "ClassificationEvidence"

// boundedClassificationShapes pins the carrier shape. Adding, removing or
// retyping a field is a deliberate review decision, not a compiler detail: the
// carrier must stay a flat, fixed-size metadata summary.
var boundedClassificationShapes = map[string]reflect.Type{
	"Operation":       reflect.TypeFor[aipapi.Operation](),
	"ClientUserAgent": reflect.TypeFor[string](),
	"ToolCategories":  reflect.TypeFor[sessionclassification.ToolCategorySet](),
}

// contentBearingNameFragments name request content or unbounded request
// material. No carrier field may hold data under one of these names.
var contentBearingNameFragments = []string{
	"prompt", "message", "transcript", "argument", "content", "path",
	"projectroot", "header", "body", "payload", "call", "history",
	"instruction", "text", "attachment", "tooldef", "toolname", "definition",
}

// forbiddenCarrierFields are whole top-level field names that would smuggle
// unbounded or content material into a proof/wire-fact carrier. The name list is
// a secondary net only: the shape rules below already reject the container kinds
// those fields would need, whatever they are called.
var forbiddenCarrierFields = []string{
	"Headers", "RawHeaders", "HeaderMap", "ToolDefs", "ToolDefinitions",
	"ToolNames", "Tools", "Messages", "MessageTree", "Prompt", "Call",
	"CanonicalCall", "Body", "Payload", "Content", "Text", "Instructions",
	"Attachments", "Transcript", "ToolArguments", "Arguments",
}

// carrierContainerExceptionFields are the pre-existing, non-classification
// fields that already own a bounded container, keyed by carrier type.
//
// The inventory is exhaustive and exactly one entry qualifies:
// largebody.Proof.RequiredCapabilities ([]aipapi.Capability). It predates this
// carrier, holds protocol capability names rather than request content, is
// bounded by capabilityfacts.ValidateCapabilities against the same
// semantic-fact budget, and is already charged to Proof.AggregateFactBytes.
// largebody.WireSessionFacts owns no container at all.
//
// The exception list is ratcheted in both directions: any container field not
// listed here is a violation, and any listed name that stops owning a container
// is a stale exception.
// TestSessionClassificationEvidence_CarrierGuardRejectsUnboundedAndReferenceShapedFields
// pins both halves, so the list can neither grow silently nor hide a field.
var carrierContainerExceptionFields = map[reflect.Type]map[string]bool{
	reflect.TypeFor[largebody.Proof]():            {"RequiredCapabilities": true},
	reflect.TypeFor[largebody.WireSessionFacts](): {},
}

// TestSessionClassificationEvidence_ProofAndWireFactsCarryOnlyTheBoundedSDKCarrier
// pins the wire representation used for classification: one bounded SDK
// evidence value, with no raw header bag, tool list, prompt text, transcript,
// local path, shadow canonical Call or unbounded container anywhere in the
// carrier (requirements 5.2, 5.5, 7.1, 7.2, 7.3, 12.7).
func TestSessionClassificationEvidence_ProofAndWireFactsCarryOnlyTheBoundedSDKCarrier(t *testing.T) {
	t.Parallel()

	for _, carrier := range []struct {
		name   string
		typeOf reflect.Type
	}{
		{name: "Proof", typeOf: reflect.TypeFor[largebody.Proof]()},
		{name: "WireSessionFacts", typeOf: reflect.TypeFor[largebody.WireSessionFacts]()},
	} {
		t.Run(carrier.name, func(t *testing.T) {
			t.Parallel()

			violations := classificationCarrierViolations(carrier.typeOf, carrierContainerExceptionFields[carrier.typeOf])
			require.Empty(t, violations,
				"%s must carry only bounded classification metadata; found: %s", carrier.name, strings.Join(violations, "; "))
		})
	}

	require.NoError(t, largebody.DefaultTestWireTurnFacts().AssertNoShadowCall())
}

// TestSessionClassificationEvidence_CarrierGuardRejectsUnboundedAndReferenceShapedFields
// is the load-bearing self-test for the guard above. Every fixture is a complete
// carrier that would be acceptable if it did not also smuggle exactly one shape,
// so each case proves a specific rule fires instead of proving that a broadly
// failing predicate rejects everything.
//
// The shapes below are the ones that slipped through the previous guard, which
// combined a dynamic-map check with a field-name list: any slice (including a
// nested slice-of-slice) and any pointer passed unnoticed, because neither kind
// is reflect.Map and neither name appeared in the list.
func TestSessionClassificationEvidence_CarrierGuardRejectsUnboundedAndReferenceShapedFields(t *testing.T) {
	t.Parallel()

	type promptFixture struct {
		Prompt string
	}
	type funcFixture func()
	type chanFixture chan int

	// Depth fixtures: each is a benign-looking struct whose *interior* smuggles
	// one shape. The outer struct looks bounded, which is exactly why the
	// top-level container rule cannot see the payload.
	type deepSliceFixture struct {
		Inner struct {
			Values []string
		}
	}
	type blobSliceFixture struct {
		Blobs []byte
	}
	type blobPointerFixture struct {
		Blob *string
	}
	type evidenceListFixture struct {
		Items []sessionclassification.Evidence
	}
	type uintptrHandleFixture struct {
		Handle uintptr
	}

	tests := []struct {
		name  string
		field reflect.StructField
	}{
		{name: "slice of raw tool names", field: exportedField("RawToolNames", reflect.TypeFor[[]string]())},
		{name: "nested slice of slices", field: exportedField("Segments", reflect.TypeFor[[][]byte]())},
		{name: "slice of pointers", field: exportedField("Deferred", reflect.TypeFor[[]*string]())},
		{name: "pointer to string", field: exportedField("Deferred", reflect.TypeFor[*string]())},
		{name: "double pointer", field: exportedField("Deferred", reflect.TypeFor[**string]())},
		{name: "header map", field: exportedField("Headers", reflect.TypeFor[map[string]string]())},
		{name: "raw header bag", field: exportedField("RawHeaders", reflect.TypeFor[map[string][]string]())},
		{name: "fixed size array", field: exportedField("ToolDefs", reflect.TypeFor[[8]string]())},
		{name: "shadow canonical call", field: exportedField("ShadowCall", reflect.TypeFor[aipapi.Call]())},
		{name: "shadow call behind a slice", field: exportedField("Pending", reflect.TypeFor[[]aipapi.Call]())},
		{name: "loose tool category bits", field: exportedField("LooseBits", reflect.TypeFor[sessionclassification.ToolCategorySet]())},
		{name: "second evidence carrier", field: exportedField("Loose", reflect.TypeFor[sessionclassification.Evidence]())},
		{name: "slice of evidence carriers", field: exportedField("History", reflect.TypeFor[[]sessionclassification.Evidence]())},
		{name: "nested prompt struct", field: exportedField("Nested", reflect.TypeFor[promptFixture]())},
		// Depth cases: a smuggling struct parked one level below a top-level
		// field. containerKindChain does not follow structs, so these are caught
		// by the sub-fact walk rather than by the top-level container rule.
		{name: "slice one struct level down", field: exportedField("Inner", reflect.TypeFor[deepSliceFixture]())},
		{name: "blob slice one level down", field: exportedField("Blobs", reflect.TypeFor[blobSliceFixture]())},
		{name: "pointer one level down", field: exportedField("Holder", reflect.TypeFor[blobPointerFixture]())},
		{name: "evidence list one level down", field: exportedField("Holder", reflect.TypeFor[evidenceListFixture]())},
		{name: "uintptr handle at top level", field: exportedField("Handle", reflect.TypeFor[uintptr]())},
		{name: "uintptr handle one level down", field: exportedField("Holder", reflect.TypeFor[uintptrHandleFixture]())},
		{name: "local path string", field: exportedField("WorkspacePath", reflect.TypeFor[string]())},
		{name: "prompt string", field: exportedField("Prompt", reflect.TypeFor[string]())},
		{name: "interface field", field: exportedField("Carrier", reflect.TypeFor[any]())},
		{name: "func field", field: exportedField("Compile", reflect.TypeFor[funcFixture]())},
		{name: "chan field", field: exportedField("Pending", reflect.TypeFor[chanFixture]())},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fixture := carrierFixtureWith(tc.field)
			violations := classificationCarrierViolations(fixture, nil)
			require.NotEmpty(t, violations,
				"the guard accepted a carrier smuggling %s %s (requirements 5.5, 7.2)",
				tc.field.Name, tc.field.Type)
			require.True(t, slices.ContainsFunc(violations, func(v string) bool {
				return strings.Contains(v, "."+tc.field.Name)
			}), "violations must name the offending field %s; got: %s", tc.field.Name, strings.Join(violations, "; "))
		})
	}

	t.Run("bounded scalar additions stay accepted", func(t *testing.T) {
		t.Parallel()

		fixture := carrierFixtureWith(
			exportedField("RetryBudget", reflect.TypeFor[int64]()),
			exportedField("ProfileHint", reflect.TypeFor[string]()),
			exportedField("Eligible", reflect.TypeFor[bool]()),
		)
		require.Empty(t, classificationCarrierViolations(fixture, nil),
			"a carrier of fixed-shape scalars must remain acceptable")
	})

	t.Run("container exception list is minimal", func(t *testing.T) {
		t.Parallel()

		// With no documented exception the real Proof must report exactly the one
		// pre-existing container field. Anything else appearing here would mean
		// the exception list was quietly widened to hide a smuggling field.
		proofType := reflect.TypeFor[largebody.Proof]()
		violations := classificationCarrierViolations(proofType, nil)
		require.Equal(t, []string{".RequiredCapabilities"}, topLevelCarrierFields(proofType, violations),
			"only the one pre-existing container field may be flagged; full findings: %s", strings.Join(violations, "; "))

		// WireSessionFacts owns no container at all, so with no documented
		// exception it must report nothing. Otherwise a container field added
		// there together with a matching exception entry would pass silently,
		// because the "must carry exactly one carrier" rule and every other
		// rule would still be satisfied.
		wireType := reflect.TypeFor[largebody.WireSessionFacts]()
		wireViolations := classificationCarrierViolations(wireType, nil)
		require.Empty(t, wireViolations,
			"WireSessionFacts must own no unexcepted container field; found: %s", strings.Join(wireViolations, "; "))
		require.Empty(t, carrierContainerExceptionFields[wireType],
			"WireSessionFacts must document no container exceptions")
	})

	t.Run("stale container exception is reported", func(t *testing.T) {
		t.Parallel()

		proofType := reflect.TypeFor[largebody.Proof]()
		violations := classificationCarrierViolations(proofType, map[string]bool{
			"RequiredCapabilities": true,
			"RemovedEarlier":       true,
		})
		require.Len(t, violations, 1, "a stale exception must be the only finding so it can be deleted")
		require.Contains(t, violations[0], "stale documented exception")
	})
}

// TestSessionClassificationEvidence_RetentionGuardRejectsSmugglingFixture proves the
// content-bearing name and dynamic-shape rules stay load bearing: a synthetic
// content-shaped struct that adds a header bag, tool list, prompt text,
// transcript tree, local path or shadow Call to the carrier must be reported.
func TestSessionClassificationEvidence_RetentionGuardRejectsSmugglingFixture(t *testing.T) {
	t.Parallel()

	type nestedFixture struct {
		Prompt string
	}
	type contentShapedFixture struct {
		Decision      bool
		Messages      []string
		Nested        nestedFixture
		ToolArguments map[string]string
		WorkspacePath string
		RawHeaders    map[string][]string
		ShadowCall    aipapi.Call
	}

	violations := classificationCarrierViolations(reflect.TypeFor[contentShapedFixture](), nil)
	joined := strings.Join(violations, ", ")
	for _, field := range []string{"Messages", "Nested", "Prompt", "ToolArguments", "WorkspacePath", "RawHeaders", "ShadowCall"} {
		require.Contains(t, joined, "."+field, "retention guard did not identify synthetic %s field; findings: %s", field, joined)
	}
}

// carrierFixtureWith builds a complete, otherwise-legitimate carrier type plus
// one extra field, so each hostile fixture isolates exactly one rule.
func carrierFixtureWith(extra ...reflect.StructField) reflect.Type {
	fields := []reflect.StructField{
		exportedField("Operation", reflect.TypeFor[aipapi.Operation]()),
		exportedField("ClientUserAgent", reflect.TypeFor[string]()),
		exportedField(classificationCarrierFieldName, reflect.TypeFor[sessionclassification.Evidence]()),
	}
	return reflect.StructOf(append(fields, extra...))
}

func exportedField(name string, typ reflect.Type) reflect.StructField {
	return reflect.StructField{Name: name, Type: typ}
}

// topLevelCarrierFields returns the distinct top-level carrier fields named by a
// violation list. Assertions use it to state "exactly this field is flagged"
// without depending on how many independent rules happen to fire for it.
func topLevelCarrierFields(typ reflect.Type, violations []string) []string {
	prefix := typ.Name() + "."
	seen := map[string]bool{}
	var fields []string
	for _, violation := range violations {
		reference, _, _ := strings.Cut(violation, " ")
		rest := strings.TrimPrefix(reference, prefix)
		if rest == reference {
			continue
		}
		name, _, _ := strings.Cut(rest, ".")
		if seen[name] {
			continue
		}
		seen[name] = true
		fields = append(fields, "."+name)
	}
	return fields
}

// classificationCarrierViolations inspects a candidate large-body carrier struct
// (the real Proof, the real WireSessionFacts, or a test fixture standing in for
// one) and returns every way it could carry request material to the
// classification path. A non-empty result means the wire representation is no
// longer bounded metadata.
//
// Rules, in order:
//
//  1. exactly one classification carrier, exposed under the reviewed name and
//     typed as the provider-neutral SDK evidence value;
//  2. no loose tool-category bits and no shadow aipapi.Call at top level;
//  3. no forbidden top-level field name;
//  4. no unbounded or reference-shaped top-level field. The field type is
//     followed through every container edge (pointer, slice, array, map); any
//     such edge, or any dynamic terminal kind, is a violation unless the field is
//     a documented pre-existing exception;
//  5. no content-bearing name holding data, no dynamic map/interface/func/chan/
//     unsafe-pointer and no shadow Call reachable through a nested struct;
//  6. the carrier itself is a flat fixed-shape summary with no container or
//     content-bearing field.
func classificationCarrierViolations(typ reflect.Type, exceptions map[string]bool) []string {
	var violations []string
	evidenceType := reflect.TypeFor[sessionclassification.Evidence]()
	carriers := 0
	documented := make(map[string]bool, len(exceptions))

	for field := range typ.Fields() {
		path := fmt.Sprintf("%s.%s", typ.Name(), field.Name)

		if field.Type == evidenceType {
			carriers++
			if field.Name != classificationCarrierFieldName {
				violations = append(violations, fmt.Sprintf(
					"%s carries classification evidence under unreviewed field name %q", path, field.Name))
			}
			continue
		}
		if field.Type == reflect.TypeFor[sessionclassification.ToolCategorySet]() {
			violations = append(violations, path+" exposes tool classification bits outside the bounded carrier")
		}
		if field.Type == reflect.TypeFor[aipapi.Call]() {
			violations = append(violations, path+" retains a shadow canonical Call")
		}
		if slices.Contains(forbiddenCarrierFields, field.Name) {
			violations = append(violations, path+" is a forbidden content/container field name")
		}
		if contentBearingField(field) {
			violations = append(violations, path+" has a content-bearing name holding data")
		}
		if chain := containerKindChain(field.Type); len(chain) > 0 {
			if exceptions[field.Name] {
				// A documented pre-existing container. Its shape is owned and
				// validated by the package that defines it
				// (capabilityfacts.ValidateCapabilities), so neither rule 4 nor
				// the sub-fact walk re-judges it here.
				documented[field.Name] = true
				continue
			}
			violations = append(violations, fmt.Sprintf(
				"%s has unbounded/reference shape %s; a bounded carrier owns no container outside the documented exceptions",
				path, kindChainString(chain)))
			continue
		}
		violations = append(violations, nestedCarrierViolations(field.Type, path, map[reflect.Type]bool{})...)
	}

	if carriers != 1 {
		violations = append(violations, fmt.Sprintf(
			"%s must carry exactly one %s carrier, found %d", typ.Name(), classificationCarrierFieldName, carriers))
	} else {
		violations = append(violations, classificationCarrierShapeViolations(evidenceType)...)
	}

	for name := range exceptions {
		if !documented[name] {
			violations = append(violations, fmt.Sprintf(
				"%s.%s is a stale documented exception: it no longer owns a container, so the exception must be deleted",
				typ.Name(), name))
		}
	}
	return violations
}

// containerKindChain follows every container edge of typ - pointer, slice, array
// and map - and returns the kinds traversed plus a dynamic terminal kind. Any
// non-empty result means the field owns unbounded or reference-shaped storage.
//
// Nested structs are deliberately not followed here: the carriers legitimately
// own bounded sub-fact structs (turn shape, compaction facts, session input)
// whose shapes are validated by their owning packages. Rule 5 covers what those
// sub-facts may contain.
func containerKindChain(typ reflect.Type) []reflect.Kind {
	var chain []reflect.Kind
	for {
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
			chain = append(chain, typ.Kind())
			typ = typ.Elem()
		case reflect.Interface, reflect.Func, reflect.Chan, reflect.UnsafePointer, reflect.Uintptr:
			// uintptr is rejected alongside the reference kinds because it is a
			// raw handle that unsafe.Pointer(uintptr) can convert back into a
			// live pointer, which would smuggle storage past a shape-only check.
			return append(chain, typ.Kind())
		default:
			return chain
		}
	}
}

func kindChainString(chain []reflect.Kind) string {
	parts := make([]string, 0, len(chain))
	for _, kind := range chain {
		parts = append(parts, kind.String())
	}
	return strings.Join(parts, "->")
}

// nestedCarrierViolations walks a sub-fact struct and reports, at every depth:
//
//   - content-bearing names that hold data;
//   - dynamic maps, interfaces, funcs, chans and raw uintptr handles;
//   - any shadow canonical Call;
//   - any container whose storage is not bounded by construction, which is what
//     closes the hole where a smuggling struct is parked one level below a
//     top-level field, for example Extra.Bag{Values []string}.
//
// Residual limitation, stated honestly: this guard rejects storage *shapes*; it
// cannot inspect content. A fixed-shape scalar added to a sub-fact under a benign
// name (Extra.Note string) is therefore not detectable here, and no reflection
// rule can make it so - reviewing a newly added field remains a human
// responsibility. Everything this rule does guarantee is that a carrier cannot
// grow unbounded, dynamic or reference-shaped storage at any depth, cannot reach
// a shadow Call, cannot carry a list of evidence carriers, and cannot hold
// non-count data under a content-bearing name.
func nestedCarrierViolations(typ reflect.Type, path string, visited map[reflect.Type]bool) []string {
	switch typ.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Interface, reflect.Func, reflect.Chan,
		reflect.UnsafePointer, reflect.Uintptr:
		// A pointer is rejected on sight rather than dereferenced: dereferencing
		// first would erase the very edge being judged.
		return []string{fmt.Sprintf("%s has a dynamic or reference shape (%s)", path, typ.Kind())}
	case reflect.Slice, reflect.Array:
		// A sequence is only tolerated when its storage is bounded by
		// construction: a validated shape sequence (capabilityfacts.TurnShape
		// items and parts) or a fixed-width digest sequence (compactionfacts
		// item and tail hashes). []string, [][]byte and [16]string are not.
		if !boundedSubFactContainer(typ) {
			return []string{fmt.Sprintf("%s has unbounded element storage %s", path, kindChainString(containerKindChain(typ)))}
		}
		return nestedCarrierViolations(typ.Elem(), path+"[]", visited)
	}
	if typ == reflect.TypeFor[aipapi.Call]() {
		return []string{path + " retains aipapi.Call"}
	}
	if typ.Kind() != reflect.Struct || visited[typ] {
		return nil
	}
	visited[typ] = true
	defer delete(visited, typ)

	var violations []string
	for field := range typ.Fields() {
		fieldPath := path + "." + field.Name
		if contentBearingField(field) {
			violations = append(violations, fieldPath+" has a content-bearing name holding data")
		}
		if containsCarrierOrCall(field.Type, map[reflect.Type]bool{}) {
			violations = append(violations, fieldPath+" holds a list of classification carriers or canonical Calls")
		}
		violations = append(violations, nestedCarrierViolations(field.Type, fieldPath, visited)...)
	}
	return violations
}

// boundedSubFactContainer reports whether a slice or array inside a sub-fact has
// storage that is bounded by construction.
//
// Accepted, because the carriers legitimately own them and their owning packages
// validate them under the semantic-fact budget:
//
//   - a sequence of validated shapes   ([]capabilityfacts.TurnItemShape)
//   - a fixed-width digest sequence    ([][32]byte, [2][32]uint8)
//   - a fixed-width array of fixed-width values ([32]byte)
//
// Rejected, because the element count or element width is caller-controlled:
//
//   - a sequence of variable-width scalars ([]string, []byte)
//   - a nested sequence                ([][]byte)
//   - a fixed array of variable-width scalars ([16]string)
func boundedSubFactContainer(typ reflect.Type) bool {
	elem := typ.Elem()
	switch elem.Kind() {
	case reflect.Struct, reflect.Array:
		// A shape sequence and a fixed-width digest sequence are both bounded.
		return true
	case reflect.Slice, reflect.Map, reflect.Pointer, reflect.Interface,
		reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return false
	default:
		// Scalar element: a slice of scalars is unbounded in count, so only a
		// fixed-width array of fixed-width values qualifies.
		return typ.Kind() == reflect.Array && fixedWidthKind(elem.Kind())
	}
}

// fixedWidthKind reports whether a scalar kind has a size fixed by its type.
func fixedWidthKind(kind reflect.Kind) bool {
	switch kind {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128:
		return true
	default:
		return false
	}
}

// containsCarrierOrCall reports whether a field type can hold a list of bounded
// classification carriers or canonical Calls. A carrier field is a single fixed
// value, so any container that can hold several of them is by definition
// unbounded request material, even though each element is itself a scalar struct.
func containsCarrierOrCall(typ reflect.Type, visited map[reflect.Type]bool) bool {
	for {
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
			typ = typ.Elem()
		case reflect.Interface, reflect.Func, reflect.Chan, reflect.UnsafePointer:
			return true
		default:
			return typ == reflect.TypeFor[sessionclassification.Evidence]() ||
				typ == reflect.TypeFor[aipapi.Call]()
		}
	}
}

// contentBearingField reports whether a field name carries request content and
// the field holds data rather than a bounded counter.
//
// The numeric exemption is what lets the carriers keep their audited size facts:
// Proof.BodyBytes and ClientTurnShape.TotalContentBytes are bounded counters, not
// retained material. Any non-numeric field under one of these names is a
// violation.
func contentBearingField(field reflect.StructField) bool {
	lowered := strings.ToLower(field.Name)
	if !slices.ContainsFunc(contentBearingNameFragments, func(fragment string) bool {
		return strings.Contains(lowered, fragment)
	}) {
		return false
	}
	switch field.Type.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return false
	default:
		return true
	}
}

// classificationCarrierShapeViolations pins the shipped evidence carrier: three
// fixed-shape scalar fields and nothing else.
func classificationCarrierShapeViolations(evidence reflect.Type) []string {
	var violations []string
	if evidence.Kind() != reflect.Struct {
		return []string{fmt.Sprintf("classification carrier %s is not a struct", evidence)}
	}
	if evidence.NumField() != len(boundedClassificationShapes) {
		violations = append(violations, fmt.Sprintf(
			"classification carrier %s has %d fields, want exactly %d", evidence, evidence.NumField(), len(boundedClassificationShapes)))
	}
	for inner := range evidence.Fields() {
		if want, ok := boundedClassificationShapes[inner.Name]; !ok {
			violations = append(violations, fmt.Sprintf(
				"classification carrier must not gain field %q (requirements 5.5, 7.2)", inner.Name))
		} else if inner.Type != want {
			violations = append(violations, fmt.Sprintf(
				"classification carrier field %q has type %s, want %s", inner.Name, inner.Type, want))
		}
		if chain := containerKindChain(inner.Type); len(chain) > 0 {
			violations = append(violations, fmt.Sprintf(
				"classification carrier field %q has unbounded/reference shape %s", inner.Name, kindChainString(chain)))
		}
		lowered := strings.ToLower(inner.Name)
		if slices.ContainsFunc(contentBearingNameFragments, func(fragment string) bool {
			return strings.Contains(lowered, fragment)
		}) {
			violations = append(violations, fmt.Sprintf(
				"classification carrier field %q is content-bearing", inner.Name))
		}
	}
	return violations
}
