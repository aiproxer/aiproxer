//go:build precommit

package frontendpipe_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Full release certification stays explicit; ordinary tests retain the bounded behavioral regressions.
func TestFindingB1_RealPipeline_TransientAllocBounded(t *testing.T) {
	type sizeCase struct {
		name   string
		target int
	}

	sizes := []sizeCase{
		{name: "1MiB", target: 1 << 20},
	}
	if !testing.Short() {
		sizes = append(
			sizes,
			sizeCase{name: "5MiB", target: 5 << 20},
			sizeCase{name: "20MiB", target: 20 << 20},
		)
	}

	lanes := []struct {
		laneID      string
		profile     frontendpipe.FrontendProfile
		urlPath     string
		bodyBuilder func(tb testing.TB, target int) []byte
	}{
		{
			laneID:  "openai_responses",
			profile: openairesponses.NewProfile(),
			urlPath: "/v1/responses",
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
			for _, sz := range sizes {
				t.Run(sz.name, func(t *testing.T) {
					body := lane.bodyBuilder(t, sz.target)

					benchRes := testing.Benchmark(func(b *testing.B) {
						b.Helper()
						b.ReportAllocs()
						b.ResetTimer()
						for b.Loop() {
							spoolDir := b.TempDir()
							exec := &ratchetAssessorExecutor{}
							spec := newRatchetPipeSpec(spoolDir, lane.profile, lane.urlPath, exec)

							req := httptest.NewRequest(http.MethodPost, lane.urlPath, bytes.NewReader(body))
							rec := httptest.NewRecorder()

							frontendpipe.ServeHTTP(&spec, rec, req)
							if rec.Code != http.StatusOK {
								b.Fatalf("ServeHTTP failed with status %d", rec.Code)
							}
						}
					})

					require.True(t, benchRes.N > 0, "benchmark executed zero iterations — vacuous measurement")

					allocBytesPerOp := benchRes.AllocedBytesPerOp()
					allocsPerOp := benchRes.AllocsPerOp()

					t.Logf("[%s/%s] real pipeline transient: %d B/op, %d allocs/op (target ceiling: %d B/op)",
						lane.laneID, sz.name, allocBytesPerOp, allocsPerOp, maxAllowedProofBytesPerOp)

					// Target Invariant: Real pipeline transient allocations must NOT scale with body size,
					// and must remain strictly bounded by memory_spool_bytes + max_semantic_fact_bytes + fixed_buffers.
					// Under current defect (candidate.go:387), readCompletedSource causes end-to-end B/op of:
					// ~2.5MB @1MiB, ~11.3MB @5MiB, ~50.4MB @20MiB.
					assert.LessOrEqual(t, allocBytesPerOp, maxAllowedProofBytesPerOp,
						"real pipeline B/op (%d B) must be bounded by %d B, but exceeded target invariant by %d B (O(payload) allocation in candidate.go:387)",
						allocBytesPerOp, maxAllowedProofBytesPerOp, allocBytesPerOp-maxAllowedProofBytesPerOp)
				})
			}
		})
	}
}
