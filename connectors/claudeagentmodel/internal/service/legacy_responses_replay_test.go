package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/claudeagentmodel/internal/bridge"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func legacyResponsesWrapperReplayCall(prompt, assistant, callID, result string) *aipapi.Call {
	call := chatToolCallCall(prompt)
	wire, err := json.Marshal(map[string]any{
		"type":      "function_call",
		"call_id":   callID,
		"name":      "Read",
		"arguments": `{"file_path":"x"}`,
	})
	if err != nil {
		panic(err)
	}
	call.Messages = append(call.Messages,
		aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart(assistant)}},
		aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
			Kind:    aipapi.PartJSON,
			Content: wire,
		}}},
		aipapi.Message{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
			Kind:       aipapi.PartToolResult,
			ToolCallID: callID,
			Content:    json.RawMessage(`"` + result + `"`),
		}}},
	)
	return call
}

// TestS3_InjectedCallThenTextSuffixDoesNotReleaseAndRetryResumes proves the
// call-then-text shape cannot release a parked invocation when an extra
// assistant message follows the exact call. The retained pending call must
// still accept the original separate-text replay afterwards.
func TestS3_InjectedCallThenTextSuffixDoesNotReleaseAndRetryResumes(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "")
	defer func() { _ = eng.Close(context.Background()) }()
	openAndPark(t, eng, chatToolCallCall("judge suffix probe"))

	injected := legacyResponsesWrapperReplayCall("judge suffix probe", "let me look", "toolu_01ABC", "contents")
	result := injected.Messages[len(injected.Messages)-1]
	injected.Messages = append(injected.Messages[:len(injected.Messages)-1], aipapi.Message{
		Role:  aipapi.RoleAssistant,
		Parts: []aipapi.Part{aipapi.TextPart("CLIENT_INJECTED_TEXT_NOT_EMITTED_BY_MODEL")},
	}, result)
	stream, err := eng.Open(context.Background(), injected)
	if err == nil {
		events, drainErr := drainEvents(t, stream)
		if drainErr != nil {
			t.Fatal(drainErr)
		}
		var text strings.Builder
		for _, event := range events {
			if event.Kind == aipapi.EventTextDelta {
				text.WriteString(event.Delta)
			}
		}
		if strings.Contains(text.String(), "got:contents") {
			t.Fatalf("parked invocation released despite unproven additional assistant suffix; native output=%q", text.String())
		}
	} else {
		t.Logf("extra assistant suffix refused: %v", err)
	}

	retry := legacyResponsesWrapperReplayCall("judge suffix probe", "let me look", "toolu_01ABC", "contents")
	stream, err = eng.Open(context.Background(), retry)
	if err != nil {
		t.Fatalf("correct retry rejected after suffix mismatch: %v", err)
	}
	events, err := drainEvents(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, event := range events {
		if event.Kind == aipapi.EventTextDelta {
			text.WriteString(event.Delta)
		}
	}
	if !strings.Contains(text.String(), "got:contents") {
		t.Fatalf("correct retry did not resume the retained pending call: %q", text.String())
	}
}

// TestS3_EarlierSameCallResultDoesNotReleaseAndRetryResumes proves an earlier
// result for the committed call id is not silently dropped by releasing the
// final result. The request is refused, the parked call survives, and the
// correct retry resumes it.
func TestS3_EarlierSameCallResultDoesNotReleaseAndRetryResumes(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "")
	defer func() { _ = eng.Close(context.Background()) }()
	openAndPark(t, eng, chatToolCallCall("duplicate result probe"))

	parked, err := eng.sessions.Lookup("toolu_01ABC", "")
	if err != nil {
		t.Fatal(err)
	}

	duplicate := chatContinuationCall("duplicate result probe", "let me look", "toolu_01ABC", `"contents"`)
	resultMessage := duplicate.Messages[len(duplicate.Messages)-1]
	duplicate.Messages = append(duplicate.Messages[:len(duplicate.Messages)-1],
		aipapi.Message{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
			Kind:       aipapi.PartToolResult,
			ToolCallID: "toolu_01ABC",
			Text:       "FIRST_RESULT",
		}}},
		resultMessage)

	stream, openErr := eng.Open(context.Background(), duplicate)
	if openErr == nil {
		events, drainErr := drainEvents(t, stream)
		if drainErr != nil {
			t.Fatal(drainErr)
		}
		var text strings.Builder
		for _, event := range events {
			if event.Kind == aipapi.EventTextDelta {
				text.WriteString(event.Delta)
			}
		}
		if strings.Contains(text.String(), "got:contents") {
			t.Fatalf("parked invocation released while dropping the earlier result; native output=%q", text.String())
		}
		t.Fatalf("earlier same-call result was not refused: %v", eventKindsOf(events))
	}
	if code := terminalCode(t, openErr); !strings.Contains(code, bridge.CodeContinuationMismatch) {
		t.Fatalf("wrong refusal for earlier same-call result: %s", code)
	}
	if _, ok := parked.PendingSnapshot(); !ok {
		t.Fatal("refused duplicate consumed the original pending call")
	}

	stream, err = eng.Open(context.Background(), chatContinuationCall("duplicate result probe", "let me look", "toolu_01ABC", `"contents"`))
	if err != nil {
		t.Fatalf("correct retry rejected after duplicate-result refusal: %v", err)
	}
	events, err := drainEvents(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, event := range events {
		if event.Kind == aipapi.EventTextDelta {
			text.WriteString(event.Delta)
		}
	}
	if text.String() != "got:contents" {
		t.Fatalf("correct retry did not resume the retained pending call: %q", text.String())
	}
}

// TestS3_LegacyResponsesWrapperReplayPreservesHistory proves the legacy
// openairesponses canonical function_call envelope is replayed in order when no
// parked turn exists (for example after a proxy restart). It must degrade
// visibly, never reject the history as an unknown tool call.
func TestS3_LegacyResponsesWrapperReplayPreservesHistory(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "echo_prompt")
	defer func() { _ = eng.Close(context.Background()) }()
	call := legacyResponsesWrapperReplayCall("legacy replay", "let me look", "toolu_01ABC", "contents")
	stream, err := eng.Open(context.Background(), call)
	if err != nil {
		t.Fatalf("legacy wrapper replay was rejected: %v", err)
	}
	events, err := drainEvents(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	text := ""
	sawReplay := false
	for _, event := range events {
		if event.Kind == aipapi.EventTextDelta {
			text += event.Delta
		}
		if event.Kind == aipapi.EventWarning && event.WarningCode == "history_replay_degraded" {
			sawReplay = true
		}
	}
	callAt := strings.Index(text, "[assistant tool_call toolu_01ABC Read]\n{\"file_path\":\"x\"}")
	resultAt := strings.Index(text, "[tool_result toolu_01ABC]\ncontents")
	if callAt < 0 || resultAt < 0 || callAt > resultAt {
		t.Fatalf("replayed history lost the wrapper call/result order: %q", text)
	}
	if !sawReplay {
		t.Fatalf("missing history_replay_degraded: %v", eventKindsOf(events))
	}
}
