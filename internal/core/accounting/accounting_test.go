package accounting_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/accounting"
)

func TestDeriveTokenBreakdown_clampsDerivedCategories(t *testing.T) {
	t.Parallel()

	got := accounting.DeriveTokenBreakdown(accounting.TokenUsage{
		InputTokens:      10,
		CacheReadTokens:  3,
		CacheWriteTokens: 2,
		OutputTokens:     7,
		ReasoningTokens:  4,
	})

	if got.NonCachedInputTokens != 5 {
		t.Fatalf("NonCachedInputTokens: got %d want 5", got.NonCachedInputTokens)
	}
	if got.NonReasoningOutputTokens != 3 {
		t.Fatalf("NonReasoningOutputTokens: got %d want 3", got.NonReasoningOutputTokens)
	}
}

func TestDeriveTokenBreakdown_subcomponentExceedsParentClampsToZero(t *testing.T) {
	t.Parallel()

	got := accounting.DeriveTokenBreakdown(accounting.TokenUsage{
		InputTokens:     5,
		CacheReadTokens: 9,
		OutputTokens:    2,
		ReasoningTokens: 5,
	})
	if got.NonCachedInputTokens != 0 {
		t.Fatalf("NonCachedInputTokens=%d want 0 (cache exceeds input; SubMoneyChecked underflow)", got.NonCachedInputTokens)
	}
	if got.NonReasoningOutputTokens != 0 {
		t.Fatalf("NonReasoningOutputTokens=%d want 0 (reasoning exceeds output)", got.NonReasoningOutputTokens)
	}
}
