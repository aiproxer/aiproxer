//go:build precommit

package openailegacy_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Full release certification stays explicit; ordinary tests retain the bounded behavioral regressions.
func TestCompaction_MemoryBudget_FlatAllocationsAcross1_5_20MiB(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping memory measurement in short mode")
	}

	sizes := []struct {
		name string
		mb   int
	}{
		{"1MiB", 1},
		{"5MiB", 5},
		{"20MiB", 20},
	}

	for _, tc := range sizes {
		t.Run(tc.name, func(t *testing.T) {
			// Pre-generate the payload outside measurement
			var sb strings.Builder
			sb.Grow(tc.mb*1024*1024 + 1024)
			sb.WriteString(`{"model":"gpt-4o","messages":[`)
			chunk := "0123456789abcdef0123456789abcdef0123456789abcdef"
			numMsgs := 1
			msgSize := tc.mb * 1024 * 1024
			if tc.mb > 5 {
				numMsgs = tc.mb / 4
				msgSize = 4 * 1024 * 1024
			}
			for i := 0; i < numMsgs; i++ {
				if i > 0 {
					sb.WriteString(",")
				}
				sb.WriteString(`{"role":"user","content":"`)
				if i == 0 {
					sb.WriteString("context checkpoint compaction: ")
				}
				startLen := sb.Len()
				for sb.Len()-startLen < msgSize {
					sb.WriteString(chunk)
				}
				sb.WriteString(`"}`)
			}
			sb.WriteString(`]}`)
			payloadBytes := []byte(sb.String())
			prof := openailegacy.NewProfile()
			in := defaultChatProofInput(payloadBytes, "", nil)

			// Correctness assertion executed outside the allocation measurement loop
			out, err := prof.CompileProof(context.Background(), in)
			require.NoError(t, err)
			require.True(t, out.State.Proof.CompactionComplete)

			// Benchmark allocation measurement isolated to CompileProof (fixtures excluded)
			benchRes := testing.Benchmark(func(b *testing.B) {
				b.Helper()
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					_, err := prof.CompileProof(context.Background(), in)
					if err != nil {
						b.Fatalf("CompileProof failed: %v", err)
					}
				}
			})

			allocBytes := uint64(benchRes.AllocedBytesPerOp())
			t.Logf("Payload %d MiB: AllocedBytesPerOp = %d bytes (%.2f KiB)", tc.mb, allocBytes, float64(allocBytes)/1024.0)

			// Flat O(chunkSize) requirement: must be strictly less than 448 KiB ceiling
			const maxCeilingBytes = 448 * 1024
			assert.Less(t, allocBytes, uint64(maxCeilingBytes),
				"CompileProof heap allocation (%d bytes) must not exceed %d bytes ceiling for %d MiB payload",
				allocBytes, maxCeilingBytes, tc.mb)
		})
	}
}
