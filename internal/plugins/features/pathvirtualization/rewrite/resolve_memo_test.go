package rewrite_test

// Spec: b-leg-path-virtualization Task 12.1, requirements.md 9.3 and 9.4, against
// design.md "Existing Architecture and Placement" and "Testing Strategy / Benchmarks".
//
// This file is the regression guard for the production performance defect Task 11.1
// measured and reported: the walk resolved a tool's policy once per OCCURRENCE rather than
// once per distinct name, so a tool no profile layer claims had its declared argument
// schema re-read and re-walked on every occurrence. At 1000 occurrences of one unclaimed
// tool that measured 223.0 against 62.0 allocations per occurrence - a 3.6x amplification,
// flat per occurrence, i.e. linear in (occurrences x declared-schema size).
//
// Four properties are pinned, and the third is what makes the first two mean something:
//
//	ONE RESOLUTION PER NAME PER PASS. Repeated occurrences of one tool name consult the
//	    policy once and every one of them is still rewritten: the count is asserted
//	    against published aliases, so a resolver that answered nothing for everything
//	    could not satisfy it.
//	THE MEMO IS PER PASS, NOT PER PROCESS. Two walks over two calls consult it twice, so
//	    nothing can be carried from one request into the next. Requirement 2.3's
//	    no-mutable-mapping rule is about the ROOT mapping and this is a different thing,
//	    but a cross-request cache of derived policy answers would still be a mutable
//	    store, so the scope is asserted rather than assumed.
//	NO SELECTOR SEMANTICS CHANGE. Every surface of a mixed-policy fixture rewrites to
//	    exactly the bytes and statistics it produces when carried alone in its own call.
//	    That is the property a memo can silently break, and it is checked against the
//	    REWRITER'S OWN output rather than against a restatement of the resolution order.
//	THE TWO RESOLUTION SITES STAY INDEPENDENT. A tool call offers its declared schema to
//	    the resolution; a tool result offers none. One walk that sees a call and a result
//	    for the same name must resolve both, so a memo keyed on the name alone - which
//	    would hand the result surface the call surface's resolution - is refused.

import (
	"bytes"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/schemainfer"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// The bounded tool names the mixed-policy fixture resolves. They carry no path, alias,
// workspace tag, or document byte, so no failure message in this file can publish one.
const (
	memoBuiltinToolName    = "read_file"
	memoOperatorToolName   = "synthetic_list_paths"
	memoUndeclaredToolName = "synthetic_undeclared_reader"
	memoNearMissToolName   = "synthetic_unclaimed_reader_near_miss"
)

// memoInference counts how many times the walk consults the policy, wrapping the REAL
// shipped inference step so the count measures the production cost and not a stub's.
//
// It is a count rather than a time for the reason the sibling scaling guard uses: an
// invocation count is a deterministic function of the code and its input.
type memoInference struct {
	inner pathvirtualization.ArgumentInference
	calls int
}

// InferArgumentSelectors implements pathvirtualization.ArgumentInference.
func (i *memoInference) InferArgumentSelectors(declaredSchema []byte) pathvirtualization.SelectorSet {
	i.calls++
	return i.inner.InferArgumentSelectors(declaredSchema)
}

// memoResolver builds one resolver over a chosen set of profile layers, wrapping the
// inference step in a counter when inference is on.
//
// The caller names the operator and built-in layers as nil or not, because which layers
// exist IS the axis the mixed fixture varies.
func memoResolver(tb testing.TB, operator []pathvirtualization.ToolProfile,
	builtin []pathvirtualization.ToolProfile, inference bool,
) (*pathvirtualization.Resolver, *memoInference) {
	tb.Helper()
	var operatorCompiled, builtinCompiled []pathvirtualization.CompiledProfile
	var reject pathvirtualization.SelectorReject
	if len(operator) > 0 {
		if operatorCompiled, reject = pathvirtualization.CompileToolProfiles(operator); reject != pathvirtualization.SelectorRejectNone {
			tb.Fatalf("compile operator profiles: reject %v", reject)
		}
	}
	if len(builtin) > 0 {
		if builtinCompiled, reject = pathvirtualization.CompileToolProfiles(builtin); reject != pathvirtualization.SelectorRejectNone {
			tb.Fatalf("compile built-in profiles: reject %v", reject)
		}
	}
	var step pathvirtualization.ArgumentInference
	var counted *memoInference
	if inference {
		inferrer, inferReject := schemainfer.New(schemainfer.DefaultPathKeys())
		if inferReject != pathvirtualization.SelectorRejectNone {
			tb.Fatalf("compile inference vocabulary: reject %v", inferReject)
		}
		counted = &memoInference{inner: benchInference{inferrer}}
		step = counted
	}
	resolver, reject := pathvirtualization.NewResolver(operatorCompiled, builtinCompiled, step)
	if reject != pathvirtualization.SelectorRejectNone {
		tb.Fatalf("new resolver: reject %v", reject)
	}
	return resolver, counted
}

// memoShippedPolicy is the mixed policy the equivalence fixture resolves against: the
// SHIPPED built-in layer, one operator profile, and the optional inference step. It is
// the only combination in which all four answers of the resolution order are reachable in
// one walk.
func memoShippedPolicy(tb testing.TB) *pathvirtualization.Resolver {
	tb.Helper()
	resolver, _ := memoResolver(tb,
		[]pathvirtualization.ToolProfile{{
			Names:              []string{memoOperatorToolName},
			ArgPointers:        []string{"/file_path"},
			ResultJSONPointers: []string{"/written"},
			OpaqueResultMode:   pathvirtualization.OpaqueResultModePathLines,
		}},
		pathvirtualization.BuiltinToolProfiles(),
		true)
	return resolver
}

// memoMixedTools is the declared tool list the mixed fixture carries. A tool declared with
// no parameters is the "inference has nothing to prove" case; a name absent from this
// list entirely is the "no schema at all" case.
func memoMixedTools() []aipapi.ToolDef {
	return []aipapi.ToolDef{
		{Name: memoBuiltinToolName, Parameters: json.RawMessage(benchDeclaredSchema)},
		{Name: memoOperatorToolName, Parameters: json.RawMessage(benchDeclaredSchema)},
		{Name: benchUnclaimedToolName, Parameters: json.RawMessage(benchDeclaredSchema)},
		{Name: memoUndeclaredToolName},
	}
}

// memoMixedNames is the tool-name axis of the mixed fixture: one name per answer the
// resolution order can give, plus a near-miss spelling that must not inherit any other
// name's resolution.
var memoMixedNames = []string{
	memoBuiltinToolName,
	memoOperatorToolName,
	benchUnclaimedToolName,
	memoUndeclaredToolName,
	memoNearMissToolName,
}

// memoToolCallItem builds one canonical tool-call item for a named tool.
//
// index only selects WHICH fixture path the selected argument carries, so two occurrences
// of one name differ in their payload while sharing one resolution.
func memoToolCallItem(name string, index int) aipapi.Item {
	return aipapi.Item{
		Kind:   aipapi.ItemKindToolCall,
		ID:     "item_call_" + strconv.Itoa(index),
		Status: aipapi.ItemStatusCompleted,
		ToolCall: &aipapi.ToolCallItem{
			CallID:    "call_" + strconv.Itoa(index),
			Name:      name,
			Arguments: json.RawMessage(`{"file_path":"` + memoFixturePath(index) + `"}`),
		},
	}
}

// memoFixturePath is the i-th real path under the first fixture root.
func memoFixturePath(index int) string {
	return benchOccurrencePath(benchRootFixtures[0], index)
}

// memoMixedToolCall builds one canonical item-authoritative call carrying every mixed name
// `repeats` times, so one walk over it must resolve each name once and rewrite every
// occurrence.
func memoMixedToolCall(t *testing.T, repeats int) *aipapi.Call {
	t.Helper()
	var items []aipapi.Item
	for range repeats {
		for _, name := range memoMixedNames {
			items = append(items, memoToolCallItem(name, len(items)))
		}
	}
	call := &aipapi.Call{Items: items, Tools: memoMixedTools()}
	if err := call.Validate(); err != nil {
		t.Fatalf("the mixed fixture must be canonical: %v", err)
	}
	return call
}

// mustMarshalMemoJSON renders one value as JSON so a change to any nested slice or
// RawMessage is visible where a Go comparison would not catch it.
func mustMarshalMemoJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("value is not encodable: %v", err)
	}
	return string(encoded)
}

// TestRepeatedIdenticalToolNamesResolveOncePerPass is the defect's regression guard.
//
// The fixture is the shape that reached it: ONE unclaimed tool name, a declared schema,
// and many occurrences. Without the memo every occurrence re-read the schema, so the
// consultation count equalled the occurrence count.
func TestRepeatedIdenticalToolNamesResolveOncePerPass(t *testing.T) {
	t.Parallel()
	const occurrences = 64

	fixture := benchRootFixtures[0]
	mapping := benchActiveMapping(t, fixture.root)
	resolver, counted := memoResolver(t, nil, nil, true)
	rewriter := rewrite.New(mapping, resolver)
	tools := []aipapi.ToolDef{{
		Name:       benchUnclaimedToolName,
		Parameters: json.RawMessage(benchDeclaredSchema),
	}}
	call := benchRequestCall(t, fixture, occurrences, benchUnclaimedToolName, tools)

	published, stats, err := rewriter.RewriteCall(call)
	if err != nil {
		t.Fatalf("RewriteCall: %v", err)
	}
	// Non-vacuity, stated before the memo assertion: the one resolution really did decide
	// every occurrence. Without it a resolver answering "no selector" for everything
	// would consult the step once and look perfect.
	if stats.Rewritten != occurrences {
		t.Fatalf("rewrote %d occurrences, want %d", stats.Rewritten, occurrences)
	}
	if got := counted.calls; got != 1 {
		t.Fatalf("the policy was consulted %d times over %d occurrences of one tool name, want 1",
			got, occurrences)
	}
	if published == call {
		t.Fatal("the pass published no change, so the single resolution decided nothing")
	}
	for i := range published.Items {
		if !bytes.Contains(published.Items[i].ToolCall.Arguments, []byte(mapping.VirtualRoot)) {
			t.Fatalf("occurrence %d carries no alias, so it was not resolved from the memo", i)
		}
	}
}

// TestTheResolutionMemoIsScopedToOneWalk keeps the memo out of process state.
//
// Two walks over two calls of the same shape must consult the policy twice, not once. A
// memo held on the Rewriter would make the second walk free and would outlive the request
// it was built from, which is the shape requirement 2.3 forbids for the root mapping and
// the shape a generation-shared Rewriter must not acquire for policy answers.
func TestTheResolutionMemoIsScopedToOneWalk(t *testing.T) {
	t.Parallel()
	const occurrences = 8

	fixture := benchRootFixtures[0]
	mapping := benchActiveMapping(t, fixture.root)
	resolver, counted := memoResolver(t, nil, nil, true)
	rewriter := rewrite.New(mapping, resolver)
	tools := []aipapi.ToolDef{{
		Name:       benchUnclaimedToolName,
		Parameters: json.RawMessage(benchDeclaredSchema),
	}}

	for walk := 1; walk <= 2; walk++ {
		call := benchRequestCall(t, fixture, occurrences, benchUnclaimedToolName, tools)
		if _, stats, err := rewriter.RewriteCall(call); err != nil {
			t.Fatalf("walk %d: %v", walk, err)
		} else if stats.Rewritten != occurrences {
			t.Fatalf("walk %d rewrote %d occurrences, want %d", walk, stats.Rewritten, occurrences)
		}
		if counted.calls != walk {
			t.Fatalf("after %d walks the policy was consulted %d times, want %d: the memo outlived its walk",
				walk, counted.calls, walk)
		}
	}
}

// TestMemoizationDoesNotChangeWhatAnyOccurrenceResolvesTo is the selector-semantics
// guard.
//
// Every surface of the mixed fixture is rewritten on its own, in a call carrying only that
// one item and the same declared tools, and the published bytes and statistics must match
// the whole-walk result item by item. Comparing against the rewriter's OWN output is what
// makes this independent of the resolution order: a memo that dropped, merged, or crossed
// a selector would change a published byte, and the statistics would stop summing.
func TestMemoizationDoesNotChangeWhatAnyOccurrenceResolvesTo(t *testing.T) {
	t.Parallel()
	const repeats = 3

	mapping := benchActiveMapping(t, benchRootFixtures[0].root)
	tools := memoMixedTools()
	rewriter := rewrite.New(mapping, memoShippedPolicy(t))

	// The single-occurrence walks read the ORIGINAL items. Handing them the published
	// ones would be the wrong comparison twice over: an already virtualized argument
	// rewrites to nothing, so every per-occurrence measurement would be zero.
	original := memoMixedToolCall(t, repeats)
	whole, wholeStats, err := rewriter.RewriteCall(original)
	if err != nil {
		t.Fatalf("RewriteCall over the whole fixture: %v", err)
	}
	if len(whole.Items) != len(memoMixedNames)*repeats {
		t.Fatalf("the whole fixture carried %d items, want %d",
			len(whole.Items), len(memoMixedNames)*repeats)
	}
	// Non-vacuity: the fixture must exercise more than one answer, or "every occurrence
	// behaves the same" would be satisfiable by a walk that resolves nothing.
	if wholeStats.Rewritten == 0 {
		t.Fatal("the mixed fixture rewrote nothing, so the equivalence proves nothing")
	}

	sum := rewrite.Stats{}
	for i := range original.Items {
		single := &aipapi.Call{Tools: tools, Items: original.Items[i : i+1]}
		published, stats, err := rewriter.RewriteCall(single)
		if err != nil {
			t.Fatalf("RewriteCall over occurrence %d alone: %v", i, err)
		}
		if got, want := mustMarshalMemoJSON(t, published.Items[0]),
			mustMarshalMemoJSON(t, whole.Items[i]); got != want {
			t.Errorf("occurrence %d rewrote to different bytes when carried alone than in the whole walk", i)
		}
		sum.Eligible += stats.Eligible
		sum.Rewritten += stats.Rewritten
		sum.BytesBefore += stats.BytesBefore
		sum.BytesAfter += stats.BytesAfter
		sum.Skips = memoMergeSkips(sum.Skips, stats.Skips)
	}
	if got, want := mustMarshalMemoJSON(t, sum), mustMarshalMemoJSON(t, wholeStats); got != want {
		t.Errorf("the whole walk's statistics differ from the sum of the per-occurrence walks:\n got %s\nwant %s",
			got, want)
	}
}

// memoMergeSkips folds one walk's skip tallies into the running sum, preserving the
// ascending reason order the engine publishes so the merged value is comparable.
func memoMergeSkips(into, from []rewrite.Skip) []rewrite.Skip {
	for _, skip := range from {
		merged := false
		for i := range into {
			if into[i].Reason == skip.Reason {
				into[i].Count += skip.Count
				merged = true
				break
			}
		}
		if !merged {
			into = append(into, skip)
		}
	}
	return into
}

// TestTheTwoResolutionSitesResolveIndependently keeps the memo from merging the surfaces.
//
// A tool CALL offers its declared schema to the resolution, because that is what the
// inference step proves argument locations from. A tool RESULT offers none, and that is
// structural: a result has no declared schema, so a resolution derived from one is a
// different answer from the same name's call-surface resolution. A memo keyed on the name
// alone would collapse the two sites and hand a result surface a resolution built from a
// schema it never offered.
//
// Both consultations must therefore happen. The count is what proves the sites stayed
// separate even where today's published bytes would not notice.
func TestTheTwoResolutionSitesResolveIndependently(t *testing.T) {
	t.Parallel()

	fixture := benchRootFixtures[0]
	mapping := benchActiveMapping(t, fixture.root)
	resolver, counted := memoResolver(t, nil, nil, true)
	rewriter := rewrite.New(mapping, resolver)
	call := &aipapi.Call{
		Tools: []aipapi.ToolDef{{
			Name:       benchUnclaimedToolName,
			Parameters: json.RawMessage(benchDeclaredSchema),
		}},
		Items: []aipapi.Item{
			memoToolCallItem(benchUnclaimedToolName, 0),
			{
				Kind:   aipapi.ItemKindToolResult,
				ID:     "item_result",
				Status: aipapi.ItemStatusCompleted,
				ToolResult: &aipapi.ToolResultItem{
					CallID: "call_0",
					Name:   benchUnclaimedToolName,
					Output: memoFixturePath(1),
				},
			},
		},
	}
	if err := call.Validate(); err != nil {
		t.Fatalf("the call/result fixture must be canonical: %v", err)
	}

	published, stats, err := rewriter.RewriteCall(call)
	if err != nil {
		t.Fatalf("RewriteCall: %v", err)
	}
	if got := counted.calls; got != 2 {
		t.Fatalf("the call surface and the result surface produced %d resolutions, want 2: "+
			"they offer different inputs and must not share one memo entry", got)
	}
	if stats.Rewritten != 1 {
		t.Fatalf("rewrote %d occurrences, want 1: inference proves argument locations only, "+
			"so the result surface is ordinary content", stats.Rewritten)
	}
	if bytes.Contains([]byte(published.Items[1].ToolResult.Output), []byte(mapping.VirtualRoot)) {
		t.Error("the opaque result surface was rewritten; requirement 3.8 keeps inferred tools out of it")
	}
	if !bytes.Contains(published.Items[0].ToolCall.Arguments, []byte(mapping.VirtualRoot)) {
		t.Error("the argument surface was not rewritten")
	}
}
