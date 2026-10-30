package proxymemory

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Issue #1383: settling a dirty bank with no outstanding work must preserve
// the summary of still-live, previously consolidated extractions.
func TestWorkerDrainedBatchPreservesLiveMemory(t *testing.T) {
	h := newWorkerTestHarness(t, workerTestConfig())
	e := workerExtraction(h.base)
	b := workerClaimedBank()
	b.ConsolidatedNames = []string{SummaryName(e)}
	h.store.extractions = []Extraction{e}
	h.store.banks = []ClaimedBank{b}
	h.worker.Tick(context.Background())
	require.Empty(t, h.aux.submitCalls())
	pubs := h.store.publishCalls()
	require.Len(t, pubs, 1)
	require.Equal(t, b.MemoryMD, pubs[0].publication.MemoryMD)
	require.Equal(t, b.MemorySummary, pubs[0].publication.MemorySummary)
}

// A batch-omitted extraction is not removed, but its unseen document must not
// be advertised as new input in the CHANGES section either.
func TestPhase2ChangesOnlyAddsCarriedDocuments(t *testing.T) {
	e1 := phase2TestExtraction(1, 1000, "one")
	e2 := phase2TestExtraction(2, 2000, "two")
	req, _, err := BuildPhase2Request(phase2TestConfig(), Phase2Input{
		Extractions: []Extraction{e1},
		WindowNames: []string{SummaryName(e1), SummaryName(e2)},
	})
	require.NoError(t, err)
	user := phase2TestUser(t, req)
	require.Contains(t, user, "added: rollout_summaries/"+SummaryName(e1))
	require.False(t, strings.Contains(user, "added: rollout_summaries/"+SummaryName(e2)))
}
