package openailegacy

import (
	"encoding/json"
	"testing"

	"github.com/openai/openai-go/v3"

	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestHandleChunk_reasoningContentEmitsReasoningDelta_RED(t *testing.T) {
	t.Parallel()
	raw := `{"id":"cc","object":"chat.completion.chunk","created":1,"model":"moonshot-v1-8k","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"think","content":"hi"},"finish_reason":null}]}`
	var ch openai.ChatCompletionChunk
	if err := json.Unmarshal([]byte(raw), &ch); err != nil {
		t.Fatal(err)
	}
	s := &chatStream{}
	if err := s.handleChunk(ch); err != nil {
		t.Fatal(err)
	}
	var gotReason, gotText string
	for _, ev := range stream.DrainPending(&s.pending) {
		switch ev.Kind {
		case aipapi.EventReasoningDelta:
			gotReason += ev.Delta
		case aipapi.EventTextDelta:
			gotText += ev.Delta
		}
	}
	if gotReason != "think" {
		t.Fatalf("RED: stream must map reasoning_content to EventReasoningDelta, got %q", gotReason)
	}
	if gotText != "hi" {
		t.Fatalf("text=%q", gotText)
	}
}
