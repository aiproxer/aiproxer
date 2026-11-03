package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connectors/claudeagentmodel/internal/bridge"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// The connector module must not import root-module internal packages (the host
// owns multi-user authority), so these fixtures are the canonical calls the
// unchanged OpenAI Chat and Anthropic Messages decoders produce for the wire
// bodies quoted in each test. The wire mapping itself is covered by the root
// frontend suites; this file proves the connector's canonical envelope, the
// collected shape those encoders consume, and the parked/resume loop.

// chatToolCallCall mirrors openailegacy DecodeChatRequest for:
//
//	{"messages":[{"role":"user","content":"read the file"}],
//	 "tools":[{"type":"function","function":{"name":"Read","description":"read a file",
//	   "parameters":{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}}}]}
func chatToolCallCall(prompt string) *aipapi.Call {
	return &aipapi.Call{
		ID:    "chat-turn",
		Route: aipapi.RouteIntent{Selector: "anthropic/sonnet"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart(prompt)},
		}},
		Tools: []aipapi.ToolDef{{
			Name:        "Read",
			Description: "read a file",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}`),
		}},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
	}
}

// chatContinuationCall mirrors openailegacy DecodeChatRequest for a role:tool
// message: the result content is carried as JSON in Part.Content and the assistant
// tool call keeps the exact opaque id and argument bytes.
func chatContinuationCall(prompt, assistantText, callID, resultJSON string) *aipapi.Call {
	call := chatToolCallCall(prompt)
	call.ID = "chat-continuation"
	call.Messages = append(call.Messages,
		aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
			aipapi.TextPart(assistantText),
			{Kind: aipapi.PartJSON, ToolCallID: callID, ToolName: "Read", Content: json.RawMessage(`{"file_path":"x"}`)},
		}},
		aipapi.Message{Role: aipapi.RoleTool, Parts: []aipapi.Part{
			{Kind: aipapi.PartToolResult, ToolCallID: callID, Content: json.RawMessage(resultJSON)},
		}},
	)
	return call
}

// anthropicContinuationCall mirrors anthropic DecodeMessageRequest: tool_use
// keeps the id/name/input and tool_result flattens text into Part.Text.
func anthropicContinuationCall(prompt, assistantText, callID, resultText string) *aipapi.Call {
	call := chatToolCallCall(prompt)
	call.ID = "anthropic-continuation"
	call.Messages = append(call.Messages,
		aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
			aipapi.TextPart(assistantText),
			{Kind: aipapi.PartJSON, ToolCallID: callID, ToolName: "Read", Content: json.RawMessage(`{"file_path":"x"}`)},
		}},
		aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{
			{Kind: aipapi.PartToolResult, ToolCallID: callID, Text: resultText},
		}},
	)
	return call
}

// toolEngine builds an engine whose private sidecar is the SDK-shaped tool
// fixture. It proves the canonical event envelope and the parked/resume loop
// without a token or a real SDK process.
func toolEngine(t *testing.T, mode string) *engine {
	t.Helper()
	deps := testDeps(t)
	deps.sidecar.Script = toolScript(t)
	built, err := newEngine(context.Background(), testConfig(t), nil, deps)
	if err != nil {
		t.Fatal(err)
	}
	if mode != "" {
		built.env = append(built.env, "FAKE_MODE="+mode)
	}
	return built
}

func toolScript(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "bridge", "testdata", "tool-sidecar.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func drainEvents(t *testing.T, stream aipapi.ManagedEventStream) ([]aipapi.Event, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var events []aipapi.Event
	for {
		event, err := stream.Recv(ctx)
		if errors.Is(err, io.EOF) {
			return events, nil
		}
		if err != nil {
			return events, err
		}
		events = append(events, event)
	}
}

func openAndPark(t *testing.T, eng *engine, call *aipapi.Call) []aipapi.Event {
	t.Helper()
	stream, err := eng.Open(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	events, err := drainEvents(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func eventKindsOf(events []aipapi.Event) []aipapi.EventKind {
	out := make([]aipapi.EventKind, 0, len(events))
	for _, event := range events {
		out = append(out, event.Kind)
	}
	return out
}

func TestToolLoop_StreamingEnvelopeAndCollectedShape(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "")
	defer func() { _ = eng.Close(context.Background()) }()

	stream, err := eng.Open(context.Background(), chatToolCallCall("read the file"))
	if err != nil {
		t.Fatal(err)
	}
	// Streaming: the canonical event order the Chat and Anthropic SSE encoders
	// both consume. Tool events follow the message frame and precede the terminal.
	events, err := drainEvents(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	want := []aipapi.EventKind{
		aipapi.EventResponseStarted, aipapi.EventMessageStarted, aipapi.EventTextDelta,
		aipapi.EventToolCallStarted, aipapi.EventToolCallArgsDelta, aipapi.EventToolCallFinished,
		aipapi.EventResponseFinished,
	}
	got := eventKindsOf(events)
	if len(got) != len(want) {
		t.Fatalf("events=%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events=%v want=%v", got, want)
		}
	}
	if events[3].ToolCallID != "toolu_01ABC" || events[3].ToolName != "Read" {
		t.Fatalf("tool started=%+v", events[3])
	}
	if events[4].Delta != `{"file_path":"x"}` {
		t.Fatalf("args delta=%q", events[4].Delta)
	}
	if events[6].FinishReason != "tool_use" {
		t.Fatalf("terminal=%+v", events[6])
	}

	// Collected: the shape both non-streaming encoders read (ordered tool calls
	// with the exact id/name/arguments and the terminal finish reason).
	collectedStream, err := eng.Open(context.Background(), chatToolCallCall("read the file"))
	if err != nil {
		t.Fatal(err)
	}
	collected, err := aipapi.Collect(context.Background(), collectedStream)
	if err != nil {
		t.Fatal(err)
	}
	tools := collected.OrderedToolCalls()
	if len(tools) != 1 || tools[0].ID != "toolu_01ABC" || tools[0].Name != "Read" || tools[0].Arguments != `{"file_path":"x"}` {
		t.Fatalf("collected tools=%+v", tools)
	}
	if collected.FinishReason != "tool_use" {
		t.Fatalf("collected finish=%q", collected.FinishReason)
	}
}

func TestToolLoop_ChatAndAnthropicContinuationsReleaseExactlyOnce(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		continuation *aipapi.Call
		wantResult   string
	}{
		{
			name:         "openai chat role:tool",
			continuation: chatContinuationCall("read the file", "let me look", "toolu_01ABC", `"contents"`),
			wantResult:   "contents",
		},
		{
			name:         "anthropic tool_result",
			continuation: anthropicContinuationCall("read the file", "let me look", "toolu_01ABC", "contents"),
			wantResult:   "contents",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eng := toolEngine(t, "")
			defer func() { _ = eng.Close(context.Background()) }()
			openAndPark(t, eng, chatToolCallCall("read the file"))

			stream, err := eng.Open(context.Background(), tc.continuation)
			if err != nil {
				t.Fatal(err)
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
			if !strings.Contains(text.String(), "got:"+tc.wantResult) {
				t.Fatalf("continuation text=%q events=%v", text.String(), eventKindsOf(events))
			}
			if events[len(events)-1].FinishReason != "end_turn" {
				t.Fatalf("terminal=%+v", events[len(events)-1])
			}
			if _, err := eng.sessions.Lookup("toolu_01ABC", ""); err == nil {
				t.Fatal("the released call must not be releasable again")
			} else {
				var continuationErr *bridge.ContinuationError
				if !errors.As(err, &continuationErr) || continuationErr.Code != bridge.CodeContinuationConsumed {
					t.Fatalf("err=%v", err)
				}
			}
		})
	}
}

func TestToolLoop_EmptyArgumentsSurviveExactly(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "empty_args")
	defer func() { _ = eng.Close(context.Background()) }()
	call := chatToolCallCall("read the file")
	call.Tools = []aipapi.ToolDef{{Name: "Read", Parameters: json.RawMessage(`{"type":"object"}`)}}
	events := openAndPark(t, eng, call)
	for _, event := range events {
		if event.Kind == aipapi.EventToolCallArgsDelta && event.Delta != "{}" {
			t.Fatalf("args delta=%q", event.Delta)
		}
	}
}

func TestToolLoop_ExactResultContentIsPreserved(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "")
	defer func() { _ = eng.Close(context.Background()) }()
	openAndPark(t, eng, chatToolCallCall("read the file"))
	// A Chat client that sends a JSON string keeps its exact text; a structured
	// JSON result keeps its exact fields.
	structured := chatContinuationCall("read the file", "let me look", "toolu_01ABC", `{"error":"permission denied","code":13}`)
	stream, err := eng.Open(context.Background(), structured)
	if err != nil {
		t.Fatal(err)
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
	if !strings.Contains(text.String(), `got:{"code":13,"error":"permission denied"}`) {
		t.Fatalf("text=%q", text.String())
	}
}

func TestToolLoop_ForeignResultIsATypedRefusal(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "")
	defer func() { _ = eng.Close(context.Background()) }()
	openAndPark(t, eng, chatToolCallCall("read the file"))
	foreign := chatContinuationCall("read the file", "let me look", "toolu_FOREIGN", `"contents"`)
	_, err := eng.Open(context.Background(), foreign)
	var terminal backendplugin.TerminalError
	if !errors.As(err, &terminal) {
		t.Fatalf("err=%v", err)
	}
	pluginErr := terminal.TerminalPluginError()
	if pluginErr.Code != backendplugin.ErrorCodeFailedPrecondition || !strings.Contains(pluginErr.Message, "continuation_mismatch") {
		t.Fatalf("plugin error=%+v", pluginErr)
	}
	// The real pending call was not released by the refused attempt.
	if _, err := eng.sessions.Lookup("toolu_01ABC", ""); err != nil {
		t.Fatalf("pending call lost: %v", err)
	}
}

func TestToolLoop_BlockedNativeToolIsATypedFailure(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "native_tool")
	defer func() { _ = eng.Close(context.Background()) }()
	stream, err := eng.Open(context.Background(), chatToolCallCall("read the file"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = drainEvents(t, stream)
	_ = stream.Close()
	if err == nil {
		t.Fatal("expected the native tool attempt to fail closed")
	}
	if strings.Contains(err.Error(), "read the file") {
		t.Fatal("the failure must not echo prompt content")
	}
}

func TestToolLoop_TwoSessionsDoNotCrossResume(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "")
	defer func() { _ = eng.Close(context.Background()) }()
	first := chatToolCallCall("read a")
	first.Session.AuthoritativeSessionID = "session-a"
	openAndPark(t, eng, first)
	second := chatToolCallCall("other")
	second.Session.AuthoritativeSessionID = "session-b"
	openAndPark(t, eng, second)

	foreign := chatContinuationCall("read a", "let me look", "toolu_01ABC", `"contents"`)
	foreign.Session.AuthoritativeSessionID = "session-b"
	_, err := eng.Open(context.Background(), foreign)
	var terminal backendplugin.TerminalError
	if !errors.As(err, &terminal) || !strings.Contains(terminal.TerminalPluginError().Message, "continuation_mismatch") {
		t.Fatalf("err=%v", err)
	}
	if _, err := eng.sessions.Lookup("toolu_01ABC", "session-a"); err != nil {
		t.Fatalf("session-a's parked call must survive: %v", err)
	}
}

func TestToolLoop_RouteOrCatalogueChangeWhileParkedRefuses(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "")
	defer func() { _ = eng.Close(context.Background()) }()
	openAndPark(t, eng, chatToolCallCall("read the file"))

	changedCatalogue := chatContinuationCall("read the file", "let me look", "toolu_01ABC", `"contents"`)
	changedCatalogue.Tools = []aipapi.ToolDef{{Name: "Bash", Parameters: json.RawMessage(`{"type":"object"}`)}}
	if _, err := eng.Open(context.Background(), changedCatalogue); err == nil {
		t.Fatal("a changed catalogue must not release the parked turn")
	}
	changedModel := chatContinuationCall("read the file", "let me look", "toolu_01ABC", `"contents"`)
	changedModel.Route.Selector = "anthropic/opus"
	if _, err := eng.Open(context.Background(), changedModel); err == nil {
		t.Fatal("a changed model must not release the parked turn")
	}
	if _, err := eng.sessions.Lookup("toolu_01ABC", ""); err != nil {
		t.Fatalf("pending call lost: %v", err)
	}
}

func TestToolLoop_MixedResultAndUserTextKeepsSourceOrderWithoutRelease(t *testing.T) {
	t.Parallel()
	parked := toolEngine(t, "")
	defer func() { _ = parked.Close(context.Background()) }()
	openAndPark(t, parked, chatToolCallCall("read the file"))

	eng := toolEngine(t, "echo_prompt")
	defer func() { _ = eng.Close(context.Background()) }()
	mixed := anthropicContinuationCall("read the file", "let me look", "toolu_01ABC", "contents")
	mixed.Messages[len(mixed.Messages)-1] = aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{
		aipapi.TextPart("also check this"),
		{Kind: aipapi.PartToolResult, ToolCallID: "toolu_01ABC", Text: "contents"},
	}}
	stream, err := eng.Open(context.Background(), mixed)
	if err != nil {
		t.Fatal(err)
	}
	events, err := drainEvents(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	sawReplayWarning := false
	var transcript strings.Builder
	for _, event := range events {
		if event.Kind == aipapi.EventWarning && event.WarningCode == "history_replay_degraded" {
			sawReplayWarning = true
		}
		if event.Kind == aipapi.EventTextDelta {
			transcript.WriteString(event.Delta)
		}
	}
	if !sawReplayWarning {
		t.Fatalf("mixed content must disclose the history replay: %v", eventKindsOf(events))
	}
	userIndex := strings.Index(transcript.String(), "also check this")
	toolIndex := strings.Index(transcript.String(), "[tool_result toolu_01ABC]")
	if userIndex < 0 || toolIndex < 0 || userIndex > toolIndex {
		t.Fatalf("source order was not preserved: %q", transcript.String())
	}
	if _, err := parked.sessions.Lookup("toolu_01ABC", ""); err != nil {
		t.Fatalf("the parked call must stay pending: %v", err)
	}
}

func TestToolLoop_NextUserTurnAfterASettledLoopIsAReplay(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "")
	defer func() { _ = eng.Close(context.Background()) }()
	openAndPark(t, eng, chatToolCallCall("read the file"))
	continuation := chatContinuationCall("read the file", "let me look", "toolu_01ABC", `"contents"`)
	stream, err := eng.Open(context.Background(), continuation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drainEvents(t, stream); err != nil {
		t.Fatal(err)
	}

	settled := chatContinuationCall("read the file", "let me look", "toolu_01ABC", `"contents"`)
	// A second completed call in the history is replayed too; only a trailing
	// result group can be a continuation.
	settled.Messages = append(settled.Messages,
		aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
			aipapi.TextPart("got:contents"),
			{Kind: aipapi.PartJSON, ToolCallID: "toolu_02SECOND", ToolName: "Read", Content: json.RawMessage(`{"file_path":"y"}`)},
		}},
		aipapi.Message{Role: aipapi.RoleTool, Parts: []aipapi.Part{
			{Kind: aipapi.PartToolResult, ToolCallID: "toolu_02SECOND", Content: json.RawMessage(`"more"`)},
		}},
		aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("thanks")}},
	)
	next, err := eng.Open(context.Background(), settled)
	if err != nil {
		t.Fatal(err)
	}
	events, err := drainEvents(t, next)
	if err != nil {
		t.Fatal(err)
	}
	if warningIndex(events, "history_replay_degraded") < 0 {
		t.Fatalf("events=%v", eventKindsOf(events))
	}
}

func TestToolLoop_ToolChoiceNoneExposesNoTools(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "")
	defer func() { _ = eng.Close(context.Background()) }()
	call := chatToolCallCall("just answer")
	call.ToolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceNone}
	events := openAndPark(t, eng, call)
	for _, event := range events {
		switch event.Kind {
		case aipapi.EventToolCallStarted, aipapi.EventToolCallArgsDelta, aipapi.EventToolCallFinished:
			t.Fatalf("tool events must not exist under ToolChoice none: %v", eventKindsOf(events))
		}
	}
	if events[len(events)-1].FinishReason != "end_turn" {
		t.Fatalf("terminal=%+v", events[len(events)-1])
	}
}

func TestToolLoop_ForcedToolChoiceIsAnObservableDowngrade(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "")
	defer func() { _ = eng.Close(context.Background()) }()
	call := chatToolCallCall("read the file")
	call.ToolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired}
	stream, err := eng.Open(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	events, err := drainEvents(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.Close()
	if warningIndex(events, "tool_choice_degraded") < 0 {
		t.Fatalf("events=%v", eventKindsOf(events))
	}
}

func TestToolLoop_StrictSemanticsRejectsForcedChoiceBeforeUpstream(t *testing.T) {
	t.Parallel()
	deps := testDeps(t)
	deps.sidecar.Script = toolScript(t)
	cfg := testConfig(t)
	cfg.StrictSemantics = true
	eng, err := newEngine(context.Background(), cfg, nil, deps)
	if err != nil {
		t.Fatal(err)
	}
	call := chatToolCallCall("read the file")
	call.ToolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired}
	if _, err := eng.Open(context.Background(), call); !errors.Is(err, aipapi.ErrCapabilityReject) {
		t.Fatalf("err=%v", err)
	}
	if eng.sessions.LiveRuntimes() != 0 {
		t.Fatalf("live=%d", eng.sessions.LiveRuntimes())
	}
}

func TestToolLoop_ItemAuthorityToolActivityParks(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "")
	defer func() { _ = eng.Close(context.Background()) }()
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "anthropic/sonnet"},
		Items: []aipapi.Item{
			{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "read the file"}}},
		},
		Tools: chatToolCallCall("read the file").Tools,
	}
	events := openAndPark(t, eng, call)
	if last := events[len(events)-1]; last.Kind != aipapi.EventResponseFinished || last.FinishReason != "tool_use" {
		t.Fatalf("events=%v", eventKindsOf(events))
	}
	if _, err := eng.sessions.Lookup("toolu_01ABC", ""); err != nil {
		t.Fatalf("the item tool call must be parked: %v", err)
	}
}

func TestToolLoop_PendingTTLExpiresTheConversation(t *testing.T) {
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
	if _, err := eng.Open(context.Background(), chatContinuationCall("read the file", "let me look", "toolu_01ABC", `"contents"`)); err == nil {
		t.Fatal("expected an expired continuation refusal")
	} else {
		var terminal backendplugin.TerminalError
		if !errors.As(err, &terminal) || !strings.Contains(terminal.TerminalPluginError().Message, "continuation_expired") {
			t.Fatalf("err=%v", err)
		}
	}
}

func toolCatalogueTwo() []aipapi.ToolDef {
	return []aipapi.ToolDef{
		{Name: "Read", Parameters: json.RawMessage(`{"type":"object","properties":{"file_path":{"type":"string"}}}`)},
		{Name: "Bash", Parameters: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`)},
	}
}

func TestToolLoop_SequentialCallsPreserveFullHistory(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "second_tool")
	defer func() { _ = eng.Close(context.Background()) }()

	fresh := chatToolCallCall("read then run")
	fresh.Tools = toolCatalogueTwo()
	openAndPark(t, eng, fresh)

	first := chatContinuationCall("read then run", "let me look", "toolu_01ABC", `"contents"`)
	first.Tools = toolCatalogueTwo()
	stream, err := eng.Open(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	events, err := drainEvents(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || events[len(events)-1].Kind != aipapi.EventResponseFinished || events[len(events)-1].FinishReason != "tool_use" {
		t.Fatalf("second call did not park: %v", eventKindsOf(events))
	}

	// The full history now carries two completed tool results: the first is
	// history, the trailing one is the new continuation.
	second := aipapi.Call{
		ID:         "second-continuation",
		Route:      aipapi.RouteIntent{Selector: "anthropic/sonnet"},
		Tools:      toolCatalogueTwo(),
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
		Messages: append(append([]aipapi.Message(nil), first.Messages...),
			aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
				aipapi.TextPart("got:contents"),
				{Kind: aipapi.PartJSON, ToolCallID: "toolu_02SECOND", ToolName: "Bash", Content: json.RawMessage(`{"file_path":"z"}`)},
			}},
			aipapi.Message{Role: aipapi.RoleTool, Parts: []aipapi.Part{
				{Kind: aipapi.PartToolResult, ToolCallID: "toolu_02SECOND", Content: json.RawMessage(`"ok"`)},
			}},
		),
	}
	resumed, err := eng.Open(context.Background(), &second)
	if err != nil {
		t.Fatalf("sequential second result was rejected: %v", err)
	}
	final, err := drainEvents(t, resumed)
	if err != nil {
		t.Fatal(err)
	}
	last := final[len(final)-1]
	if last.Kind != aipapi.EventResponseFinished || last.FinishReason != "end_turn" {
		t.Fatalf("events=%v", eventKindsOf(final))
	}
	text := ""
	for _, event := range final {
		if event.Kind == aipapi.EventTextDelta {
			text += event.Delta
		}
	}
	if !strings.Contains(text, "got:ok") {
		t.Fatalf("final text=%q", text)
	}
}

func TestToolLoop_InterveningUserTextNeverSilentlyDrops(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		messages []aipapi.Message
	}{
		{
			name: "separate message before the result",
			messages: []aipapi.Message{
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("read the file")}},
				{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
					aipapi.TextPart("let me look"),
					{Kind: aipapi.PartJSON, ToolCallID: "toolu_01ABC", ToolName: "Read", Content: json.RawMessage(`{"file_path":"x"}`)},
				}},
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("also check this")}},
				{Role: aipapi.RoleTool, Parts: []aipapi.Part{
					{Kind: aipapi.PartToolResult, ToolCallID: "toolu_01ABC", Content: json.RawMessage(`"contents"`)},
				}},
			},
		},
		{
			name: "same message as the result",
			messages: []aipapi.Message{
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("read the file")}},
				{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
					aipapi.TextPart("let me look"),
					{Kind: aipapi.PartJSON, ToolCallID: "toolu_01ABC", ToolName: "Read", Content: json.RawMessage(`{"file_path":"x"}`)},
				}},
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{
					aipapi.TextPart("also check this"),
					{Kind: aipapi.PartToolResult, ToolCallID: "toolu_01ABC", Content: json.RawMessage(`"contents"`)},
				}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			parked := toolEngine(t, "")
			defer func() { _ = parked.Close(context.Background()) }()
			openAndPark(t, parked, chatToolCallCall("read the file"))

			// A prompt-echo engine proves the replay carried the intervening text.
			eng := toolEngine(t, "echo_prompt")
			defer func() { _ = eng.Close(context.Background()) }()
			call := &aipapi.Call{
				ID:         "mixed",
				Route:      aipapi.RouteIntent{Selector: "anthropic/sonnet"},
				Tools:      chatToolCallCall("x").Tools,
				ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
				Messages:   tc.messages,
			}
			stream, err := eng.Open(context.Background(), call)
			if err != nil {
				t.Fatal(err)
			}
			events, err := drainEvents(t, stream)
			if err != nil {
				t.Fatal(err)
			}
			var transcript strings.Builder
			sawReplay := false
			for _, event := range events {
				if event.Kind == aipapi.EventWarning && event.WarningCode == "continuation_replayed" {
					sawReplay = true
				}
				if event.Kind == aipapi.EventTextDelta {
					transcript.WriteString(event.Delta)
				}
			}
			if !sawReplay {
				t.Fatalf("missing continuation_replayed warning: %v", eventKindsOf(events))
			}
			if !strings.Contains(transcript.String(), "also check this") {
				t.Fatalf("intervening text was dropped: %q", transcript.String())
			}
			// The parked native turn was not released.
			if _, err := parked.sessions.Lookup("toolu_01ABC", ""); err != nil {
				t.Fatalf("the parked call must stay pending: %v", err)
			}
		})
	}
}

func TestToolLoop_ChangedCommittedRoleRefuses(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "")
	defer func() { _ = eng.Close(context.Background()) }()
	openAndPark(t, eng, chatToolCallCall("read the file"))

	call := &aipapi.Call{
		ID:         "wrong-role",
		Route:      aipapi.RouteIntent{Selector: "anthropic/sonnet"},
		Tools:      chatToolCallCall("x").Tools,
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("read the file")}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{
				{Kind: aipapi.PartJSON, ToolCallID: "toolu_01ABC", ToolName: "Read", Content: json.RawMessage(`{"file_path":"x"}`)},
			}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{
				{Kind: aipapi.PartToolResult, ToolCallID: "toolu_01ABC", Content: json.RawMessage(`"contents"`)},
			}},
		},
	}
	_, err := eng.Open(context.Background(), call)
	var terminal backendplugin.TerminalError
	if !errors.As(err, &terminal) || !strings.Contains(terminal.TerminalPluginError().Message, "continuation_mismatch") {
		t.Fatalf("err=%v", err)
	}
	if _, err := eng.sessions.Lookup("toolu_01ABC", ""); err != nil {
		t.Fatalf("the parked call must stay pending: %v", err)
	}
}

func TestToolLoop_ChangedPriorResultRefuses(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "second_tool")
	defer func() { _ = eng.Close(context.Background()) }()
	fresh := chatToolCallCall("read then run")
	fresh.Tools = toolCatalogueTwo()
	openAndPark(t, eng, fresh)

	first := chatContinuationCall("read then run", "let me look", "toolu_01ABC", `"contents"`)
	first.Tools = toolCatalogueTwo()
	stream, err := eng.Open(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drainEvents(t, stream); err != nil {
		t.Fatal(err)
	}

	// The same request with a modified prior result content must not reproduce the
	// committed prefix.
	tampered := aipapi.Call{
		ID:         "tampered",
		Route:      aipapi.RouteIntent{Selector: "anthropic/sonnet"},
		Tools:      toolCatalogueTwo(),
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
		Messages: append(append([]aipapi.Message(nil), first.Messages...),
			aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
				aipapi.TextPart("got:contents"),
				{Kind: aipapi.PartJSON, ToolCallID: "toolu_02SECOND", ToolName: "Bash", Content: json.RawMessage(`{"file_path":"z"}`)},
			}},
			aipapi.Message{Role: aipapi.RoleTool, Parts: []aipapi.Part{
				{Kind: aipapi.PartToolResult, ToolCallID: "toolu_02SECOND", Content: json.RawMessage(`"ok"`)},
			}},
		),
	}
	// Replace the first completed result with tampered content.
	tampered.Messages[2].Parts[0].Content = json.RawMessage(`"TAMPERED"`)
	_, err = eng.Open(context.Background(), &tampered)
	var terminal backendplugin.TerminalError
	if !errors.As(err, &terminal) || !strings.Contains(terminal.TerminalPluginError().Message, "continuation_mismatch") {
		t.Fatalf("err=%v", err)
	}
}

func TestToolLoop_ErrorLookingContentIsPreservedWithoutSynthesizedStatus(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "")
	defer func() { _ = eng.Close(context.Background()) }()
	openAndPark(t, eng, chatToolCallCall("read the file"))
	continuation := chatContinuationCall("read the file", "let me look", "toolu_01ABC", `"{\"error\":\"permission denied\",\"is_error\":true}"`)
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
	// The caller's exact text is forwarded; the connector neither infers failure
	// from the string nor claims a success status.
	if !strings.Contains(text, `got:{"error":"permission denied","is_error":true}`) {
		t.Fatalf("text=%q", text)
	}
}

func TestToolLoop_DuplicateCommittedCallRefuses(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "")
	defer func() { _ = eng.Close(context.Background()) }()
	openAndPark(t, eng, chatToolCallCall("read the file"))

	// The same opaque call id appears in two assistant messages. Canonical
	// validation accepts the message-based request, so the connector must refuse
	// it before releasing; otherwise the text between the occurrences disappears.
	call := &aipapi.Call{
		ID:         "duplicate-call",
		Route:      aipapi.RouteIntent{Selector: "anthropic/sonnet"},
		Tools:      chatToolCallCall("x").Tools,
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("read the file")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
				aipapi.TextPart("let me look"),
				{Kind: aipapi.PartJSON, ToolCallID: "toolu_01ABC", ToolName: "Read", Content: json.RawMessage(`{"file_path":"x"}`)},
			}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
				aipapi.TextPart("INJECTED-VISIBLE-TEXT"),
				{Kind: aipapi.PartJSON, ToolCallID: "toolu_01ABC", ToolName: "Read", Content: json.RawMessage(`{"file_path":"x"}`)},
			}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{
				{Kind: aipapi.PartToolResult, ToolCallID: "toolu_01ABC", Content: json.RawMessage(`"contents"`)},
			}},
		},
	}
	if err := call.Validate(); err != nil {
		t.Fatalf("canonical validation rejected the fixture: %v", err)
	}
	_, err := eng.Open(context.Background(), call)
	var terminal backendplugin.TerminalError
	if !errors.As(err, &terminal) || !strings.Contains(terminal.TerminalPluginError().Message, "continuation_mismatch") {
		t.Fatalf("err=%v", err)
	}
	if _, err := eng.sessions.Lookup("toolu_01ABC", ""); err != nil {
		t.Fatalf("the pending call must not be released: %v", err)
	}
}

func TestToolLoop_RegexSchemasRefuseBeforeUpstream(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "")
	defer func() { _ = eng.Close(context.Background()) }()
	schemas := []string{
		`{"type":"object","properties":{"x":{"type":"string","pattern":"^[a-z]+$"}}}`,
		`{"type":"object","properties":{"x":{"type":"string","pattern":"["}}}`,
		`{"type":"object","patternProperties":{"^x":{"type":"string"}}}`,
		`{"type":"object","patternProperties":{"[":{"type":"string"}}}`,
		`{"type":"object","properties":{"x":{"type":"object","patternProperties":{"^x":{"type":"string"}}}}}`,
	}
	for _, schema := range schemas {
		call := chatToolCallCall("read the file")
		call.Tools = []aipapi.ToolDef{{Name: "Read", Parameters: json.RawMessage(schema)}}
		_, err := eng.Open(context.Background(), call)
		var terminal backendplugin.TerminalError
		if !errors.As(err, &terminal) {
			t.Fatalf("schema %s was admitted: %v", schema, err)
		}
		pluginErr := terminal.TerminalPluginError()
		if pluginErr.Code != backendplugin.ErrorCodeFailedPrecondition || !strings.Contains(pluginErr.Message, "not supported") {
			t.Fatalf("schema %s error=%+v", schema, pluginErr)
		}
		if eng.sessions.LiveRuntimes() != 0 {
			t.Fatalf("schema %s started upstream runtime(s)", schema)
		}
	}
}

func TestToolLoop_PostToolTextFailureIsBoundedAndNonRetryable(t *testing.T) {
	t.Parallel()
	eng := toolEngine(t, "post_tool_text")
	defer func() { _ = eng.Close(context.Background()) }()
	stream, err := eng.Open(context.Background(), chatToolCallCall("read the file"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = drainEvents(t, stream)
	_ = stream.Close()
	var terminal backendplugin.TerminalError
	if !errors.As(err, &terminal) {
		t.Fatalf("err=%v", err)
	}
	pluginErr := terminal.TerminalPluginError()
	if pluginErr.Code != backendplugin.ErrorCodeFailedPrecondition || pluginErr.Retryable {
		t.Fatalf("plugin error=%+v", pluginErr)
	}
	if !strings.Contains(pluginErr.Message, "text after a tool_use block") {
		t.Fatalf("message was not the connector-owned summary: %q", pluginErr.Message)
	}
	if strings.Contains(pluginErr.Message, "raw sidecar detail") {
		t.Fatalf("raw sidecar detail leaked: %q", pluginErr.Message)
	}
}
