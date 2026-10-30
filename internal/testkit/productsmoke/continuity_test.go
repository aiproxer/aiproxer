//go:build productsmoke

package productsmoke

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const (
	continuityToolCallID = "call_round_trip_1"
	continuityToolArgs   = `{"location":"Paris"}`
	continuityToolResult = `{"temperature_c":18,"conditions":"sunny"}`
	continuityTurnTwo    = "The weather in Paris is 18C and sunny."
)

// toolRoundTripScript scripts the first turn's assistant tool call. The
// arguments arrive in two fragments so the client-side assembly is exercised.
func toolRoundTripScript() fakeScript {
	return fakeScript{
		Model: smokeNative,
		ToolCall: &fakeToolCall{
			ID:             continuityToolCallID,
			Name:           smokeToolName,
			Arguments:      continuityToolArgs,
			ArgumentDeltas: []string{`{"location":`, `"Paris"}`},
		},
		Usage: fakeUsage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18},
	}
}

// toolRoundTripContinuationScript scripts the second turn's text answer.
func toolRoundTripContinuationScript() fakeScript {
	return fakeScript{
		Model:  smokeNative,
		Deltas: []string{"The weather in Paris ", "is 18C and sunny."},
		Usage:  fakeUsage{PromptTokens: 21, CompletionTokens: 9, TotalTokens: 30},
	}
}

// assistantToolCallMessage is the assistant message the client sends back in
// turn two, copied from the tool call turn one streamed.
func assistantToolCallMessage() chatMessage {
	return chatMessage{
		Role:    "assistant",
		Content: "",
		ToolCalls: []chatToolCall{{
			ID:   continuityToolCallID,
			Type: "function",
			Function: chatToolCallFunction{
				Name:      smokeToolName,
				Arguments: continuityToolArgs,
			},
		}},
	}
}

// toolResultMessage is the client's tool result for that call.
func toolResultMessage() chatMessage {
	return chatMessage{
		Role:       "tool",
		Content:    continuityToolResult,
		ToolCallID: continuityToolCallID,
	}
}

// continuityTurnOneBody is the first-turn client request. Its bare canonical
// model resolves through the proxy's default route.
func continuityTurnOneBody() []byte {
	raw, err := json.Marshal(chatBody{
		Model:    smokeCanonical,
		Stream:   true,
		Messages: smokeMessages(),
		Tools:    smokeTools(),
	})
	if err != nil {
		panic(err)
	}
	return raw
}

// continuityTurnTwoBody is the second-turn client request: turn one's messages
// plus the assistant tool call and the tool result, in that order.
func continuityTurnTwoBody() []byte {
	messages := append(smokeMessages(), assistantToolCallMessage(), toolResultMessage())
	raw, err := json.Marshal(chatBody{
		Model:    smokeCanonical,
		Stream:   true,
		Messages: messages,
		Tools:    smokeTools(),
	})
	if err != nil {
		panic(err)
	}
	return raw
}

// TestProduct_Continuity proves a tool call and its result survive a second
// turn through the running proxy in order, without added or reordered history.
func TestProduct_Continuity(t *testing.T) {
	t.Run("tool_round_trip", testToolRoundTrip)
}

func testToolRoundTrip(t *testing.T) {
	t.Parallel()
	canary := newCanary(t)
	turnOneScript := toolRoundTripScript()
	provider := newOpenAILegacyProvider(t, canary, turnOneScript)
	h := startSUT(t, sutConfig{
		Canary:    canary,
		Frontends: []string{"openai-legacy"},
		Backends: []backendWiring{{
			ID:      smokeBackendID,
			Kind:    kindOpenAILegacyCompatible,
			BaseURL: provider.OpenAILegacyBaseURL(),
			Models:  []modelWiring{{Canonical: smokeCanonical, Native: smokeNative}},
		}},
		DefaultRoute: smokeModel,
	})

	turnOneWire := doJSON(t.Context(), h.BaseURL()+openAIChatPath, continuityTurnOneBody())
	turnOneFailures := failureContext{sut: h, provider: provider, wire: turnOneWire}
	if err := checkOpenAIStream(turnOneWire, turnOneScript, h.evidence.redact); err != nil {
		failf(t, turnOneFailures, "tool_round_trip turn 1: the client did not observe the scripted tool call: %v", err)
	}
	firstRequests := provider.Requests()
	if len(firstRequests) != 1 {
		failf(t, turnOneFailures, "provider received %d requests after turn 1, want exactly 1", len(firstRequests))
	}
	if err := checkTranslatedChat(firstRequests[0], smokeMessages(), smokeTools(), h.evidence.redact); err != nil {
		failf(t, turnOneFailures, "tool_round_trip turn 1: %v", err)
	}

	// Turn two changes only the provider's answer; the request history the
	// client sends is the witness under test.
	provider.SetScript(toolRoundTripContinuationScript())
	turnTwoScript := toolRoundTripContinuationScript()
	turnTwoWire := doJSON(t.Context(), h.BaseURL()+openAIChatPath, continuityTurnTwoBody())
	turnTwoFailures := failureContext{sut: h, provider: provider, wire: turnTwoWire}
	if err := checkOpenAIStream(turnTwoWire, turnTwoScript, h.evidence.redact); err != nil {
		failf(t, turnTwoFailures, "tool_round_trip turn 2: %v", err)
	}
	requests := provider.Requests()
	if len(requests) != 2 {
		failf(t, turnTwoFailures, "provider received %d requests, want 2 (turn 1 and turn 2, no retries)", len(requests))
	}
	if err := checkToolRoundTripHistory(requests[1], smokeMessages(), assistantToolCallMessage(), toolResultMessage(), smokeTools(), h.evidence.redact); err != nil {
		failf(t, turnTwoFailures, "tool_round_trip turn 2 history: %v", err)
	}
	h.evidence.write("provider-requests.txt", provider.RequestLog())
	summary := fmt.Sprintf(
		"turn_1_messages=%d turn_2_messages=%d tool_call_id=%s tool_result=%q provider_requests=%d\n",
		len(smokeMessages()), len(smokeMessages())+2, continuityToolCallID, continuityToolResult, len(requests))
	h.evidence.write("continuity.txt", summary)
	t.Logf("tool round trip passed: provider observed %d requests; evidence %s", len(requests), h.evidence.dir)
}

// checkToolRoundTripHistory asserts the provider's second-turn history equals
// the first-turn messages followed by the assistant tool call and the tool
// result, in order, with nothing added or reordered.
func checkToolRoundTripHistory(req providerRequest, firstTurn []chatMessage, assistant, toolResult chatMessage, wantTools []chatTool, redact redactor) error {
	if err := checkProviderIdentity(req, openAIChatPath); err != nil {
		return err
	}
	var got translatedChat
	if err := json.Unmarshal(req.Body, &got); err != nil {
		return fmt.Errorf("provider request body is not JSON: %v", err)
	}
	if got.Model != smokeNative {
		return fmt.Errorf("provider saw model %q, want the translated native id %q", got.Model, smokeNative)
	}
	wantCount := len(firstTurn) + 2
	if len(got.Messages) != wantCount {
		return fmt.Errorf("provider saw %d second-turn messages, want %d (turn-one history plus the assistant tool call and the tool result)",
			len(got.Messages), wantCount)
	}
	for i, want := range firstTurn {
		message := got.Messages[i]
		if message.Role != want.Role {
			return fmt.Errorf("message %d role %q, want turn-one role %q", i, message.Role, want.Role)
		}
		text, present, err := chatMessageText(message.Content)
		if err != nil {
			return fmt.Errorf("message %d content: %v", i, err)
		}
		if !present || text != want.Content {
			return fmt.Errorf("message %d content %q (present=%t), want turn-one content %q", i, text, present, want.Content)
		}
		if message.ToolCallID != "" || len(message.ToolCalls) > 0 {
			return fmt.Errorf("message %d carries tool fields added to turn-one history", i)
		}
	}
	assistantAt := len(firstTurn)
	gotAssistant := got.Messages[assistantAt]
	if gotAssistant.Role != assistant.Role {
		return fmt.Errorf("message %d role %q, want the assistant tool-call role %q", assistantAt, gotAssistant.Role, assistant.Role)
	}
	if text, present, err := chatMessageText(gotAssistant.Content); err != nil {
		return fmt.Errorf("message %d content: %v", assistantAt, err)
	} else if present && text != "" {
		return fmt.Errorf("message %d assistant tool-call content %q, want no text", assistantAt, excerpt(redact, text, wireFailureDetailExcerpt))
	}
	if gotAssistant.ToolCallID != "" {
		return fmt.Errorf("message %d assistant tool call carries a tool_call_id %q", assistantAt, gotAssistant.ToolCallID)
	}
	if len(gotAssistant.ToolCalls) != len(assistant.ToolCalls) {
		return fmt.Errorf("message %d carries %d tool calls, want %d", assistantAt, len(gotAssistant.ToolCalls), len(assistant.ToolCalls))
	}
	for i, want := range assistant.ToolCalls {
		call := gotAssistant.ToolCalls[i]
		if call.ID != want.ID {
			return fmt.Errorf("message %d tool call %d id %q, want %q", assistantAt, i, call.ID, want.ID)
		}
		if call.Type != want.Type {
			return fmt.Errorf("message %d tool call %d type %q, want %q", assistantAt, i, call.Type, want.Type)
		}
		if call.Function.Name != want.Function.Name {
			return fmt.Errorf("message %d tool call %d name %q, want %q", assistantAt, i, call.Function.Name, want.Function.Name)
		}
		if err := checkSameJSON(call.Function.Arguments, want.Function.Arguments); err != nil {
			return fmt.Errorf("message %d tool call %d arguments: %v", assistantAt, i, err)
		}
	}
	toolAt := assistantAt + 1
	gotTool := got.Messages[toolAt]
	if gotTool.Role != toolResult.Role {
		return fmt.Errorf("message %d role %q, want the tool-result role %q", toolAt, gotTool.Role, toolResult.Role)
	}
	if gotTool.ToolCallID != toolResult.ToolCallID {
		return fmt.Errorf("message %d tool_call_id %q, want %q", toolAt, gotTool.ToolCallID, toolResult.ToolCallID)
	}
	if text, present, err := chatMessageText(gotTool.Content); err != nil {
		return fmt.Errorf("message %d content: %v", toolAt, err)
	} else if !present || text != toolResult.Content {
		return fmt.Errorf("message %d tool result %q (present=%t), want %q", toolAt, text, present, toolResult.Content)
	}
	if len(gotTool.ToolCalls) > 0 {
		return fmt.Errorf("message %d tool result carries %d tool calls", toolAt, len(gotTool.ToolCalls))
	}
	return checkTranslatedTools(got.Tools, wantTools, redact)
}

// chatMessageText decodes a string message content, distinguishing an absent or
// null content from an empty string.
func chatMessageText(raw json.RawMessage) (string, bool, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "", false, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return "", false, err
	}
	return text, true, nil
}

// checkSameJSON compares two JSON documents semantically, so re-encoding does
// not make equal arguments differ.
func checkSameJSON(got, want string) error {
	var gotValue, wantValue any
	if err := json.Unmarshal([]byte(got), &gotValue); err != nil {
		return fmt.Errorf("observed arguments %q are not JSON: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		return fmt.Errorf("expected arguments %q are not JSON: %v", want, err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		return fmt.Errorf("arguments %s, want %s", got, want)
	}
	return nil
}
