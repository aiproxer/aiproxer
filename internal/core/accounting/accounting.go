// Package accounting contains pure token-usage accounting helpers and the
// cost-source vocabulary of usage evidence. Pricing and cost estimation are
// Enterprise code (open-core-billing-split 7.4).
package accounting

const (
	CostSourceProviderReported = "provider_reported"
	CostSourceEstimated        = "estimated"
	CostSourceUnavailable      = "unavailable"
)

type TokenUsage struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	ReasoningTokens  int64
}

type TokenBreakdown struct {
	TokenUsage
	NonCachedInputTokens     int64
	NonReasoningOutputTokens int64
}

// ProviderCost is an optional authoritative provider-reported cost.
// Present distinguishes authoritative zero from an absent cost.

func DeriveTokenBreakdown(usage TokenUsage) TokenBreakdown {
	// Non-cached / non-reasoning residuals use checked non-negative subtraction
	// (SubMoneyChecked) so underflow cannot wrap; clamp to zero when components
	// exceed the parent total (malformed or provider-skewed usage).
	in := usage.InputTokens
	if v, ok := SubMoneyChecked(in, usage.CacheReadTokens); ok {
		in = v
	} else {
		in = 0
	}
	if v, ok := SubMoneyChecked(in, usage.CacheWriteTokens); ok {
		in = v
	} else {
		in = 0
	}
	out := usage.OutputTokens
	if v, ok := SubMoneyChecked(out, usage.ReasoningTokens); ok {
		out = v
	} else {
		out = 0
	}
	return TokenBreakdown{
		TokenUsage:               usage,
		NonCachedInputTokens:     in,
		NonReasoningOutputTokens: out,
	}
}

// SubMoneyChecked subtracts non-negative money (nano) amounts without underflow.
// Also used for non-negative token residuals in DeriveTokenBreakdown.
func SubMoneyChecked(a, b int64) (int64, bool) {
	if a < 0 || b < 0 || a < b {
		return 0, false
	}
	return a - b, true
}
