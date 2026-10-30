package rewrite_test

// Spec: b-leg-path-virtualization Task 11.1, requirements.md 9.2, 9.3 and 9.4, and the
// two prior findings this feature recorded about a quadratic parse cost: the standing
// guard that a super-linear cost cannot come back silently.
//
// Benchmarks are measurements and cannot gate anything. These tests are the guard, and
// they are expressed in ALLOCATIONS for one reason: an allocation measurement is a
// deterministic function of the code and its input, so it is identical on every host,
// on every CPU, and under every timing condition, while a wall-clock threshold is
// none of those things. A relative-scaling assertion is used rather than an absolute
// count, because the absolute count legitimately moves whenever the byte-splice
// internals change, whereas a growth factor between two input sizes only moves if the
// cost stops being proportional to its input.
//
// Two measurement units appear below, and the split is not stylistic. The occurrence
// guards and the DEPTH guard are expressed in allocation COUNT where a count can see
// the defect, and the depth guard's own unit is chosen by measurement: rebuilding one
// pointer path per nesting level adds about one allocation per level but roughly thirty
// times the BYTES, so a count would show a 10% difference and pass while the pass
// allocated half a gigabyte. See the depth guard for the numbers and for why it
// measures allocated bytes.
//
// Both kinds of assertion are deliberately LOOSE. Each compares the per-occurrence (or
// per-level) allocation cost at the largest tier against the cost at the smallest and
// allows a factor of two. Linear cost passes with a factor near one; a reintroduced
// quadratic passes with a factor of about 100, far outside a bound of two while leaving
// ordinary implementation churn plenty of room.
//
// None of these tests calls t.Parallel, and that is load-bearing rather than an
// oversight. testing.AllocsPerRun and runtime.ReadMemStats both pin or read
// process-wide allocation state, so a concurrent parallel sibling would be counted as
// if it were this test's own. The testing package runs serial top-level tests one at a
// time and releases parallel ones only afterwards, so a serial test here is measured on
// a quiet process.

import (
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
)

// scalingRuns is how many measured executions each allocation sample averages.
//
// The count is a count, not a time: repeated identical executions allocate identically,
// so more runs cost time without buying accuracy. Three keeps the sample cheap enough to
// sit in the ordinary test suite while still running the measured path more than once.
const scalingRuns = 3

// scalingTolerance is how much the per-occurrence cost may grow between the smallest and
// the largest tier. See the file comment for why it is two and not one.
const scalingTolerance = 2.0

// TestSelectorGuidedMutationAllocationsScaleWithOccurrenceCount pins requirement 9.2's
// bounded-cost claim on the selector-guided argument path: one payload's cost is
// proportional to the number of occurrences it carries, not quadratic in it.
//
// This is the guard the benchmark tiers exist to inform. The benchmark shows the
// per-occurrence numbers; this test refuses a regression that would be easy to miss in
// a total.
func TestSelectorGuidedMutationAllocationsScaleWithOccurrenceCount(t *testing.T) {
	fixture := benchRootFixtures[0]
	mapping := benchActiveMapping(t, fixture.root)
	pointers := benchArgPointerSet(t)
	decide := benchVirtualizeDecider(mapping)

	perOccurrence := make([]float64, 0, len(benchOccurrenceTiers))
	for _, occurrences := range benchOccurrenceTiers {
		raw := benchArgumentDocument(fixture, occurrences)
		sample := func() {
			if _, _, err := rewrite.ApplySelectedValues(raw, pointers, decide); err != nil {
				t.Fatalf("ApplySelectedValues over %d occurrences: %v", occurrences, err)
			}
		}
		allocs := testing.AllocsPerRun(scalingRuns, sample)
		t.Logf("occurrences=%4d allocs=%7d allocs-per-occurrence=%.2f",
			occurrences, int(allocs), allocs/float64(occurrences))
		perOccurrence = append(perOccurrence, allocs/float64(occurrences))
	}

	smallest := perOccurrence[0]
	largest := perOccurrence[len(perOccurrence)-1]
	if largest > scalingTolerance*smallest {
		t.Fatalf("selector-guided mutation allocated %.1f objects per occurrence at the largest tier "+
			"and %.1f at the smallest: the per-occurrence cost grew by %.1fx, which is not proportional "+
			"to the occurrence count (requirement 9.2)",
			largest, smallest, largest/smallest)
	}
}

// TestSecondOutboundPassIsCheaperAndChangesNoByte pins design.md's "Second outbound
// pass fast-skips already virtualized roots" as a measurable property rather than as a
// claim: the second pass must allocate strictly less than the first and must return the
// very same call.
//
// The byte half is asserted with pointer identity, which is stronger than comparing
// bytes: a pass that returns its input pointer cannot have published anything, so no
// comparison of the published bytes can add anything to it. The cost half is asserted on
// allocation counts for the same reason the scaling test uses them.
func TestSecondOutboundPassIsCheaperAndChangesNoByte(t *testing.T) {
	// The largest occurrence tier is the one where a redundant clone or a redundant
	// re-serialization would be most visible, so it is the tier the claim is made about.
	const occurrences = 1000

	fixture := benchRootFixtures[0]
	rewriter := benchOperatorRewriter(t, fixture.root, pathvirtualization.ToolProfile{
		Names:       []string{benchUnclaimedToolName},
		ArgPointers: []string{"/file_path", "/paths"},
	})
	first := benchRequestCall(t, fixture, occurrences, benchUnclaimedToolName, nil)

	published, firstStats, err := rewriter.RewriteCall(first)
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if firstStats.Rewritten != occurrences {
		t.Fatalf("first pass rewrote %d occurrences, want %d", firstStats.Rewritten, occurrences)
	}

	second, secondStats, err := rewriter.RewriteCall(published)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if second != published {
		t.Fatal("the second pass published a new call; an already-virtualized request " +
			"must be republished as the very same call")
	}
	if secondStats.Rewritten != 0 || secondStats.BytesSaved() != 0 {
		t.Fatalf("the second pass reported %d rewritten occurrences and %d saved bytes, want 0 and 0",
			secondStats.Rewritten, secondStats.BytesSaved())
	}

	firstAllocs := testing.AllocsPerRun(scalingRuns, func() {
		if _, _, err := rewriter.RewriteCall(first); err != nil {
			t.Fatalf("first pass: %v", err)
		}
	})
	secondAllocs := testing.AllocsPerRun(scalingRuns, func() {
		if _, _, err := rewriter.RewriteCall(published); err != nil {
			t.Fatalf("second pass: %v", err)
		}
	})
	if secondAllocs >= firstAllocs {
		t.Fatalf("the second pass allocated %.0f objects and the first %.0f: reapplying a rewrite "+
			"to an already-virtualized request must not cost what publishing it cost",
			secondAllocs, firstAllocs)
	}
}

// The DEPTH guards below are the second recorded finding of the same family: the walk
// that locates selected leaves rebuilt the complete JSON-Pointer text of every container
// it opened, at every nesting level, including inside subtrees holding no selected leaf
// at all, and every open frame retained its own text. One nesting level therefore cost
// the length of every reference token above it, so a valid document well inside the
// 1 MiB argument bound could allocate hundreds of megabytes and grow quadratically in
// its own nesting depth rather than in its size.
//
// The measurement unit is ALLOCATED BYTES, not allocation count, and that is a measured
// choice rather than a stylistic one. The defect adds roughly one allocation per nesting
// level (the retained path string) on top of the decoder's own ~10 per level, so an
// allocation COUNT moves from ~11.1 to ~10.1 per level: a ten percent difference that any
// sane tolerance would wave through, while the BYTES move from ~31,700 to ~990 per level
// on the fixture below, a factor of thirty-two. runtime.ReadMemStats.TotalAlloc is used
// for the same reason the sibling reserved-scan guard uses it: it is a monotonic
// cumulative byte total, so a collection happening at any point inside the measured
// region cannot change it, unlike a live heap reading.

// depthSegmentName pads each nesting level to roughly a hundred bytes.
//
// The name length is the point of the fixture: a quadratic path cost is proportional to
// the reference tokens ABOVE a level, so a short member name hides most of it. At
// ~98 bytes per level the guarded fixture is a ~146 KiB document at the largest tier,
// which is inside the default 1 MiB mandatory argument bound a real completed call can
// carry.
const depthSegmentName = "nested_segment_name_padding_to_a_hundred_byte_json_member_name"

// depthSegmentPrefix names the subtree the unselected nesting lives in. It is a distinct
// member from the selected one so the nesting cannot accidentally be inside the selected
// location, and it is not a pointer any profile declares.
const depthSegmentPrefix = "unselected_nesting"

// depthOccurrenceTiers are the nesting depths the depth guards compare. The top tier is
// the deepest nesting a realistic payload carries; the span is wide enough that a cost
// that stops being proportional to the depth is visible in a ratio rather than hidden in
// a total.
var depthOccurrenceTiers = []int{250, 500, 1000}

// depthAllocationRuns is how many measured passes each byte sample averages.
const depthAllocationRuns = 3

// depthByteSlack absorbs a per-level byte cost that is legitimately tiny at one tier and
// small at another, so the relative guard keeps its meaning without dividing by zero.
const depthByteSlack = 256

// depthAllocationByteRatio bounds one pass's allocated bytes as a multiple of the
// document it read.
//
// A pass proportional to its input allocates the decoded document, the located spans,
// and the spliced output: a small fixed multiple, measured at 16x on this fixture. The
// quadratic path cost measured 520x on the same fixture, so this bound sits four times
// above the real cost and thirteen times below the defect. It is deliberately expressed
// as a RATIO rather than an absolute byte total, because a fixed total would have to be
// re-derived every time the byte-splice internals change, while the ratio is a property
// of the cost's SHAPE.
const depthAllocationByteRatio = 64

// deepUnselectedDocument builds one argument document holding one selected member beside
// a subtree of depth unselected nesting levels, every level named by a ~100-byte member.
//
// The selected member is at depth one, so no profile could ever name anything below the
// nesting: the walk must still traverse it, because a payload may nest its selected
// member arbitrarily deep, but nothing in it can be a selected leaf. That is what makes
// this the worst case for the defect and a no-op for the fix.
func deepUnselectedDocument(depth int) []byte {
	var doc strings.Builder
	doc.Grow(depth*(len(depthSegmentName)+8) + 64)
	doc.WriteString(`{"file_path":"`)
	doc.WriteString(benchPOSIXWorktreeRoot)
	doc.WriteString(`/src/` + benchUnclaimedToolName + `_selected.go","`)
	doc.WriteString(depthSegmentPrefix + `":`)
	for i := range depth {
		doc.WriteString(`{"` + depthSegmentPrefix + strconv.Itoa(i) + depthSegmentName + `":`)
	}
	doc.WriteString(`{}`)
	for range depth {
		doc.WriteByte('}')
	}
	doc.WriteByte('}')
	return []byte(doc.String())
}

// measureDepthBytes reports the average number of bytes one pass over raw allocates.
//
// The warm-up call matters: it pays every lazily-populated decoder and map-growth cost
// once, so the measured region reports the steady-state cost of the pass rather than the
// first-touch cost of the fixture.
func measureDepthBytes(t *testing.T, raw []byte, pointers pathvirtualization.SelectorSet) float64 {
	t.Helper()
	decide := func(string) rewrite.ValueDecision {
		return rewrite.ValueDecision{Replacement: benchPOSIXWorktreeRoot, Eligible: true}
	}
	if _, _, err := rewrite.ApplySelectedValues(raw, pointers, decide); err != nil {
		t.Fatalf("ApplySelectedValues over %d bytes: %v", len(raw), err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range depthAllocationRuns {
		published, pass, err := rewrite.ApplySelectedValues(raw, pointers, decide)
		if err != nil {
			t.Fatalf("ApplySelectedValues over %d bytes: %v", len(raw), err)
		}
		// The pass must keep working while it is measured: a walk that found no leaf
		// would publish nothing and cost almost nothing, which is exactly how a
		// measurement can become vacuous.
		if len(published) == 0 || !pass.Changed || pass.Replaced != 1 {
			t.Fatalf("the depth fixture published nothing: changed=%t replaced=%d",
				pass.Changed, pass.Replaced)
		}
	}
	runtime.ReadMemStats(&after)
	return float64(after.TotalAlloc-before.TotalAlloc) / float64(depthAllocationRuns)
}

// TestUnselectedNestingAllocationsStayBoundedOverDocumentSize is the absolute half of
// the depth guard: a pass over one deeply nested document must not allocate memory
// proportional to the product of its size and its nesting depth.
//
// It pins the ANSWER as well as the cost, so no future bound can be bought by walking
// less or by selecting nothing.
func TestUnselectedNestingAllocationsStayBoundedOverDocumentSize(t *testing.T) {
	depth := depthOccurrenceTiers[len(depthOccurrenceTiers)-1]
	raw := deepUnselectedDocument(depth)
	pointers := benchArgPointerSet(t)

	perPass := measureDepthBytes(t, raw, pointers)
	ratio := perPass / float64(len(raw))
	t.Logf("depth=%d payload_bytes=%d bytes_per_pass=%.0f bytes_per_payload_byte=%.1f",
		depth, len(raw), perPass, ratio)
	if ratio > depthAllocationByteRatio {
		t.Fatalf("one pass over a %d-byte document nested %d levels deep allocated %.0f bytes, "+
			"%.1f bytes per payload byte: locating selected leaves must cost the document, not the "+
			"document times its nesting depth (bound %d bytes per payload byte)",
			len(raw), depth, perPass, ratio, depthAllocationByteRatio)
	}
}

// TestUnselectedNestingAllocationsScaleWithDepth is the relative half of the depth
// guard, mirroring the occurrence guard above: the per-level cost of one pass must stay
// proportional to the number of nesting levels instead of growing with it.
//
// It catches the same defect as the absolute bound from the other side, so a future
// change that keeps the deepest fixture inside the ratio while reintroducing growth per
// level still fails here.
func TestUnselectedNestingAllocationsScaleWithDepth(t *testing.T) {
	pointers := benchArgPointerSet(t)
	perLevel := make([]float64, 0, len(depthOccurrenceTiers))
	for _, depth := range depthOccurrenceTiers {
		raw := deepUnselectedDocument(depth)
		perPass := measureDepthBytes(t, raw, pointers)
		cost := perPass / float64(depth)
		t.Logf("depth=%5d payload_bytes=%7d bytes_per_pass=%9.0f bytes_per_level=%.1f",
			depth, len(raw), perPass, cost)
		perLevel = append(perLevel, cost)
	}

	smallest := perLevel[0]
	largest := perLevel[len(perLevel)-1]
	if largest > scalingTolerance*smallest+depthByteSlack {
		t.Fatalf("one pass allocated %.0f bytes per nesting level at the deepest tier and %.0f at the "+
			"shallowest: the per-level cost grew by a factor that is not proportional to the nesting "+
			"depth, a factor of %.1f",
			largest, smallest, largest/(smallest+depthByteSlack))
	}
}
