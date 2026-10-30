package anthropic

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestCommandCodeUsagePreservesPresenceAndProviderLineage(t *testing.T) {
	t.Parallel()
	zero := 0
	cache := 3
	reasoning := 2
	ev := commandcodeUsageEvent(&usageFields{
		InputTokens: &zero, OutputTokens: &zero, CacheReadInputTokens: &cache,
		ReasoningTokens: &reasoning, ServiceTier: "standard",
	}, "msg_123")
	if ev == nil || !ev.UsagePresence.InputTokens || !ev.UsagePresence.OutputTokens || !ev.UsagePresence.CacheReadTokens || !ev.UsagePresence.ReasoningTokens {
		t.Fatalf("present zero/cache/reasoning fields were lost: %+v", ev)
	}
	if ev.InputTokens != 0 || ev.OutputTokens != 0 || ev.CacheReadTokens != cache || ev.Accounting.ProviderRequestID != "msg_123" || ev.Accounting.ServiceContext != "standard" {
		t.Fatalf("usage mapping lost value or provider context: %+v", ev)
	}
}

func TestCommandCodeUsageRejectsNegativeAndAbsentFields(t *testing.T) {
	t.Parallel()
	negative := -1
	if ev := commandcodeUsageEvent(&usageFields{InputTokens: &negative}, "msg_bad"); ev != nil {
		t.Fatalf("negative provider usage should remain unavailable: %+v", ev)
	}
	if ev := commandcodeUsageEvent(&usageFields{}, "msg_absent"); ev != nil {
		t.Fatalf("absent provider usage should remain unavailable: %+v", ev)
	}
}

func TestCommandCodeNativeAnthropicFieldsRetainLifetimeAndServerToolUsage(t *testing.T) {
	t.Parallel()
	zero := 0
	search := 2
	u := &usageFields{
		InputTokens:   &zero,
		CacheCreation: &anthropicCacheCreationFields{Ephemeral5mInputTokens: &zero, Ephemeral1hInputTokens: &search},
		ServerToolUse: &anthropicServerToolUseFields{WebSearchRequests: &search},
	}
	ev := commandcodeUsageEvent(u, "msg-native")
	if ev == nil {
		t.Fatal("native usage event is nil")
	}
	measures := commandcodeNativeMeasures(ev.RawUsageJSON)
	if len(measures) != 3 {
		t.Fatalf("native Anthropic measures = %d, want lifetime 5m/1h and web search", len(measures))
	}
}

func TestCommandCodeAnthropicStreamBridgesSplitUsageAtTerminal(t *testing.T) {
	t.Parallel()
	body := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg-split","usage":{"input_tokens":11,"output_tokens":0,"cache_read_input_tokens":3}}}`,
		`data: {"type":"message_delta","usage":{"output_tokens":8}}`,
		`data: {"type":"message_delta","usage":{"input_tokens":11,"output_tokens":8,"cache_read_input_tokens":3}}`,
		`data: {"type":"message_stop"}`,
	}, "\n")
	stream := newManagedSSEStream(&http.Response{Body: io.NopCloser(strings.NewReader(body))})
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

func TestCommandCodeAnthropicInterruptedStreamFlushesCumulativeUsage(t *testing.T) {
	t.Parallel()
	body := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg-interrupted","usage":{"input_tokens":11,"output_tokens":0,"cache_read_input_tokens":3}}}`,
		`data: {"type":"message_delta","usage":{"output_tokens":8}}`,
	}, "\n")
	stream := newManagedSSEStream(&http.Response{Body: io.NopCloser(strings.NewReader(body))})
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

func TestCommandCodeAnthropicLegacyStreamRetainsCanonicalUsageKey(t *testing.T) {
	t.Parallel()
	body := `data: {"type":"message_start","message":{"id":"msg-legacy","usage":{"input_tokens":11,"output_tokens":0}}}`
	stream := newManagedSSEStream(&http.Response{Body: io.NopCloser(strings.NewReader(body))})
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

func TestCommandCodeUsage_DeclaresCacheExclusions(t *testing.T) {
	t.Parallel()
	input, cache, write := 300, 600, 120
	ev := commandcodeUsageEvent(&usageFields{InputTokens: &input, CacheReadInputTokens: &cache, CacheCreationInputTokens: &write}, "msg-excl")
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

func TestCommandCodeAnnotateUsage_DeclaresCacheExclusions(t *testing.T) {
	t.Parallel()
	input, cache := 300, 600
	events := []aipapi.Event{
		{Kind: aipapi.EventUsageDelta, InputTokens: input, CacheReadTokens: cache, UsagePresence: aipapi.UsagePresence{InputTokens: true, CacheReadTokens: true}},
		{Kind: aipapi.EventTextDelta, Delta: "hi"},
	}
	annotateProviderUsageEvents(events, "msg-annotate")
	if !events[0].Accounting.InputTokensExcludeCacheRead || !events[0].Accounting.InputTokensExcludeCacheWrite {
		t.Fatalf("annotated usage must declare both cache exclusions: %+v", events[0].Accounting)
	}
	if events[0].InputTokens != 300 || events[0].CacheReadTokens != 600 {
		t.Fatalf("annotation changed native counters: %+v", events[0])
	}
	if events[1].Accounting.InputTokensExcludeCacheRead || events[1].Accounting.InputTokensExcludeCacheWrite {
		t.Fatalf("non-usage event gained a cache exclusion: %+v", events[1].Accounting)
	}
}

func TestCommandCodeMergeAnthropicUsage_PreservesCacheExclusions(t *testing.T) {
	t.Parallel()
	input, cache, write, output := 300, 600, 120, 8
	start := commandcodeUsageEvent(&usageFields{InputTokens: &input, CacheReadInputTokens: &cache, CacheCreationInputTokens: &write}, "msg-merge")
	delta := commandcodeUsageEvent(&usageFields{OutputTokens: &output}, "msg-merge")
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

func TestCommandCodeAnthropicStreamBridgeCarriesCacheExclusions(t *testing.T) {
	t.Parallel()
	body := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg-excl-stream","usage":{"input_tokens":300,"output_tokens":0,"cache_read_input_tokens":600}}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		`data: {"type":"message_delta","usage":{"output_tokens":8}}`,
		`data: {"type":"message_stop"}`,
	}, "\n")
	stream := newManagedSSEStream(&http.Response{Body: io.NopCloser(strings.NewReader(body))})
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
