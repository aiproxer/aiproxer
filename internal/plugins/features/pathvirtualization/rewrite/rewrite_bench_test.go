package rewrite_test

// Spec: b-leg-path-virtualization Task 11.1, requirements.md 9.2, 9.3, 9.4 and 9.6,
// against design.md "Testing Strategy / Benchmarks" and "Observability".
//
// This file measures the outbound half of the feature over growing occurrences:
// requirement 9.4's "selector-guided argument rewriting" and its "representative
// path-oriented result rewriting", design.md's "Idempotent second outbound pass", and
// the 10/100/1000 occurrence request fixtures design.md asks for.
//
// The two roots and their lengths are the requirement 9.6 fixtures: a deep synthetic
// POSIX monorepo worktree root of 151 bytes and a long synthetic Windows drive worktree
// root of 220 bytes, whose occurrence paths are 187 and 256 bytes respectively. The
// Windows figure matters most here: 256 bytes is four under MAX_PATH, so the
// 1000-occurrence tier is a real long-path measurement rather than a short-path one in
// disguise. Every fixture root lives under `/synthetic/` or `C:\synthetic\`, every
// benchmark name is a fixed label, and no benchmark prints a path, an alias, a workspace
// tag, or a document.
//
// Fixtures are built before the timed region and nothing inside a benchmark body
// touches the filesystem, the clock, or an RNG. No benchmark asserts on elapsed
// time: the stable guard against a reintroduced super-linear cost is in
// occurrence_scaling_test.go and is expressed in allocations.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/schemainfer"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// benchPOSIXWorktreeRoot is the long synthetic POSIX monorepo worktree root: 151 bytes,
// deep enough that the occurrence paths under it are 187 bytes each.
const benchPOSIXWorktreeRoot = "/synthetic/build-agent/workspaces/aiproxer-example-project/monorepo" +
	"/services/interactive-proxy/.worktrees/b-leg-path-virtualization-deep-posix-worktree"

// benchWindowsWorktreeRoot is the long synthetic Windows drive worktree root: 220 bytes,
// so the occurrence paths under it are 256 bytes and MAX_PATH pressure is real.
const benchWindowsWorktreeRoot = `C:\synthetic\build-agent\source\repos\aiproxer-example-project` +
	`\.worktrees\b-leg-path-virtualization-windows-long-worktree\connector-support\backends` +
	`\interactive-proxy-provider\internal\plugins\features\pathvirtualization`

// benchResultToolName is the operator-layer tool name the path-oriented result
// fixtures claim. It is a name no shipped built-in claims, so the only thing these
// fixtures can exercise is the result surface (requirement 3.8).
const benchResultToolName = "synthetic_list_paths"

// benchUnclaimedToolName is a tool name NO profile layer claims, so the optional
// schema-inference step is the layer that answers for it.
const benchUnclaimedToolName = "synthetic_unclaimed_reader"

// benchOccurrenceTiers are the occurrence counts design.md asks for. They span two
// decades so a per-occurrence cost that stops being flat is visible in the numbers
// rather than only in a total.
var benchOccurrenceTiers = []int{10, 100, 1000}

// benchRootFixture pairs one long root with its separator, which is the only
// flavor-dependent byte the fixture builders need.
type benchRootFixture struct {
	name string
	root string
	sep  string
}

// benchRootFixtures is the requirement 9.6 pair: one long POSIX monorepo worktree
// root and one long Windows worktree root at MAX_PATH pressure.
var benchRootFixtures = []benchRootFixture{
	{"posix_deep_monorepo_worktree", benchPOSIXWorktreeRoot, "/"},
	{"windows_drive_long_worktree", benchWindowsWorktreeRoot, `\`},
}

// benchDeclaredSchema is a synthetic declared argument schema whose only path-key
// member is `file_path`, so an inferred resolution publishes exactly one selector.
const benchDeclaredSchema = `{"type":"object","properties":{"file_path":{"type":"string"},` +
	`"limit":{"type":"integer"},"content":{"type":"string"}},"required":["file_path"]}`

// benchmark sinks; each body assigns to exactly one so the measured call cannot be
// optimized away.
var (
	benchSinkBytes []byte
	benchSinkPass  rewrite.DocumentPass
	benchSinkErr   error
	benchSinkCall  *aipapi.Call
	benchSinkStats rewrite.Stats
)

// benchOccurrencePath is the i-th real path under one fixture root. Every occurrence
// is distinct so no per-occurrence cost can come from a repeated-value fast path.
func benchOccurrencePath(fixture benchRootFixture, index int) string {
	return fixture.root + fixture.sep + "src" + fixture.sep + "synthetic_service" +
		fixture.sep + "file_" + fmt.Sprintf("%05d", index) + ".go"
}

// benchWriteJSONString writes one JSON string literal. Fixtures are built outside the
// timed region, so the allocation this costs is not measured; correctness is.
func benchWriteJSONString(buf *bytes.Buffer, value string) {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("synthetic fixture path is not encodable: " + err.Error())
	}
	buf.Write(encoded)
}

// benchArgumentDocument builds one completed-argument document holding exactly
// occurrences selected path occurrences: one single-string location plus
// occurrences-1 array-of-string locations, with an unselected prose field beside them.
//
// Both selected shapes are present because they are both reachable: a single
// location and a homogeneous list are separate pointer targets with separate leaf
// accounting, and measuring only one of them would leave the other unmeasured.
func benchArgumentDocument(fixture benchRootFixture, occurrences int,
) []byte {
	var buf bytes.Buffer
	buf.WriteString(`{"file_path":`)
	benchWriteJSONString(&buf, benchOccurrencePath(fixture, 0))
	buf.WriteString(`,"paths":[`)
	for i := 1; i < occurrences; i++ {
		if i > 1 {
			buf.WriteByte(',')
		}
		benchWriteJSONString(&buf, benchOccurrencePath(fixture, i))
	}
	buf.WriteString(`],"content":"synthetic prose that no selector may reach"}`)
	return buf.Bytes()
}

// benchArgPointerSet compiles the operator pointer set the argument fixtures select:
// one single-string member and one array-of-string member.
func benchArgPointerSet(tb testing.TB) pathvirtualization.SelectorSet {
	tb.Helper()
	compiled, reject := pathvirtualization.CompileProfiles([]pathvirtualization.ProfileInput{{
		Names:       []string{benchUnclaimedToolName},
		ArgPointers: []string{"/file_path", "/paths"},
	}})
	if reject != pathvirtualization.SelectorRejectNone {
		tb.Fatalf("compile argument profile: reject %v", reject)
	}
	return compiled[0].ArgPointers
}

// benchVirtualizeDecider is the outbound decider over one mapping: it is exactly what
// the rewriter's own unexported decider does, expressed here so the isolated payload
// benchmark measures the mapping decision without the call walk around it.
func benchVirtualizeDecider(mapping pathvirtualization.Mapping) rewrite.ValueDecider {
	return func(value string) rewrite.ValueDecision {
		virtualized, matched := mapping.VirtualizePath(value)
		return rewrite.ValueDecision{Replacement: virtualized, Eligible: matched}
	}
}

// benchOperatorRewriter binds one fixture root to an operator profile claiming the
// fixture tool name with the given selectors and opaque mode.
func benchOperatorRewriter(tb testing.TB, root string, profile pathvirtualization.ToolProfile) *rewrite.Rewriter {
	tb.Helper()
	mapping := benchActiveMapping(tb, root)
	compiled, reject := pathvirtualization.CompileToolProfiles([]pathvirtualization.ToolProfile{profile})
	if reject != pathvirtualization.SelectorRejectNone {
		tb.Fatalf("compile operator profile: reject %v", reject)
	}
	resolver, reject := pathvirtualization.NewResolver(compiled, nil, nil)
	if reject != pathvirtualization.SelectorRejectNone {
		tb.Fatalf("new resolver: reject %v", reject)
	}
	return rewrite.New(mapping, resolver)
}

// benchActiveMapping derives one fixture mapping and fails unless the fixture really
// is an ACTIVE mapping. The check runs while the fixture is built, never inside a
// timed region.
func benchActiveMapping(tb testing.TB, root string) pathvirtualization.Mapping {
	tb.Helper()
	mapping, reason := pathvirtualization.DeriveMapping(root)
	if reason != pathvirtualization.SkipReasonNone {
		tb.Fatalf("derive fixture mapping: reason %v", reason)
	}
	if mapping.VirtualRoot == "" {
		tb.Fatal("the fixture root must derive an active alias (requirement 1.4)")
	}
	return mapping
}

// benchBuiltinRewriter binds the fixture mapping to the SHIPPED built-in profile
// layer, which is the policy a deployment gets with no operator configuration.
func benchBuiltinRewriter(tb testing.TB, root string) *rewrite.Rewriter {
	tb.Helper()
	mapping := benchActiveMapping(tb, root)
	builtin, reject := pathvirtualization.CompileToolProfiles(pathvirtualization.BuiltinToolProfiles())
	if reject != pathvirtualization.SelectorRejectNone {
		tb.Fatalf("compile built-in profiles: reject %v", reject)
	}
	resolver, reject := pathvirtualization.NewResolver(nil, builtin, nil)
	if reject != pathvirtualization.SelectorRejectNone {
		tb.Fatalf("new resolver: reject %v", reject)
	}
	return rewrite.New(mapping, resolver)
}

// benchInference adapts the real schema-inference step to the lexical core's optional
// inference port, so the inference-fallback benchmark measures the shipped step rather
// than a stub.
type benchInference struct{ inferrer *schemainfer.Inferrer }

// InferArgumentSelectors implements pathvirtualization.ArgumentInference.
func (i benchInference) InferArgumentSelectors(declaredSchema []byte) pathvirtualization.SelectorSet {
	return i.inferrer.InferArguments(aipapi.ToolDef{Parameters: declaredSchema}).Pointers
}

// benchInferenceRewriter binds the fixture mapping to a resolver that has NO profile
// layer at all, so every resolution of the unclaimed tool name is answered by the
// schema-inference step from the tool's declared schema bytes.
func benchInferenceRewriter(tb testing.TB, root string) *rewrite.Rewriter {
	tb.Helper()
	mapping, reason := pathvirtualization.DeriveMapping(root)
	if reason != pathvirtualization.SkipReasonNone {
		tb.Fatalf("derive fixture mapping: reason %v", reason)
	}
	if mapping.VirtualRoot == "" {
		tb.Fatal("the fixture root must derive an active alias (requirement 1.4)")
	}
	inferrer, reject := schemainfer.New(schemainfer.DefaultPathKeys())
	if reject != pathvirtualization.SelectorRejectNone {
		tb.Fatalf("compile inference vocabulary: reject %v", reject)
	}
	resolver, reject := pathvirtualization.NewResolver(nil, nil, benchInference{inferrer})
	if reject != pathvirtualization.SelectorRejectNone {
		tb.Fatalf("new resolver: reject %v", reject)
	}
	return rewrite.New(mapping, resolver)
}

// benchRequestCall builds one canonical item-authoritative request carrying
// occurrences tool calls, each with exactly one selected path occurrence.
//
// The request is validated while it is built, so a fixture the canonical model would
// reject fails the benchmark before the timed region rather than measuring a shape no
// deployment sends.
func benchRequestCall(
	tb testing.TB,
	fixture benchRootFixture,
	occurrences int,
	toolName string,
	tools []aipapi.ToolDef,
) *aipapi.Call {
	tb.Helper()
	items := make([]aipapi.Item, 0, occurrences)
	for i := range occurrences {
		var buf bytes.Buffer
		buf.WriteString(`{"file_path":`)
		benchWriteJSONString(&buf, benchOccurrencePath(fixture, i))
		buf.WriteString(`,"content":"synthetic prose that no selector may reach"}`)
		items = append(items, aipapi.Item{
			Kind:   aipapi.ItemKindToolCall,
			ID:     fmt.Sprintf("item_call_%05d", i),
			Status: aipapi.ItemStatusCompleted,
			ToolCall: &aipapi.ToolCallItem{
				CallID:    fmt.Sprintf("call_%05d", i),
				Name:      toolName,
				Arguments: json.RawMessage(buf.Bytes()),
			},
		})
	}
	call := &aipapi.Call{Items: items, Tools: tools}
	if err := call.Validate(); err != nil {
		tb.Fatalf("the %s request fixture must be canonical: %v", fixture.name, err)
	}
	return call
}

// BenchmarkSelectorGuidedArgumentMutation is requirement 9.4's selector-guided
// argument benchmark, isolated from the call walk: one payload, one compiled pointer
// set, the mapping's own decision per leaf, and the byte splice that publishes them.
//
// It uses the exported payload primitive the inbound expansion pass shares with the
// outbound direction, so both directions are measured on one engine rather than on
// two copies that could drift. The per-occurrence metric is the realized byte saving
// the pass reports, which is a deterministic count rather than a timing.
func BenchmarkSelectorGuidedArgumentMutation(b *testing.B) {
	for _, fixture := range benchRootFixtures {
		for _, occurrences := range benchOccurrenceTiers {
			b.Run(fmt.Sprintf("%s/occurrences_%d", fixture.name, occurrences), func(b *testing.B) {
				mapping := benchActiveMapping(b, fixture.root)
				pointers := benchArgPointerSet(b)
				decide := benchVirtualizeDecider(mapping)
				raw := benchArgumentDocument(fixture, occurrences)

				_, pass, err := rewrite.ApplySelectedValues(raw, pointers, decide)
				if err != nil {
					b.Fatalf("fixture payload: %v", err)
				}
				if pass.Eligible != occurrences || !pass.Changed {
					b.Fatalf("fixture selected %d eligible leaves (changed=%v), want %d",
						pass.Eligible, pass.Changed, occurrences)
				}
				verified := pass.Stats()

				b.ReportAllocs()
				for b.Loop() {
					benchSinkBytes, benchSinkPass, benchSinkErr = rewrite.ApplySelectedValues(raw, pointers, decide)
				}
				benchReportOccurrenceMetrics(b, verified, occurrences)
			})
		}
	}
}

// BenchmarkOutboundRequestRewriteOccurrences is design.md's request fixture tier: the
// whole outbound pass over a canonical request that grows from 10 to 1000 path
// occurrences, which is the growth shape requirement 9's objective is about.
//
// The two sub-kinds are the two resolutions a deployment can actually end up with, and
// they are measured separately on purpose:
//
//   - builtin_profile answers from the shipped built-in layer, so the declared schema
//     is never read and the pass cost is the payload work alone;
//   - inference_fallback answers from the schema-inference step, because no layer
//     claims the tool name. That path re-reads the declared schema once per
//     occurrence, so its cost grows in occurrences times schema size rather than in
//     occurrences alone. It is measured here rather than hidden: the numbers are the
//     evidence for whether that amplification is acceptable, and it is reported as a
//     finding in the task summary instead of being tuned away in a test-only change.
func BenchmarkOutboundRequestRewriteOccurrences(b *testing.B) {
	for _, fixture := range benchRootFixtures {
		for _, occurrences := range benchOccurrenceTiers {
			b.Run(fmt.Sprintf("%s/occurrences_%d/builtin_profile", fixture.name, occurrences),
				func(b *testing.B) {
					rewriter := benchBuiltinRewriter(b, fixture.root)
					tools := []aipapi.ToolDef{{
						Name:       "read_file",
						Parameters: json.RawMessage(benchDeclaredSchema),
					}}
					call := benchRequestCall(b, fixture, occurrences, "read_file", tools)

					_, stats, err := rewriter.RewriteCall(call)
					if err != nil {
						b.Fatalf("fixture request: %v", err)
					}
					if stats.Rewritten != occurrences {
						b.Fatalf("fixture rewrote %d occurrences, want %d", stats.Rewritten, occurrences)
					}
					verified := stats

					b.ReportAllocs()
					for b.Loop() {
						benchSinkCall, benchSinkStats, benchSinkErr = rewriter.RewriteCall(call)
					}
					benchReportOccurrenceMetrics(b, verified, occurrences)
				})
		}
	}
	for _, occurrences := range benchOccurrenceTiers {
		b.Run(fmt.Sprintf("%s/occurrences_%d/inference_fallback",
			benchRootFixtures[0].name, occurrences), func(b *testing.B) {
			fixture := benchRootFixtures[0]
			rewriter := benchInferenceRewriter(b, fixture.root)
			tools := []aipapi.ToolDef{{
				Name:       benchUnclaimedToolName,
				Parameters: json.RawMessage(benchDeclaredSchema),
			}}
			call := benchRequestCall(b, fixture, occurrences, benchUnclaimedToolName, tools)

			_, stats, err := rewriter.RewriteCall(call)
			if err != nil {
				b.Fatalf("fixture request: %v", err)
			}
			if stats.Rewritten != occurrences {
				b.Fatalf("fixture rewrote %d occurrences, want %d", stats.Rewritten, occurrences)
			}
			verified := stats

			b.ReportAllocs()
			for b.Loop() {
				benchSinkCall, benchSinkStats, benchSinkErr = rewriter.RewriteCall(call)
			}
			benchReportOccurrenceMetrics(b, verified, occurrences)
		})
	}
}

// BenchmarkIdempotentSecondOutboundPass is design.md's "Idempotent second outbound
// pass", measured as a PAIR so the comparison is self-contained: both passes run over
// the same fixture under the same policy, so the two rows describe reapplication alone
// and not two different resolver configurations.
//
// second_pass must be measurably cheaper than first_pass AND must change no byte.
// Cheapness is what the two rows show; unchangedness is proven structurally before the
// timed region, because a pass that returned the input pointer can only be one that
// published nothing at all. The second pass is cheaper for a concrete reason: it finds
// no real-root prefix, so it never reaches the deep clone the first pass pays to
// publish a change. It is NOT free, because it still parses every payload whole to
// prove no selected leaf carries a real root.
func BenchmarkIdempotentSecondOutboundPass(b *testing.B) {
	for _, fixture := range benchRootFixtures {
		for _, occurrences := range benchOccurrenceTiers {
			profile := pathvirtualization.ToolProfile{
				Names:       []string{benchUnclaimedToolName},
				ArgPointers: []string{"/file_path", "/paths"},
			}
			b.Run(fmt.Sprintf("%s/occurrences_%d/first_pass", fixture.name, occurrences),
				func(b *testing.B) {
					rewriter := benchOperatorRewriter(b, fixture.root, profile)
					call := benchRequestCall(b, fixture, occurrences, benchUnclaimedToolName, nil)
					_, stats, err := rewriter.RewriteCall(call)
					if err != nil {
						b.Fatalf("first pass: %v", err)
					}
					if stats.Rewritten != occurrences {
						b.Fatalf("first pass rewrote %d occurrences, want %d",
							stats.Rewritten, occurrences)
					}

					b.ReportAllocs()
					for b.Loop() {
						benchSinkCall, benchSinkStats, benchSinkErr = rewriter.RewriteCall(call)
					}
					benchReportOccurrenceMetrics(b, stats, occurrences)
				})

			b.Run(fmt.Sprintf("%s/occurrences_%d/second_pass", fixture.name, occurrences),
				func(b *testing.B) {
					rewriter := benchOperatorRewriter(b, fixture.root, profile)
					first := benchRequestCall(b, fixture, occurrences, benchUnclaimedToolName, nil)
					published, firstStats, err := rewriter.RewriteCall(first)
					if err != nil {
						b.Fatalf("first pass: %v", err)
					}
					if firstStats.Rewritten != occurrences {
						b.Fatalf("first pass rewrote %d occurrences, want %d",
							firstStats.Rewritten, occurrences)
					}
					second, secondStats, err := rewriter.RewriteCall(published)
					if err != nil {
						b.Fatalf("second pass: %v", err)
					}
					if second != published {
						b.Fatal("the second pass published a new call: an already-virtualized " +
							"request must come back as the very same call")
					}
					if secondStats.Rewritten != 0 || secondStats.BytesSaved() != 0 {
						b.Fatalf("the second pass reported %d rewritten occurrences and %d saved bytes, want 0 and 0",
							secondStats.Rewritten, secondStats.BytesSaved())
					}

					b.ReportAllocs()
					for b.Loop() {
						benchSinkCall, benchSinkStats, benchSinkErr = rewriter.RewriteCall(published)
					}
					// An idempotent pass realizes no saving at all, so only the
					// occurrence count is reported: it is what makes this row's
					// per-occurrence cost comparable with the first-pass row.
					b.ReportMetric(float64(occurrences), "occurrences")
				})
		}
	}
}

// BenchmarkPathOrientedResultRewrite is requirement 9.4's "representative
// path-oriented result rewriting", measured over both result surfaces an operator
// profile can enable.
//
// opaque_path_lines is the bounded line recognizer over a listing that holds one
// location per line plus one prose line the recognizer must refuse, so the refusal
// branch is inside the measurement rather than beside it.
// structured_result_pointers is the explicit result-pointer surface over a structured
// JSON part, where the payload also carries an unselected member that must survive.
//
// The listing is built with no trailing newline so the final line is a real line the
// recognizer has to accept rather than an artifact of the terminator.
func BenchmarkPathOrientedResultRewrite(b *testing.B) {
	fixture := benchRootFixtures[0]
	for _, occurrences := range benchOccurrenceTiers {
		b.Run(fmt.Sprintf("%s/occurrences_%d/opaque_path_lines", fixture.name, occurrences),
			func(b *testing.B) {
				rewriter := benchOperatorRewriter(b, fixture.root, pathvirtualization.ToolProfile{
					Names:            []string{benchResultToolName},
					OpaqueResultMode: pathvirtualization.OpaqueResultModePathLines,
				})
				call := benchOpaqueResultCall(b, fixture, occurrences)

				_, stats, err := rewriter.RewriteCall(call)
				if err != nil {
					b.Fatalf("fixture call: %v", err)
				}
				if stats.Rewritten != occurrences {
					b.Fatalf("fixture rewrote %d result occurrences, want %d", stats.Rewritten, occurrences)
				}
				verified := stats

				b.ReportAllocs()
				for b.Loop() {
					benchSinkCall, benchSinkStats, benchSinkErr = rewriter.RewriteCall(call)
				}
				benchReportOccurrenceMetrics(b, verified, occurrences)
			})

		b.Run(fmt.Sprintf("%s/occurrences_%d/structured_result_pointers", fixture.name, occurrences),
			func(b *testing.B) {
				rewriter := benchOperatorRewriter(b, fixture.root, pathvirtualization.ToolProfile{
					Names:              []string{benchResultToolName},
					ResultJSONPointers: []string{"/written"},
				})
				call := benchStructuredResultCall(b, fixture, occurrences)

				_, stats, err := rewriter.RewriteCall(call)
				if err != nil {
					b.Fatalf("fixture call: %v", err)
				}
				if stats.Rewritten != occurrences {
					b.Fatalf("fixture rewrote %d result occurrences, want %d", stats.Rewritten, occurrences)
				}
				verified := stats

				b.ReportAllocs()
				for b.Loop() {
					benchSinkCall, benchSinkStats, benchSinkErr = rewriter.RewriteCall(call)
				}
				benchReportOccurrenceMetrics(b, verified, occurrences)
			})
	}
}

// benchOpaqueListing builds a path listing whose lines hold nothing but one location,
// preceded by a prose line the line recognizer must refuse.
func benchOpaqueListing(fixture benchRootFixture, occurrences int,
) string {
	var listing strings.Builder
	listing.WriteString("synthetic listing header: total ")
	listing.WriteString(strconv.Itoa(occurrences))
	listing.WriteByte('\n')
	for i := range occurrences {
		if i > 0 {
			listing.WriteByte('\n')
		}
		listing.WriteString(benchOccurrencePath(fixture, i))
	}
	return listing.String()
}

// benchOpaqueResultCall builds one canonical request whose tool result carries the
// opaque listing as its Output. Canonical validation refuses a result that carries
// Output and Parts at once, so this surface is its own call.
func benchOpaqueResultCall(
	tb testing.TB,
	fixture benchRootFixture,
	occurrences int,
) *aipapi.Call {
	tb.Helper()
	call := &aipapi.Call{Items: []aipapi.Item{
		benchResultToolCallItem(tb, fixture),
		{
			Kind:   aipapi.ItemKindToolResult,
			ID:     "item_result",
			Status: aipapi.ItemStatusCompleted,
			ToolResult: &aipapi.ToolResultItem{
				CallID: "call_result",
				Name:   benchResultToolName,
				Output: benchOpaqueListing(fixture, occurrences),
			},
		},
	}}
	if err := call.Validate(); err != nil {
		tb.Fatalf("the opaque result fixture must be canonical: %v", err)
	}
	return call
}

// benchStructuredResultCall builds one canonical request whose tool result carries a
// structured JSON content part with a selected array member and an unselected
// sibling beside it.
func benchStructuredResultCall(
	tb testing.TB,
	fixture benchRootFixture,
	occurrences int,
) *aipapi.Call {
	tb.Helper()
	var buf bytes.Buffer
	buf.WriteString(`{"written":[`)
	for i := range occurrences {
		if i > 0 {
			buf.WriteByte(',')
		}
		benchWriteJSONString(&buf, benchOccurrencePath(fixture, i))
	}
	buf.WriteString(`],"summary":"synthetic prose that no selector may reach"}`)

	call := &aipapi.Call{Items: []aipapi.Item{
		benchResultToolCallItem(tb, fixture),
		{
			Kind:   aipapi.ItemKindToolResult,
			ID:     "item_result",
			Status: aipapi.ItemStatusCompleted,
			ToolResult: &aipapi.ToolResultItem{
				CallID: "call_result",
				Name:   benchResultToolName,
				Parts: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartJSON, Text: buf.String()},
				},
			},
		},
	}}
	if err := call.Validate(); err != nil {
		tb.Fatalf("the structured result fixture must be canonical: %v", err)
	}
	return call
}

// benchResultToolCallItem is the tool call every result fixture must carry, since
// canonical validation refuses an orphan tool result.
//
// Its single argument names no selector the result profiles declare, so the argument
// surface records `no_selectors` and the reported counts stay attributable to the result
// surface alone. The path is built from the same fixture as the result it accompanies,
// so a fixture cannot mix two roots by accident.
func benchResultToolCallItem(tb testing.TB, fixture benchRootFixture) aipapi.Item {
	tb.Helper()
	var buf bytes.Buffer
	buf.WriteString(`{"file_path":`)
	benchWriteJSONString(&buf, benchOccurrencePath(fixture, 0))
	buf.WriteString(`}`)
	return aipapi.Item{
		Kind:   aipapi.ItemKindToolCall,
		ID:     "item_call",
		Status: aipapi.ItemStatusCompleted,
		ToolCall: &aipapi.ToolCallItem{
			CallID:    "call_result",
			Name:      benchResultToolName,
			Arguments: json.RawMessage(buf.Bytes()),
		},
	}
}

// benchSavingsPerOccurrence converts one content-free statistics value into the
// per-occurrence byte saving design.md "Observability" asks an operator to be able to
// calculate. It is a deterministic count, never a timing.
func benchSavingsPerOccurrence(stats rewrite.Stats, occurrences int) float64 {
	if occurrences == 0 {
		return 0
	}
	return float64(stats.BytesSaved()) / float64(occurrences)
}

// benchReportOccurrenceMetrics reports the two deterministic numbers every
// occurrence-tiered benchmark in this file carries: how many occurrences one measured
// operation covered, and how many bytes that pass realized per occurrence.
//
// Both are COUNTS, so they are identical on every host and comparable across runs
// without being a threshold anything can fail against. They are reported AFTER the
// timed loop on purpose: entering the loop resets the extra metrics the testing
// package prints, so a metric set before the loop is silently dropped. The unit
// spelling carries no slash for the same reason the tier does not: the testing package
// parses the printed line on slashes and discards a metric whose unit contains one.
func benchReportOccurrenceMetrics(b *testing.B, verified rewrite.Stats, occurrences int) {
	b.Helper()
	b.ReportMetric(float64(occurrences), "occurrences")
	b.ReportMetric(benchSavingsPerOccurrence(verified, occurrences), "B-per-occurrence")
}
