package openailegacy_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func reasoningParts(msgs []aipapi.Message) []aipapi.Part {
	var out []aipapi.Part
	for _, m := range msgs {
		if m.Role != aipapi.RoleAssistant {
			continue
		}
		for _, p := range m.Parts {
			if p.Kind == aipapi.PartReasoning {
				out = append(out, p)
			}
		}
	}
	return out
}

func TestDecodeChat_assistantReasoningContent_RED(t *testing.T) {
	t.Parallel()
	body := readGolden(t, "create_assistant_reasoning_content.json")
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatalf("RED: decode assistant reasoning_content must succeed: %v", err)
	}
	parts := reasoningParts(d.Call.Messages)
	if len(parts) != 1 {
		t.Fatalf("RED: expected one PartReasoning, got %d parts: %+v", len(parts), d.Call.Messages)
	}
	r := parts[0].Reasoning
	if r == nil {
		t.Fatal("RED: PartReasoning.Reasoning must be set")
	}
	if r.Dialect != aipapi.ReasoningDialectOpenAIChatTextV1 {
		t.Fatalf("RED: dialect = %q, want %q", r.Dialect, aipapi.ReasoningDialectOpenAIChatTextV1)
	}
	if r.Text != "think" {
		t.Fatalf("RED: reasoning text = %q, want %q", r.Text, "think")
	}
}

func TestDecodeChat_assistantReasoningAlias_RED(t *testing.T) {
	t.Parallel()
	body := readGolden(t, "create_assistant_reasoning_alias.json")
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatalf("RED: decode assistant reasoning alias must succeed: %v", err)
	}
	parts := reasoningParts(d.Call.Messages)
	if len(parts) != 1 {
		t.Fatalf("RED: expected one PartReasoning, got %d parts: %+v", len(parts), d.Call.Messages)
	}
	r := parts[0].Reasoning
	if r == nil {
		t.Fatal("RED: PartReasoning.Reasoning must be set")
	}
	if r.Dialect != aipapi.ReasoningDialectOpenAIChatTextV1 {
		t.Fatalf("RED: dialect = %q, want %q", r.Dialect, aipapi.ReasoningDialectOpenAIChatTextV1)
	}
	if r.Text != "think" {
		t.Fatalf("RED: reasoning text = %q, want %q", r.Text, "think")
	}
}

func TestDecodeChat_assistantReasoningConflictReasoningContentWins_RED(t *testing.T) {
	t.Parallel()
	// Documented winner when both fields differ: official reasoning_content wins over reasoning alias.
	body := readGolden(t, "create_assistant_reasoning_conflict.json")
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatalf("RED: decode conflicting reasoning fields must succeed or validate deterministically: %v", err)
	}
	parts := reasoningParts(d.Call.Messages)
	if len(parts) != 1 {
		t.Fatalf("RED: expected PartReasoning when both fields present, got %d reasoning parts: %+v", len(parts), d.Call.Messages)
	}
	r := parts[0].Reasoning
	if r == nil {
		t.Fatal("RED: PartReasoning.Reasoning must be set")
	}
	if r.Text != "official" {
		t.Fatalf("RED: conflicting fields must pick reasoning_content (%q), got %q", "official", r.Text)
	}
}

func TestEncode_reasoningStream_characterization(t *testing.T) {
	t.Parallel()
	events := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningDelta, Delta: "think"},
		{Kind: aipapi.EventTextDelta, Delta: "answer"},
		{Kind: aipapi.EventResponseFinished},
	}
	call := &aipapi.Call{
		Route:      aipapi.RouteIntent{Selector: "stub:gpt-4o-mini"},
		Extensions: mustModelExt(t, "gpt-4o-mini"),
	}
	opts := openailegacy.EncodeOptions{CompletionID: "chatcmpl_reasoning_stream", CreatedAt: 1715620000}
	streamRec := httptest.NewRecorder()
	if err := openailegacy.WriteStreamSSE(t.Context(), streamRec, call, aipapi.NewFixedEventStream(events), opts); err != nil {
		t.Fatalf("WriteStreamSSE: %v", err)
	}
	streamBody := streamRec.Body.String()
	if !strings.Contains(streamBody, `"reasoning_content":"think"`) {
		t.Fatalf("stream chunks must carry reasoning_content where legal, got %s", streamBody)
	}
}

func TestEncode_reasoningNonStreamOutput_RED(t *testing.T) {
	t.Parallel()
	events := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningDelta, Delta: "think"},
		{Kind: aipapi.EventTextDelta, Delta: "answer"},
		{Kind: aipapi.EventResponseFinished},
	}
	call := &aipapi.Call{
		Route:      aipapi.RouteIntent{Selector: "stub:gpt-4o-mini"},
		Extensions: mustModelExt(t, "gpt-4o-mini"),
	}
	opts := openailegacy.EncodeOptions{CompletionID: "chatcmpl_reasoning_nonstream", CreatedAt: 1715620000}
	nsRec := httptest.NewRecorder()
	if err := openailegacy.WriteNonStreamJSON(t.Context(), nsRec, call, aipapi.NewFixedEventStream(events), opts); err != nil {
		t.Fatalf("WriteNonStreamJSON: %v", err)
	}
	var ns struct {
		Choices []struct {
			Message struct {
				Content          json.RawMessage `json:"content"`
				ReasoningContent string          `json:"reasoning_content"`
				Reasoning        string          `json:"reasoning"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(nsRec.Body.Bytes(), &ns); err != nil {
		t.Fatalf("nonstream json: %v", err)
	}
	if len(ns.Choices) != 1 {
		t.Fatalf("nonstream choices: %+v", ns.Choices)
	}
	got := ns.Choices[0].Message.ReasoningContent
	if got == "" {
		got = ns.Choices[0].Message.Reasoning
	}
	if got != "think" {
		t.Fatalf("RED: nonstream assistant message must include reasoning text, got %#v body=%s", ns.Choices[0].Message, nsRec.Body.String())
	}
}
