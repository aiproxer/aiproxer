package openaiusage

import (
	"encoding/json"
	"math"
	"math/big"
	"strconv"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/accounting"
	"github.com/aiproxer/aiproxer/internal/safecast"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/respjson"
	"github.com/openai/openai-go/v3/responses"
)

const (
	aipCacheWriteTokensKey = "x_aip_cache_write_tokens"
	// standardCacheWriteTokensKey is the official provider-family
	// prompt_tokens_details / input_tokens_details cache-write member. It is
	// owned by the native usage mapping (provider_evidence.go); the
	// x_aip_cache_write_tokens extension is only a compatible-provider fallback.
	standardCacheWriteTokensKey = "cache_write_tokens"
	usageCostKey                = "cost"
	defaultProviderCurrency     = "USD"
	providerCostNanoScale       = int64(1_000_000_000)
)

func ChatUsageEvent(usage openai.CompletionUsage) aipapi.Event {
	input, inputPresent := nonNegativeProviderCount(usage.PromptTokens, usage.JSON.PromptTokens.Valid())
	output, outputPresent := nonNegativeProviderCount(usage.CompletionTokens, usage.JSON.CompletionTokens.Valid())
	cached, cachedPresent := nonNegativeProviderCount(usage.PromptTokensDetails.CachedTokens, usage.PromptTokensDetails.JSON.CachedTokens.Valid())
	reasoning, reasoningPresent := nonNegativeProviderCount(usage.CompletionTokensDetails.ReasoningTokens, usage.CompletionTokensDetails.JSON.ReasoningTokens.Valid())
	total, totalPresent := nonNegativeProviderCount(usage.TotalTokens, usage.JSON.TotalTokens.Valid())
	ev := aipapi.Event{
		Kind:            aipapi.EventUsageDelta,
		InputTokens:     input,
		OutputTokens:    output,
		CacheReadTokens: cached,
		ReasoningTokens: reasoning,
		TotalTokens:     total,
		UsagePresence: aipapi.UsagePresence{
			InputTokens: inputPresent, OutputTokens: outputPresent,
			CacheReadTokens: cachedPresent, ReasoningTokens: reasoningPresent,
			TotalTokens: totalPresent,
		},
		RawUsageJSON: rawJSON(usage.RawJSON(), usage),
		Accounting: aipapi.UsageAccountingMetadata{
			Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported,
			Authority: aipapi.UsageAuthorityAuthoritative, DedupeKey: "openai.chat.usage:stream",
		},
	}
	applyPromptDetailsExtensions(&ev, usage.PromptTokensDetails.JSON.ExtraFields, usage.PromptTokensDetails.RawJSON())
	applyUsageCostExtensions(&ev, usage.JSON.ExtraFields, usage.RawJSON())
	return ev
}

func ResponsesUsageEvent(u responses.ResponseUsage) aipapi.Event {
	input, inputPresent := nonNegativeProviderCount(u.InputTokens, u.JSON.InputTokens.Valid())
	output, outputPresent := nonNegativeProviderCount(u.OutputTokens, u.JSON.OutputTokens.Valid())
	cached, cachedPresent := nonNegativeProviderCount(u.InputTokensDetails.CachedTokens, u.InputTokensDetails.JSON.CachedTokens.Valid())
	reasoning, reasoningPresent := nonNegativeProviderCount(u.OutputTokensDetails.ReasoningTokens, u.OutputTokensDetails.JSON.ReasoningTokens.Valid())
	total, totalPresent := nonNegativeProviderCount(u.TotalTokens, u.JSON.TotalTokens.Valid())
	ev := aipapi.Event{
		Kind:            aipapi.EventUsageDelta,
		InputTokens:     input,
		OutputTokens:    output,
		CacheReadTokens: cached,
		ReasoningTokens: reasoning,
		TotalTokens:     total,
		UsagePresence: aipapi.UsagePresence{
			InputTokens: inputPresent, OutputTokens: outputPresent,
			CacheReadTokens: cachedPresent, ReasoningTokens: reasoningPresent,
			TotalTokens: totalPresent,
		},
		RawUsageJSON: rawJSON(u.RawJSON(), u),
		Accounting: aipapi.UsageAccountingMetadata{
			Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported,
			Authority: aipapi.UsageAuthorityAuthoritative, DedupeKey: "openai.responses.usage:stream",
		},
	}
	applyPromptDetailsExtensions(&ev, u.InputTokensDetails.JSON.ExtraFields, u.InputTokensDetails.RawJSON())
	applyUsageCostExtensions(&ev, u.JSON.ExtraFields, u.RawJSON())
	return ev
}

func nonNegativeProviderCount(value int64, present bool) (int, bool) {
	if !present || value < 0 {
		return 0, false
	}
	converted := safecast.IntFromInt64Clamp(value)
	if int64(converted) != value {
		return 0, false
	}
	return converted, true
}

func rawJSON(raw string, usage any) string {
	if raw != "" {
		return raw
	}
	b, err := json.Marshal(usage)
	if err != nil {
		return ""
	}
	return string(b)
}

func applyPromptDetailsExtensions(ev *aipapi.Event, extras map[string]respjson.Field, detailsRaw string) {
	if ev == nil {
		return
	}
	// The official provider-family cache_write_tokens member is authoritative:
	// when it is present on the details object (even with a malformed lexeme)
	// the native usage mapping owns the quantity, so the non-standard
	// x_aip_cache_write_tokens extension must not stand in for it or add a
	// second, conflicting observed value.
	if detailsJSONHasMember(detailsRaw, standardCacheWriteTokensKey) {
		return
	}
	if len(extras) > 0 {
		if f, ok := extras[aipCacheWriteTokensKey]; ok && f.Valid() {
			if value, valid := parseIntFieldFromJSON(f.Raw()); valid {
				ev.CacheWriteTokens = value
				ev.UsagePresence.CacheWriteTokens = true
			}
		}
	}
	if ev.CacheWriteTokens == 0 {
		if value, ok := cacheWriteFromDetailsJSON(detailsRaw); ok {
			ev.CacheWriteTokens = value
			ev.UsagePresence.CacheWriteTokens = true
		}
	}
}

// detailsJSONHasMember reports whether a detail JSON object carries a present,
// non-null member with the given key. It mirrors the native usage presence
// policy: an absent member and a JSON null member both carry no provider
// quantity, so only a real value counts as present.
func detailsJSONHasMember(raw, key string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return false
	}
	value, ok := probe[key]
	if !ok {
		return false
	}
	trimmed := strings.TrimSpace(string(value))
	return trimmed != "" && trimmed != "null"
}

func cacheWriteFromDetailsJSON(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return 0, false
	}
	value, ok := probe[aipCacheWriteTokensKey]
	if !ok {
		return 0, false
	}
	return parseIntFieldFromJSON(string(value))
}

func applyUsageCostExtensions(ev *aipapi.Event, extras map[string]respjson.Field, usageRaw string) {
	if ev == nil {
		return
	}
	if len(extras) > 0 {
		if f, ok := extras[usageCostKey]; ok && f.Valid() {
			applyProviderCost(ev, f.Raw())
		}
	}
	if !ev.CostPresent {
		applyProviderCost(ev, providerCostRawFromUsageJSON(usageRaw))
	}
}

func providerCostRawFromUsageJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var probe struct {
		Cost json.RawMessage `json:"cost"`
	}
	if err := json.Unmarshal([]byte(raw), &probe); err != nil || len(probe.Cost) == 0 {
		return ""
	}
	return string(probe.Cost)
}

func applyProviderCost(ev *aipapi.Event, raw string) {
	nano, ok := providerCostNanoUnits(raw)
	if !ok || nano < 0 {
		return
	}
	ev.CostNanoUnits = nano
	ev.Currency = defaultProviderCurrency
	ev.CostSource = accounting.CostSourceProviderReported
	ev.CostPresent = true
}

func intFieldFromJSON(raw string) int {
	value, _ := parseIntFieldFromJSON(raw)
	return value
}

func parseIntFieldFromJSON(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	decimal, err := sdkmetering.ParseDecimal(raw)
	if err != nil || decimal.Scale != 0 || strings.HasPrefix(raw, "-") || strings.HasPrefix(decimal.Coefficient, "-") {
		return 0, false
	}
	n, err := strconv.ParseInt(decimal.Coefficient, 10, 0)
	if err != nil || n < 0 {
		return 0, false
	}
	return int(n), true
}

func providerCostNanoUnits(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	rat, ok := new(big.Rat).SetString(raw)
	if !ok {
		var f float64
		if err := json.Unmarshal([]byte(raw), &f); err != nil {
			return 0, false
		}
		if f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		scaled := f * float64(providerCostNanoScale)
		if math.IsInf(scaled, 0) || scaled >= float64(math.MaxInt64) {
			return 0, false
		}
		return int64(scaled + 0.5), true
	}
	if rat.Sign() < 0 {
		return 0, false
	}
	if rat.Sign() == 0 {
		return 0, true
	}
	rat.Mul(rat, big.NewRat(providerCostNanoScale, 1))
	q, r := new(big.Int), new(big.Int)
	q.QuoRem(rat.Num(), rat.Denom(), r)
	if new(big.Int).Mul(r, big.NewInt(2)).Cmp(rat.Denom()) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return 0, false
	}
	return q.Int64(), true
}
