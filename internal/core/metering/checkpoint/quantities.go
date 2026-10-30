package checkpoint

import (
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// QuantitiesFromTokenCounts maps token components to metering quantities using
// the default inclusion schema vocabulary. Zero counts remain Present when
// present is true; omitted totals leave PresenceUnknown on the checkpoint.
func QuantitiesFromTokenCounts(input, output, cacheRead, cacheWrite, reasoning, total int64, totalPresent bool) []metering.Quantity {
	out := []metering.Quantity{
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: input, Present: true},
		{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: output, Present: true},
		{Component: metering.ComponentCacheReadInputToken, Unit: metering.UnitToken, Value: cacheRead, Present: true},
		{Component: metering.ComponentCacheWriteInputToken, Unit: metering.UnitToken, Value: cacheWrite, Present: true},
		{Component: metering.ComponentReasoningOutputToken, Unit: metering.UnitToken, Value: reasoning, Present: true},
	}
	if totalPresent {
		out = append(out, metering.Quantity{
			Component: metering.ComponentTotalToken,
			Unit:      metering.UnitToken,
			Value:     total,
			Present:   true,
		})
	}
	return out
}

// QuantitiesFromCountAndMaxOutput derives legal ingress quantities without a Call.
// Always emits request/count=1. When maxOutputTokens is non-nil, emits output_token
// with that value Present:true. Does not invent input_token (req 7.2).
func QuantitiesFromCountAndMaxOutput(maxOutputTokens *int) []metering.Quantity {
	var max64 *int64
	if maxOutputTokens != nil {
		v := int64(*maxOutputTokens)
		max64 = &v
	}
	return QuantitiesFromCountAndMaxOutput64(max64)
}

// QuantitiesFromCountAndMaxOutput64 derives legal ingress quantities without a Call
// using an int64 max output token bound.
func QuantitiesFromCountAndMaxOutput64(maxOutputTokens *int64) []metering.Quantity {
	out := []metering.Quantity{
		{Component: metering.ComponentRequest, Unit: metering.UnitCount, Value: 1, Present: true},
	}
	if maxOutputTokens != nil {
		out = append(out, metering.Quantity{
			Component: metering.ComponentOutputToken,
			Unit:      metering.UnitToken,
			Value:     *maxOutputTokens,
			Present:   true,
		})
	}
	return out
}

// QuantitiesFromCall derives legal ingress quantities from a frozen Call WITHOUT
// tokenization. Always emits request/count=1. When Options.MaxOutputTokens is
// set, emits output_token with that value Present:true. Does not invent
// input_token. Does not emit output_token=0 when max is omitted (req 7.2).
// Deferred counting merges input_token later via MergeQuantities.
func QuantitiesFromCall(call aipapi.Call) []metering.Quantity {
	return QuantitiesFromCountAndMaxOutput(call.Options.MaxOutputTokens)
}

// MergeQuantities merges additions into base by component while preserving
// checkpoint identity elsewhere. An existing Present output_token bound is never
// replaced by an addition (conservative exposure / req 7.2–7.3). Other Present
// additions fill missing components or refresh non-output values (deferred input
// counting).
func MergeQuantities(base, additions []metering.Quantity) []metering.Quantity {
	type entry struct {
		q metering.Quantity
	}
	byComp := make(map[string]entry, len(base)+len(additions))
	order := make([]string, 0, len(base)+len(additions))
	for _, q := range base {
		if _, ok := byComp[q.Component]; !ok {
			order = append(order, q.Component)
		}
		byComp[q.Component] = entry{q: q}
	}
	for _, q := range additions {
		existing, ok := byComp[q.Component]
		if !ok {
			order = append(order, q.Component)
			byComp[q.Component] = entry{q: q}
			continue
		}
		if q.Component == metering.ComponentOutputToken && existing.q.Present {
			continue
		}
		if !q.Present {
			continue
		}
		byComp[q.Component] = entry{q: q}
	}
	out := make([]metering.Quantity, 0, len(order))
	for _, c := range order {
		out = append(out, byComp[c].q)
	}
	return out
}

// QuantityComponentValue returns the Present value for component, if any.
func QuantityComponentValue(qs []metering.Quantity, component string) (int64, bool) {
	for _, q := range qs {
		if q.Component == component && q.Present {
			return q.Value, true
		}
	}
	return 0, false
}

// ApplyQuantities sets Public.Quantities and updates Presence.
// It does not mutate CheckpointID, StreamID, Boundary, Lifecycle, Correlation, or Call.
func (s *Snapshot) ApplyQuantities(qs []metering.Quantity) {
	if s == nil {
		return
	}
	s.Public.Quantities = qs
	if len(qs) == 0 {
		s.Public.Presence = metering.PresenceUnknown
		return
	}
	s.Public.Presence = metering.PresencePresent
}

// MergeQuantities merges additions into Public.Quantities without changing
// CheckpointID, StreamID, Boundary, Lifecycle, Correlation, or Call.
func (s *Snapshot) MergeQuantities(additions []metering.Quantity) {
	if s == nil {
		return
	}
	s.ApplyQuantities(MergeQuantities(s.Public.Quantities, additions))
}

// DeriveAndApplyIngressQuantities derives ingress quantities from the frozen
// Call and applies them to Public.
func (s *Snapshot) DeriveAndApplyIngressQuantities() {
	if s == nil {
		return
	}
	s.ApplyQuantities(QuantitiesFromCall(s.Call))
}
