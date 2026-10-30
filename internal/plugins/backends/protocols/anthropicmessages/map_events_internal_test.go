package anthropicmessages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"

	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipsdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestHandleEvent_thinkingDeltaFromJSON(t *testing.T) {
	t.Parallel()
	raw := `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"reason-chunk"}}`
	var u anthropic.MessageStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	s := &msgStream{}
	_ = s.handleEvent(u)
	var got []string
	for _, ev := range stream.DrainPending(&s.pending) {
		if ev.Kind == aipapi.EventReasoningDelta {
			got = append(got, ev.Delta)
		}
	}
	if len(got) != 1 || got[0] != "reason-chunk" {
		t.Fatalf("reasoning deltas: %v", got)
	}
}

func TestHandleEvent_reasoningDeltaAliasesFromJSON(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "reasoning field", raw: `{"type":"content_block_delta","index":0,"delta":{"type":"reasoning_delta","reasoning":"reason-alias"}}`, want: "reason-alias"},
		{name: "text field", raw: `{"type":"content_block_delta","index":0,"delta":{"type":"reasoning_delta","text":"text-alias"}}`, want: "text-alias"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var u anthropic.MessageStreamEventUnion
			if err := json.Unmarshal([]byte(tc.raw), &u); err != nil {
				t.Fatal(err)
			}
			s := &msgStream{}
			if err := s.handleEvent(u); err != nil {
				t.Fatalf("handleEvent: %v", err)
			}
			var got []string
			for _, ev := range stream.DrainPending(&s.pending) {
				if ev.Kind == aipapi.EventReasoningDelta {
					got = append(got, ev.Delta)
				}
			}
			if len(got) != 1 || got[0] != tc.want {
				t.Fatalf("reasoning deltas: %v, want %q", got, tc.want)
			}
		})
	}
}

func TestHandleEvent_thinkingBlockStartMapsInitialReasoningAndSignature(t *testing.T) {
	t.Parallel()
	raw := `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"initial plan","signature":"sig-start"}}`
	var u anthropic.MessageStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	s := &msgStream{}
	if err := s.handleEvent(u); err != nil {
		t.Fatalf("handleEvent: %v", err)
	}
	events := stream.DrainPending(&s.pending)
	gotKinds := kindsOf(events)
	wantKinds := []aipapi.EventKind{
		aipapi.EventResponseStarted,
		aipapi.EventMessageStarted,
		aipapi.EventReasoningDelta,
		aipapi.EventReasoningSignatureDelta,
	}
	if !slices.Equal(gotKinds, wantKinds) {
		t.Fatalf("kinds = %v, want %v", gotKinds, wantKinds)
	}
	if events[2].Delta != "initial plan" || events[3].Signature != "sig-start" {
		t.Fatalf("thinking start events = %+v", events)
	}
}

func TestHandleEvent_reasoningBlockStartAliasMapsInitialReasoning(t *testing.T) {
	t.Parallel()
	raw := `{"type":"content_block_start","index":0,"content_block":{"type":"reasoning","reasoning":"initial alias"}}`
	var u anthropic.MessageStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	s := &msgStream{}
	if err := s.handleEvent(u); err != nil {
		t.Fatalf("handleEvent: %v", err)
	}
	var got []string
	for _, ev := range stream.DrainPending(&s.pending) {
		if ev.Kind == aipapi.EventReasoningDelta {
			got = append(got, ev.Delta)
		}
	}
	if len(got) != 1 || got[0] != "initial alias" {
		t.Fatalf("reasoning deltas: %v", got)
	}
}

func TestHandleEvent_signatureDeltaFromJSON(t *testing.T) {
	t.Parallel()
	raw := `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-123"}}`
	var u anthropic.MessageStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	s := &msgStream{}
	if err := s.handleEvent(u); err != nil {
		t.Fatalf("handleEvent: %v", err)
	}
	var got []string
	for _, ev := range stream.DrainPending(&s.pending) {
		if ev.Kind == aipapi.EventReasoningSignatureDelta {
			got = append(got, ev.Signature)
		}
	}
	if len(got) != 1 || got[0] != "sig-123" {
		t.Fatalf("signature deltas: %v", got)
	}
}

func TestHandleEvent_signatureDeltaEmptyIsNoOp(t *testing.T) {
	t.Parallel()
	raw := `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":""}}`
	var u anthropic.MessageStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	s := &msgStream{}
	if err := s.handleEvent(u); err != nil {
		t.Fatalf("handleEvent: %v", err)
	}
	for _, ev := range stream.DrainPending(&s.pending) {
		if ev.Kind == aipapi.EventReasoningSignatureDelta {
			t.Fatalf("unexpected signature event: %+v", ev)
		}
	}
}

func TestHandleEvent_thinkingDeltaEstablishesFrame(t *testing.T) {
	t.Parallel()
	raw := `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"plan"}}`
	var u anthropic.MessageStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	s := &msgStream{}
	if err := s.handleEvent(u); err != nil {
		t.Fatalf("handleEvent: %v", err)
	}
	got := kindsOf(stream.DrainPending(&s.pending))
	want := []aipapi.EventKind{aipapi.EventResponseStarted, aipapi.EventMessageStarted, aipapi.EventReasoningDelta}
	if !slices.Equal(got, want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
}

func TestHandleEvent_signatureDeltaEstablishesFrame(t *testing.T) {
	t.Parallel()
	raw := `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-xyz"}}`
	var u anthropic.MessageStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	s := &msgStream{}
	if err := s.handleEvent(u); err != nil {
		t.Fatalf("handleEvent: %v", err)
	}
	got := kindsOf(stream.DrainPending(&s.pending))
	want := []aipapi.EventKind{aipapi.EventResponseStarted, aipapi.EventMessageStarted, aipapi.EventReasoningSignatureDelta}
	if !slices.Equal(got, want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
}

func TestHandleEvent_redactedThinkingStartMapsOpaqueDelta(t *testing.T) {
	t.Parallel()
	raw := `{"type":"content_block_start","index":0,"content_block":{"type":"redacted_thinking","data":"opaque-blob"}}`
	var u anthropic.MessageStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	s := &msgStream{}
	if err := s.handleEvent(u); err != nil {
		t.Fatalf("handleEvent: %v", err)
	}
	var opaque []byte
	for _, ev := range stream.DrainPending(&s.pending) {
		if ev.Kind == aipapi.EventReasoningOpaqueDelta {
			opaque = ev.Opaque
		}
	}
	if len(opaque) == 0 {
		t.Fatal("RED: redacted_thinking start must map to EventReasoningOpaqueDelta")
	}
	var got struct {
		Type string `json:"type"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal(opaque, &got); err != nil {
		t.Fatalf("opaque envelope: %v", err)
	}
	if got.Type != "redacted_thinking" || got.Data != "opaque-blob" {
		t.Fatalf("opaque structural miss type_ok=%v data_ok=%v", got.Type == "redacted_thinking", got.Data == "opaque-blob")
	}
}

func kindsOf(events []aipapi.Event) []aipapi.EventKind {
	out := make([]aipapi.EventKind, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.Kind)
	}
	return out
}

func TestHandleEvent_assistantImageURLContentBlockStart(t *testing.T) {
	t.Parallel()
	raw := `{"type":"content_block_start","index":0,"content_block":{"type":"image","source":{"type":"url","url":"https://cdn.example.com/out.png"}}}`
	var u anthropic.MessageStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	s := &msgStream{}
	_ = s.handleEvent(u)
	var refs []string
	for _, ev := range stream.DrainPending(&s.pending) {
		if ev.Kind == aipapi.EventAssistantImageRef {
			refs = append(refs, ev.AssistantRef)
		}
	}
	if len(refs) != 1 || refs[0] != "https://cdn.example.com/out.png" {
		t.Fatalf("assistant image refs: %v", refs)
	}
}

func TestHandleEvent_assistantDocumentURLContentBlockStart(t *testing.T) {
	t.Parallel()
	raw := `{"type":"content_block_start","index":1,"content_block":{"type":"document","source":{"type":"url","url":"https://files.example.com/a.pdf","media_type":"application/pdf"},"title":"A"}}`
	var u anthropic.MessageStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	s := &msgStream{}
	_ = s.handleEvent(u)
	var got []aipapi.Event
	for _, ev := range stream.DrainPending(&s.pending) {
		if ev.Kind == aipapi.EventAssistantFileRef {
			got = append(got, ev)
		}
	}
	if len(got) != 1 || got[0].AssistantRef != "https://files.example.com/a.pdf" || got[0].AssistantMIME != "application/pdf" || got[0].AssistantName != "A" {
		t.Fatalf("assistant file event: %+v", got)
	}
}

//nolint:paralleltest // wire steps share msgStream; inner t.Run is for failure attribution only
func TestHandleEvent_toolUseStreamFromJSON(t *testing.T) {
	t.Parallel()
	events := []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_01","name":"get_weather","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"NYC\"}"}}`,
		`{"type":"content_block_stop","index":0}`,
	}
	s := &msgStream{}
	for i, raw := range events {
		t.Run(fmt.Sprintf("wire_step_%d", i), func(t *testing.T) {
			var u anthropic.MessageStreamEventUnion
			if err := json.Unmarshal([]byte(raw), &u); err != nil {
				t.Fatalf("unmarshal %q: %v", raw, err)
			}
			_ = s.handleEvent(u)
		})
	}
	var names []string
	var args strings.Builder
	for _, ev := range stream.DrainPending(&s.pending) {
		switch ev.Kind {
		case aipapi.EventToolCallStarted:
			names = append(names, ev.ToolName)
		case aipapi.EventToolCallArgsDelta:
			args.WriteString(ev.Delta)
		case aipapi.EventToolCallFinished:
			if ev.ToolCallID != "toolu_01" {
				t.Fatalf("finish id: %q", ev.ToolCallID)
			}
		}
	}
	if len(names) != 1 || names[0] != "get_weather" {
		t.Fatalf("tool names: %v", names)
	}
	if got := args.String(); got != `{"city":"NYC"}` {
		t.Fatalf("args concatenated: %q", got)
	}
}

func TestUsageFromMessageDelta_usageDetails(t *testing.T) {
	t.Parallel()
	raw := `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":11,"output_tokens":8,"cache_read_input_tokens":3,"cache_creation_input_tokens":4}}`
	var u anthropic.MessageStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	delta, ok := u.AsAny().(anthropic.MessageDeltaEvent)
	if !ok {
		t.Fatalf("event type: %T", u.AsAny())
	}

	ev := usageFromMessageDelta(delta)
	if ev == nil {
		t.Fatal("usage event is nil")
		return
	}
	if ev.InputTokens != 11 || ev.OutputTokens != 8 {
		t.Fatalf("usage tokens: in=%d out=%d", ev.InputTokens, ev.OutputTokens)
	}
	assertUsageIntField(t, *ev, "CacheReadTokens", 3)
	assertUsageIntField(t, *ev, "CacheWriteTokens", 4)
	assertUsageIntField(t, *ev, "TotalTokens", 19)
	assertUsageRawJSONContains(t, *ev, "cache_read_input_tokens")
	// Anthropic reports input_tokens outside the cache-read and the
	// cache-creation subsets, so both relations travel with the counters.
	if !ev.Accounting.InputTokensExcludeCacheRead || !ev.Accounting.InputTokensExcludeCacheWrite {
		t.Fatalf("accounting = %+v, want both cache exclusions", ev.Accounting)
	}
	if !ev.UsagePresence.InputTokens || !ev.UsagePresence.OutputTokens || !ev.UsagePresence.CacheReadTokens || !ev.UsagePresence.CacheWriteTokens {
		t.Fatalf("presence = %+v, want every reported counter present", ev.UsagePresence)
	}
	var col aipapi.Collected
	col.AccumulateUsage(*ev)
	if col.UncachedInputTokens() != 11 {
		t.Fatalf("uncached=%d want 11", col.UncachedInputTokens())
	}
	if col.InclusiveInputTokens() != 18 {
		t.Fatalf("inclusive=%d want 18", col.InclusiveInputTokens())
	}
}

func TestAnthropicUsageCountRejectsNegativeProviderValue(t *testing.T) {
	t.Parallel()
	if got, ok := anthropicUsageCount(-1, true); ok || got != 0 {
		t.Fatalf("negative count = %d/%v, want unavailable", got, ok)
	}
	if got, ok := anthropicUsageCount(7, false); !ok || got != 7 {
		t.Fatalf("non-zero typed value = %d/%v, want retained", got, ok)
	}
	if got, ok := anthropicUsageCount(0, false); ok || got != 0 {
		t.Fatalf("absent zero count = %d/%v, want unavailable", got, ok)
	}
}

func TestAnthropicSafeTotalRejectsOverflow(t *testing.T) {
	t.Parallel()
	maxInt := int(^uint(0) >> 1)
	if got, ok := anthropicSafeTotal(maxInt, 1); ok || got != 0 {
		t.Fatalf("overflow total = %d/%v, want unavailable", got, ok)
	}
}

func TestAnthropicEvidenceMapsCacheLifetimeAndServerTools(t *testing.T) {
	t.Parallel()
	raw := `{"type":"message_start","message":{"id":"msg-anthropic","type":"message","role":"assistant","content":[],"model":"claude","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":11,"output_tokens":8,"cache_read_input_tokens":3,"cache_creation_input_tokens":4,"cache_creation":{"ephemeral_5m_input_tokens":5,"ephemeral_1h_input_tokens":6},"output_tokens_details":{"thinking_tokens":2},"server_tool_use":{"web_search_requests":1,"web_fetch_requests":0},"service_tier":"priority"}}}`
	var union anthropic.MessageStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &union); err != nil {
		t.Fatal(err)
	}
	start, ok := union.AsAny().(anthropic.MessageStartEvent)
	if !ok {
		t.Fatalf("event type: %T", union.AsAny())
	}
	ev := usageFromMessageStart(start)
	if ev == nil {
		t.Fatal("usage event is nil")
	}
	if !ev.Accounting.InputTokensExcludeCacheRead || !ev.Accounting.InputTokensExcludeCacheWrite {
		t.Fatalf("accounting = %+v, want both cache exclusions", ev.Accounting)
	}
	draft := anthropicEvidenceDraft(*ev, start.Message.Usage, "anthropic.messages.v2")
	var col aipapi.Collected
	col.AccumulateUsage(*ev)
	if col.UncachedInputTokens() != 11 || ev.InputTokens != 11 || ev.CacheReadTokens != 3 {
		t.Fatalf("usage=%+v uncached=%d", ev, col.UncachedInputTokens())
	}
	if len(draft.Measures) != 9 {
		t.Fatalf("anthropic measures = %d, want five provider token/cache plus two lifetime and two server-tool entries", len(draft.Measures))
	}
	foundFive, foundHour, foundSearch, foundFetch := false, false, false, false
	for _, measure := range draft.Measures {
		for _, dimension := range measure.Key.Dimensions {
			switch dimension.Value {
			case "5m":
				foundFive = true
			case "1h":
				foundHour = true
			case "web_search":
				foundSearch = true
			case "web_fetch":
				foundFetch = true
			}
		}
	}
	if !foundFive || !foundHour || !foundSearch || !foundFetch {
		t.Fatalf("lifetime/server-tool measures missing: %+v", draft.Measures)
	}
}

func TestAnthropicSafeUsageEvidenceRejectsMalformedCounts(t *testing.T) {
	t.Parallel()
	raw := `{"cache_creation":{"ephemeral_5m_input_tokens":-1,"ephemeral_1h_input_tokens":1e100},"server_tool_use":{"web_fetch_requests":"oops","web_search_requests":0}}`
	var usage anthropic.Usage
	if err := json.Unmarshal([]byte(raw), &usage); err != nil {
		t.Fatal(err)
	}
	evidence := anthropicSafeUsageEvidence(usage)
	if len(evidence) != 1 || evidence[0].Path != "$.usage.web_search_requests" || evidence[0].Lexeme != "0" {
		t.Fatalf("safe evidence = %+v, want only valid present zero", evidence)
	}
}

func TestAnthropicEvidenceRetainsOnlyNativeTotalWhenSurfaced(t *testing.T) {
	t.Parallel()
	withoutTotal := `{"input_tokens":3,"output_tokens":2}`
	var delta anthropic.MessageDeltaUsage
	if err := json.Unmarshal([]byte(withoutTotal), &delta); err != nil {
		t.Fatal(err)
	}
	ev := usageFromMessageDelta(anthropic.MessageDeltaEvent{Usage: delta})
	if ev == nil {
		t.Fatal("usage event is nil")
	}
	draft := anthropicEvidenceDraft(*ev, delta, "anthropic.messages.v2")
	for _, measure := range draft.Measures {
		if measure.Key.Component == aipsdkmetering.ComponentTotalToken {
			t.Fatalf("derived total crossed provider evidence seam: %+v", measure)
		}
	}

	withTotal := `{"input_tokens":3,"output_tokens":2,"total_tokens":99}`
	if err := json.Unmarshal([]byte(withTotal), &delta); err != nil {
		t.Fatal(err)
	}
	ev = usageFromMessageDelta(anthropic.MessageDeltaEvent{Usage: delta})
	draft = anthropicEvidenceDraft(*ev, delta, "anthropic.messages.v2")
	found := false
	for _, measure := range draft.Measures {
		if measure.Key.Component == aipsdkmetering.ComponentTotalToken {
			found = measure.Value != nil && measure.Value.Coefficient == "99"
		}
	}
	if !found {
		t.Fatalf("surfaced total_tokens was not retained: %+v", draft.Measures)
	}
}

type errDecoderAnthropic struct{ err error }

type sequenceDecoderAnthropic struct {
	events []ssestream.Event
	index  int
}

func (d *sequenceDecoderAnthropic) Event() ssestream.Event {
	if d.index == 0 || d.index > len(d.events) {
		return ssestream.Event{}
	}
	return d.events[d.index-1]
}

func (d *sequenceDecoderAnthropic) Next() bool {
	if d.index >= len(d.events) {
		return false
	}
	d.index++
	return true
}

func (d *sequenceDecoderAnthropic) Close() error { return nil }

func (d *sequenceDecoderAnthropic) Err() error { return nil }

func TestMsgStream_ProviderUsageDeltaRetainsTypedValueWithoutSDKPresence(t *testing.T) {
	t.Parallel()
	dec := &sequenceDecoderAnthropic{events: []ssestream.Event{
		{Type: "message_start", Data: []byte(`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`)},
		{Type: "content_block_start", Data: []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)},
		{Type: "content_block_delta", Data: []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`)},
		{Type: "message_delta", Data: []byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`)},
		{Type: "message_stop", Data: []byte(`{"type":"message_stop"}`)},
	}}
	sdk := ssestream.NewStream[anthropic.MessageStreamEventUnion](dec, nil)
	es := newMessageStream(sdk, "anthropic", 0)
	var usage *aipapi.Event
	for {
		ev, err := es.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == aipapi.EventUsageDelta {
			copy := ev
			usage = &copy
		}
	}
	if usage == nil || usage.OutputTokens != 2 {
		t.Fatalf("usage=%+v, want output_tokens=2", usage)
	}
	if usage.InputTokens != 0 || usage.CacheReadTokens != 0 || usage.UsagePresence.InputTokens || usage.UsagePresence.CacheReadTokens {
		t.Fatalf("output-only delta invented input/cache usage: %+v", usage)
	}
}

func TestMsgStream_AnthropicSplitUsagePreservesCumulativeV2Evidence(t *testing.T) {
	t.Parallel()
	dec := &sequenceDecoderAnthropic{events: []ssestream.Event{
		{Type: "message_start", Data: []byte(`{"type":"message_start","message":{"id":"m-split","type":"message","role":"assistant","model":"claude","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":11,"output_tokens":0,"cache_read_input_tokens":3}}}`)},
		{Type: "message_delta", Data: []byte(`{"type":"message_delta","delta":{},"usage":{"output_tokens":8}}`)},
		// A later cumulative snapshot repeats the fields already observed. It
		// must not create a duplicate revision.
		{Type: "message_delta", Data: []byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":11,"output_tokens":8,"cache_read_input_tokens":3}}`)},
		{Type: "message_stop", Data: []byte(`{"type":"message_stop"}`)},
	}}
	sdk := ssestream.NewStream[anthropic.MessageStreamEventUnion](dec, nil)
	es := newMessageStream(sdk, "anthropic", 0)
	for {
		_, err := es.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	s, ok := es.(*msgStream)
	if !ok {
		t.Fatalf("newMessageStream returned %T", es)
	}
	s.BindEconomicEvidence(coremetering.ObservationIdentity{StoreID: "store", BLegID: "b-leg"})
	observations := s.DrainEconomicObservations()
	if len(observations) != 2 {
		t.Fatalf("split/cumulative observations=%d, want initial and final revision", len(observations))
	}
	final := observations[len(observations)-1]
	if !hasAnthropicMeasure(final, aipsdkmetering.ComponentInputToken, "11") ||
		!hasAnthropicMeasure(final, aipsdkmetering.ComponentOutputToken, "8") ||
		!hasAnthropicMeasure(final, aipsdkmetering.ComponentCacheReadInputToken, "3") {
		t.Fatalf("final cumulative measures=%+v", final.Measures)
	}
}

func TestMsgStream_StartOnlyInputProjectsCanonicalUsageWithoutDoubleCount(t *testing.T) {
	t.Parallel()
	dec := &sequenceDecoderAnthropic{events: []ssestream.Event{
		{Type: "message_start", Data: []byte(`{"type":"message_start","message":{"id":"m-start-only","type":"message","role":"assistant","model":"claude","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":11,"cache_read_input_tokens":3}}}`)},
		{Type: "content_block_start", Data: []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)},
		{Type: "content_block_delta", Data: []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`)},
		{Type: "message_delta", Data: []byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":8}}`)},
		{Type: "message_stop", Data: []byte(`{"type":"message_stop"}`)},
	}}
	sdk := ssestream.NewStream[anthropic.MessageStreamEventUnion](dec, nil)
	es := newMessageStream(sdk, "anthropic", 0)
	var kinds []aipapi.EventKind
	var usageEvents []aipapi.Event
	var col aipapi.Collected
	for {
		ev, err := es.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, ev.Kind)
		if ev.Kind == aipapi.EventTextDelta && (ev.Accounting.InputTokensExcludeCacheRead || ev.Accounting.InputTokensExcludeCacheWrite) {
			t.Fatalf("text event declared a cache relation: %+v", ev.Accounting)
		}
		if ev.Kind == aipapi.EventUsageDelta {
			usageEvents = append(usageEvents, ev)
			col.AccumulateUsage(ev)
		}
	}
	// Start-only input/cache must reach the client-visible stream exactly once.
	if col.InputTokens != 11 || col.OutputTokens != 8 || col.CacheReadTokens != 3 {
		t.Fatalf("collected input=%d output=%d cache_read=%d, want 11/8/3", col.InputTokens, col.OutputTokens, col.CacheReadTokens)
	}
	if len(usageEvents) != 2 {
		t.Fatalf("usage events=%d, want start projection plus delta increment", len(usageEvents))
	}
	first, second := usageEvents[0], usageEvents[1]
	if !first.UsagePresence.InputTokens || !first.UsagePresence.CacheReadTokens || first.UsagePresence.OutputTokens {
		t.Fatalf("start projection must carry input/cache only: %+v", first)
	}
	if first.InputTokens != 11 || first.CacheReadTokens != 3 {
		t.Fatalf("start projection=%+v, want input=11 cache_read=3", first)
	}
	if !second.UsagePresence.OutputTokens || second.UsagePresence.InputTokens || second.UsagePresence.CacheReadTokens {
		t.Fatalf("delta increment must carry output only: %+v", second)
	}
	for i, ev := range usageEvents {
		if ev.Accounting.DedupeKey != "anthropic.messages:stream" {
			t.Fatalf("usageEvents[%d].DedupeKey=%q, want stable stream key", i, ev.Accounting.DedupeKey)
		}
		if !ev.Accounting.InputTokensExcludeCacheRead || !ev.Accounting.InputTokensExcludeCacheWrite {
			t.Fatalf("usageEvents[%d].Accounting=%+v, want both cache exclusions", i, ev.Accounting)
		}
	}
	// Ordering: start usage immediately after the message frame, before content;
	// terminal ownership: the committed terminal still closes the stream.
	wantKinds := []aipapi.EventKind{aipapi.EventResponseStarted, aipapi.EventMessageStarted, aipapi.EventUsageDelta, aipapi.EventTextDelta, aipapi.EventUsageDelta, aipapi.EventResponseFinished}
	if !slices.Equal(kinds, wantKinds) {
		t.Fatalf("kinds=%v, want %v", kinds, wantKinds)
	}
}

func TestMsgStream_RepeatedCumulativeSnapshotsEmitCanonicalUsageOnce(t *testing.T) {
	t.Parallel()
	dec := &sequenceDecoderAnthropic{events: []ssestream.Event{
		{Type: "message_start", Data: []byte(`{"type":"message_start","message":{"id":"m-repeat","type":"message","role":"assistant","model":"claude","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":11,"cache_read_input_tokens":3}}}`)},
		{Type: "message_delta", Data: []byte(`{"type":"message_delta","delta":{},"usage":{"output_tokens":8}}`)},
		{Type: "message_delta", Data: []byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":11,"output_tokens":8,"cache_read_input_tokens":3}}`)},
		{Type: "message_stop", Data: []byte(`{"type":"message_stop"}`)},
	}}
	sdk := ssestream.NewStream[anthropic.MessageStreamEventUnion](dec, nil)
	es := newMessageStream(sdk, "anthropic", 0)
	var usageEvents []aipapi.Event
	var col aipapi.Collected
	sawTerminal := false
	for {
		ev, err := es.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == aipapi.EventResponseFinished {
			sawTerminal = true
		}
		if ev.Kind == aipapi.EventUsageDelta {
			usageEvents = append(usageEvents, ev)
			col.AccumulateUsage(ev)
		}
	}
	// The repeated cumulative snapshot must not emit a third canonical event:
	// additive collectors would otherwise count every quantity twice.
	if len(usageEvents) != 2 {
		t.Fatalf("usage events=%d, want start projection plus one delta increment", len(usageEvents))
	}
	if col.InputTokens != 11 || col.OutputTokens != 8 || col.CacheReadTokens != 3 {
		t.Fatalf("collected input=%d output=%d cache_read=%d, want 11/8/3", col.InputTokens, col.OutputTokens, col.CacheReadTokens)
	}
	if !sawTerminal {
		t.Fatal("missing committed terminal")
	}
	// Native V2 evidence still merges every wire frame into one cumulative
	// snapshot per revision.
	s, ok := es.(*msgStream)
	if !ok {
		t.Fatalf("newMessageStream returned %T", es)
	}
	s.BindEconomicEvidence(coremetering.ObservationIdentity{StoreID: "store", BLegID: "b-leg"})
	observations := s.DrainEconomicObservations()
	if len(observations) != 2 {
		t.Fatalf("observations=%d, want initial and final revision", len(observations))
	}
	final := observations[len(observations)-1]
	if !hasAnthropicMeasure(final, aipsdkmetering.ComponentInputToken, "11") ||
		!hasAnthropicMeasure(final, aipsdkmetering.ComponentOutputToken, "8") ||
		!hasAnthropicMeasure(final, aipsdkmetering.ComponentCacheReadInputToken, "3") {
		t.Fatalf("final cumulative measures=%+v", final.Measures)
	}
}

func TestMsgStream_AnthropicPartialUsageSurvivesInterruptedStream(t *testing.T) {
	t.Parallel()
	dec := &sequenceDecoderAnthropic{events: []ssestream.Event{
		{Type: "message_start", Data: []byte(`{"type":"message_start","message":{"id":"m-partial","type":"message","role":"assistant","model":"claude","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":11,"output_tokens":0,"cache_read_input_tokens":3}}}`)},
	}}
	sdk := ssestream.NewStream[anthropic.MessageStreamEventUnion](dec, nil)
	es := newMessageStream(sdk, "anthropic", 0)
	for {
		_, err := es.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	s, ok := es.(*msgStream)
	if !ok {
		t.Fatalf("message stream is %T, want *msgStream", es)
	}
	s.BindEconomicEvidence(coremetering.ObservationIdentity{StoreID: "store", BLegID: "b-leg"})
	observations := s.DrainEconomicObservations()
	if len(observations) != 1 || !hasAnthropicMeasure(observations[0], aipsdkmetering.ComponentInputToken, "11") || !hasAnthropicMeasure(observations[0], aipsdkmetering.ComponentCacheReadInputToken, "3") {
		t.Fatalf("partial observations=%+v, want input/cache evidence", observations)
	}
}

func hasAnthropicMeasure(observation aipsdkmetering.Observation, component, coefficient string) bool {
	for _, measure := range observation.Measures {
		if measure.Key.Component == component && measure.Value != nil && measure.Value.Coefficient == coefficient {
			return true
		}
	}
	return false
}

func (d *errDecoderAnthropic) Event() ssestream.Event {
	return ssestream.Event{Data: []byte(`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}`)}
}

func (d *errDecoderAnthropic) Next() bool { return false }

func (d *errDecoderAnthropic) Close() error { return nil }

func (d *errDecoderAnthropic) Err() error { return d.err }

func TestMsgStream_Recv_wrapsSDKErr(t *testing.T) {
	t.Parallel()
	root := errors.New("root")
	sdk := ssestream.NewStream[anthropic.MessageStreamEventUnion](&errDecoderAnthropic{err: root}, nil)
	es := newMessageStream(sdk, "anthropic", 0)
	s, ok := es.(*msgStream)
	if !ok {
		t.Fatalf("newMessageStream returned %T", es)
	}
	_, err := s.Recv(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "anthropic: recv stream") {
		t.Fatalf("got %q", err.Error())
	}
	if !errors.Is(err, root) {
		t.Fatalf("underlying: %v", err)
	}
}

func TestMsgStream_Recv_nilContext(t *testing.T) {
	t.Parallel()
	s := &msgStream{}
	_, err := s.Recv(nil) //nolint:staticcheck // deliberate nil ctx; expect aipapi.ErrNilContext
	if !errors.Is(err, aipapi.ErrNilContext) {
		t.Fatalf("got %v", err)
	}
}

func assertUsageIntField(t *testing.T, ev aipapi.Event, name string, want int64) {
	t.Helper()
	field := reflect.ValueOf(ev).FieldByName(name)
	if !field.IsValid() {
		return
	}
	if got := field.Int(); got != want {
		t.Fatalf("%s: got %d, want %d", name, got, want)
	}
}

func assertUsageRawJSONContains(t *testing.T, ev aipapi.Event, needle string) {
	t.Helper()
	field := reflect.ValueOf(ev).FieldByName("RawUsageJSON")
	if !field.IsValid() {
		return
	}
	switch field.Kind() {
	case reflect.String:
		if !strings.Contains(field.String(), needle) {
			t.Fatalf("RawUsageJSON: %q does not contain %q", field.String(), needle)
		}
	case reflect.Slice:
		if !strings.Contains(string(field.Bytes()), needle) {
			t.Fatalf("RawUsageJSON: %q does not contain %q", string(field.Bytes()), needle)
		}
	}
}
