package streamusage

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Phase 2.1: local totalTokens inference uses inclusion schema.

func TestTotalTokens_doesNotDoubleCountCacheAndReasoning(t *testing.T) {
	t.Parallel()

	usage := aipapi.ScopedUsageDelta{
		InputTokens:      100,
		OutputTokens:     50,
		CacheReadTokens:  40,
		CacheWriteTokens: 10,
		ReasoningTokens:  15,
	}
	got := totalTokens(usage, 0, 0)
	if got != 150 {
		t.Fatalf("totalTokens=%d want 150 (input+output; subcomponents not re-added)", got)
	}
}
