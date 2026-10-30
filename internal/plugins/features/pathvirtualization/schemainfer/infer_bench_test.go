package schemainfer_test

// Spec: b-leg-path-virtualization Task 11.1, requirements.md 9.2, 9.3, 9.4 and 9.6,
// against design.md "Testing Strategy / Benchmarks" and the "Performance" section's
// "Selector traversal is bounded by config/schema/canonical limits".
//
// This file measures the schema-inference read, because that read is the one place in
// this feature whose cost is a function of ATTACKER- or PROVIDER-SUPPLIED declared
// bytes rather than of the workspace's own paths. An earlier round of this feature
// found a quadratic parse cost here: a 185 KB declared chain spent 14.7 s because
// every level re-entered the decoder on its own full subtree. The reader now reads a
// bounded window of declared levels, so its cost is a constant multiple of the
// DECLARED SIZE rather than of nesting depth.
//
// These two benchmarks are the standing evidence for that fix, and they probe it from
// the two directions that matter:
//
//   - BenchmarkInferArgumentsWideDeclaredSchema grows the declared WIDTH at one level,
//     so a reader that re-scanned per node would show up as a rising per-byte cost;
//   - BenchmarkInferArgumentsDeepDeclaredChain grows the declared DEPTH far past the
//     read window, so a reader that read below the window would show up as a cost that
//     stops tracking the declared size at all.
//
// Neither benchmark asserts on elapsed time. Both report the declared size as a metric
// so the per-byte cost is derivable, and both are verified before the timed region to
// have taken the outcome they are meant to measure.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/schemainfer"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// benchWidthTiers are the declared member counts. They span the two decades the
// occurrence tiers use elsewhere in this feature, and the largest one stays under the
// inference step's own node budget (1024 declared objects) so the fixture is refused
// for its size rather than measured for it.
var benchWidthTiers = []int{10, 100, 1000}

// benchDepthTiers are declared nesting levels, all far beyond the reader's own window
// of declared levels, so what each tier adds is bytes the reader must NOT read.
var benchDepthTiers = []int{40, 80, 160}

// benchmark sinks; the measured call cannot be optimized away when its result is dropped.
var (
	benchSinkTool   aipapi.ToolDef
	benchSinkResult schemainfer.Result
)

// benchInferrer is the shipped inference step over the shipped vocabulary, built once
// per benchmark rather than per iteration: the vocabulary is immutable, and rebuilding
// it inside the loop would measure compilation instead of the read.
func benchInferrer(tb testing.TB) *schemainfer.Inferrer {
	tb.Helper()
	inferrer, reject := schemainfer.New(schemainfer.DefaultPathKeys())
	if reject != pathvirtualization.SelectorRejectNone {
		tb.Fatalf("New(path keys) reject = %v, want none", reject)
	}
	return inferrer
}

// benchWideSchema declares one level holding `properties` distinct members.
//
// The first members carry the shipped vocabulary names, so the walk really does publish
// selectors and really does sort them; every later member is inert, so the read has to
// materialize it without producing anything. Member names are DISTINCT because a
// declaration that repeats one is refused as ambiguous, which would turn the fixture
// into a different measurement.
func benchWideSchema(properties int) []byte {
	keys := schemainfer.DefaultPathKeys()
	var schema strings.Builder
	schema.WriteString(`{"type":"object","properties":{`)
	for i := range properties {
		if i > 0 {
			schema.WriteByte(',')
		}
		if i < len(keys) {
			fmt.Fprintf(&schema, "%q:{\"type\":\"string\"}", keys[i])
			continue
		}
		fmt.Fprintf(&schema, `"member_%05d":{"type":"string"}`, i)
	}
	schema.WriteString(`}}`)
	return []byte(schema.String())
}

// benchDeepSchema declares a chain `levels` container levels below a member no
// accepted pointer can reach, plus one reachable member at the top level.
//
// Every chain member is named `nested`, and every level below the read window is bytes
// the reader is supposed to skip. The reachable member keeps the outcome `inferred`, so
// a refusal cannot make a cheap tier look fast.
func benchDeepSchema(levels int) []byte {
	nested := `{"type":"string"}`
	for range levels {
		nested = `{"type":"object","properties":{"nested":` + nested + `}}`
	}
	return []byte(`{"type":"object","properties":{"file_path":{"type":"string"},"nested":` + nested + `}}`)
}

// benchLevelPrefix and benchLevelSuffix are one declared container level of that chain.
const (
	benchLevelPrefix = `{"type":"object","properties":{"nested":`
	benchLevelSuffix = `}}`
)

// benchDeepSchemaAtLeast declares the largest chain a tool definition can legally carry
// up to the declared size the schema is capped at.
//
// The size is expressed in BYTES rather than levels so the row is comparable with the
// contract bound rather than with the depth tiers. One level's bytes are emitted in
// order rather than by repeated concatenation, so building the largest fixture costs
// linear time instead of quadratic time; the construction is outside every timed
// region regardless, but a fixture builder that is itself quadratic would make the
// benchmark look slow for the wrong reason.
func benchDeepSchemaAtLeast(minBytes int) []byte {
	perLevel := len(benchLevelPrefix) + len(benchLevelSuffix)
	levels := (minBytes + perLevel - 1) / perLevel
	var schema strings.Builder
	schema.Grow(minBytes + 2*len(benchLevelPrefix))
	schema.WriteString(`{"type":"object","properties":{"file_path":{"type":"string"},"nested":`)
	for range levels {
		schema.WriteString(benchLevelPrefix)
	}
	schema.WriteString(`{"type":"string"}`)
	for range levels {
		schema.WriteString(benchLevelSuffix)
	}
	schema.WriteString(`}}`)
	return []byte(schema.String())
}

// benchVerifyInference runs one inference call before the timed region and fails the
// benchmark unless the outcome is the one the tier is meant to measure.
func benchVerifyInference(
	tb testing.TB,
	label string,
	inferrer *schemainfer.Inferrer,
	tool aipapi.ToolDef,
	wantPointers int,
) schemainfer.Result {
	tb.Helper()
	result := inferrer.InferArguments(tool)
	if result.Outcome != schemainfer.OutcomeInferred {
		tb.Fatalf("%s: outcome = %v, want inferred", label, result.Outcome)
	}
	if len(result.Pointers) != wantPointers {
		tb.Fatalf("%s: published %d selectors, want %d", label, len(result.Pointers), wantPointers)
	}
	return result
}

// BenchmarkInferArgumentsWideDeclaredSchema measures the inference read over a growing
// declared WIDTH, which is the shape a real provider schema has: many members at one
// level rather than deep nesting.
//
// Per-byte cost is the number to read off this row. If the read were quadratic in
// members, the per-byte cost would climb across the tiers; a flat per-byte cost is what
// "a constant multiple of the declared size" means in practice.
func BenchmarkInferArgumentsWideDeclaredSchema(b *testing.B) {
	inferrer := benchInferrer(b)
	keys := schemainfer.DefaultPathKeys()
	for _, properties := range benchWidthTiers {
		b.Run(fmt.Sprintf("members_%d", properties), func(b *testing.B) {
			schema := benchWideSchema(properties)
			tool := aipapi.ToolDef{Name: "synthetic_unclaimed_reader", Parameters: schema}
			wantPointers := min(properties, len(keys))
			benchVerifyInference(b, "wide fixture", inferrer, tool, wantPointers)

			b.ReportAllocs()
			for b.Loop() {
				benchSinkResult = inferrer.InferArguments(tool)
			}
			b.ReportMetric(float64(properties), "declared-members")
			b.ReportMetric(float64(len(schema)), "declared-B")
		})
	}
}

// BenchmarkInferArgumentsDeepDeclaredChain measures the inference read over a growing
// declared DEPTH that is already far outside the reader's window.
//
// The interesting property is that the cost BARELY MOVES: each tier adds declared bytes
// the reader must not read, so a reader that re-scanned per level would make these
// numbers climb super-linearly while a windowed reader keeps them nearly flat. This is
// the standing regression probe for the quadratic parse cost this feature once had.
func BenchmarkInferArgumentsDeepDeclaredChain(b *testing.B) {
	inferrer := benchInferrer(b)
	for _, levels := range benchDepthTiers {
		b.Run(fmt.Sprintf("levels_%d", levels), func(b *testing.B) {
			schema := benchDeepSchema(levels)
			tool := aipapi.ToolDef{Name: "synthetic_unclaimed_reader", Parameters: schema}
			benchVerifyInference(b, "deep fixture", inferrer, tool, 1)

			b.ReportAllocs()
			for b.Loop() {
				benchSinkTool = tool
				benchSinkResult = inferrer.InferArguments(benchSinkTool)
			}
			b.ReportMetric(float64(levels), "declared-levels")
			b.ReportMetric(float64(len(schema)), "declared-B")
		})
	}
}

// benchLargestInContractDeclaredBytes is the declared size the shipped schema bound
// allows, rounded to 71% of it.
//
// The fraction is not decoration: it is the exact shape of the input that exposed the
// quadratic parse cost this feature once had, so this row is directly comparable with
// the measurement recorded when that cost was fixed. Its purpose is to keep the cost of
// the LARGEST legal declaration visible, because a reader that is linear in declared
// bytes is still linear in bytes a caller controls.
func BenchmarkInferArgumentsLargestInContractDeclaration(b *testing.B) {
	inferrer := benchInferrer(b)
	schema := benchDeepSchemaAtLeast(aipapi.MaxToolParametersBytes * 71 / 100)
	tool := aipapi.ToolDef{Name: "synthetic_unclaimed_reader", Parameters: schema}
	benchVerifyInference(b, "largest in-contract fixture", inferrer, tool, 1)

	b.ReportAllocs()
	for b.Loop() {
		benchSinkResult = inferrer.InferArguments(tool)
	}
	b.ReportMetric(float64(len(schema)), "declared-B")
}
