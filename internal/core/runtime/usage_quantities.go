package runtime

import (
	"context"

	accountingpreflight "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/preflight"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// attemptRatingQuantities builds token quantities for usage-authority quota
// admission. It intentionally carries no monetary estimate or price reference.
func attemptRatingQuantities(decision accountingpreflight.Decision) []metering.Quantity {
	qs := []metering.Quantity{{
		Component: metering.ComponentInputToken,
		Unit:      metering.UnitToken,
		Value:     int64(decision.Count.InputTokens),
		Present:   true,
	}}
	if out, ok := explicitOutputQuantity(decision); ok {
		qs = append(qs, out)
	}
	if decision.Count.CacheReadTokens > 0 {
		qs = append(qs, metering.Quantity{Component: metering.ComponentCacheReadInputToken, Unit: metering.UnitToken, Value: int64(decision.Count.CacheReadTokens), Present: true})
	}
	if decision.Count.CacheWriteTokens > 0 {
		qs = append(qs, metering.Quantity{Component: metering.ComponentCacheWriteInputToken, Unit: metering.UnitToken, Value: int64(decision.Count.CacheWriteTokens), Present: true})
	}
	if decision.Count.ReasoningTokens > 0 {
		qs = append(qs, metering.Quantity{Component: metering.ComponentReasoningOutputToken, Unit: metering.UnitToken, Value: int64(decision.Count.ReasoningTokens), Present: true})
	}
	return qs
}

// explicitOutputQuantity returns output only when an explicit bound or positive
// counted output exists. Unknown output remains absent for quota policy matching.
func explicitOutputQuantity(decision accountingpreflight.Decision) (metering.Quantity, bool) {
	if decision.AdjustedMaxOutputTokens != nil {
		v := int64(*decision.AdjustedMaxOutputTokens)
		if v < 0 {
			return metering.Quantity{}, false
		}
		return metering.Quantity{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: v, Present: true}, true
	}
	if decision.Count.OutputTokens > 0 {
		return metering.Quantity{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: int64(decision.Count.OutputTokens), Present: true}, true
	}
	return metering.Quantity{}, false
}

// finalOperatorAttemptQuantities prefers frozen backend-ingress checkpoint
// quantities over stale preflight values. It is used only for non-money quota
// and rate-limit authority admission.
func finalOperatorAttemptQuantities(ctx context.Context, blegID string, decision accountingpreflight.Decision) []metering.Quantity {
	fallback := attemptRatingQuantities(decision)
	holder := meteringHolderFrom(ctx)
	if holder == nil {
		return fallback
	}
	be := holder.BackendIngressFor(blegID)
	if be == nil || len(be.Public.Quantities) == 0 {
		return fallback
	}
	merged := append([]metering.Quantity(nil), be.Public.Quantities...)
	if !quantityComponentPresent(merged, metering.ComponentOutputToken) {
		for _, q := range fallback {
			if q.Component == metering.ComponentOutputToken {
				merged = append(merged, q)
				break
			}
		}
	}
	return merged
}

// usageEventQuantities converts explicit protocol usage presence into stable
// non-monetary metering quantities. It is retained for quota/telemetry tests;
// it does not estimate or rate money.
func usageEventQuantities(ev aipapi.Event) []metering.Quantity {
	if ev.Kind != aipapi.EventUsageDelta {
		return nil
	}
	presence := ev.UsagePresence
	values := []struct {
		present   *bool
		component string
		value     int64
	}{
		{&presence.InputTokens, metering.ComponentInputToken, int64(ev.InputTokens)},
		{&presence.OutputTokens, metering.ComponentOutputToken, int64(ev.OutputTokens)},
		{&presence.CacheReadTokens, metering.ComponentCacheReadInputToken, int64(ev.CacheReadTokens)},
		{&presence.CacheWriteTokens, metering.ComponentCacheWriteInputToken, int64(ev.CacheWriteTokens)},
		{&presence.ReasoningTokens, metering.ComponentReasoningOutputToken, int64(ev.ReasoningTokens)},
		{&presence.TotalTokens, metering.ComponentTotalToken, int64(ev.TotalTokens)},
	}
	out := make([]metering.Quantity, 0, len(values))
	for _, v := range values {
		if *v.present {
			out = append(out, metering.Quantity{Component: v.component, Unit: metering.UnitToken, Value: v.value, Present: true})
		}
	}
	return out
}

func quantityComponentPresent(qs []metering.Quantity, component string) bool {
	for _, q := range qs {
		if q.Component == component && q.Present {
			return true
		}
	}
	return false
}
