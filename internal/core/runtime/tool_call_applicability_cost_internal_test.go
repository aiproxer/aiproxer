package runtime

// Cost and isolation guards for the per-tool-call applicability derivation.
//
// [toolCallAssembler.deriveCallRequirements] runs on the attempt's receive loop
// once per EventToolCallStarted, so its cost is latency on the streaming hot
// path rather than setup cost. It asks every DECLARER whether its declared
// completeness requirement governs the call, and each answer runs on its own
// DETACHED copy of the tool catalog.
//
// WHY THE COPY IS PER DECLARER, which is the load-bearing decision here and the
// thing a cost-only reading of this code gets wrong. The copy exists to keep
// extension code away from state whose integrity the ASSEMBLER depends on, and
// that state is not only the assembler's own catalog: it is every OTHER
// declarer's view of the catalog inside the same derivation. One copy shared
// across declarers would let a declarer that scribbles in place rename or
// rewrite the entries its SIBLING is about to resolve against, so the sibling's
// applicability answer would be computed from forged input. For a sibling that
// declared a mandatory bound that is not a slowdown, it is a silent bypass of
// that sibling's requirement, reached from a sibling that is itself extension
// code: no error, no refusal, and a call released past a bound nobody enforced.
// So each declarer gets an independent copy, and the isolation is per declarer
// rather than merely per derivation.
//
// The consequence for cost is accepted deliberately: an O(catalog) copy per
// declarer, on a chain whose declarer count is bounded by the plugins a host
// composes. What MUST hold is that the growth stays LINEAR in the declarer
// count. A shared copy would be cheaper by a constant factor and is still wrong,
// so the guard below pins LINEARITY, which is the property separating the
// accepted cost from a super-linear regression, and the isolation tests pin the
// reason that constant factor is not available to spend.
//
// The guard is expressed in ALLOCATIONS rather than wall-clock time for the
// reason the sibling scaling guards in this feature use: an allocation count is
// a deterministic function of the code and its input, so it is identical on every
// host, while a timing threshold is not.
//
// The cost test is deliberately SERIAL. testing.AllocsPerRun reads process-wide
// allocation state, so a concurrent parallel sibling would be counted as this
// file's own.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

// jsonBytesOfCatalog renders a catalog so two of them can be compared
// byte-for-byte. Marshaling is used rather than a field-by-field walk because
// the point of the assertion is that NOTHING the extension code could reach
// changed, including a field this test does not know about.
func jsonBytesOfCatalog(catalog []aipapi.ToolDef) ([]byte, error) {
	return json.Marshal(catalog)
}

// costCatalogTools is the catalog width the derivation runs over. It is the tier
// that makes the per-declarer catalog copy, rather than the per-declarer
// bookkeeping, the dominant allocation.
const costCatalogTools = 64

// costDeclarerTiers are the declarer counts compared against each other. They
// differ by a factor of four so LINEAR growth through the origin is a
// multiplier of four and the bound above that can absorb ordinary constant
// factors without pinning the constant itself.
const (
	costFewDeclarers  = 2
	costManyDeclarers = 8
)

// costSuperLinearBound is the allowed allocation growth when the declarer count is
// QUADRUPLED. Linear growth is a multiplier of four, so this sits above that for
// constant factors and measurement noise, and far below the compounding shapes
// the guard exists to catch.
//
// A QUADRUPLED count rather than a one-to-eight comparison is deliberate.
// Comparing one declarer against eight cannot distinguish "one catalog copy per
// declarer", which is linear and intended, from any fixed multiple of it: they
// differ only in the constant, so the measured ratio IS that constant and the
// assertion would be tuning a number rather than pinning a property.
const costSuperLinearBound = 8

// costCatalog builds a catalog of n tools, each carrying a Parameters document
// wide enough that cloning it is a real per-tool cost rather than a fixed header
// copy.
func costCatalog(n int) []aipapi.ToolDef {
	catalog := make([]aipapi.ToolDef, 0, n)
	for i := range n {
		catalog = append(catalog, aipapi.ToolDef{
			Name:       fmt.Sprintf("cost_tool_%03d", i),
			Parameters: []byte(`{"type":"object","properties":{"file_path":{"type":"string"},"content":{"type":"string"}}}`),
		})
	}
	return catalog
}

// applicabilityCostSink keeps the measured derivation observable so the
// measurement is of the real call and not of an eliminated one.
var applicabilityCostSink *callRequirements

// TestToolCallAssembler_ApplicabilityCostGrowsLinearlyWithDeclarerCount pins the
// accepted per-declarer copy as LINEAR rather than super-linear.
//
// The catalog is deliberately WIDE (64 tools) so the copy dominates the
// measurement rather than the per-declarer bookkeeping. See the file comment for
// why a one-to-eight declarer comparison could not pin this.
func TestToolCallAssembler_ApplicabilityCostGrowsLinearlyWithDeclarerCount(t *testing.T) {
	catalog := costCatalog(costCatalogTools)
	target := catalog[0].Name

	measure := func(declarers int) float64 {
		t.Helper()
		finalizers := make([]toolcall.Finalizer, 0, declarers)
		for i := range declarers {
			finalizers = append(finalizers, newScopedFin(t, fmt.Sprintf("cost_declarer_%d", i), i,
				toolcall.BufferingSpec{
					MaxArgsBytes: toolcall.MinMandatoryMaxArgsBytes,
					Overflow:     toolcall.OverflowReject,
				}, target))
		}
		a := newToolCallAssembler(finalizers, 0, catalog)
		if a == nil {
			t.Fatalf("fixture: the assembler must be constructed for declarers=%d", declarers)
		}
		// Fixture guard, outside the measurement: a declarer that governs nothing
		// would make the derivation return before it copied anything, so the case
		// would pass vacuously.
		reqs := a.deriveCallRequirements(target)
		if reqs == nil {
			t.Fatal("fixture: no applicable requirements")
		}
		if len(reqs.items) != declarers {
			t.Fatalf("fixture: declarers=%d must each govern %q, got %d requirements",
				declarers, target, len(reqs.items))
		}
		return testing.AllocsPerRun(20, func() {
			applicabilityCostSink = a.deriveCallRequirements(target)
		})
	}

	few := measure(costFewDeclarers)
	many := measure(costManyDeclarers)
	if few <= 0 {
		t.Fatalf("fixture: the measured derivation allocated nothing, so the ratio is meaningless: %v", few)
	}
	if ratio := many / few; ratio > float64(costSuperLinearBound) {
		t.Fatalf("applicability derivation allocations grew %0.2fx when the declarer count grew %dx "+
			"(%d to %d, %.0f to %.0f allocs, bound %d): the cost must stay LINEAR in the declarer "+
			"count, so a per-declarer catalog copy is a fixed multiple rather than one that compounds",
			ratio, costManyDeclarers/costFewDeclarers, costFewDeclarers, costManyDeclarers,
			few, many, costSuperLinearBound)
	}
}

// hostileApplicabilityFin is a declarer that clobbers everything it is handed,
// in place. It stands in for extension code that mutates the values the consumer
// passed it rather than merely reading them.
func hostileApplicabilityFin(t *testing.T, id string, order int, spec toolcall.BufferingSpec, selected ...string) *scopedExpansionFin {
	t.Helper()
	fin := newScopedFin(t, id, order, spec, selected...)
	fin.mutate = func(_ aipapi.ToolDef, tools []aipapi.ToolDef) {
		for i := range tools {
			tools[i].Name = "clobbered"
			for j := range tools[i].Parameters {
				tools[i].Parameters[j] = 'x'
			}
		}
	}
	return fin
}

// The sibling actually reads both detached inputs, rather than only the scalar
// tool name, so sharing a mutable catalog makes this test fail.
type catalogReadingApplicability struct {
	*scopedExpansionFin
	schema []byte
}

func (f catalogReadingApplicability) ToolCallBufferingApplies(name string, tool aipapi.ToolDef, catalog []aipapi.ToolDef) bool {
	if tool.Name != name || !bytes.Equal(tool.Parameters, f.schema) {
		return false
	}
	for _, entry := range catalog {
		if entry.Name == name {
			return bytes.Equal(entry.Parameters, f.schema)
		}
	}
	return false
}

// TestToolCallAssembler_ApplicabilityCannotForgeASiblingsApplicability is the
// isolation guard, and it is the reason the per-declarer copy is not a
// performance choice.
//
// Two declarers share the chain. The FIRST is hostile: it renames every catalog
// entry it is handed, in place. The SECOND is an ordinary mandatory declarer
// that answers by exact tool name, which is what every shipped declarer does. If
// the two shared one copy the sibling would resolve its name against the FORGED
// catalog, find nothing, answer "does not govern this call", and its mandatory
// requirement would never attach - a silent bypass reached from extension code.
//
// The assertion is behavioural rather than a copy count, because the property is
// the DECISION and not the mechanism: an implementation that keeps the decision
// correct passes, and the per-declarer copy is simply the current way to do that.
func TestToolCallAssembler_ApplicabilityCannotForgeASiblingsApplicability(t *testing.T) {
	t.Parallel()

	catalog := costCatalog(4)
	target := catalog[0].Name
	spec := toolcall.BufferingSpec{
		MaxArgsBytes: toolcall.MinMandatoryMaxArgsBytes,
		Overflow:     toolcall.OverflowReject,
	}

	hostile := hostileApplicabilityFin(t, "hostile_applicability", 0, spec, target)
	sibling := catalogReadingApplicability{newScopedFin(t, "sibling_applicability", 1, spec, target), bytes.Clone(catalog[0].Parameters)}

	a := newToolCallAssembler([]toolcall.Finalizer{hostile, sibling}, 0, catalog)
	if a == nil {
		t.Fatal("fixture: the assembler must be constructed for a non-empty finalizer list and catalog")
	}

	reqs := a.deriveCallRequirements(target)
	if reqs == nil {
		t.Fatal("a hostile applicability answer must not make the derivation itself disappear")
	}
	// The SIBLING is the load-bearing assertion: it is the declarer whose
	// applicability the hostile one must not be able to forge, and the one that
	// would silently lose its mandatory requirement.
	if len(reqs.items) != 2 {
		t.Fatalf("both declarers must govern %q, got %d requirements: a sibling whose applicability "+
			"was computed from a forged catalog has silently lost its mandatory requirement",
			target, len(reqs.items))
	}
	if reqs.requirementFor(sibling.order) == nil {
		t.Fatalf("the sibling declarer must still hold a requirement for %q", target)
	}
}

// TestToolCallAssembler_ApplicabilityCannotCorruptTheAssemblersOwnCatalog is the
// second isolation half: extension code must not reach the catalog that every
// LATER finalization and every future call reads.
//
// This is weaker than the sibling case above on purpose, and the difference is
// the point. A copy per DECLARER is what the sibling property requires, and it
// happens to also protect the assembler. Were the two ever separated - by a
// caching or sharing optimization on the catalog path - this is the test that
// fails first, because it asserts the assembler's own bytes rather than a
// derived decision.
func TestToolCallAssembler_ApplicabilityCannotCorruptTheAssemblersOwnCatalog(t *testing.T) {
	t.Parallel()

	catalog := costCatalog(4)
	before, err := jsonBytesOfCatalog(catalog)
	if err != nil {
		t.Fatalf("catalog fixture must marshal: %v", err)
	}

	hostile := hostileApplicabilityFin(t, "hostile_applicability", 0, toolcall.BufferingSpec{
		MaxArgsBytes: toolcall.MinMandatoryMaxArgsBytes,
		Overflow:     toolcall.OverflowReject,
	}, catalog[0].Name)

	a := newToolCallAssembler([]toolcall.Finalizer{hostile}, 0, catalog)
	if a == nil {
		t.Fatal("fixture: the assembler must be constructed for a non-empty finalizer list and catalog")
	}
	reqs := a.deriveCallRequirements(catalog[0].Name)
	if reqs == nil {
		t.Fatal("fixture: no applicable requirement")
	}
	if len(reqs.items) != 1 {
		t.Fatalf("fixture: the hostile declarer must still govern the call, got %d requirements", len(reqs.items))
	}

	after, err := jsonBytesOfCatalog(a.catalog)
	if err != nil {
		t.Fatalf("assembler catalog must marshal: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("a hostile applicability answer corrupted the assembler's own tool catalog: " +
			"the copy handed to extension code must be detached from it")
	}
}
