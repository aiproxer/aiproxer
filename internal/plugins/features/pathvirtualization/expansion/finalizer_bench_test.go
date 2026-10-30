package expansion_test

// Spec: b-leg-path-virtualization Task 11.1, requirements.md 9.2, 9.3, 9.4 and 9.6,
// against design.md "Testing Strategy / Benchmarks" (completed-call expansion) and
// "Observability".
//
// This file measures the INBOUND half: the completed-tool-call expansion finalizer of
// design.md section 7, over the 10/100/1000 occurrence fixtures, on both long
// requirement 9.6 roots.
//
// The measurement is of the whole pass, not of one step inside it, because the whole
// pass is what a deployment pays: the per-call mapping derivation (SHA-256 workspace
// tag included), the exact-name selector resolution, the shared byte splice, and the
// published-document validation of design.md section 7 step 9. A step-wise benchmark
// would let the most expensive step be measured in isolation and would never show that
// the inbound direction re-derives the mapping on every completed call.
//
// Fixtures are synthetic and built before the timed region. The alias spelling is
// derived from the fixture root rather than hard-coded, the roots live under
// `/synthetic/` and `C:\synthetic\`, and no benchmark prints a path, an alias, a
// workspace tag, or a document. No benchmark asserts on elapsed time.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/expansion"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	sdkworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// benchPOSIXWorktreeRoot is the long synthetic POSIX monorepo worktree root: 151 bytes,
// deep enough that the paths under its alias are 187 bytes each.
const benchPOSIXWorktreeRoot = "/synthetic/build-agent/workspaces/aiproxer-example-project/monorepo" +
	"/services/interactive-proxy/.worktrees/b-leg-path-virtualization-deep-posix-worktree"

// benchWindowsWorktreeRoot is the long synthetic Windows drive worktree root: 220 bytes,
// so the paths under its alias are 256 bytes and MAX_PATH pressure is real.
const benchWindowsWorktreeRoot = `C:\synthetic\build-agent\source\repos\aiproxer-example-project` +
	`\.worktrees\b-leg-path-virtualization-windows-long-worktree\connector-support\backends` +
	`\interactive-proxy-provider\internal\plugins\features\pathvirtualization`

// benchExpansionToolName is the operator-layer tool name these fixtures claim. It is a
// name no shipped built-in claims, so the selector that reaches the occurrence is an
// operator decision rather than a built-in one.
const benchExpansionToolName = "synthetic_bulk_reader"

// benchOccurrenceTiers are the occurrence counts design.md asks for.
var benchOccurrenceTiers = []int{10, 100, 1000}

// benchRootFixtures is the requirement 9.6 pair: one long POSIX monorepo worktree root
// and one long Windows worktree root at MAX_PATH pressure.
var benchRootFixtures = []benchRootFixture{
	{"posix_deep_monorepo_worktree", benchPOSIXWorktreeRoot, "/"},
	{"windows_drive_long_worktree", benchWindowsWorktreeRoot, `\`},
}

// benchRootFixture pairs one long root with the separator its suffix is spelled with.
type benchRootFixture struct {
	name string
	root string
	sep  string
}

// benchmark sinks; each body assigns to one of them so the measured call cannot be
// optimized away.
var (
	benchSinkResult toolcall.Result
	benchSinkErr    error
)

// benchOccurrenceAlias is the i-th alias-rooted path under one fixture root's derived
// virtual root, which is the shape the inbound direction receives: the backend sends
// back the alias it was given.
func benchOccurrenceAlias(mapping pathvirtualization.Mapping, sep string, index int) string {
	return mapping.VirtualRoot + "src" + sep + "synthetic_service" +
		sep + "file_" + fmt.Sprintf("%05d", index) + ".go"
}

// benchWriteJSONString writes one JSON string literal. The Windows fixtures carry
// backslashes, which a JSON literal escapes, so the fixture is spelled the way a wire
// payload really spells it rather than as Go source text.
func benchWriteJSONString(buf *bytes.Buffer, value string) {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("synthetic fixture alias is not encodable: " + err.Error())
	}
	buf.Write(encoded)
}

// benchCompletedArgs builds one completed argument document holding exactly occurrences
// selected alias occurrences: one single-string location plus occurrences-1
// array-of-string locations, with an unselected prose field beside them.
//
// The document is built OUTSIDE every timed region. At the 1000-occurrence tier it is
// about 56 KiB on the POSIX fixture, well inside the shipped default mandatory bound of
// 1 MiB, so the largest fixture is a call the assembler would really complete rather
// than one that trips the mandatory-overflow observation.
func benchCompletedArgs(mapping pathvirtualization.Mapping, sep string, occurrences int) []byte {
	var buf bytes.Buffer
	buf.WriteString(`{"file_path":`)
	benchWriteJSONString(&buf, benchOccurrenceAlias(mapping, sep, 0))
	buf.WriteString(`,"paths":[`)
	for i := 1; i < occurrences; i++ {
		if i > 1 {
			buf.WriteByte(',')
		}
		benchWriteJSONString(&buf, benchOccurrenceAlias(mapping, sep, i))
	}
	buf.WriteString(`],"content":"synthetic prose that no selector may reach"}`)
	return buf.Bytes()
}

// benchExpansionResolver builds the shipped exact-name resolver claiming one tool name
// with the two argument selectors the fixtures select.
func benchExpansionResolver(tb testing.TB) *pathvirtualization.Resolver {
	tb.Helper()
	compiled, reject := pathvirtualization.CompileProfiles([]pathvirtualization.ProfileInput{{
		Names:       []string{benchExpansionToolName},
		ArgPointers: []string{"/file_path", "/paths"},
	}})
	if reject != pathvirtualization.SelectorRejectNone {
		tb.Fatalf("compile argument profile: reject %v", reject)
	}
	resolver, reject := pathvirtualization.NewResolver(compiled, nil, nil)
	if reject != pathvirtualization.SelectorRejectNone {
		tb.Fatalf("new resolver: reject %v", reject)
	}
	return resolver
}

// BenchmarkCompletedCallExpansion is requirement 9.4's "completed-tool-call reverse
// expansion" benchmark and design.md's completed-call expansion row.
//
// It measures the finalizer exactly as the assembler invokes it: the same signature,
// the same rewrite mode, the same default mandatory bound, and the authoritative
// per-turn workspace view as the only authority for the project root. A reporter is
// installed so the row can report the very counters an operator's audit mode would
// see; the reporter is a fixed content-free label sink and holds no fixture text.
//
// Each tier's fixture is verified BEFORE the timed region to expand every occurrence,
// so no row can describe a pass that refused its own fixture.
func BenchmarkCompletedCallExpansion(b *testing.B) {
	for _, fixture := range benchRootFixtures {
		for _, occurrences := range benchOccurrenceTiers {
			b.Run(fmt.Sprintf("%s/occurrences_%d", fixture.name, occurrences), func(b *testing.B) {
				mapping, reason := pathvirtualization.DeriveMapping(fixture.root)
				if reason != pathvirtualization.SkipReasonNone {
					b.Fatalf("derive fixture mapping: reason %v", reason)
				}
				if mapping.VirtualRoot == "" {
					b.Fatal("the fixture root must derive an active alias (requirement 1.4)")
				}
				var observed expansion.Report
				finalizer, err := expansion.NewFinalizer(
					benchExpansionResolver(b), rewrite.ModeRewrite, expansion.Policy{},
					expansion.WithReporter(func(report expansion.Report) { observed = report }),
				)
				if err != nil {
					b.Fatalf("NewFinalizer: %v", err)
				}
				args := benchCompletedArgs(mapping, fixture.sep, occurrences)
				call := toolcall.CompletedCall{
					ToolCallID: "call_bench",
					ToolName:   benchExpansionToolName,
					ArgsJSON:   args,
				}
				tool := aipapi.ToolDef{Name: benchExpansionToolName}
				meta := toolcall.Meta{
					Workspace: sdkworkspace.WorkspaceView{ProjectRoot: fixture.root},
				}

				res, err := finalizer.Finalize(context.Background(), call, tool, nil, meta)
				if err != nil {
					b.Fatalf("fixture expansion returned a Go error: %v", err)
				}
				if res.Action != toolcall.ActionRewrite {
					b.Fatalf("fixture expansion action = %v, want rewrite", res.Action)
				}
				if observed.Stats.Eligible != occurrences {
					b.Fatalf("fixture expansion reached %d eligible leaves, want %d",
						observed.Stats.Eligible, occurrences)
				}
				expandedBytes := len(res.ArgsJSON)
				aliasBytes := len(call.ArgsJSON)

				b.ReportAllocs()
				for b.Loop() {
					benchSinkResult, benchSinkErr = finalizer.Finalize(
						context.Background(), call, tool, nil, meta)
				}
				b.ReportMetric(float64(occurrences), "occurrences")
				// Expansion ADDS the bytes the outbound pass removed, so this metric is
				// a cost, not a saving, and it is deliberately labelled differently
				// from the outbound rows so the two can never be read as one number.
				b.ReportMetric(benchExpandedPerOccurrence(
					aliasBytes, expandedBytes, occurrences), "B-per-occurrence-expanded")
			})
		}
	}
}

// benchExpandedPerOccurrence reports how many bytes the INBOUND direction restores per
// occurrence. It is a deterministic byte count, never a timing.
func benchExpandedPerOccurrence(aliasBytes, expandedBytes, occurrences int) float64 {
	if occurrences == 0 {
		return 0
	}
	return float64(expandedBytes-aliasBytes) / float64(occurrences)
}
