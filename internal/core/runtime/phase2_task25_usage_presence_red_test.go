package runtime

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// Phase 2.5: UsagePresence drives rating quantity inclusion (req 2.9).

func TestPhase25_UsageEventRatingQuantities_UsesUsagePresence(t *testing.T) {
	t.Parallel()

	t.Run("present_zero_all_components", func(t *testing.T) {
		t.Parallel()
		ev := aipapi.Event{
			Kind: aipapi.EventUsageDelta,
			UsagePresence: aipapi.UsagePresence{
				InputTokens: true, OutputTokens: true,
				CacheReadTokens: true, CacheWriteTokens: true,
				ReasoningTokens: true, TotalTokens: true,
			},
		}
		qs := usageEventQuantities(ev)
		wantOrder := []string{
			metering.ComponentInputToken,
			metering.ComponentOutputToken,
			metering.ComponentCacheReadInputToken,
			metering.ComponentCacheWriteInputToken,
			metering.ComponentReasoningOutputToken,
			metering.ComponentTotalToken,
		}
		if len(qs) != len(wantOrder) {
			t.Fatalf("len=%d want %d qs=%+v", len(qs), len(wantOrder), qs)
		}
		for i, component := range wantOrder {
			q := qs[i]
			if q.Component != component || !q.Present || q.Value != 0 || q.Unit != metering.UnitToken {
				t.Fatalf("index %d got %+v want component=%s present zero token", i, q, component)
			}
		}
	})

	t.Run("absent_nonzero_all_components_ignored", func(t *testing.T) {
		t.Parallel()
		ev := aipapi.Event{
			Kind:             aipapi.EventUsageDelta,
			InputTokens:      100,
			OutputTokens:     50,
			CacheReadTokens:  7,
			CacheWriteTokens: 3,
			ReasoningTokens:  9,
			TotalTokens:      169,
			UsagePresence:    aipapi.UsagePresence{InputTokens: true},
		}
		qs := usageEventQuantities(ev)
		if len(qs) != 1 || qs[0].Component != metering.ComponentInputToken || qs[0].Value != 100 {
			t.Fatalf("only marked input must appear: %+v", qs)
		}
		for _, component := range []string{
			metering.ComponentOutputToken,
			metering.ComponentCacheReadInputToken,
			metering.ComponentCacheWriteInputToken,
			metering.ComponentReasoningOutputToken,
			metering.ComponentTotalToken,
		} {
			if _, ok := quantityValue(qs, component); ok {
				t.Fatalf("unmarked %s must not be inferred from nonzero value (req 2.9)", component)
			}
		}
	})

	t.Run("deterministic_component_ordering", func(t *testing.T) {
		t.Parallel()
		ev := aipapi.Event{
			Kind:             aipapi.EventUsageDelta,
			InputTokens:      1,
			OutputTokens:     2,
			CacheReadTokens:  3,
			CacheWriteTokens: 4,
			ReasoningTokens:  5,
			TotalTokens:      6,
			UsagePresence: aipapi.UsagePresence{
				TotalTokens: true, ReasoningTokens: true, CacheWriteTokens: true,
				CacheReadTokens: true, OutputTokens: true, InputTokens: true,
			},
		}
		qs := usageEventQuantities(ev)
		want := []struct {
			component string
			value     int64
		}{
			{metering.ComponentInputToken, 1},
			{metering.ComponentOutputToken, 2},
			{metering.ComponentCacheReadInputToken, 3},
			{metering.ComponentCacheWriteInputToken, 4},
			{metering.ComponentReasoningOutputToken, 5},
			{metering.ComponentTotalToken, 6},
		}
		if len(qs) != len(want) {
			t.Fatalf("len=%d want %d", len(qs), len(want))
		}
		for i := range want {
			if qs[i].Component != want[i].component || qs[i].Value != want[i].value || qs[i].Unit != metering.UnitToken || !qs[i].Present {
				t.Fatalf("order/unit mismatch at %d: got %+v want %+v", i, qs[i], want[i])
			}
		}
	})
}

func quantityValue(qs []metering.Quantity, component string) (int64, bool) {
	for _, q := range qs {
		if q.Component == component && q.Present {
			return q.Value, true
		}
	}
	return 0, false
}
