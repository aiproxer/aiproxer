package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"

	"github.com/aiproxer/aiproxer/connectors/claudeagentmodel/internal/bridge"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// responsesItemCall mirrors the canonical item-authority shape produced by the
// openresponses frontend for an OpenAI Responses request with instructions and
// one user message. The frontend owns wire decoding; this fixture is the
// canonical envelope the connector receives.
func responsesItemCall(prompt string) *aipapi.Call {
	return &aipapi.Call{
		ID:         "responses-turn",
		Route:      aipapi.RouteIntent{Selector: "anthropic/sonnet"},
		Tools:      chatToolCallCall(prompt).Tools,
		Items:      []aipapi.Item{responsesUserItem(prompt)},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
	}
}

func responsesUserItem(text string) aipapi.Item {
	return aipapi.Item{
		Kind:    aipapi.ItemKindMessage,
		Status:  aipapi.ItemStatusCompleted,
		Role:    aipapi.RoleUser,
		Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: text}},
	}
}

func responsesAssistantItem(text string) aipapi.Item {
	return aipapi.Item{
		Kind:    aipapi.ItemKindMessage,
		Status:  aipapi.ItemStatusCompleted,
		Role:    aipapi.RoleAssistant,
		Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: text}},
	}
}

func responsesToolCallItem(callID, name, args string) aipapi.Item {
	// The pinned OpenAI Responses wire carries function_call.arguments as a JSON
	// string containing the argument document. Keep that exact decoder shape in
	// the fixture; the connector's correlation normalizes it to the document.
	encoded, err := json.Marshal(args)
	if err != nil {
		panic(err)
	}
	return aipapi.Item{
		Kind:   aipapi.ItemKindToolCall,
		Status: aipapi.ItemStatusCompleted,
		ToolCall: &aipapi.ToolCallItem{
			CallID:    callID,
			Name:      name,
			Arguments: encoded,
		},
	}
}

func responsesToolResultItem(callID, name, output string) aipapi.Item {
	return aipapi.Item{
		Kind:   aipapi.ItemKindToolResult,
		Status: aipapi.ItemStatusCompleted,
		ToolResult: &aipapi.ToolResultItem{
			CallID: callID,
			Name:   name,
			Output: output,
		},
	}
}

func responsesContinuation(prompt, assistantText, callID, result string) *aipapi.Call {
	call := responsesItemCall(prompt)
	call.ID = "responses-continuation"
	call.Items = []aipapi.Item{
		responsesUserItem(prompt),
		responsesAssistantItem(assistantText),
		responsesToolCallItem(callID, "Read", `{"file_path":"x"}`),
		responsesToolResultItem(callID, "Read", result),
	}
	call.PreviousResponseID = "resp_parent"
	call.Extensions = map[string]json.RawMessage{
		aipcont.MaterializerProvenanceExtension: json.RawMessage(`true`),
	}
	return call
}

// geminiContinuation mirrors the canonical shape produced by the Gemini
// frontend: functionCall carries only a name (ToolCallID is empty) and
// functionResponse uses the tool name as its correlation key.
func geminiContinuation(prompt, assistantText, name, args, result string) *aipapi.Call {
	call := chatToolCallCall(prompt)
	call.ID = "gemini-continuation"
	call.Messages = append(call.Messages,
		aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
			aipapi.TextPart(assistantText),
			{Kind: aipapi.PartJSON, ToolName: name, Content: json.RawMessage(args)},
		}},
		aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{{
			Kind:       aipapi.PartToolResult,
			ToolCallID: name,
			ToolName:   name,
			Text:       result,
		}}},
	)
	return call
}

func terminalCode(t *testing.T, err error) string {
	t.Helper()
	var terminal backendplugin.TerminalError
	if !errors.As(err, &terminal) {
		t.Fatalf("expected a terminal error, got %v", err)
	}
	return terminal.TerminalPluginError().Message
}

func TestS3_ResponsesItemLoopResumesParkedTurn(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "")
	defer func() { _ = eng.Close(context.Background()) }()
	openAndPark(t, eng, responsesItemCall("read the file"))

	continuation := responsesContinuation("read the file", "let me look", "toolu_01ABC", "contents")
	stream, err := eng.Open(context.Background(), continuation)
	if err != nil {
		t.Fatal(err)
	}
	events, err := drainEvents(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	text := ""
	for _, event := range events {
		if event.Kind == aipapi.EventTextDelta {
			text += event.Delta
		}
	}
	if text != "got:contents" {
		t.Fatalf("text=%q events=%v", text, eventKindsOf(events))
	}
	if last := events[len(events)-1]; last.Kind != aipapi.EventResponseFinished || last.FinishReason != "end_turn" {
		t.Fatalf("terminal=%+v", last)
	}
	if _, err := eng.sessions.Lookup("toolu_01ABC", ""); err == nil {
		t.Fatal("the item call must be consumed after release")
	}
}

func TestS3_ResponsesPreviousResponseRequiresMaterializedPrefix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*aipapi.Call)
	}{
		{
			name: "not materialized",
			mutate: func(call *aipapi.Call) {
				call.Extensions = nil
			},
		},
		{
			name: "truncated item history",
			mutate: func(call *aipapi.Call) {
				call.Items = []aipapi.Item{
					responsesUserItem("read the file"),
					responsesAssistantItem("let me look"),
					responsesToolResultItem("toolu_01ABC", "Read", "contents"),
				}
			},
		},
		{
			name: "unresolved item reference",
			mutate: func(call *aipapi.Call) {
				call.Items = append([]aipapi.Item{{
					Kind:      aipapi.ItemKindItemReference,
					ID:        "item_ref_1",
					Reference: &aipapi.ItemReference{ID: "prior_item"},
				}}, call.Items...)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eng := toolEngine(t, "")
			defer func() { _ = eng.Close(context.Background()) }()
			openAndPark(t, eng, responsesItemCall("read the file"))
			continuation := responsesContinuation("read the file", "let me look", "toolu_01ABC", "contents")
			tc.mutate(continuation)
			_, err := eng.Open(context.Background(), continuation)
			if !strings.Contains(terminalCode(t, err), bridge.CodeHistoryUnavailable) {
				t.Fatalf("err=%v", err)
			}
			if _, err := eng.sessions.Lookup("toolu_01ABC", ""); err != nil {
				t.Fatalf("the parked call must not be released: %v", err)
			}
		})
	}
}

func TestS3_ResponsesItemTamperRefusesThenCorrectRetrySucceeds(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "")
	defer func() { _ = eng.Close(context.Background()) }()
	openAndPark(t, eng, responsesItemCall("read the file"))

	tampered := []struct {
		name   string
		mutate func(*aipapi.Call)
	}{
		{
			name: "reordered",
			mutate: func(call *aipapi.Call) {
				call.Items[1], call.Items[2] = call.Items[2], call.Items[1]
			},
		},
		{
			name: "changed arguments",
			mutate: func(call *aipapi.Call) {
				call.Items[2].ToolCall.Arguments = json.RawMessage(`{"file_path":"/etc/passwd"}`)
			},
		},
		{
			name: "duplicated call",
			mutate: func(call *aipapi.Call) {
				call.Items = append(call.Items[:3], append([]aipapi.Item{responsesToolCallItem("toolu_01ABC", "Read", `{"file_path":"x"}`)}, call.Items[3:]...)...)
			},
		},
	}
	for _, tc := range tampered {
		t.Run(tc.name, func(t *testing.T) {
			call := responsesContinuation("read the file", "let me look", "toolu_01ABC", "contents")
			tc.mutate(call)
			_, err := eng.Open(context.Background(), call)
			if err == nil || !strings.Contains(terminalCode(t, err), bridge.CodeContinuationMismatch) {
				t.Fatalf("err=%v", err)
			}
			if _, err := eng.sessions.Lookup("toolu_01ABC", ""); err != nil {
				t.Fatalf("the parked call must survive a refused continuation: %v", err)
			}
		})
	}

	// The exact request still releases the untouched parked turn.
	stream, err := eng.Open(context.Background(), responsesContinuation("read the file", "let me look", "toolu_01ABC", "contents"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drainEvents(t, stream); err != nil {
		t.Fatal(err)
	}

	// A late duplicate is typed rather than replayed.
	_, err = eng.Open(context.Background(), responsesContinuation("read the file", "let me look", "toolu_01ABC", "contents"))
	if err == nil || !strings.Contains(terminalCode(t, err), bridge.CodeContinuationConsumed) {
		t.Fatalf("late duplicate err=%v", err)
	}
}

func TestS3_ResponsesTwoSerialToolLoopsPreserveHistory(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "second_tool")
	defer func() { _ = eng.Close(context.Background()) }()

	fresh := responsesItemCall("read then run")
	fresh.Tools = toolCatalogueTwo()
	openAndPark(t, eng, fresh)

	first := responsesContinuation("read then run", "let me look", "toolu_01ABC", "contents")
	first.Tools = toolCatalogueTwo()
	stream, err := eng.Open(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	events, err := drainEvents(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	if last := events[len(events)-1]; last.Kind != aipapi.EventResponseFinished || last.FinishReason != "tool_use" {
		t.Fatalf("first loop terminal=%v", eventKindsOf(events))
	}

	second := responsesContinuation("read then run", "let me look", "toolu_02SECOND", "ok")
	second.Tools = toolCatalogueTwo()
	second.Items = []aipapi.Item{
		responsesUserItem("read then run"),
		responsesAssistantItem("let me look"),
		responsesToolCallItem("toolu_01ABC", "Read", `{"file_path":"x"}`),
		responsesToolResultItem("toolu_01ABC", "Read", "contents"),
		responsesAssistantItem("got:contents"),
		responsesToolCallItem("toolu_02SECOND", "Bash", `{"file_path":"z"}`),
		responsesToolResultItem("toolu_02SECOND", "Bash", "ok"),
	}
	resumed, err := eng.Open(context.Background(), second)
	if err != nil {
		t.Fatalf("second item loop was rejected: %v", err)
	}
	final, err := drainEvents(t, resumed)
	if err != nil {
		t.Fatal(err)
	}
	text := ""
	for _, event := range final {
		if event.Kind == aipapi.EventTextDelta {
			text += event.Delta
		}
	}
	if text != "got:ok" {
		t.Fatalf("final text=%q events=%v", text, eventKindsOf(final))
	}
}

func TestS3_GeminiNameOnlyLoopResumesParkedTurn(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "")
	defer func() { _ = eng.Close(context.Background()) }()
	openAndPark(t, eng, chatToolCallCall("read the file"))

	continuation := geminiContinuation("read the file", "let me look", "Read", `{"file_path":"x"}`, `{"contents":"x"}`)
	nameProof, shape, err := bridge.NameContinuation(*continuation, "Read")
	if err != nil || shape != bridge.ContinuationReady {
		t.Fatalf("name proof shape=%v err=%v", shape, err)
	}
	stream, err := eng.Open(context.Background(), continuation)
	if err != nil {
		t.Fatal(err)
	}
	events, err := drainEvents(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	text := ""
	for _, event := range events {
		if event.Kind == aipapi.EventTextDelta {
			text += event.Delta
		}
	}
	if text != `got:{"contents":"x"}` {
		t.Fatalf("text=%q events=%v", text, eventKindsOf(events))
	}
	if _, err := eng.sessions.LookupName(nameProof, ""); err == nil {
		t.Fatal("the consumed name-only call must not be releasable")
	} else {
		var continuationErr *bridge.ContinuationError
		if !errors.As(err, &continuationErr) || continuationErr.Code != bridge.CodeContinuationConsumed {
			t.Fatalf("err=%v", err)
		}
	}
}

func TestS3_GeminiDuplicateNameAndSessionIsolation(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "")
	defer func() { _ = eng.Close(context.Background()) }()

	park := func(sessionKey, prompt string) {
		call := chatToolCallCall(prompt)
		call.Session.AuthoritativeSessionID = sessionKey
		openAndPark(t, eng, call)
	}
	park("session-a", "read the file")
	park("session-b", "read the file")

	continuation := geminiContinuation("read the file", "let me look", "Read", `{"file_path":"x"}`, "contents")
	_, err := eng.Open(context.Background(), continuation)
	if err == nil || !strings.Contains(terminalCode(t, err), bridge.CodeAmbiguousContinuation) {
		t.Fatalf("stateless duplicate name err=%v", err)
	}

	for _, key := range []string{"session-a", "session-b"} {
		call := geminiContinuation("read the file", "let me look", "Read", `{"file_path":"x"}`, "contents")
		call.Session.AuthoritativeSessionID = key
		stream, err := eng.Open(context.Background(), call)
		if err != nil {
			t.Fatalf("%s continuation: %v", key, err)
		}
		events, err := drainEvents(t, stream)
		if err != nil {
			t.Fatalf("%s drain: %v", key, err)
		}
		if last := events[len(events)-1]; last.Kind != aipapi.EventResponseFinished || last.FinishReason != "end_turn" {
			t.Fatalf("%s terminal=%+v", key, last)
		}
	}
}

func TestS3_GeminiTwoSerialSameNameLoops(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "second_tool")
	defer func() { _ = eng.Close(context.Background()) }()
	openAndPark(t, eng, chatToolCallCall("read then run"))

	first := geminiContinuation("read then run", "let me look", "Read", `{"file_path":"x"}`, "contents")
	stream, err := eng.Open(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	events, err := drainEvents(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	if last := events[len(events)-1]; last.Kind != aipapi.EventResponseFinished || last.FinishReason != "tool_use" {
		t.Fatalf("first loop terminal=%v", eventKindsOf(events))
	}

	second := geminiContinuation("read then run", "let me look", "Read", `{"file_path":"z"}`, "ok")
	second.Messages = []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("read then run")}},
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
			aipapi.TextPart("let me look"),
			{Kind: aipapi.PartJSON, ToolName: "Read", Content: json.RawMessage(`{"file_path":"x"}`)},
		}},
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{{
			Kind:       aipapi.PartToolResult,
			ToolCallID: "Read",
			ToolName:   "Read",
			Text:       "contents",
		}}},
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
			aipapi.TextPart("got:contents"),
			{Kind: aipapi.PartJSON, ToolName: "Read", Content: json.RawMessage(`{"file_path":"z"}`)},
		}},
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{{
			Kind:       aipapi.PartToolResult,
			ToolCallID: "Read",
			ToolName:   "Read",
			Text:       "ok",
		}}},
	}
	resumed, err := eng.Open(context.Background(), second)
	if err != nil {
		t.Fatalf("second same-name loop was rejected: %v", err)
	}
	final, err := drainEvents(t, resumed)
	if err != nil {
		t.Fatal(err)
	}
	text := ""
	for _, event := range final {
		if event.Kind == aipapi.EventTextDelta {
			text += event.Delta
		}
	}
	if text != "got:ok" {
		t.Fatalf("final text=%q events=%v", text, eventKindsOf(final))
	}
}

func TestS3_OmittedToolsContinuationRetainsSnapshot(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "second_tool")
	defer func() { _ = eng.Close(context.Background()) }()
	fresh := chatToolCallCall("read the file")
	fresh.Tools = toolCatalogueTwo()
	openAndPark(t, eng, fresh)

	continuation := chatContinuationCall("read the file", "let me look", "toolu_01ABC", `"contents"`)
	continuation.Tools = nil
	stream, err := eng.Open(context.Background(), continuation)
	if err != nil {
		t.Fatal(err)
	}
	events, err := drainEvents(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	sawRetained := false
	for _, event := range events {
		if event.Kind == aipapi.EventWarning && event.WarningCode == "tool_snapshot_retained" {
			sawRetained = true
		}
	}
	if !sawRetained {
		t.Fatalf("missing tool_snapshot_retained warning: %v", eventKindsOf(events))
	}
	// The resumed turn emits a second call from the retained snapshot. Without
	// the retained catalogue this native name could not be resolved.
	if last := events[len(events)-1]; last.Kind != aipapi.EventResponseFinished || last.FinishReason != "tool_use" {
		t.Fatalf("retained snapshot did not serve the second call: %v", eventKindsOf(events))
	}
	if _, err := eng.sessions.Lookup("toolu_02SECOND", ""); err != nil {
		t.Fatalf("the second retained-snapshot call was not parked: %v", err)
	}
}

func TestS3_StrictOmittedToolsRefusesWithoutRelease(t *testing.T) {
	t.Parallel()
	deps := testDeps(t)
	deps.sidecar.Script = toolScript(t)
	cfg := testConfig(t)
	cfg.StrictSemantics = true
	eng, err := newEngine(context.Background(), cfg, nil, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = eng.Close(context.Background()) }()
	openAndPark(t, eng, chatToolCallCall("read the file"))

	continuation := chatContinuationCall("read the file", "let me look", "toolu_01ABC", `"contents"`)
	continuation.Tools = nil
	if _, err := eng.Open(context.Background(), continuation); !errors.Is(err, aipapi.ErrCapabilityReject) {
		t.Fatalf("err=%v", err)
	}
	if _, err := eng.sessions.Lookup("toolu_01ABC", ""); err != nil {
		t.Fatalf("the parked call must survive a strict refusal: %v", err)
	}
}

func TestS3_ToolChoiceNoneContinuationRefusedWithoutRelease(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "second_tool")
	defer func() { _ = eng.Close(context.Background()) }()
	fresh := chatToolCallCall("read the file")
	fresh.Tools = toolCatalogueTwo()
	openAndPark(t, eng, fresh)

	continuation := chatContinuationCall("read the file", "let me look", "toolu_01ABC", `"contents"`)
	continuation.Tools = nil
	continuation.ToolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceNone}
	_, err := eng.Open(context.Background(), continuation)
	if !errors.Is(err, aipapi.ErrCapabilityReject) {
		t.Fatalf("tool_choice none continuation err=%v", err)
	}
	if !strings.Contains(err.Error(), "tool_choice none") {
		t.Fatalf("refusal must name the prohibited tool choice: %v", err)
	}
	if _, err := eng.sessions.Lookup("toolu_01ABC", ""); err != nil {
		t.Fatalf("the parked call must survive a tool-choice-none refusal: %v", err)
	}
	if _, err := eng.sessions.Lookup("toolu_02SECOND", ""); err == nil {
		t.Fatal("the refused continuation must not create a new pending tool call")
	}
	if live := eng.sessions.LiveRuntimes(); live != 1 {
		t.Fatalf("live runtimes=%d, want 1", live)
	}
}

func TestS3_ToolChoiceNoneRefusalDoesNotConsumePending(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "second_tool")
	defer func() { _ = eng.Close(context.Background()) }()
	fresh := chatToolCallCall("read the file")
	fresh.Tools = toolCatalogueTwo()
	openAndPark(t, eng, fresh)

	refused := chatContinuationCall("read the file", "let me look", "toolu_01ABC", `"contents"`)
	refused.Tools = nil
	refused.ToolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceNone}
	if _, err := eng.Open(context.Background(), refused); !errors.Is(err, aipapi.ErrCapabilityReject) {
		t.Fatalf("tool_choice none continuation err=%v", err)
	}
	if _, err := eng.sessions.Lookup("toolu_01ABC", ""); err != nil {
		t.Fatalf("the parked call must survive the refusal: %v", err)
	}

	retry := chatContinuationCall("read the file", "let me look", "toolu_01ABC", `"contents"`)
	retry.Tools = nil
	retry.ToolChoice = aipapi.ToolChoice{}
	stream, err := eng.Open(context.Background(), retry)
	if err != nil {
		t.Fatalf("the retry must serve the preserved pending call: %v", err)
	}
	events, err := drainEvents(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	sawRetained := false
	for _, event := range events {
		if event.Kind == aipapi.EventWarning && event.WarningCode == "tool_snapshot_retained" {
			sawRetained = true
		}
	}
	if !sawRetained {
		t.Fatalf("missing tool_snapshot_retained warning: %v", eventKindsOf(events))
	}
	if last := events[len(events)-1]; last.Kind != aipapi.EventResponseFinished || last.FinishReason != "tool_use" {
		t.Fatalf("retained snapshot did not serve the second call: %v", eventKindsOf(events))
	}
	if _, err := eng.sessions.Lookup("toolu_02SECOND", ""); err != nil {
		t.Fatalf("the second retained-snapshot call was not parked: %v", err)
	}
}

func TestS3_ToolChoiceNoneNameOnlyContinuationRefusedWithoutRelease(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "second_tool")
	defer func() { _ = eng.Close(context.Background()) }()
	fresh := chatToolCallCall("read the file")
	fresh.Tools = toolCatalogueTwo()
	openAndPark(t, eng, fresh)

	continuation := geminiContinuation("read the file", "let me look", "Read", `{"file_path":"x"}`, "contents")
	continuation.Tools = nil
	continuation.ToolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceNone}
	if _, err := eng.Open(context.Background(), continuation); !errors.Is(err, aipapi.ErrCapabilityReject) {
		t.Fatalf("tool_choice none name-only continuation err=%v", err)
	}
	if !eng.sessions.HasLiveName("Read") {
		t.Fatal("the parked name-only call must survive the refusal")
	}
	if _, err := eng.sessions.Lookup("toolu_01ABC", ""); err != nil {
		t.Fatalf("the parked call must survive a tool-choice-none refusal: %v", err)
	}
}

func TestS3_ChangedNameOnlyModelOrCatalogueRefuses(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "")
	defer func() { _ = eng.Close(context.Background()) }()
	openAndPark(t, eng, chatToolCallCall("read the file"))

	changedModel := geminiContinuation("read the file", "let me look", "Read", `{"file_path":"x"}`, "contents")
	changedModel.Route.Selector = "anthropic/opus"
	if _, err := eng.Open(context.Background(), changedModel); err == nil || !strings.Contains(terminalCode(t, err), bridge.CodeContinuationMismatch) {
		t.Fatalf("changed model err=%v", err)
	}

	changedCatalogue := geminiContinuation("read the file", "let me look", "Read", `{"file_path":"x"}`, "contents")
	changedCatalogue.Tools = []aipapi.ToolDef{{Name: "Bash", Parameters: json.RawMessage(`{"type":"object"}`)}}
	if _, err := eng.Open(context.Background(), changedCatalogue); err == nil || !strings.Contains(terminalCode(t, err), bridge.CodeContinuationMismatch) {
		t.Fatalf("changed catalogue err=%v", err)
	}
	if !eng.sessions.HasLiveName("Read") {
		t.Fatal("the parked name-only call must survive")
	}
}

func TestS3_PreviousResponseFreshTurnIsAnHonestReplay(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "echo_prompt")
	defer func() { _ = eng.Close(context.Background()) }()
	call := responsesItemCall("second")
	call.ID = "responses-fresh"
	call.Items = []aipapi.Item{
		responsesUserItem("first"),
		responsesAssistantItem("answer"),
		responsesUserItem("second"),
	}
	call.PreviousResponseID = "resp_parent"
	call.Extensions = map[string]json.RawMessage{
		aipcont.MaterializerProvenanceExtension: json.RawMessage(`true`),
	}
	stream, err := eng.Open(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	events, err := drainEvents(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	sawReplay := false
	text := ""
	for _, event := range events {
		if event.Kind == aipapi.EventWarning && event.WarningCode == "history_replay_degraded" {
			sawReplay = true
		}
		if event.Kind == aipapi.EventTextDelta {
			text += event.Delta
		}
	}
	if !sawReplay {
		t.Fatalf("fresh previous-response turn must disclose replay: %v", eventKindsOf(events))
	}
	if !strings.Contains(text, "second") || !strings.Contains(text, "first") {
		t.Fatalf("replayed transcript=%q", text)
	}

	unmaterialized := responsesItemCall("second")
	unmaterialized.PreviousResponseID = "resp_parent"
	if _, err := eng.Open(context.Background(), unmaterialized); err == nil || !strings.Contains(terminalCode(t, err), bridge.CodeHistoryUnavailable) {
		t.Fatalf("unmaterialized previous response err=%v", err)
	}
}

func TestS3_TypedNameOnlyExpiryAndAmbiguity(t *testing.T) {
	t.Parallel()
	deps := testDeps(t)
	deps.sidecar.Script = toolScript(t)
	cfg := testConfig(t)
	cfg.PendingTTLSeconds = 0.05
	eng, err := newEngine(context.Background(), cfg, nil, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = eng.Close(context.Background()) }()
	openAndPark(t, eng, chatToolCallCall("read the file"))
	time.Sleep(120 * time.Millisecond)
	continuation := geminiContinuation("read the file", "let me look", "Read", `{"file_path":"x"}`, "contents")
	if _, err := eng.Open(context.Background(), continuation); err == nil || !strings.Contains(terminalCode(t, err), bridge.CodeContinuationExpired) {
		t.Fatalf("expired err=%v", err)
	}
}

// itemInvocation is the ABI form the host produces for an ordered-item call
// after negotiating FeatureOrderedItems. It proves the descriptor's advertised
// ordered-item surface reaches the connector's canonical call.
func itemInvocation(id, prompt string) *backendplugin.Invocation {
	text := prompt
	parameters := backendplugin.RawJSONFromBytes([]byte(`{"type":"object","properties":{"file_path":{"type":"string"}}}`))
	return &backendplugin.Invocation{
		RequestID:        id,
		AttemptID:        "a",
		ALegID:           "a",
		BLegID:           "b",
		CanonicalModelID: "anthropic/sonnet",
		Tools: []backendplugin.ToolDef{{
			Name:           "Read",
			Description:    "read a file",
			ParametersJSON: parameters,
		}},
		ItemAuthority: true,
		Items: []backendplugin.InvocationItem{{
			Kind:   string(aipapi.ItemKindMessage),
			ID:     "msg_1",
			Status: string(aipapi.ItemStatusCompleted),
			Role:   backendplugin.RoleUser,
			Content: []backendplugin.InvocationContentPart{{
				Kind: backendplugin.PartKindText,
				Text: &text,
			}},
		}},
		Options: backendplugin.GenerationOptions{ResponseSchemaJSON: backendplugin.RawJSONAbsentValue()},
	}
}

func TestS3_OrderedItemInvocationExecutesThroughTheABI(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "")
	defer func() { _ = eng.Close(context.Background()) }()
	stream, err := executeOnce(t, eng, itemInvocation("responses-item", "read the file"))
	if err != nil {
		t.Fatal(err)
	}
	if terminal := terminalFrame(t, stream.sent); terminal.Status != backendplugin.TerminalSuccess {
		t.Fatalf("terminal=%+v", terminal)
	}
	sawCall := false
	for _, frame := range stream.sent {
		if frame.Event != nil && frame.Event.Kind == backendplugin.EventToolCallFinished {
			sawCall = true
		}
	}
	if !sawCall {
		t.Fatalf("ordered item invocation produced no tool call: %+v", stream.sent)
	}
	if _, err := eng.sessions.Lookup("toolu_01ABC", ""); err != nil {
		t.Fatalf("the ordered item call was not parked: %v", err)
	}
}

// TestS3_ProjectedResponsesContinuationResumes proves the production host path
// while the resolved profile keeps message authority: the host projects the
// materialized item trajectory to legacy messages before the ABI, which does not
// carry PreviousResponseID. The materializer provenance extension survives and
// the canonical exact-id prefix proof still releases the parked turn.
func TestS3_ProjectedResponsesContinuationResumes(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "")
	defer func() { _ = eng.Close(context.Background()) }()
	projectTarget := aipapi.DefaultLegacyProjectionTarget(
		aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
		aipapi.ReasoningReplaySupport{},
	)
	freshItem := responsesItemCall("read the file")
	freshProjected, err := aipapi.ProjectItemsToLegacyView(*freshItem, projectTarget)
	if err != nil {
		t.Fatal(err)
	}
	fresh := *freshItem
	fresh.Items = nil
	fresh.Instructions = freshProjected.Instructions
	fresh.Messages = freshProjected.Messages
	openAndPark(t, eng, &fresh)

	itemContinuation := responsesContinuation("read the file", "let me look", "toolu_01ABC", "contents")
	projected, err := aipapi.ProjectItemsToLegacyView(*itemContinuation, aipapi.DefaultLegacyProjectionTarget(
		aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
		aipapi.ReasoningReplaySupport{},
	))
	if err != nil {
		t.Fatal(err)
	}
	legacy := *itemContinuation
	legacy.Items = nil
	legacy.Instructions = projected.Instructions
	legacy.Messages = projected.Messages
	// The backend-plugin ABI has no PreviousResponseID field; only the
	// provenance extension is preserved.
	legacy.PreviousResponseID = ""
	stream, err := eng.Open(context.Background(), &legacy)
	if err != nil {
		t.Fatalf("projected continuation was rejected: %v", err)
	}
	events, err := drainEvents(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	text := ""
	for _, event := range events {
		if event.Kind == aipapi.EventTextDelta {
			text += event.Delta
		}
	}
	if text != "got:contents" {
		t.Fatalf("text=%q events=%v", text, eventKindsOf(events))
	}
}

func projectedResponsesCall(t *testing.T, call *aipapi.Call, target aipapi.LegacyProjectionTarget) *aipapi.Call {
	t.Helper()
	projected, err := aipapi.ProjectItemsToLegacyView(*call, target)
	if err != nil {
		t.Fatal(err)
	}
	out := *call
	out.Items = nil
	out.Instructions = projected.Instructions
	out.Messages = projected.Messages
	// The backend-plugin ABI has no PreviousResponseID field; only the
	// provenance extension is preserved.
	out.PreviousResponseID = ""
	return &out
}

func TestS3_ProjectedResponsesTwoSerialLoops(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "second_tool")
	defer func() { _ = eng.Close(context.Background()) }()
	target := aipapi.DefaultLegacyProjectionTarget(
		aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
		aipapi.ReasoningReplaySupport{},
	)
	openProjected := func(call *aipapi.Call) []aipapi.Event {
		t.Helper()
		stream, err := eng.Open(context.Background(), projectedResponsesCall(t, call, target))
		if err != nil {
			t.Fatal(err)
		}
		events, err := drainEvents(t, stream)
		if err != nil {
			t.Fatal(err)
		}
		return events
	}

	fresh := responsesItemCall("read then run")
	fresh.Tools = toolCatalogueTwo()
	if events := openProjected(fresh); events[len(events)-1].FinishReason != "tool_use" {
		t.Fatalf("fresh terminal=%v", eventKindsOf(events))
	}

	first := responsesContinuation("read then run", "let me look", "toolu_01ABC", "contents")
	first.Tools = toolCatalogueTwo()
	if events := openProjected(first); events[len(events)-1].FinishReason != "tool_use" {
		t.Fatalf("first loop terminal=%v", eventKindsOf(events))
	}

	second := responsesContinuation("read then run", "let me look", "toolu_02SECOND", "ok")
	second.Tools = toolCatalogueTwo()
	second.Items = []aipapi.Item{
		responsesUserItem("read then run"),
		responsesAssistantItem("let me look"),
		responsesToolCallItem("toolu_01ABC", "Read", `{"file_path":"x"}`),
		responsesToolResultItem("toolu_01ABC", "Read", "contents"),
		responsesAssistantItem("got:contents"),
		responsesToolCallItem("toolu_02SECOND", "Bash", `{"file_path":"z"}`),
		responsesToolResultItem("toolu_02SECOND", "Bash", "ok"),
	}
	events := openProjected(second)
	text := ""
	for _, event := range events {
		if event.Kind == aipapi.EventTextDelta {
			text += event.Delta
		}
	}
	if text != "got:ok" {
		t.Fatalf("final text=%q events=%v", text, eventKindsOf(events))
	}
}

func TestS3_NameOnlyNoPendingIsAHonestReplay(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "echo_prompt")
	defer func() { _ = eng.Close(context.Background()) }()
	// No conversation is parked. The request carries completed Gemini history
	// and a trailing functionResponse, so it is served as a labeled replay
	// rather than guessed into an unrelated parked turn.
	call := geminiContinuation("read the file", "let me look", "Read", `{"file_path":"x"}`, "contents")
	stream, err := eng.Open(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	events, err := drainEvents(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	sawReplay := false
	for _, event := range events {
		if event.Kind == aipapi.EventWarning && event.WarningCode == "history_replay_degraded" {
			sawReplay = true
		}
	}
	if !sawReplay {
		t.Fatalf("missing history_replay_degraded: %v", eventKindsOf(events))
	}
}

func TestS3_ReplayedResponsesArgumentsAreNotDoubleEncoded(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "echo_prompt")
	defer func() { _ = eng.Close(context.Background()) }()
	call := responsesItemCall("next")
	call.Items = []aipapi.Item{
		responsesUserItem("read the file"),
		responsesAssistantItem("let me look"),
		responsesToolCallItem("toolu_01ABC", "Read", `{"file_path":"x"}`),
		responsesToolResultItem("toolu_01ABC", "Read", "contents"),
		responsesUserItem("next"),
	}
	call.PreviousResponseID = "resp_parent"
	call.Extensions = map[string]json.RawMessage{
		aipcont.MaterializerProvenanceExtension: json.RawMessage(`true`),
	}
	stream, err := eng.Open(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	events, err := drainEvents(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	text := ""
	for _, event := range events {
		if event.Kind == aipapi.EventTextDelta {
			text += event.Delta
		}
	}
	if !strings.Contains(text, `{"file_path":"x"}`) {
		t.Fatalf("replayed arguments lost the document: %q", text)
	}
	if strings.Contains(text, `\"file_path\"`) {
		t.Fatalf("replayed arguments are double-encoded: %q", text)
	}
}

func TestS3_ConsumedNamePrefixDoesNotShadowDifferentPrefixSurvivor(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "unique_ids")
	defer func() { _ = eng.Close(context.Background()) }()
	park := func(sessionKey, prompt string) {
		call := chatToolCallCall(prompt)
		call.Session.AuthoritativeSessionID = sessionKey
		openAndPark(t, eng, call)
	}
	park("session-a", "read a")
	park("session-b", "read b")

	callA := geminiContinuation("read a", "let me look", "Read", `{"file_path":"x"}`, "contents-a")
	callA.Session.AuthoritativeSessionID = "session-a"
	stream, err := eng.Open(context.Background(), callA)
	if err != nil {
		t.Fatal(err)
	}
	events, err := drainEvents(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	if text := eventText(events); text != "got:contents-a" {
		t.Fatalf("A text=%q", text)
	}

	// A genuine replay is still typed as consumed while B is live.
	replay := geminiContinuation("read a", "let me look", "Read", `{"file_path":"x"}`, "contents-a")
	if _, err := eng.Open(context.Background(), replay); err == nil ||
		!strings.Contains(terminalCode(t, err), bridge.CodeContinuationConsumed) {
		t.Fatalf("A replay err=%v", err)
	}
	// B's distinct prefix is not shadowed by A's consumed marker.
	stream, err = eng.Open(context.Background(), geminiContinuation("read b", "let me look", "Read", `{"file_path":"x"}`, "contents-b"))
	if err != nil {
		t.Fatalf("B was shadowed by A's tombstone: %v", err)
	}
	events, err = drainEvents(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	if text := eventText(events); text != "got:contents-b" {
		t.Fatalf("B text=%q", text)
	}
}

func TestS3_NewSameNamePendingDoesNotEraseReplayProtection(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "unique_ids")
	defer func() { _ = eng.Close(context.Background()) }()
	park := func(sessionKey, prompt string) {
		call := chatToolCallCall(prompt)
		call.Session.AuthoritativeSessionID = sessionKey
		openAndPark(t, eng, call)
	}

	park("session-a", "same prompt")
	callA := geminiContinuation("same prompt", "let me look", "Read", `{"file_path":"x"}`, "old-result-a")
	callA.Session.AuthoritativeSessionID = "session-a"
	stream, err := eng.Open(context.Background(), callA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drainEvents(t, stream); err != nil {
		t.Fatal(err)
	}

	// B is a new conversation with the same visible prefix and a new native id.
	park("session-b", "same prompt")
	// A's old stateless retry must not select and release B.
	retry := geminiContinuation("same prompt", "let me look", "Read", `{"file_path":"x"}`, "old-result-a")
	if _, err := eng.Open(context.Background(), retry); err == nil ||
		!strings.Contains(terminalCode(t, err), bridge.CodeAmbiguousContinuation) {
		t.Fatalf("A retry err=%v", err)
	}
	if !eng.sessions.HasLiveName("Read") {
		t.Fatal("B was released by A's retry")
	}
	// B remains releasable only through its trusted session.
	callB := geminiContinuation("same prompt", "let me look", "Read", `{"file_path":"x"}`, "new-result-b")
	callB.Session.AuthoritativeSessionID = "session-b"
	stream, err = eng.Open(context.Background(), callB)
	if err != nil {
		t.Fatalf("trusted B continuation: %v", err)
	}
	events, err := drainEvents(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	if text := eventText(events); text != "got:new-result-b" {
		t.Fatalf("B text=%q", text)
	}
}

func TestS3_NameContinuationRejectsConflictingSecondOccurrence(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "")
	defer func() { _ = eng.Close(context.Background()) }()
	openAndPark(t, eng, chatToolCallCall("read the file"))

	attack := geminiContinuation("read the file", "let me look", "Read", `{"file_path":"x"}`, "result-for-other-call")
	attack.Messages = []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("read the file")}},
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
			aipapi.TextPart("let me look"),
			{Kind: aipapi.PartJSON, ToolName: "Read", Content: json.RawMessage(`{"file_path":"x"}`)},
		}},
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("added user content")}},
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
			aipapi.TextPart("second"),
			{Kind: aipapi.PartJSON, ToolName: "Read", Content: json.RawMessage(`{"file_path":"y"}`)},
		}},
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{{
			Kind:       aipapi.PartToolResult,
			ToolCallID: "Read",
			ToolName:   "Read",
			Text:       "result-for-other-call",
		}}},
	}
	if err := attack.Validate(); err != nil {
		t.Fatalf("attack fixture must be canonical: %v", err)
	}
	if _, err := eng.Open(context.Background(), attack); err == nil ||
		!strings.Contains(terminalCode(t, err), bridge.CodeContinuationMismatch) {
		t.Fatalf("conflicting occurrence err=%v", err)
	}
	if !eng.sessions.HasLiveName("Read") {
		t.Fatal("the original pending was released by the conflicting occurrence")
	}

	correct := geminiContinuation("read the file", "let me look", "Read", `{"file_path":"x"}`, "contents")
	stream, err := eng.Open(context.Background(), correct)
	if err != nil {
		t.Fatalf("correct retry after refusal: %v", err)
	}
	events, err := drainEvents(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	if text := eventText(events); text != "got:contents" {
		t.Fatalf("correct text=%q", text)
	}
}

func eventText(events []aipapi.Event) string {
	var text strings.Builder
	for _, event := range events {
		if event.Kind == aipapi.EventTextDelta {
			text.WriteString(event.Delta)
		}
	}
	return text.String()
}

func TestS3_ResolvedAndListedCapabilitiesKeepMessageAuthority(t *testing.T) {
	t.Parallel()
	eng := testEngine(t, "")
	profile, err := eng.profile(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Capabilities.OrderedItems {
		t.Fatal("the resolved execution profile must keep message authority")
	}
	models, err := eng.listModels(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range models.Models {
		if model.Capabilities.OrderedItems {
			t.Fatalf("listed model %q advertises ordered items while the execution profile does not", model.CanonicalModelID)
		}
	}
}

func TestS3_ExpiredNamePrefixDoesNotShadowDifferentPrefixSurvivor(t *testing.T) {
	t.Parallel()
	now := time.Now()
	deps := testDeps(t)
	deps.sidecar.Script = toolScript(t)
	deps.now = func() time.Time { return now }
	cfg := testConfig(t)
	cfg.PendingTTLSeconds = 10
	eng, err := newEngine(context.Background(), cfg, nil, deps)
	if err != nil {
		t.Fatal(err)
	}
	eng.env = append(eng.env, "FAKE_MODE=unique_ids")
	defer func() { _ = eng.Close(context.Background()) }()

	freshA := chatToolCallCall("read a")
	freshA.Session.AuthoritativeSessionID = "session-a"
	openAndPark(t, eng, freshA)
	callA := geminiContinuation("read a", "let me look", "Read", `{"file_path":"x"}`, "result-a")
	callA.Session.AuthoritativeSessionID = "session-a"

	// Advance only the connector's clock; A's real timer cannot fire during the
	// test. The explicit Resuming request below triggers typed expiry.
	now = now.Add(11 * time.Second)
	if _, err := eng.Open(context.Background(), callA); err == nil ||
		!strings.Contains(terminalCode(t, err), bridge.CodeContinuationExpired) {
		t.Fatalf("A expiry err=%v", err)
	}

	// B starts after A's expiry with a different visible prefix.
	freshB := chatToolCallCall("read b")
	freshB.Session.AuthoritativeSessionID = "session-b"
	openAndPark(t, eng, freshB)

	replayA := geminiContinuation("read a", "let me look", "Read", `{"file_path":"x"}`, "result-a")
	if _, err := eng.Open(context.Background(), replayA); err == nil ||
		!strings.Contains(terminalCode(t, err), bridge.CodeContinuationExpired) {
		t.Fatalf("A replay err=%v", err)
	}
	stream, err := eng.Open(context.Background(), geminiContinuation("read b", "let me look", "Read", `{"file_path":"x"}`, "result-b"))
	if err != nil {
		t.Fatalf("B was shadowed by A's expired tombstone: %v", err)
	}
	events, err := drainEvents(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	if text := eventText(events); text != "got:result-b" {
		t.Fatalf("B text=%q", text)
	}
}
