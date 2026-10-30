// Package plane holds pure dual-plane usage projection helpers used by runtime
// metering and settlement without importing the executor.
package plane

import (
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func appendPresentQuantity(out []metering.Quantity, include bool, component, unit string, value int64) []metering.Quantity {
	if !include {
		return out
	}
	return append(out, metering.Quantity{
		Component: component, Unit: unit, Value: value, Present: true,
	})
}

// QuantitiesFromUsageEvent maps a usage event to present metering quantities.
// Explicit UsagePresence wins; unmarked legacy events only emit nonzero counters.
func QuantitiesFromUsageEvent(ev aipapi.Event) []metering.Quantity {
	p := ev.UsagePresence
	if p.Any() {
		var out []metering.Quantity
		out = appendPresentQuantity(out, p.InputTokens, metering.ComponentInputToken, metering.UnitToken, int64(ev.InputTokens))
		out = appendPresentQuantity(out, p.OutputTokens, metering.ComponentOutputToken, metering.UnitToken, int64(ev.OutputTokens))
		out = appendPresentQuantity(out, p.CacheReadTokens, metering.ComponentCacheReadInputToken, metering.UnitToken, int64(ev.CacheReadTokens))
		out = appendPresentQuantity(out, p.CacheWriteTokens, metering.ComponentCacheWriteInputToken, metering.UnitToken, int64(ev.CacheWriteTokens))
		out = appendPresentQuantity(out, p.ReasoningTokens, metering.ComponentReasoningOutputToken, metering.UnitToken, int64(ev.ReasoningTokens))
		out = appendPresentQuantity(out, p.TotalTokens, metering.ComponentTotalToken, metering.UnitToken, int64(ev.TotalTokens))
		return out
	}
	if ev.Kind != aipapi.EventUsageDelta {
		return nil
	}
	var out []metering.Quantity
	out = appendPresentQuantity(out, ev.InputTokens != 0, metering.ComponentInputToken, metering.UnitToken, int64(ev.InputTokens))
	out = appendPresentQuantity(out, ev.OutputTokens != 0, metering.ComponentOutputToken, metering.UnitToken, int64(ev.OutputTokens))
	out = appendPresentQuantity(out, ev.CacheReadTokens != 0, metering.ComponentCacheReadInputToken, metering.UnitToken, int64(ev.CacheReadTokens))
	out = appendPresentQuantity(out, ev.CacheWriteTokens != 0, metering.ComponentCacheWriteInputToken, metering.UnitToken, int64(ev.CacheWriteTokens))
	out = appendPresentQuantity(out, ev.ReasoningTokens != 0, metering.ComponentReasoningOutputToken, metering.UnitToken, int64(ev.ReasoningTokens))
	out = appendPresentQuantity(out, ev.TotalTokens != 0, metering.ComponentTotalToken, metering.UnitToken, int64(ev.TotalTokens))
	if ev.TotalTokens == 0 && len(out) > 0 {
		derived := int64(ev.InputTokens + ev.OutputTokens)
		out = appendPresentQuantity(out, derived != 0, metering.ComponentTotalToken, metering.UnitToken, derived)
	}
	return out
}

// MoneyFromUsageEvent returns money only when CostPresent is set (req 2.9).
func MoneyFromUsageEvent(ev aipapi.Event) *metering.MoneyObservation {
	if !ev.CostPresent {
		return nil
	}
	return &metering.MoneyObservation{
		NanoUnits: ev.CostNanoUnits,
		Currency:  strings.TrimSpace(ev.Currency),
		Present:   true,
		Source:    metering.SourceProviderReported,
	}
}

// CustomerPlaneUsageEvent projects client-visible usage and strips all provider
// billable scopes and provider money (design D1/D2, req 1.6).
// The result is a fresh UsageDelta built from retained scopes: aipapi.Event has
// no correlation/trace/model identity fields, and provider envelope fields such
// as RawUsageJSON must not leak onto the customer plane.
func CustomerPlaneUsageEvent(ev aipapi.Event) aipapi.Event {
	if ev.Kind == "" {
		return aipapi.Event{}
	}
	out := aipapi.Event{UsageScopes: []aipapi.ScopedUsageDelta{}}
	included := false
	if len(ev.UsageScopes) > 0 {
		for _, scope := range ev.UsageScopes {
			if scope.Accounting.Plane == aipapi.UsagePlaneProviderBillable {
				continue
			}
			out.UsageScopes = append(out.UsageScopes, scope)
			included = true
		}
	} else if ev.Accounting.Plane != aipapi.UsagePlaneProviderBillable {
		out.UsageScopes = append(out.UsageScopes, aipapi.ScopedUsageDelta{
			InputTokens:      ev.InputTokens,
			OutputTokens:     ev.OutputTokens,
			CacheReadTokens:  ev.CacheReadTokens,
			CacheWriteTokens: ev.CacheWriteTokens,
			ReasoningTokens:  ev.ReasoningTokens,
			TotalTokens:      ev.TotalTokens,
			UsagePresence:    ev.UsagePresence,
			Accounting:       ev.Accounting,
		})
		included = true
	}
	if !included {
		return aipapi.Event{}
	}
	projectAggregatedUsageCounters(&out)
	out.Kind = aipapi.EventUsageDelta
	out.CostNanoUnits = 0
	out.Currency = ""
	out.CostSource = ""
	out.CostPresent = false
	return out
}

func projectAggregatedUsageCounters(out *aipapi.Event) {
	if out == nil || len(out.UsageScopes) == 0 {
		return
	}
	var (
		input, output, cacheRead, cacheWrite, reasoning, total int
		presence                                               aipapi.UsagePresence
		accounting                                             aipapi.UsageAccountingMetadata
		haveAccounting                                         bool
	)
	for _, scope := range out.UsageScopes {
		input += scope.InputTokens
		output += scope.OutputTokens
		cacheRead += scope.CacheReadTokens
		cacheWrite += scope.CacheWriteTokens
		reasoning += scope.ReasoningTokens
		total += scope.TotalTokens
		presence = presence.Union(scope.UsagePresence)
		if !haveAccounting {
			accounting = scope.Accounting
			haveAccounting = true
		}
	}
	out.InputTokens = input
	out.OutputTokens = output
	out.CacheReadTokens = cacheRead
	out.CacheWriteTokens = cacheWrite
	out.ReasoningTokens = reasoning
	out.TotalTokens = total
	out.UsagePresence = presence
	out.Accounting = accounting
}
