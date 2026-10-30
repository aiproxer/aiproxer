package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestMiniMaxAnthropicUsage_PreservesCacheReasoningAndPresentZero(t *testing.T) {
	raw := []byte(`{"type":"message_delta","message":{"id":"msg-1"},"usage":{"input_tokens":0,"output_tokens":7,"cache_read_input_tokens":3,"cache_creation_input_tokens":0,"reasoning_tokens":2}}`)
	var payload anthropicSSEPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	payload.usagePresent = true
	ev := minimaxAnthropicUsageEvent(payload.Usage, payload.usagePresent, payload.Message.ID)
	if ev == nil {
		t.Fatal("usage event missing")
	}
	if !ev.UsagePresence.InputTokens || ev.InputTokens != 0 || !ev.UsagePresence.CacheWriteTokens || ev.CacheWriteTokens != 0 {
		t.Fatalf("present zero lost: %+v", *ev)
	}
	if !ev.UsagePresence.CacheReadTokens || ev.CacheReadTokens != 3 || !ev.UsagePresence.ReasoningTokens || ev.ReasoningTokens != 2 {
		t.Fatalf("cache/reasoning mapping: %+v", *ev)
	}
	if ev.Accounting.ProviderRequestID != "msg-1" || ev.Accounting.Source != "provider_reported" {
		t.Fatalf("lineage: %+v", ev.Accounting)
	}
}

func TestMiniMaxUsage_MalformedOverflowWireValueFailsClosed(t *testing.T) {
	var payload anthropicSSEPayload
	if err := json.Unmarshal([]byte(`{"type":"message_delta","usage":{"output_tokens":999999999999999999999999999999999999999}}`), &payload); err == nil {
		t.Fatal("overflow usage value accepted")
	}
	negative := -1
	if ev := minimaxAnthropicUsageEvent(anthropicUsageFields{OutputTokens: &negative}, true, "msg-negative"); ev != nil {
		t.Fatalf("negative usage should remain unavailable: %+v", ev)
	}
}

func TestMiniMaxNativeAnthropicFieldsRetainLifetimeAndServerToolUsage(t *testing.T) {
	zero := 0
	search := 2
	u := anthropicUsageFields{
		InputTokens:   &zero,
		CacheCreation: &anthropicCacheCreationFields{Ephemeral5mInputTokens: &zero, Ephemeral1hInputTokens: &search},
		ServerToolUse: &anthropicServerToolUseFields{WebSearchRequests: &search},
	}
	ev := minimaxAnthropicUsageEvent(u, true, "msg-native")
	if ev == nil {
		t.Fatal("native usage event is nil")
	}
	measures := minimaxAnthropicNativeMeasures(ev.RawUsageJSON)
	if len(measures) != 3 {
		t.Fatalf("native Anthropic measures = %d, want lifetime 5m/1h and web search", len(measures))
	}
}

func TestMiniMaxAnthropicStreamBridgesSplitUsageAtTerminal(t *testing.T) {
	t.Parallel()
	body := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg-split","usage":{"input_tokens":11,"output_tokens":0,"cache_read_input_tokens":3}}}`,
		`data: {"type":"message_delta","usage":{"output_tokens":8}}`,
		`data: {"type":"message_delta","usage":{"input_tokens":11,"output_tokens":8,"cache_read_input_tokens":3}}`,
		`data: {"type":"message_stop"}`,
	}, "\n")
	stream := newAnthropicManagedSSEStream(&http.Response{Body: io.NopCloser(strings.NewReader(body))})
	defer func() { _ = stream.Close() }()
	for {
		ev, err := stream.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Accounting.DedupeKey != "" {
			t.Fatalf("canonical usage event retained durable dedupe key %q", ev.Accounting.DedupeKey)
		}
	}
	got := stream.DrainAccountingEvidence()
	if len(got) != 1 {
		t.Fatalf("terminal V1 evidence=%d, want one cumulative record", len(got))
	}
	if got[0].InputTokens == nil || *got[0].InputTokens != 11 || got[0].OutputTokens == nil || *got[0].OutputTokens != 8 || got[0].CacheReadTokens == nil || *got[0].CacheReadTokens != 3 {
		t.Fatalf("cumulative V1 usage=%+v", got[0])
	}
}

func TestMiniMaxAnthropicInterruptedStreamFlushesCumulativeUsage(t *testing.T) {
	t.Parallel()
	body := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg-interrupted","usage":{"input_tokens":11,"output_tokens":0,"cache_read_input_tokens":3}}}`,
		`data: {"type":"message_delta","usage":{"output_tokens":8}}`,
	}, "\n")
	stream := newAnthropicManagedSSEStream(&http.Response{Body: io.NopCloser(strings.NewReader(body))})
	defer func() { _ = stream.Close() }()
	for {
		_, err := stream.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	got := stream.DrainAccountingEvidence()
	if len(got) != 1 || got[0].InputTokens == nil || *got[0].InputTokens != 11 || got[0].OutputTokens == nil || *got[0].OutputTokens != 8 || got[0].CacheReadTokens == nil || *got[0].CacheReadTokens != 3 {
		t.Fatalf("interrupted cumulative V1 usage=%+v, want input=11 output=8 cache=3", got)
	}
}

func TestMiniMaxAnthropicLegacyStreamRetainsCanonicalUsageKey(t *testing.T) {
	body := `data: {"type":"message_start","message":{"id":"msg-legacy","usage":{"input_tokens":11,"output_tokens":0}}}`
	stream := newAnthropicManagedSSEStream(&http.Response{Body: io.NopCloser(strings.NewReader(body))})
	stream.SetEnabled(false)
	defer func() { _ = stream.Close() }()
	var usageSeen bool
	for {
		ev, err := stream.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == aipapi.EventUsageDelta {
			usageSeen = true
			if ev.Accounting.DedupeKey == "" {
				t.Fatal("legacy canonical usage lost its durable dedupe key")
			}
		}
	}
	if !usageSeen {
		t.Fatal("legacy canonical usage event was not delivered")
	}
	if got := stream.DrainAccountingEvidence(); len(got) != 0 {
		t.Fatalf("disabled sideband emitted %d records", len(got))
	}
}

func TestMiniMaxAnthropicUsage_DeclaresCacheExclusions(t *testing.T) {
	input, cache, write := 300, 600, 120
	ev := minimaxAnthropicUsageEvent(anthropicUsageFields{InputTokens: &input, CacheReadInputTokens: &cache, CacheCreationInputTokens: &write}, true, "msg-excl")
	if ev == nil {
		t.Fatal("usage event missing")
	}
	if !ev.Accounting.InputTokensExcludeCacheRead || !ev.Accounting.InputTokensExcludeCacheWrite {
		t.Fatalf("Anthropic input must declare both cache exclusions: %+v", ev.Accounting)
	}
	if ev.InputTokens != 300 || ev.CacheReadTokens != 600 || ev.CacheWriteTokens != 120 || !ev.UsagePresence.CacheWriteTokens || !strings.Contains(ev.RawUsageJSON, `"cache_creation_input_tokens":120`) {
		t.Fatalf("native counters, presence or raw evidence changed: %+v", ev)
	}
}

func TestMiniMaxMergeAnthropicUsage_PreservesCacheExclusions(t *testing.T) {
	input, cache, write, output := 300, 600, 120, 8
	start := minimaxAnthropicUsageEvent(anthropicUsageFields{InputTokens: &input, CacheReadInputTokens: &cache, CacheCreationInputTokens: &write}, true, "msg-merge")
	delta := minimaxAnthropicUsageEvent(anthropicUsageFields{OutputTokens: &output}, true, "msg-merge")
	if start == nil || delta == nil {
		t.Fatal("usage events missing")
	}
	merged := mergeAnthropicUsageEvent(*start, *delta)
	if !merged.Accounting.InputTokensExcludeCacheRead || !merged.Accounting.InputTokensExcludeCacheWrite {
		t.Fatalf("merge dropped a cache exclusion: %+v", merged.Accounting)
	}
	if merged.InputTokens != 300 || merged.CacheReadTokens != 600 || merged.CacheWriteTokens != 120 || merged.OutputTokens != 8 {
		t.Fatalf("merge changed native counters: %+v", merged)
	}
	flipped := mergeAnthropicUsageEvent(*delta, *start)
	undeclared := mergeAnthropicUsageEvent(aipapi.Event{}, aipapi.Event{}).Accounting
	if !flipped.Accounting.InputTokensExcludeCacheRead || !flipped.Accounting.InputTokensExcludeCacheWrite || undeclared.InputTokensExcludeCacheRead || undeclared.InputTokensExcludeCacheWrite {
		t.Fatalf("merge must keep a declared exclusion and must not invent one: %+v %+v", flipped.Accounting, undeclared)
	}
	readOnly, writeOnly := *start, *delta
	readOnly.Accounting.InputTokensExcludeCacheWrite = false
	writeOnly.Accounting.InputTokensExcludeCacheRead = false
	for _, pair := range [][2]aipapi.Event{{readOnly, writeOnly}, {writeOnly, readOnly}} {
		got := mergeAnthropicUsageEvent(pair[0], pair[1]).Accounting
		if !got.InputTokensExcludeCacheRead || !got.InputTokensExcludeCacheWrite {
			t.Fatalf("mixed merge lost an independent exclusion: %+v", got)
		}
	}
	plain := mergeAnthropicUsageEvent(aipapi.Event{Kind: aipapi.EventUsageDelta}, writeOnly).Accounting
	if plain.InputTokensExcludeCacheRead || !plain.InputTokensExcludeCacheWrite {
		t.Fatalf("mixed merge must not invent an undeclared exclusion: %+v", plain)
	}
}

func TestMiniMaxAnthropicStreamBridgeCarriesCacheExclusions(t *testing.T) {
	t.Parallel()
	body := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg-excl-stream","usage":{"input_tokens":300,"output_tokens":0,"cache_read_input_tokens":600}}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		`data: {"type":"message_delta","usage":{"output_tokens":8}}`,
		`data: {"type":"message_stop"}`,
	}, "\n")
	stream := newAnthropicManagedSSEStream(&http.Response{Body: io.NopCloser(strings.NewReader(body))})
	defer func() { _ = stream.Close() }()
	for {
		ev, err := stream.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == aipapi.EventTextDelta && (ev.Accounting.InputTokensExcludeCacheRead || ev.Accounting.InputTokensExcludeCacheWrite) {
			t.Fatalf("text event gained a cache exclusion: %+v", ev.Accounting)
		}
	}
	got := stream.DrainAccountingEvidence()
	if len(got) != 1 {
		t.Fatalf("terminal V1 evidence=%d, want one cumulative record", len(got))
	}
	if !got[0].InputTokensExcludeCacheRead || !got[0].InputTokensExcludeCacheWrite {
		t.Fatalf("V1 sideband lost a cache exclusion: %+v", got[0])
	}
	if got[0].InputTokens == nil || *got[0].InputTokens != 300 || got[0].CacheReadTokens == nil || *got[0].CacheReadTokens != 600 {
		t.Fatalf("cumulative V1 usage=%+v", got[0])
	}
}
