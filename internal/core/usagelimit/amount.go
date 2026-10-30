package usagelimit

import "fmt"

type AmountUnit string

const (
	AmountUnitRequests         AmountUnit = "requests"
	AmountUnitInputTokens      AmountUnit = "input_tokens"
	AmountUnitOutputTokens     AmountUnit = "output_tokens"
	AmountUnitCacheReadTokens  AmountUnit = "cache_read_tokens"
	AmountUnitCacheWriteTokens AmountUnit = "cache_write_tokens"
	AmountUnitReasoningTokens  AmountUnit = "reasoning_tokens"
	AmountUnitTotalTokens      AmountUnit = "total_tokens"
)

func (u AmountUnit) IsKnown() bool {
	switch u {
	case AmountUnitRequests, AmountUnitInputTokens, AmountUnitOutputTokens, AmountUnitCacheReadTokens,
		AmountUnitCacheWriteTokens, AmountUnitReasoningTokens, AmountUnitTotalTokens:
		return true
	default:
		return false
	}
}

type Amount struct {
	Unit  AmountUnit
	Value int64
}

// PreflightUsage carries per-unit pre-backend estimates used during admission
// when configured rules enforce dimensions other than input tokens.
type PreflightUsage struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	ReasoningTokens  int64
	TotalTokens      int64
	// TotalTokensPresent distinguishes an authoritative total (including explicit
	// zero) from an omitted total that may be inferred via the inclusion schema.
	TotalTokensPresent bool
}

func (p PreflightUsage) AmountForUnit(unit AmountUnit) (Amount, bool) {
	switch unit {
	case AmountUnitInputTokens:
		return Amount{Unit: unit, Value: p.InputTokens}, true
	case AmountUnitOutputTokens:
		output := max(p.OutputTokens, 0)
		return Amount{Unit: unit, Value: output}, true
	case AmountUnitCacheReadTokens:
		return Amount{Unit: unit, Value: p.CacheReadTokens}, true
	case AmountUnitCacheWriteTokens:
		return Amount{Unit: unit, Value: p.CacheWriteTokens}, true
	case AmountUnitReasoningTokens:
		return Amount{Unit: unit, Value: p.ReasoningTokens}, true
	case AmountUnitTotalTokens:
		if p.TotalTokensPresent {
			return Amount{Unit: unit, Value: p.TotalTokens}, true
		}
		// Default inclusion schema: cache ⊂ input, reasoning ⊂ output.
		// Infer total = input + output without re-adding subcomponents.
		return Amount{Unit: unit, Value: p.InputTokens + p.OutputTokens}, true
	default:
		return Amount{}, false
	}
}

func (a Amount) String() string {
	return fmt.Sprintf("%d %s", a.Value, a.Unit)
}
