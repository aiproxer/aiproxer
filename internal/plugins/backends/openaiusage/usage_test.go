package openaiusage

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/accounting"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

func TestChatUsageEvent_cacheAndProviderCost(t *testing.T) {
	t.Parallel()
	raw := `{
  "prompt_tokens": 11,
  "completion_tokens": 8,
  "total_tokens": 19,
  "prompt_tokens_details": {
    "cached_tokens": 3,
    "x_aip_cache_write_tokens": 2
  },
  "completion_tokens_details": {"reasoning_tokens": 5},
  "cost": 0.00014
}`
	var usage openai.CompletionUsage
	if err := json.Unmarshal([]byte(raw), &usage); err != nil {
		t.Fatal(err)
	}

	ev := ChatUsageEvent(usage)
	if ev.InputTokens != 11 || ev.OutputTokens != 8 {
		t.Fatalf("tokens: in=%d out=%d", ev.InputTokens, ev.OutputTokens)
	}
	if ev.CacheReadTokens != 3 || ev.CacheWriteTokens != 2 {
		t.Fatalf("cache: read=%d write=%d", ev.CacheReadTokens, ev.CacheWriteTokens)
	}
	if ev.ReasoningTokens != 5 || ev.TotalTokens != 19 {
		t.Fatalf("reasoning=%d total=%d", ev.ReasoningTokens, ev.TotalTokens)
	}
	wantPresence := aipapi.UsagePresence{
		InputTokens:      true,
		OutputTokens:     true,
		CacheReadTokens:  true,
		CacheWriteTokens: true,
		ReasoningTokens:  true,
		TotalTokens:      true,
	}
	if ev.UsagePresence != wantPresence {
		t.Fatalf("usage presence = %+v, want %+v", ev.UsagePresence, wantPresence)
	}
	if ev.CostNanoUnits != 140_000 {
		t.Fatalf("CostNanoUnits = %d, want 140000", ev.CostNanoUnits)
	}
	if ev.Currency != "USD" || ev.CostSource != accounting.CostSourceProviderReported {
		t.Fatalf("cost meta: currency=%q source=%q", ev.Currency, ev.CostSource)
	}
	if !ev.CostPresent {
		t.Fatal("CostPresent = false, want true")
	}
}

func TestResponsesUsageEvent_cacheAndProviderCost(t *testing.T) {
	t.Parallel()
	raw := `{
  "input_tokens": 11,
  "output_tokens": 8,
  "total_tokens": 19,
  "input_tokens_details": {
    "cached_tokens": 3,
    "x_aip_cache_write_tokens": 2
  },
  "output_tokens_details": {"reasoning_tokens": 5},
  "cost": 0.00014
}`
	var usage responses.ResponseUsage
	if err := json.Unmarshal([]byte(raw), &usage); err != nil {
		t.Fatal(err)
	}

	ev := ResponsesUsageEvent(usage)
	if ev.CacheReadTokens != 3 || ev.CacheWriteTokens != 2 {
		t.Fatalf("cache: read=%d write=%d", ev.CacheReadTokens, ev.CacheWriteTokens)
	}
	if !ev.UsagePresence.InputTokens || !ev.UsagePresence.OutputTokens || !ev.UsagePresence.TotalTokens {
		t.Fatalf("required usage presence missing: %+v", ev.UsagePresence)
	}
	if ev.CostNanoUnits != 140_000 {
		t.Fatalf("CostNanoUnits = %d, want 140000", ev.CostNanoUnits)
	}
	if ev.CostSource != accounting.CostSourceProviderReported {
		t.Fatalf("CostSource = %q", ev.CostSource)
	}
	if !ev.CostPresent {
		t.Fatal("CostPresent = false, want true")
	}
}

func TestChatUsageEvent_ignoresMissingProviderCost(t *testing.T) {
	t.Parallel()
	raw := `{"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3}`
	var usage openai.CompletionUsage
	if err := json.Unmarshal([]byte(raw), &usage); err != nil {
		t.Fatal(err)
	}

	ev := ChatUsageEvent(usage)
	if ev.CostNanoUnits != 0 || ev.CostSource != "" || ev.CostPresent {
		t.Fatalf("unexpected cost fields: %+v", ev)
	}
}

func TestChatUsageEvent_explicitZeroProviderCost(t *testing.T) {
	t.Parallel()
	raw := `{"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3, "cost": 0}`
	var usage openai.CompletionUsage
	if err := json.Unmarshal([]byte(raw), &usage); err != nil {
		t.Fatal(err)
	}

	ev := ChatUsageEvent(usage)
	if ev.CostNanoUnits != 0 || !ev.CostPresent {
		t.Fatalf("explicit zero cost: nano=%d present=%v", ev.CostNanoUnits, ev.CostPresent)
	}
	if ev.CostSource != accounting.CostSourceProviderReported || ev.Currency != "USD" {
		t.Fatalf("cost meta: currency=%q source=%q", ev.Currency, ev.CostSource)
	}
}

func TestProviderCostNanoUnits_roundsRationalExactly(t *testing.T) {
	t.Parallel()
	nano, ok := providerCostNanoUnits("0.0000000015")
	if !ok {
		t.Fatal("providerCostNanoUnits returned !ok")
	}
	if nano != 2 {
		t.Fatalf("nano = %d, want 2", nano)
	}
}

func TestChatUsageEvent_ignoresMalformedCacheWriteExtension(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"prompt_tokens_details":{"x_aip_cache_write_tokens":-1}}`,
		`{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"prompt_tokens_details":{"x_aip_cache_write_tokens":1.2}}`,
		`{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"prompt_tokens_details":{"x_aip_cache_write_tokens":1e999}}`,
	} {
		var usage openai.CompletionUsage
		if err := json.Unmarshal([]byte(raw), &usage); err != nil {
			t.Fatal(err)
		}
		ev := ChatUsageEvent(usage)
		if ev.UsagePresence.CacheWriteTokens || ev.CacheWriteTokens != 0 {
			t.Fatalf("malformed cache-write extension became present: %+v", ev)
		}
	}
}

// TestChatUsageEvent_standardCacheWriteNotDuplicatedByExtension pins the
// provenance split introduced by R7-C1E: the standard provider-family
// cache_write_tokens member is owned by the native usage mapping
// (NativeUsageMeasures) and must not also populate the host-neutral
// cache-write field, even when a conflicting x_aip_cache_write_tokens extension
// alias is present. That keeps exactly one cache-write measure per observation.
func TestChatUsageEvent_standardCacheWriteNotDuplicatedByExtension(t *testing.T) {
	t.Parallel()
	var usage openai.CompletionUsage
	raw := `{"prompt_tokens":11,"completion_tokens":9,"total_tokens":20,"prompt_tokens_details":{"cache_write_tokens":6,"x_aip_cache_write_tokens":9}}`
	if err := json.Unmarshal([]byte(raw), &usage); err != nil {
		t.Fatal(err)
	}
	ev := ChatUsageEvent(usage)
	if ev.UsagePresence.CacheWriteTokens || ev.CacheWriteTokens != 0 {
		t.Fatalf("standard cache_write_tokens must be native-mapping owned, not event-populated: %+v", ev)
	}
}

// TestResponsesUsageEvent_standardCacheWriteNotDuplicatedByExtension is the
// Responses counterpart: the standard input_tokens_details.cache_write_tokens
// member is native-mapping owned and the extension alias must not add a second,
// conflicting host-neutral cache-write value.
func TestResponsesUsageEvent_standardCacheWriteNotDuplicatedByExtension(t *testing.T) {
	t.Parallel()
	var usage responses.ResponseUsage
	raw := `{"input_tokens":11,"output_tokens":9,"total_tokens":20,"input_tokens_details":{"cache_write_tokens":6,"x_aip_cache_write_tokens":9}}`
	if err := json.Unmarshal([]byte(raw), &usage); err != nil {
		t.Fatal(err)
	}
	ev := ResponsesUsageEvent(usage)
	if ev.UsagePresence.CacheWriteTokens || ev.CacheWriteTokens != 0 {
		t.Fatalf("standard cache_write_tokens must be native-mapping owned, not event-populated: %+v", ev)
	}
}

func TestUsageEvent_RejectsNegativeProviderCounters(t *testing.T) {
	t.Parallel()
	var chat openai.CompletionUsage
	if err := json.Unmarshal([]byte(`{"prompt_tokens":-1,"completion_tokens":2,"total_tokens":1}`), &chat); err != nil {
		t.Fatal(err)
	}
	ev := ChatUsageEvent(chat)
	if ev.UsagePresence.InputTokens || ev.InputTokens != 0 {
		t.Fatalf("negative prompt count became present: %+v", ev)
	}
	var responsesUsage responses.ResponseUsage
	if err := json.Unmarshal([]byte(`{"input_tokens":1,"output_tokens":-2,"total_tokens":-1}`), &responsesUsage); err != nil {
		t.Fatal(err)
	}
	ev = ResponsesUsageEvent(responsesUsage)
	if ev.UsagePresence.OutputTokens || ev.UsagePresence.TotalTokens || ev.OutputTokens != 0 || ev.TotalTokens != 0 {
		t.Fatalf("negative response counts became present: %+v", ev)
	}
}

func TestProviderCostNanoUnits_RejectsOverflow(t *testing.T) {
	t.Parallel()
	if _, ok := providerCostNanoUnits("1e100"); ok {
		t.Fatal("overflow provider cost accepted")
	}
}
