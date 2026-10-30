package acp

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const acpSessionUpdateUsage = "usage_update"

// maxUsageCounter bounds accepted token counters to exactly representable
// integers; larger values are dropped, never clamped or synthesized.
const maxUsageCounter = int64(1<<53 - 1)

// maxUsageModelLen bounds the provider model attribution retained in raw usage.
const maxUsageModelLen = 256

// usageInt extracts a strict non-negative integer counter. Fractional,
// negative, overflowing or non-numeric values are rejected: usage metadata
// must never be guessed.
func usageInt(v any) (int, bool) {
	var n int64
	switch t := v.(type) {
	case json.Number:
		s := strings.TrimSpace(t.String())
		parsed, err := strconv.ParseInt(s, 10, 64)
		if err != nil || strconv.FormatInt(parsed, 10) != normalizeJSONInt(s) {
			return 0, false
		}
		n = parsed
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) || t != math.Trunc(t) {
			return 0, false
		}
		n = int64(t)
	case int:
		n = int64(t)
	case int64:
		n = t
	default:
		return 0, false
	}
	if n < 0 || n > maxUsageCounter {
		return 0, false
	}
	return int(n), true
}

// normalizeJSONInt rejects non-canonical integer spellings ("01", "+1",
// "1.0", exponents) so only exact integers pass.
func normalizeJSONInt(s string) string {
	if s == "" {
		return ""
	}
	neg := false
	if strings.HasPrefix(s, "-") {
		neg = true
		s = s[1:]
	}
	if s == "" {
		return ""
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return ""
		}
	}
	s = strings.TrimLeft(s, "0")
	if s == "" {
		s = "0"
	}
	if neg && s != "0" {
		return "-" + s
	}
	return s
}

// usageAccounting annotates adapter-reported usage with the provider-billable
// convention shared by other connectors' provider evidence.
func usageAccounting() aipapi.UsageAccountingMetadata {
	return aipapi.UsageAccountingMetadata{
		Plane:     aipapi.UsagePlaneProviderBillable,
		Source:    aipapi.UsageSourceProviderReported,
		Authority: aipapi.UsageAuthorityAuthoritative,
	}
}

// usageCost extracts the adapter's {amount, currency} cost block. Amount is a
// provider-reported USD value; only finite non-negative amounts that fit int64
// nano-units convert. Invalid cost drops the cost fields, never the tokens.
func usageCost(v any) (nano int64, currency string, ok bool) {
	obj, isObj := v.(map[string]any)
	if !isObj {
		return 0, "", false
	}
	amount, isNum := usageFloat(obj["amount"])
	cur, isStr := obj["currency"].(string)
	cur = strings.TrimSpace(cur)
	if !isNum || !isStr || cur == "" || len(cur) > 16 {
		return 0, "", false
	}
	scaled := amount * 1e9
	if scaled < 0 || scaled > float64(math.MaxInt64) {
		return 0, "", false
	}
	return int64(math.Round(scaled)), cur, true
}

func usageFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case json.Number:
		f, err := t.Float64()
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return f, true
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return 0, false
		}
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	default:
		return 0, false
	}
}

// usageModel returns the adapter-attached model identity carried in the
// update's _meta (for example "_claude/model"). It is correlation metadata,
// never an authorization claim.
func usageModel(v any) string {
	meta, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"_claude/model", "model"} {
		if s, ok := meta[key].(string); ok {
			s = strings.TrimSpace(s)
			if s != "" && len(s) <= maxUsageModelLen {
				return s
			}
		}
	}
	return ""
}

// mapUsageUpdateToEvent maps one mid-stream usage_update notification to a
// cumulative total-tokens usage delta. The adapter reports context-window
// consumption (used/size) plus an optional provider cost; it does not split
// input/output, so no split is synthesized. Updates without a valid used
// counter are dropped.
func mapUsageUpdateToEvent(upd map[string]any) (aipapi.Event, bool) {
	used, ok := usageInt(upd["used"])
	if !ok {
		return aipapi.Event{}, false
	}
	ev := aipapi.Event{
		Kind:          aipapi.EventUsageDelta,
		TotalTokens:   used,
		UsagePresence: aipapi.UsagePresence{TotalTokens: true},
		Accounting:    usageAccounting(),
	}
	if nano, cur, ok := usageCost(upd["cost"]); ok {
		ev.CostNanoUnits, ev.Currency, ev.CostPresent = nano, cur, true
	}
	raw := fmt.Sprintf(`{"used":%d`, used)
	if size, ok := usageInt(upd["size"]); ok {
		raw += fmt.Sprintf(`,"size":%d`, size)
	}
	if model := usageModel(upd["_meta"]); model != "" {
		raw += fmt.Sprintf(`,"model":%s`, quoteJSONString(model))
	}
	if limit, ok := usageRateLimit(upd["_meta"]); ok {
		raw += fmt.Sprintf(`,"rateLimit":%s`, limit)
	}
	ev.RawUsageJSON = raw + "}"
	return ev, true
}

// usageRateLimit renders the adapter's _claude/rateLimit report (subscription
// quota state from rate_limit_event turns) as bounded JSON. Only the status
// enum and numeric/string scalars travel; anything else drops the block.
func usageRateLimit(v any) (string, bool) {
	meta, ok := v.(map[string]any)
	if !ok {
		return "", false
	}
	info, ok := meta["_claude/rateLimit"].(map[string]any)
	if !ok {
		return "", false
	}
	status, _ := info["status"].(string)
	switch status {
	case "allowed", "allowed_warning", "rejected":
	default:
		return "", false
	}
	out := fmt.Sprintf(`{"status":%s`, quoteJSONString(status))
	if resets, ok := usageFloat(info["resetsAt"]); ok && resets >= 0 && resets < 1<<53 {
		out += fmt.Sprintf(`,"resetsAt":%d`, int64(resets))
	}
	if typ, ok := info["rateLimitType"].(string); ok {
		if typ = strings.TrimSpace(typ); typ != "" && len(typ) <= 64 {
			clean := true
			for _, c := range typ {
				if c <= ' ' || c == '"' || c == '\\' || unicode.IsControl(c) {
					clean = false
					break
				}
			}
			if clean {
				out += fmt.Sprintf(`,"type":%s`, quoteJSONString(typ))
			}
		}
	}
	return out + "}", true
}

// mapPromptResultUsage maps the terminal session/prompt result's usage object
// (exact adapter/SDK counters) to a usage delta. It returns false when no
// valid counter exists, in which case the caller still emits the terminal
// outcome: garbage metadata never fails the turn.
func mapPromptResultUsage(result map[string]any) (aipapi.Event, bool) {
	obj, ok := result["usage"].(map[string]any)
	if !ok {
		return aipapi.Event{}, false
	}
	ev := aipapi.Event{Kind: aipapi.EventUsageDelta, Accounting: usageAccounting()}
	raw := []string{}
	set := func(name string, dst *int, present *bool) {
		if n, ok := usageInt(obj[name]); ok {
			*dst, *present = n, true
			raw = append(raw, fmt.Sprintf(`%s:%d`, quoteJSONString(name), n))
		}
	}
	set("inputTokens", &ev.InputTokens, &ev.UsagePresence.InputTokens)
	set("outputTokens", &ev.OutputTokens, &ev.UsagePresence.OutputTokens)
	// Wire names follow the adapter's sessionUsage shape (cached*), distinct
	// from the canonical CacheRead/CacheWrite event fields they populate.
	set("cachedReadTokens", &ev.CacheReadTokens, &ev.UsagePresence.CacheReadTokens)
	set("cachedWriteTokens", &ev.CacheWriteTokens, &ev.UsagePresence.CacheWriteTokens)
	set("totalTokens", &ev.TotalTokens, &ev.UsagePresence.TotalTokens)
	if !ev.UsagePresence.Any() {
		return aipapi.Event{}, false
	}
	ev.RawUsageJSON = "{" + strings.Join(raw, ",") + "}"
	return ev, true
}

func quoteJSONString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}
