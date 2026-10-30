//go:build precommit

package frontendpipe_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Full release certification stays explicit; ordinary tests retain the bounded behavioral regressions.
func TestLargePayloadProof_TransientAllocBounded(t *testing.T) {
	type sizeCase struct {
		name   string
		target int
	}

	type laneCase struct {
		laneID      string
		profile     frontendpipe.FrontendProfile
		urlPath     string
		bodyBuilder func(tb testing.TB, target int) []byte
		sizes       []sizeCase
	}

	fullSizes := []sizeCase{
		{name: "1MiB", target: 1 << 20},
	}
	if !testing.Short() {
		fullSizes = append(
			fullSizes,
			sizeCase{name: "5MiB", target: 5 << 20},
			sizeCase{name: "20MiB", target: 20 << 20},
		)
	}

	lanes := []laneCase{
		{
			laneID:  "openai_responses",
			profile: openairesponses.NewProfile(),
			urlPath: "/v1/responses",
			sizes:   fullSizes,
			bodyBuilder: func(tb testing.TB, target int) []byte {
				tb.Helper()
				if target > (8 << 20) {
					return bench20MiBChunkedBody(tb, target)
				}
				return baselineResponsesBody(tb, target)
			},
		},
		{
			laneID:  "openai_chat",
			profile: openailegacy.NewProfile(),
			urlPath: "/v1/chat/completions",
			sizes:   fullSizes,
			bodyBuilder: func(tb testing.TB, target int) []byte {
				tb.Helper()
				if target > (8 << 20) {
					return bench20MiBChatBody(tb, target)
				}
				return baselineChatBody(tb, target)
			},
		},
		{
			laneID:  "openresponses",
			profile: openresponses.NewProfile(),
			urlPath: "/openresponses/v1/responses",
			sizes:   fullSizes,
			bodyBuilder: func(tb testing.TB, target int) []byte {
				tb.Helper()
				if target > (8 << 20) {
					return bench20MiBOpenResponsesBody(tb, target)
				}
				return baselineOpenResponsesBody(tb, target)
			},
		},
	}

	for _, lane := range lanes {
		t.Run(lane.laneID, func(t *testing.T) {
			for _, sz := range lane.sizes {
				t.Run(sz.name, func(t *testing.T) {
					body := lane.bodyBuilder(t, sz.target)

					spoolDir := t.TempDir()
					spill, err := largebody.NewSpillBuffer(largebody.SpillConfig{
						SpoolDir:         spoolDir,
						MemorySpoolBytes: targetInvariantMemorySpoolBytes,
						CopyBufferSize:   32 << 10,
					})
					require.NoError(t, err, "spill buffer initialization must succeed")

					_, err = spill.Write(body)
					require.NoError(t, err, "spill write must succeed")

					src, err := spill.Complete()
					require.NoError(t, err, "spill complete must succeed")
					t.Cleanup(func() { _ = src.Close() })

					require.True(t, src.HasSpilled(), "expected payload (%d B) to have spilled to disk with memory_spool_bytes=%d", len(body), targetInvariantMemorySpoolBytes)

					proofIn := frontendpipe.ProofInput{
						Ctx:                  context.Background(),
						Headers:              make(http.Header),
						URLPath:              lane.urlPath,
						RouteSelector:        "stub:bench",
						RoutePrefixes:        routeselect.NewPrefixSet([]string{"stub", "openai"}),
						DefaultRouteSelector: "stub:bench",
						RouteFromBodyModel:   true,
						Source:               src,
						BodyBytes:            src.Size(),
					}

					// Measure proof compilation allocations under benchmark harness.
					benchRes := testing.Benchmark(func(b *testing.B) {
						b.Helper()
						b.ReportAllocs()
						b.ResetTimer()
						for b.Loop() {
							out, perr := lane.profile.CompileProof(b.Context(), proofIn)
							if perr != nil {
								b.Fatalf("CompileProof failed: %v", perr)
							}
							baselineSink = out
						}
					})

					allocBytesPerOp := benchRes.AllocedBytesPerOp()
					allocsPerOp := benchRes.AllocsPerOp()

					t.Logf("[%s/%s] proof-time transient: %d B/op, %d allocs/op (target ceiling: %d B/op)",
						lane.laneID, sz.name, allocBytesPerOp, allocsPerOp, maxAllowedProofBytesPerOp)

					// Target Invariant: Proof-time transient allocations must NOT scale with body size,
					// and must remain strictly bounded by memory_spool_bytes + max_semantic_fact_bytes + fixed_buffers.
					assert.LessOrEqual(t, allocBytesPerOp, maxAllowedProofBytesPerOp,
						"proof-time B/op (%d B) must be bounded by memory_spool_bytes (%d) + max_semantic_fact_bytes (%d) + fixed_buffers (%d) = %d B, but exceeded target invariant by %d B",
						allocBytesPerOp, targetInvariantMemorySpoolBytes, targetInvariantMaxSemanticFact, targetInvariantFixedBuffersBudget, maxAllowedProofBytesPerOp, allocBytesPerOp-maxAllowedProofBytesPerOp)
				})
			}
		})
	}
}
