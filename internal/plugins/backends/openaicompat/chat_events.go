package openaicompat

import (
	"encoding/json"
	"strings"

	legacybackend "github.com/aiproxer/aiproxer/internal/plugins/backends/openailegacy"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/respjson"
)

// ChatCompletionEvents converts a non-streaming ChatCompletion response into
// a canonical event slice. The caller owns the returned slice.
func ChatCompletionEvents(comp openai.ChatCompletion) []aipapi.Event {
	return legacybackend.CompletionEvents(comp)
}

// firstReasoningField returns the reasoning text from the first key (in order)
// whose extra field carries a usable raw JSON string. The boolean is true when
// such a field was found, so callers preserve short-circuit semantics.
func firstReasoningField(fields map[string]respjson.Field, requireNonSpace bool, keys ...string) (string, bool) {
	for _, key := range keys {
		f, ok := fields[key]
		if !ok {
			continue
		}
		raw := f.Raw()
		if raw == "" || raw == respjson.Null {
			continue
		}
		var s string
		if json.Unmarshal([]byte(raw), &s) != nil {
			continue
		}
		if requireNonSpace && strings.TrimSpace(s) == "" {
			continue
		}
		return s, true
	}
	return "", false
}

// ReasoningTextFromMessage extracts reasoning text from the "reasoning",
// "reasoning_content", or "reasoning_summary" extra fields of a
// ChatCompletionMessage.
func ReasoningTextFromMessage(msg openai.ChatCompletionMessage) string {
	if msg.JSON.ExtraFields == nil {
		return ""
	}
	if s, ok := firstReasoningField(msg.JSON.ExtraFields, false, "reasoning", "reasoning_content"); ok {
		return s
	}
	s, _ := firstReasoningField(msg.JSON.ExtraFields, true, "reasoning_summary")
	return s
}

// ReasoningTextFromChunkDelta extracts reasoning text from a streaming chunk
// delta's "reasoning", "reasoning_content", or "reasoning_summary" extra
// fields.
func ReasoningTextFromChunkDelta(delta openai.ChatCompletionChunkChoiceDelta) string {
	if delta.JSON.ExtraFields == nil {
		return ""
	}
	if s, ok := firstReasoningField(delta.JSON.ExtraFields, false, "reasoning", "reasoning_content"); ok {
		return s
	}
	s, _ := firstReasoningField(delta.JSON.ExtraFields, true, "reasoning_summary")
	return s
}
