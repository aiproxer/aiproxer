package codex

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func FuzzNativeHistoryBuilder_BoundedAndContentSafe(f *testing.F) {
	f.Add(`{"id":"r1","type":"reasoning","summary":[],"encrypted_content":"opaque"}`, uint8(0), "call-1", "lookup")
	f.Add(`not-json`, uint8(1), "", "")
	f.Add(`{"type":"reasoning","summary":[]}`, uint8(2), "call-2", "lookup")
	f.Add(`{"id":"r2","type":"reasoning","summary":[],"encrypted_content":"`+strings.Repeat("x", 256)+`"}`, uint8(3), "call-3", "lookup")

	f.Fuzz(func(t *testing.T, raw string, mutation uint8, callID, toolName string) {
		if len(raw) > aipapi.MaxReasoningOpaqueBytes*2 {
			raw = raw[:aipapi.MaxReasoningOpaqueBytes*2]
		}
		callID = boundedFuzzString(callID, "call-1", aipapi.MaxRefStringBytes)
		toolName = boundedFuzzString(toolName, "lookup", aipapi.MaxToolNameBytes)
		call := nativeHistoryFuzzCall(raw, mutation, callID, toolName)

		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("native history builder panicked: %v", recovered)
			}
		}()
		history, err := buildNativeHistory(call)
		if err != nil {
			assertNativeHistoryErrorSafe(t, err, raw)
			return
		}
		if len(history.Items) > aipapi.MaxItems || len(history.Fingerprints) != len(history.Items) {
			t.Fatalf("unbounded history: items=%d fingerprints=%d", len(history.Items), len(history.Fingerprints))
		}
		for _, fingerprint := range history.Fingerprints {
			if len(fingerprint) != 64 {
				t.Fatalf("fingerprint length = %d", len(fingerprint))
			}
		}
	})
}

func FuzzNativeHistoryBuilder_BoundaryMutation(f *testing.F) {
	f.Add(uint8(0), uint8(0))
	f.Add(uint8(1), uint8(2))
	f.Add(uint8(2), uint8(3))

	f.Fuzz(func(t *testing.T, mutation, discriminator uint8) {
		call := nativeHistoryTrajectoryCall([]aipapi.ToolDef{{Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}})
		switch mutation % 5 {
		case 0:
			call.Messages[1], call.Messages[2] = call.Messages[2], call.Messages[1]
		case 1:
			call.Messages[2].Parts[0].ToolCallID = "missing"
		case 2:
			call.Messages[1].Parts[1].Content = []byte(`{"type":"function_call","call_id":"` + string(rune('a'+discriminator%26)) + `","name":"lookup","arguments":{}}`)
		case 3:
			call.Messages[1].Parts[0].Reasoning.Dialect = aipapi.ReasoningDialectOpenAIChatTextV1
		case 4:
			call.Messages[1].Parts[1].Content = []byte(`{"type":"future_item","payload":"opaque"}`)
		}

		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("boundary mutation panicked: %v", recovered)
			}
		}()
		history, err := buildNativeHistory(call)
		if err != nil {
			assertNativeHistoryErrorSafe(t, err, "opaque")
			return
		}
		if len(history.SafeSplitIndices()) == 0 {
			t.Fatal("valid history has no safe split boundary")
		}
	})
}

func nativeHistoryFuzzCall(raw string, mutation uint8, callID, toolName string) *aipapi.Call {
	if callID == "" {
		callID = "call-1"
	}
	if toolName == "" {
		toolName = "lookup"
	}
	if mutation%3 == 0 {
		return &aipapi.Call{Items: []aipapi.Item{{
			Kind:   aipapi.ItemKindReasoning,
			Status: aipapi.ItemStatusCompleted,
			Reasoning: &aipapi.ReasoningItem{Reasoning: &aipapi.ReasoningPart{
				Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
				Opaque:  json.RawMessage(raw),
			}},
		}}}
	}
	return &aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("prompt")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
				{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1, Opaque: json.RawMessage(raw)}},
				{Kind: aipapi.PartJSON, ToolCallID: callID, ToolName: toolName, Content: json.RawMessage(`{"type":"function_call","call_id":"` + callID + `","name":"` + toolName + `","arguments":{}}`)},
			}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: callID, Content: json.RawMessage(`{"output":"result"}`)}}},
		},
	}
}

func boundedFuzzString(value, fallback string, maxLen int) string {
	if value == "" {
		return fallback
	}
	if len(value) > maxLen {
		return value[:maxLen]
	}
	return value
}

func assertNativeHistoryErrorSafe(t *testing.T, err error, payload string) {
	t.Helper()
	if err == nil || strings.TrimSpace(err.Error()) == "" {
		t.Fatal("expected a stable content-safe error")
	}
	if len(err.Error()) > 256 {
		t.Fatalf("error is not bounded: %d bytes", len(err.Error()))
	}
	// Very short fuzz values commonly occur inside fixed error category names;
	// only assert non-leakage for a distinctive payload.
	if len(payload) >= 16 && len(payload) < 256 && strings.Contains(err.Error(), payload) {
		t.Fatalf("payload leaked in error: %v", err)
	}
}
