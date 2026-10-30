package acp

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func jsonNumber(s string) json.Number { return json.Number(s) }

func TestMapSessionUpdate_usageUpdateMapsTokensAndCost(t *testing.T) {
	t.Parallel()
	upd := map[string]any{
		"sessionUpdate": "usage_update",
		"used":          jsonNumber("1234"),
		"size":          jsonNumber("200000"),
		"cost":          map[string]any{"amount": 0.0123, "currency": "USD"},
		"_meta":         map[string]any{"_claude/model": "claude-sonnet-5-5"},
	}
	evs, err := mapSessionUpdateToEvents(context.Background(), mergeMapperOptions(Config{}), upd)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != aipapi.EventUsageDelta {
		t.Fatalf("got %#v", evs)
	}
	got := evs[0]
	if got.TotalTokens != 1234 || !got.UsagePresence.TotalTokens {
		t.Fatalf("total = %d presence = %+v", got.TotalTokens, got.UsagePresence)
	}
	if got.InputTokens != 0 || got.OutputTokens != 0 || got.UsagePresence.InputTokens || got.UsagePresence.OutputTokens {
		t.Fatalf("must not synthesize input/output splits: %#v", got)
	}
	if !got.CostPresent || got.Currency != "USD" || got.CostNanoUnits != 12300000 {
		t.Fatalf("cost = present:%v %d %q", got.CostPresent, got.CostNanoUnits, got.Currency)
	}
	if got.Accounting.Source != aipapi.UsageSourceProviderReported || got.Accounting.Authority != aipapi.UsageAuthorityAuthoritative || got.Accounting.Plane != aipapi.UsagePlaneProviderBillable {
		t.Fatalf("accounting = %+v", got.Accounting)
	}
}

func TestMapSessionUpdate_usageUpdateWithoutCost(t *testing.T) {
	t.Parallel()
	upd := map[string]any{"sessionUpdate": "usage_update", "used": jsonNumber("77"), "size": jsonNumber("200000")}
	evs, err := mapSessionUpdateToEvents(context.Background(), mergeMapperOptions(Config{}), upd)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != aipapi.EventUsageDelta {
		t.Fatalf("got %#v", evs)
	}
	if evs[0].TotalTokens != 77 || evs[0].CostPresent {
		t.Fatalf("got %#v", evs[0])
	}
}

func TestMapSessionUpdate_usageUpdateCarriesRateLimit(t *testing.T) {
	t.Parallel()
	upd := map[string]any{
		"sessionUpdate": "usage_update",
		"used":          jsonNumber("500"),
		"size":          jsonNumber("200000"),
		"_meta": map[string]any{"_claude/rateLimit": map[string]any{
			"status": "rejected", "resetsAt": jsonNumber("1760000000"), "rateLimitType": "five_hour",
		}},
	}
	evs, err := mapSessionUpdateToEvents(context.Background(), mergeMapperOptions(Config{}), upd)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("got %#v", evs)
	}
	raw := evs[0].RawUsageJSON
	for _, want := range []string{`"rejected"`, `"five_hour"`, "1760000000"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("raw %q must carry %q", raw, want)
		}
	}
}

func TestMapSessionUpdate_usageUpdateDropsBadRateLimit(t *testing.T) {
	t.Parallel()
	upd := map[string]any{
		"sessionUpdate": "usage_update",
		"used":          jsonNumber("500"),
		"_meta":         map[string]any{"_claude/rateLimit": map[string]any{"status": "meltdown"}},
	}
	evs, err := mapSessionUpdateToEvents(context.Background(), mergeMapperOptions(Config{}), upd)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || strings.Contains(evs[0].RawUsageJSON, "rateLimit") {
		t.Fatalf("bad rate-limit block must drop, got %#v", evs)
	}
}

func TestMapSessionUpdate_usageUpdateMissingUsedDrops(t *testing.T) {
	t.Parallel()
	for _, upd := range []map[string]any{
		{"sessionUpdate": "usage_update", "size": jsonNumber("200000")},
		{"sessionUpdate": "usage_update", "used": "1234"},
		{"sessionUpdate": "usage_update", "used": jsonNumber("-5")},
		{"sessionUpdate": "usage_update", "used": 12.5},
	} {
		evs, err := mapSessionUpdateToEvents(context.Background(), mergeMapperOptions(Config{}), upd)
		if err != nil {
			t.Fatal(err)
		}
		if len(evs) != 0 {
			t.Fatalf("invalid usage_update must drop, got %#v", evs)
		}
	}
}

// Counters are exact: non-canonical spellings, fractions and values past the
// representable bound are dropped, never clamped or synthesized.
func TestMapSessionUpdate_usageUpdateRejectsNonExactAndOversizedCounters(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		used any
	}{
		{name: "non-canonical plus sign", used: jsonNumber("+1")},
		{name: "non-integer spelling", used: jsonNumber("1.0")},
		{name: "above the representable bound", used: jsonNumber("9007199254740992")},
		{name: "above the representable bound as float", used: float64(1 << 53)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upd := map[string]any{"sessionUpdate": "usage_update", "used": tc.used}
			evs, err := mapSessionUpdateToEvents(context.Background(), mergeMapperOptions(Config{}), upd)
			if err != nil {
				t.Fatal(err)
			}
			if len(evs) != 0 {
				t.Fatalf("counter %v must be dropped, got %#v", tc.used, evs)
			}
		})
	}
	// The documented bound is exactly representable and must survive intact.
	upd := map[string]any{"sessionUpdate": "usage_update", "used": jsonNumber("9007199254740991")}
	evs, err := mapSessionUpdateToEvents(context.Background(), mergeMapperOptions(Config{}), upd)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || !evs[0].UsagePresence.TotalTokens || evs[0].TotalTokens != 9007199254740991 {
		t.Fatalf("bound counter must map exactly, got %#v", evs)
	}
}

// An invalid cost block drops the cost fields only; the adapter-reported token
// counter still reaches canonical usage.
func TestMapSessionUpdate_usageUpdateDropsInvalidCostKeepsTokens(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		cost any
	}{
		{name: "negative amount", cost: map[string]any{"amount": -0.01, "currency": "USD"}},
		{name: "over-long currency", cost: map[string]any{"amount": 0.25, "currency": strings.Repeat("U", 17)}},
		{name: "blank currency", cost: map[string]any{"amount": 0.25, "currency": "   "}},
		{name: "non-numeric amount", cost: map[string]any{"amount": "0.25", "currency": "USD"}},
		{name: "non-finite amount", cost: map[string]any{"amount": math.Inf(1), "currency": "USD"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upd := map[string]any{"sessionUpdate": "usage_update", "used": jsonNumber("77"), "cost": tc.cost}
			evs, err := mapSessionUpdateToEvents(context.Background(), mergeMapperOptions(Config{}), upd)
			if err != nil {
				t.Fatal(err)
			}
			if len(evs) != 1 || evs[0].TotalTokens != 77 || !evs[0].UsagePresence.TotalTokens {
				t.Fatalf("invalid cost must keep tokens: %#v", evs)
			}
			if evs[0].CostPresent || evs[0].CostNanoUnits != 0 || evs[0].Currency != "" {
				t.Fatalf("invalid cost %v must drop the cost fields: %#v", tc.cost, evs[0])
			}
		})
	}
}

// Model attribution prefers the adapter-scoped key, trims it, and drops values
// past the bound instead of truncating them into the raw usage JSON.
func TestMapSessionUpdate_usageUpdateModelAttributionIsTrimmedAndBounded(t *testing.T) {
	t.Parallel()
	ev, ok := mapUsageUpdateToEvent(map[string]any{
		"used":  jsonNumber("5"),
		"_meta": map[string]any{"model": "fallback", "_claude/model": " claude-sonnet "},
	})
	if !ok {
		t.Fatal("valid counter must map")
	}
	if want := `{"used":5,"model":"claude-sonnet"}`; ev.RawUsageJSON != want {
		t.Fatalf("raw usage = %q, want %q", ev.RawUsageJSON, want)
	}
	ev, ok = mapUsageUpdateToEvent(map[string]any{
		"used":  jsonNumber("5"),
		"_meta": map[string]any{"model": strings.Repeat("m", maxUsageModelLen+1)},
	})
	if !ok || strings.Contains(ev.RawUsageJSON, "model") {
		t.Fatalf("over-bound attribution must drop: %q", ev.RawUsageJSON)
	}
}

// Rate-limit metadata is bounded: out-of-range resets and unclean type strings
// drop that field, while the status block still travels.
func TestMapSessionUpdate_usageUpdateBoundsRateLimitMetadata(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		typ  string
	}{
		{name: "control character", typ: "five\rhours"},
		{name: "space", typ: "five hours"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev, ok := mapUsageUpdateToEvent(map[string]any{
				"used": jsonNumber("5"),
				"_meta": map[string]any{"_claude/rateLimit": map[string]any{
					"status":        "allowed",
					"resetsAt":      float64(1 << 53),
					"rateLimitType": tc.typ,
				}},
			})
			if !ok {
				t.Fatal("valid counter must map")
			}
			if want := `{"used":5,"rateLimit":{"status":"allowed"}}`; ev.RawUsageJSON != want {
				t.Fatalf("raw usage = %q, want %q", ev.RawUsageJSON, want)
			}
		})
	}
}

func TestParseNDJSONLine_terminalResultUsageMapsFullSplit(t *testing.T) {
	t.Parallel()
	line := `{"jsonrpc":"2.0","id":7,"result":{"stopReason":"end_turn","usage":{"inputTokens":100,"outputTokens":50,"cachedReadTokens":20,"cachedWriteTokens":10,"totalTokens":180},"_meta":{"quota":{"token_count":{"totalTokens":180}}}}}`
	evs, err := parseNDJSONLine(context.Background(), mergeMapperOptions(Config{}), line, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Kind != aipapi.EventUsageDelta || evs[1].Kind != aipapi.EventResponseFinished {
		t.Fatalf("got %#v", evs)
	}
	got := evs[0]
	if got.InputTokens != 100 || got.OutputTokens != 50 || got.CacheReadTokens != 20 || got.CacheWriteTokens != 10 || got.TotalTokens != 180 {
		t.Fatalf("split = %+v", got)
	}
	if !got.UsagePresence.InputTokens || !got.UsagePresence.OutputTokens || !got.UsagePresence.CacheReadTokens || !got.UsagePresence.CacheWriteTokens || !got.UsagePresence.TotalTokens {
		t.Fatalf("presence = %+v", got.UsagePresence)
	}
}

func TestParseNDJSONLine_terminalResultWithoutUsageUnchanged(t *testing.T) {
	t.Parallel()
	line := `{"jsonrpc":"2.0","id":7,"result":{"stopReason":"end_turn"}}`
	evs, err := parseNDJSONLine(context.Background(), mergeMapperOptions(Config{}), line, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != aipapi.EventResponseFinished {
		t.Fatalf("got %#v", evs)
	}
}

func TestParseNDJSONLine_terminalResultGarbageUsageKeepsFinished(t *testing.T) {
	t.Parallel()
	line := `{"jsonrpc":"2.0","id":7,"result":{"stopReason":"end_turn","usage":{"inputTokens":"lots"}}}`
	evs, err := parseNDJSONLine(context.Background(), mergeMapperOptions(Config{}), line, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != aipapi.EventResponseFinished {
		t.Fatalf("garbage usage must not fail the turn, got %#v", evs)
	}
}

// Presence follows the counters that actually mapped: a partial terminal usage
// object yields a delta for the valid fields only.
func TestMapPromptResultUsage_partialCountersSetPresence(t *testing.T) {
	t.Parallel()
	ev, ok := mapPromptResultUsage(map[string]any{"usage": map[string]any{
		"totalTokens": jsonNumber("180"),
		"inputTokens": "lots",
	}})
	if !ok {
		t.Fatal("a valid counter must produce a usage delta")
	}
	if !ev.UsagePresence.TotalTokens || ev.TotalTokens != 180 {
		t.Fatalf("total = %d presence = %+v", ev.TotalTokens, ev.UsagePresence)
	}
	if ev.UsagePresence.InputTokens || ev.InputTokens != 0 {
		t.Fatalf("invalid counter must not set presence: %+v", ev.UsagePresence)
	}
	if want := `{"totalTokens":180}`; ev.RawUsageJSON != want {
		t.Fatalf("raw usage = %q, want %q", ev.RawUsageJSON, want)
	}
}
