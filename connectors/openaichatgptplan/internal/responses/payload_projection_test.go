package responses_test

// Behavior proofs for the canonical-to-wire projection (spec task 4.2).
//
// The task 4.1 fixture matrix in payload_fixtures_test.go is the primary wire
// contract, and these tests do not replace it. They exist because the task's
// rules must fail for the rule they NAME, and because the item authority
// (Call.Items) is a limit of the 4.1 corpus rather than a form somebody else
// owns:
//
//   - the system-text folding proof says the instructions string must carry
//     the folded text, so a projector that silently drops folding fails here
//     with the folding reason instead of only with a whole-body diff;
//   - the chronology proof says the projected input must follow the canonical
//     order and the canonical call ids, so a projector that reorders tool
//     items fails here with the chronology reason;
//   - the absence proof says no Codex identity prompt or <user_instructions>
//     glue may reach the request, so an injected marker fails here with the
//     marker's own name;
//   - the tool-result carrier proof says a result carried as literal Part.Text
//     must survive the real item-to-message projection, so a Content-only read
//     fails here with the tool's answer replaced by empty output;
//   - the function-call carrier proof says complete canonical identity owns the
//     call id and name while Content stays argument JSON, so argument data
//     named "type" or shaped like an envelope is neither refused nor rewritten;
//   - the join-key proof says a tool call and its result must carry the same
//     canonical id bytes, so trimming a copy for a presence check and then
//     emitting the trimmed value fails here with the mismatched ids.
//
// The item-authority tests pin the ordinary message and tool forms the 4.1
// corpus does not contain: message items, tool_call items and tool_result
// items, with the call id taken from the canonical item. Reasoning replay
// (spec task 8.2) and the integration consumers (spec task 8.1) stay outside
// this file; the refusal table records that boundary explicitly.
import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/responses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// projectedRequest projects one canonical call and fails the test when the
// seam refuses it.
func projectedRequest(t *testing.T, in responses.ProjectInput) responses.WireRequest {
	t.Helper()
	req, err := responses.ProjectCall(in)
	if err != nil {
		t.Fatalf("projecting the canonical call must produce a public Responses request, got error: %v", err)
	}
	return req
}

// decodedBody reads a projected body without loss, then structurally for
// field inspection.
//
// The ambiguity check runs first: a document that repeats a member could hide
// a value behind the repetition, and a last-wins decode would inspect a
// document the projector did not produce. The strict reader is the one the
// 4.1 contract test uses, and the check stays independent of this test's own
// structural decode.
func decodedBody(t *testing.T, req responses.WireRequest) map[string]any {
	t.Helper()
	document := scanStrictWireDocument(req.Body)
	if repeated := document.repeatedMember(); repeated != nil {
		t.Fatalf("the projected request body is ambiguous and cannot be inspected: %v", repeated)
	}
	if document.parseErr != nil {
		t.Fatalf("the projected request body is not a JSON document: %v", document.parseErr)
	}
	var body map[string]any
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatalf("the projected request body cannot be decoded: %v", err)
	}
	return body
}

// bodyString returns one top-level string field.
func bodyString(t *testing.T, body map[string]any, key string) string {
	t.Helper()
	value, ok := body[key]
	if !ok {
		t.Fatalf("the projected request is missing the top-level %q field: %v", key, body)
	}
	text, ok := value.(string)
	if !ok {
		t.Fatalf("the projected request field %q is %T, want a string", key, value)
	}
	return text
}

// inputItems returns the projected input items in canonical order.
func inputItems(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, ok := body["input"].([]any)
	if !ok {
		t.Fatalf("the projected request is missing the input array: %v", body)
	}
	items := make([]map[string]any, 0, len(raw))
	for i, value := range raw {
		item, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("the projected input item %d is %T, want an object", i, value)
		}
		items = append(items, item)
	}
	return items
}

// inputSequence renders the projected input as an ordered identity sequence.
func inputSequence(t *testing.T, body map[string]any) []string {
	t.Helper()
	items := inputItems(t, body)
	out := make([]string, 0, len(items))
	for _, item := range items {
		typ, _ := item["type"].(string)
		switch typ {
		case "message":
			role, _ := item["role"].(string)
			out = append(out, "message:"+role)
		case "function_call":
			callID, _ := item["call_id"].(string)
			out = append(out, "function_call:"+callID)
		case "function_call_output":
			callID, _ := item["call_id"].(string)
			out = append(out, "function_call_output:"+callID)
		default:
			out = append(out, typ)
		}
	}
	return out
}

// requireSequence compares two identity sequences against a named rule.
func requireSequence(t *testing.T, rule string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s\n  projected: %v\n  canonical: %v", rule, got, want)
	}
}

// TestSystemConversationTextIsFoldedIntoInstructions is the folding proof.
//
// System-role text is not emitted as an input message item: the public route
// rejects explicit system items. Its text is folded into the instructions
// string after the instruction blocks, in conversation order, and a block
// that is whitespace only contributes nothing.
func TestSystemConversationTextIsFoldedIntoInstructions(t *testing.T) {
	t.Parallel()
	const foldingRule = "the system conversation text must be folded into the instructions string after the instruction blocks, in conversation order"

	t.Run("message authority", func(t *testing.T) {
		t.Parallel()
		call := aipapi.Call{
			Instructions: []aipapi.Message{
				{Role: aipapi.RoleDeveloper, Parts: []aipapi.Part{
					aipapi.TextPart("Answer in one sentence."),
					aipapi.TextPart("Prefer small diffs."),
				}},
				{Role: aipapi.RoleDeveloper, Parts: []aipapi.Part{aipapi.TextPart("Use the repository's own tests.")}},
			},
			Messages: []aipapi.Message{
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Where is order 42?")}},
				{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("Never invent citations.")}},
				{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("Let me look that up.")}},
				{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("   ")}},
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("And order 43?")}},
				{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("Always name the shipping file.")}},
			},
		}
		body := decodedBody(t, projectedRequest(t, responses.ProjectInput{Call: call, Model: fixtureModel}))
		const want = "Answer in one sentence.\nPrefer small diffs.\n\nUse the repository's own tests.\n\nNever invent citations.\n\nAlways name the shipping file."
		if got := bodyString(t, body, "instructions"); got != want {
			t.Fatalf("%s\n  projected: %q\n  want: %q", foldingRule, got, want)
		}
		requireSequence(t, "the system-role conversation items must not become input message items; only the non-system chronology may remain",
			inputSequence(t, body),
			[]string{"message:user", "message:assistant", "message:user"})
	})

	t.Run("item authority", func(t *testing.T) {
		t.Parallel()
		call := aipapi.Call{
			Items: []aipapi.Item{
				{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "Where is order 42?"}}},
				{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleSystem, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "Never invent citations."}}},
				{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleAssistant, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "Let me look that up."}}},
				{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleSystem, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "  "}}},
				{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleSystem, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "Always name the shipping file."}}},
				{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "And order 43?"}}},
			},
		}
		body := decodedBody(t, projectedRequest(t, responses.ProjectInput{Call: call, Model: fixtureModel}))
		const want = "Never invent citations.\n\nAlways name the shipping file."
		if got := bodyString(t, body, "instructions"); got != want {
			t.Fatalf("%s\n  projected: %q\n  want: %q", foldingRule, got, want)
		}
		requireSequence(t, "the system-role message items must not become input message items",
			inputSequence(t, body),
			[]string{"message:user", "message:assistant", "message:user"})
	})
}

// TestInputPreservesCanonicalToolChronology is the chronology proof.
//
// The order of the canonical conversation is the order of the input items, and
// the call id joins a function call with its output. Nothing is reordered,
// dropped or invented, and a message that carries a function call is split at
// that part rather than moved as a whole.
func TestInputPreservesCanonicalToolChronology(t *testing.T) {
	t.Parallel()
	const chronologyRule = "the projected input must follow the canonical chronology: no message, tool call or tool result may be reordered, dropped or invented"

	t.Run("message authority", func(t *testing.T) {
		t.Parallel()
		call := aipapi.Call{
			Tools: []aipapi.ToolDef{
				{Name: "get_weather", Parameters: json.RawMessage(`{"type":"object"}`)},
				{Name: "ping"},
			},
			Messages: []aipapi.Message{
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("start")}},
				{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
					aipapi.TextPart("before"),
					{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"type":"function_call","call_id":"call_a","name":"get_weather","arguments":"{\"city\":\"Paris\"}"}`)},
					aipapi.TextPart("after"),
					{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"type":"function_call","call_id":"call_b","name":"ping","arguments":"{}"}`)},
				}},
				{Role: aipapi.RoleTool, Parts: []aipapi.Part{
					{Kind: aipapi.PartToolResult, ToolCallID: "call_a", Content: json.RawMessage(`"sunny"`)},
					{Kind: aipapi.PartToolResult, ToolCallID: "call_b", Content: json.RawMessage(`{"ok":true}`)},
				}},
			},
		}
		body := decodedBody(t, projectedRequest(t, responses.ProjectInput{Call: call, Model: fixtureModel}))
		requireSequence(t, chronologyRule,
			inputSequence(t, body),
			[]string{
				"additional_tools",
				"message:user",
				"message:assistant",
				"function_call:call_a",
				"message:assistant",
				"function_call:call_b",
				"function_call_output:call_a",
				"function_call_output:call_b",
			})
	})

	t.Run("item authority", func(t *testing.T) {
		t.Parallel()
		call := aipapi.Call{
			Items: []aipapi.Item{
				{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "start"}}},
				{Kind: aipapi.ItemKindToolCall, ToolCall: &aipapi.ToolCallItem{CallID: "call_a", Name: "get_weather", Arguments: json.RawMessage(`{"city":"Paris"}`)}},
				{Kind: aipapi.ItemKindToolResult, ToolResult: &aipapi.ToolResultItem{CallID: "call_a", Name: "get_weather", Output: "sunny"}},
				{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleAssistant, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "done"}}},
				{Kind: aipapi.ItemKindToolCall, ToolCall: &aipapi.ToolCallItem{CallID: "call_b", Name: "ping", Arguments: json.RawMessage(`{}`)}},
				{Kind: aipapi.ItemKindToolResult, ToolResult: &aipapi.ToolResultItem{CallID: "call_b", Name: "ping", Output: `{"ok":true}`}},
				{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "next"}}},
			},
		}
		body := decodedBody(t, projectedRequest(t, responses.ProjectInput{Call: call, Model: fixtureModel}))
		requireSequence(t, chronologyRule,
			inputSequence(t, body),
			[]string{
				"message:user",
				"function_call:call_a",
				"function_call_output:call_a",
				"message:assistant",
				"function_call:call_b",
				"function_call_output:call_b",
				"message:user",
			})
	})
}

// TestProjectedItemAuthorityMessageAndToolContent pins the ordinary item
// forms the 4.1 message-authority corpus does not contain.
//
// The expected document is compared as a whole, so a projection that adds a
// key the contract does not have fails the same way a projection that drops
// one does.
func TestProjectedItemAuthorityMessageAndToolContent(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Tools: []aipapi.ToolDef{
			{
				Name:        "get_weather",
				Description: "Look up the current weather.",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`),
			},
			{Name: "ping"},
		},
		Items: []aipapi.Item{
			{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleSystem, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "Never invent citations."}}},
			{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleDeveloper, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "Answer in one sentence."}}},
			{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{
				{Kind: aipapi.ContentPartText, Text: "What is in these pictures?"},
				{Kind: aipapi.ContentPartImageRef, ImageRef: "data:image/png;base64,iVBORw0KGgo=", ImageMIME: "image/png"},
				{Kind: aipapi.ContentPartImageRef, ImageRef: "https://example.test/two.png"},
			}},
			{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{
				{Kind: aipapi.ContentPartText, Text: "Summarize the attached report."},
				{Kind: aipapi.ContentPartFileRef, FileData: "JVBERi0xLjQK", FileMIME: "application/pdf", FileName: "report.pdf"},
			}},
			{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleAssistant, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "Let me check."}}},
			{Kind: aipapi.ItemKindToolCall, ToolCall: &aipapi.ToolCallItem{CallID: "call_1", Name: "get_weather", Arguments: json.RawMessage(`{"city":"Paris"}`)}},
			{Kind: aipapi.ItemKindToolResult, ToolResult: &aipapi.ToolResultItem{CallID: "call_1", Name: "get_weather", Output: "sunny"}},
			{Kind: aipapi.ItemKindToolCall, ToolCall: &aipapi.ToolCallItem{CallID: "call_2", Name: "ping", Arguments: json.RawMessage(`{}`)}},
			{Kind: aipapi.ItemKindToolResult, ToolResult: &aipapi.ToolResultItem{CallID: "call_2", Output: `{"ok":true}`}},
		},
	}
	wantBody := `{
  "model": "gpt-6.1-sol",
  "instructions": "Never invent citations.",
  "input": [
    {
      "type": "additional_tools",
      "role": "developer",
      "tools": [
        {
          "type": "function",
          "name": "get_weather",
          "description": "Look up the current weather.",
          "parameters": {
            "type": "object",
            "properties": {"city": {"type": "string"}},
            "required": ["city"]
          }
        },
        {"type": "function", "name": "ping"}
      ]
    },
    {
      "type": "message",
      "role": "developer",
      "content": [{"type": "input_text", "text": "Answer in one sentence."}]
    },
    {
      "type": "message",
      "role": "user",
      "content": [
        {"type": "input_text", "text": "What is in these pictures?"},
        {"type": "input_image", "image_url": "data:image/png;base64,iVBORw0KGgo="},
        {"type": "input_image", "image_url": "https://example.test/two.png"}
      ]
    },
    {
      "type": "message",
      "role": "user",
      "content": [
        {"type": "input_text", "text": "Summarize the attached report."},
        {"type": "input_file", "filename": "report.pdf", "file_data": "JVBERi0xLjQK"}
      ]
    },
    {
      "type": "message",
      "role": "assistant",
      "content": [{"type": "input_text", "text": "Let me check."}]
    },
    {
      "type": "function_call",
      "call_id": "call_1",
      "name": "get_weather",
      "arguments": "{\"city\":\"Paris\"}"
    },
    {
      "type": "function_call_output",
      "call_id": "call_1",
      "output": "sunny"
    },
    {
      "type": "function_call",
      "call_id": "call_2",
      "name": "ping",
      "arguments": "{}"
    },
    {
      "type": "function_call_output",
      "call_id": "call_2",
      "output": "{\"ok\":true}"
    }
  ],
  "store": false,
  "stream": true
}`
	req := projectedRequest(t, responses.ProjectInput{Call: call, Model: fixtureModel})
	got := canonicalJSON(t, "projected item-authority body", req.Body)
	want := canonicalJSON(t, "expected item-authority body", []byte(wantBody))
	if got != want {
		t.Fatalf("the item-authority projection must produce the documented ordinary message and tool request\n--- projected ---\n%s\n--- documented ---\n%s", got, want)
	}
}

// TestProjectedChatCompletionsStyleFunctionCall pins the second ordinary
// function-call carrier the canonical message model accepts: a Chat
// Completions entry whose identity sits under id/function. A projector that
// only reads the Responses field names would drop the call or invent a
// missing identity.
func TestProjectedChatCompletionsStyleFunctionCall(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("ping?")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
				{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"type":"function","id":"call_chat","function":{"name":"ping","arguments":"{\"n\":1}"}}`)},
			}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{
				{Kind: aipapi.PartToolResult, ToolCallID: "call_chat", Content: json.RawMessage(`"pong"`)},
			}},
		},
	}
	body := decodedBody(t, projectedRequest(t, responses.ProjectInput{Call: call, Model: fixtureModel}))
	requireSequence(t, "the projected input must carry the Chat Completions function call with its canonical identity",
		inputSequence(t, body),
		[]string{"message:user", "function_call:call_chat", "function_call_output:call_chat"})
	items := inputItems(t, body)
	functionCall := items[1]
	if got := functionCall["name"]; got != "ping" {
		t.Fatalf("the Chat Completions function name must come from function.name, got %v", got)
	}
	if got := functionCall["arguments"]; got != `{"n":1}` {
		t.Fatalf("the Chat Completions arguments must be the exact argument text, got %v", got)
	}
}

// TestProjectedToolResultCarrierSelection pins which canonical carrier owns a
// tool result's output text.
//
// aipapi.ProjectItemsToLegacyView emits an ordinary ToolResultItem as a
// PartToolResult whose literal output is Part.Text and whose Part.Content is
// empty; the stream-observer form carries the same answer as JSON in
// Part.Content. The projector must read the populated carrier, and when both
// are populated it must not pick one silently.
func TestProjectedToolResultCarrierSelection(t *testing.T) {
	t.Parallel()
	const carrierRule = "a canonical tool result must reach the wire as its complete output text, joined to its call id"

	t.Run("real item projection preserves the literal output", func(t *testing.T) {
		t.Parallel()
		const output = "result must survive\nsecond line: \"quoted\""
		items := aipapi.Call{Items: []aipapi.Item{
			{Kind: aipapi.ItemKindMessage, ID: "msg_user", Role: aipapi.RoleUser, Content: []aipapi.ContentPart{
				{Kind: aipapi.ContentPartText, Text: "What is in the box?"},
			}},
			{Kind: aipapi.ItemKindToolCall, ID: "fc_item", ToolCall: &aipapi.ToolCallItem{
				CallID: "call_1", Name: "inspect_box", Arguments: json.RawMessage(`{"box":"A"}`),
			}},
			{Kind: aipapi.ItemKindToolResult, ID: "result_item", ToolResult: &aipapi.ToolResultItem{
				CallID: "call_1", Name: "inspect_box", Output: output,
			}},
		}}
		if err := items.Validate(); err != nil {
			t.Fatalf("the item-authority fixture must be a valid canonical call: %v", err)
		}
		view, err := aipapi.ProjectItemsToLegacyView(items, aipapi.DefaultLegacyProjectionTarget(nil, aipapi.ReasoningReplaySupport{}))
		if err != nil {
			t.Fatalf("the public item-to-message projection must produce the legacy view, got error: %v", err)
		}
		call := aipapi.Call{Instructions: view.Instructions, Messages: view.Messages}
		if err := call.Validate(); err != nil {
			t.Fatalf("the projected message-authority call must be valid: %v", err)
		}
		carrier, ok := findToolResultPart(call.Messages, "call_1")
		if !ok {
			t.Fatalf("the real item-to-message projection must produce the tool result part for call_1")
		}
		if carrier.Text != output || len(carrier.Content) != 0 {
			t.Fatalf("this regression's premise is that the real item-to-message projection carries the output as Part.Text and leaves Part.Content empty; got Text %q Content %q", carrier.Text, carrier.Content)
		}

		body := decodedBody(t, projectedRequest(t, responses.ProjectInput{Call: call, Model: fixtureModel}))
		requireSequence(t, "the projected input must carry the item-authority call and its result in canonical order",
			inputSequence(t, body),
			[]string{"message:user", "function_call:call_1", "function_call_output:call_1"})
		projected := inputItems(t, body)
		if got := projected[1]["arguments"]; got != `{"box":"A"}` {
			t.Fatalf("%s\n  projected arguments: %v", carrierRule, got)
		}
		if got := projected[2]["call_id"]; got != "call_1" {
			t.Fatalf("%s\n  projected call id: %v", carrierRule, got)
		}
		if got := projected[2]["output"]; got != output {
			t.Fatalf("%s\n  projected output: %q\n  canonical output: %q", carrierRule, got, output)
		}
	})

	t.Run("agreeing duplicate carriers project once", func(t *testing.T) {
		t.Parallel()
		call := aipapi.Call{Messages: []aipapi.Message{
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
				Kind:       aipapi.PartToolResult,
				ToolCallID: "call_1",
				Text:       "sunny",
				Content:    json.RawMessage(`"sunny"`),
			}}},
		}}
		body := decodedBody(t, projectedRequest(t, responses.ProjectInput{Call: call, Model: fixtureModel}))
		items := inputItems(t, body)
		if len(items) != 1 {
			t.Fatalf("%s\n  the agreeing carriers are one answer and must not produce %d items: %v", carrierRule, len(items), items)
		}
		if got := items[0]["output"]; got != "sunny" {
			t.Fatalf("%s\n  projected output: %v", carrierRule, got)
		}
	})

	t.Run("disagreeing carriers are refused explicitly", func(t *testing.T) {
		t.Parallel()
		call := aipapi.Call{Messages: []aipapi.Message{
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
				Kind:       aipapi.PartJSON,
				ToolCallID: "call_1",
				ToolName:   "widget",
				Content:    json.RawMessage(`{"id":"w1"}`),
			}}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
				Kind:       aipapi.PartToolResult,
				ToolCallID: "call_1",
				ToolName:   "widget",
				Text:       "the widget is available",
				Content:    json.RawMessage(`{"ok":true}`),
			}}},
		}}
		if err := call.Validate(); err != nil {
			t.Fatalf("the disagreeing-carrier fixture must be canonical-valid: %v", err)
		}
		_, err := responses.ProjectCall(responses.ProjectInput{Call: call, Model: fixtureModel})
		if !errors.Is(err, responses.ErrCallNotRepresentable) {
			t.Fatalf("a disagreeing literal/JSON tool result must be refused, got %v", err)
		}
		var projection *responses.ProjectionError
		if !errors.As(err, &projection) || projection.Reason != responses.ProjectionReasonToolResult {
			t.Fatalf("refusal=%v, want the tool_result projection reason", err)
		}
	})
}

// findToolResultPart returns the tool-result part for one call id.
func findToolResultPart(messages []aipapi.Message, callID string) (aipapi.Part, bool) {
	for _, msg := range messages {
		if msg.Role != aipapi.RoleTool {
			continue
		}
		for _, part := range msg.Parts {
			if part.Kind == aipapi.PartToolResult && part.ToolCallID == callID {
				return part, true
			}
		}
	}
	return aipapi.Part{}, false
}

// TestProjectedStableJoinKeyThroughRealLegacyProjection pins fixture rule 9 for
// a call id that canonical validation admits by its trimmed presence.
//
// Canonical validation accepts a tool call/result id whose bytes have
// surrounding whitespace, and the public item-to-message projection copies the
// id verbatim into both message parts. A presence check may therefore trim a
// copy, but the authoritative join key itself must reach the wire unchanged on
// both items: a tool answer whose call id no longer equals its model-authored
// call's id is a corrupted history, not a lawful degradation.
func TestProjectedStableJoinKeyThroughRealLegacyProjection(t *testing.T) {
	t.Parallel()
	const (
		joinKey  = " call_1 "
		joinRule = "the call id joins a tool call to its result, so both projected items must carry the canonical bytes unchanged"
	)
	items := aipapi.Call{Items: []aipapi.Item{
		{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{
			{Kind: aipapi.ContentPartText, Text: "What is in the box?"},
		}},
		{Kind: aipapi.ItemKindToolCall, ToolCall: &aipapi.ToolCallItem{
			CallID: joinKey, Name: "inspect_box", Arguments: json.RawMessage(`{"box":"A"}`),
		}},
		{Kind: aipapi.ItemKindToolResult, ToolResult: &aipapi.ToolResultItem{
			CallID: joinKey, Name: "inspect_box", Output: "packing peanuts",
		}},
	}}
	if err := items.Validate(); err != nil {
		t.Fatalf("the premise must be a valid canonical call whose call id has surrounding whitespace: %v", err)
	}

	// The item authority must preserve the authored call id on both sides.
	direct := inputItems(t, decodedBody(t, projectedRequest(t, responses.ProjectInput{Call: items, Model: fixtureModel})))
	if len(direct) != 3 {
		t.Fatalf("%s\n  the item-authority input must carry the message, the call and its result, got %d items: %v", joinRule, len(direct), direct)
	}
	if got := direct[1]["call_id"]; got != joinKey {
		t.Fatalf("%s\n  the item-authority function call must keep the canonical call id %q, got %v", joinRule, joinKey, got)
	}
	if got := direct[2]["call_id"]; got != joinKey {
		t.Fatalf("%s\n  the item-authority function call output must keep the canonical call id %q, got %v", joinRule, joinKey, got)
	}

	// The real public item-to-message projection must copy the id verbatim, so
	// the message authority starts from the same join-key premise.
	view, err := aipapi.ProjectItemsToLegacyView(items, aipapi.DefaultLegacyProjectionTarget(nil, aipapi.ReasoningReplaySupport{}))
	if err != nil {
		t.Fatalf("the public item-to-message projection must produce the legacy view, got error: %v", err)
	}
	messageCall := aipapi.Call{Instructions: view.Instructions, Messages: view.Messages}
	if err := messageCall.Validate(); err != nil {
		t.Fatalf("the projected message-authority call must be valid: %v", err)
	}
	if len(messageCall.Messages) != 3 || len(messageCall.Messages[1].Parts) != 1 || len(messageCall.Messages[2].Parts) != 1 {
		t.Fatalf("%s\n  this regression's premise is a user message followed by one call and one result, got %d messages: %v", joinRule, len(messageCall.Messages), messageCall.Messages)
	}
	callPart := messageCall.Messages[1].Parts[0]
	resultPart := messageCall.Messages[2].Parts[0]
	if callPart.ToolCallID != joinKey || resultPart.ToolCallID != joinKey {
		t.Fatalf("%s\n  the public item-to-message projection must copy the call id verbatim, got call %q and result %q", joinRule, callPart.ToolCallID, resultPart.ToolCallID)
	}

	projected := inputItems(t, decodedBody(t, projectedRequest(t, responses.ProjectInput{Call: messageCall, Model: fixtureModel})))
	if len(projected) != 3 {
		t.Fatalf("%s\n  the message-authority input must carry the message, the call and its result, got %d items: %v", joinRule, len(projected), projected)
	}
	callID, _ := projected[1]["call_id"].(string)
	resultID, _ := projected[2]["call_id"].(string)
	if callID != resultID {
		t.Fatalf("%s\n  the projected function call carries call_id %q while its function call output carries %q, so the answer no longer joins its model-authored call", joinRule, callID, resultID)
	}
	if callID != joinKey {
		t.Fatalf("%s\n  the projected function call must keep the canonical call id %q, got %q", joinRule, joinKey, callID)
	}
}

// TestProjectedEnvelopeJoinKeyKeepsAuthoredBytes applies the same identity-bytes
// rule to the envelope carrier. When the Part carries no identity the JSON
// envelope owns the call id; trimming a copy may decide presence, but the id the
// envelope authored is the join key and must reach the wire unchanged.
func TestProjectedEnvelopeJoinKeyKeepsAuthoredBytes(t *testing.T) {
	t.Parallel()
	const (
		joinKey  = " call_2 "
		joinRule = "when the envelope owns the identity, the authored call id is the join key and must be carried through unchanged"
	)
	call := aipapi.Call{Messages: []aipapi.Message{
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
			Kind:    aipapi.PartJSON,
			Content: json.RawMessage(`{"type":"function_call","call_id":" call_2 ","name":"widget","arguments":"{}"}`),
		}}},
		{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
			Kind:       aipapi.PartToolResult,
			ToolCallID: joinKey,
			Content:    json.RawMessage(`"ok"`),
		}}},
	}}
	if err := call.Validate(); err != nil {
		t.Fatalf("the premise must be a valid canonical call whose envelope call id has surrounding whitespace: %v", err)
	}
	projected := inputItems(t, decodedBody(t, projectedRequest(t, responses.ProjectInput{Call: call, Model: fixtureModel})))
	if len(projected) != 2 {
		t.Fatalf("%s\n  the input must carry the envelope call and its result, got %d items: %v", joinRule, len(projected), projected)
	}
	callID, _ := projected[0]["call_id"].(string)
	resultID, _ := projected[1]["call_id"].(string)
	if callID != resultID {
		t.Fatalf("%s\n  the projected function call carries call_id %q while its function call output carries %q, so the answer no longer joins its model-authored call", joinRule, callID, resultID)
	}
	if callID != joinKey {
		t.Fatalf("%s\n  the projected function call must keep the authored call id %q, got %q", joinRule, joinKey, callID)
	}
}

// TestProjectedIdentityNameKeepsAuthoredBytes pins the authored function name
// against the same trimming pass that decides its presence.
//
// Presence is decided on a trimmed copy, so " widget " is a usable name, but the
// name is still model-authored content: the projector must emit the bytes the
// producer put in the history, next to the result that joins the call. Emitting
// the trimmed copy would silently rename the call the model authored.
func TestProjectedIdentityNameKeepsAuthoredBytes(t *testing.T) {
	t.Parallel()
	const (
		authoredName = " widget "
		nameRule     = "a tool call's authored name must reach the wire unchanged while its result still joins the call"
	)
	cases := []struct {
		name   string
		callID string
		part   aipapi.Part
	}{
		{
			name:   "part identity name",
			callID: "call_1",
			part: aipapi.Part{
				Kind:       aipapi.PartJSON,
				ToolCallID: "call_1",
				ToolName:   authoredName,
				Content:    json.RawMessage(`{"box":"A"}`),
			},
		},
		{
			name:   "responses envelope identity name",
			callID: "call_2",
			part: aipapi.Part{
				Kind:    aipapi.PartJSON,
				Content: json.RawMessage(`{"type":"function_call","call_id":"call_2","name":" widget ","arguments":"{}"}`),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := aipapi.Call{Messages: []aipapi.Message{
				{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{tc.part}},
				{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
					Kind:       aipapi.PartToolResult,
					ToolCallID: tc.callID,
					Text:       "packing peanuts",
				}}},
			}}
			if err := call.Validate(); err != nil {
				t.Fatalf("the premise must be a valid canonical call whose part or envelope name has surrounding whitespace: %v", err)
			}
			body := decodedBody(t, projectedRequest(t, responses.ProjectInput{Call: call, Model: fixtureModel}))
			requireSequence(t, nameRule, inputSequence(t, body),
				[]string{"function_call:" + tc.callID, "function_call_output:" + tc.callID})
			projected := inputItems(t, body)
			if got := projected[0]["name"]; got != authoredName {
				t.Fatalf("%s\n  projected name: %v\n  authored name: %q", nameRule, got, authoredName)
			}
			if got := projected[1]["call_id"]; got != tc.callID {
				t.Fatalf("%s\n  the result must keep the call id %q, got %v", nameRule, tc.callID, got)
			}
		})
	}
}

// TestProjectedChatCompletionsFallbackIdKeepsAuthoredBytes applies the same
// identity-bytes rule to the Chat Completions id fallback.
//
// A Chat Completions function entry carries its join key in "id" when it has no
// "call_id". The trimmed copy decides presence and the fallback, but the id the
// entry authored is the key its output joins and must reach the wire unchanged
// on both items.
func TestProjectedChatCompletionsFallbackIdKeepsAuthoredBytes(t *testing.T) {
	t.Parallel()
	const (
		joinKey  = " call_3 "
		joinRule = "a Chat Completions function entry's authored id is the fallback join key and must reach the wire unchanged on both items"
	)
	call := aipapi.Call{Messages: []aipapi.Message{
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
			Kind:    aipapi.PartJSON,
			Content: json.RawMessage(`{"type":"function","id":" call_3 ","function":{"name":"widget","arguments":"{}"}}`),
		}}},
		{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
			Kind:       aipapi.PartToolResult,
			ToolCallID: joinKey,
			Content:    json.RawMessage(`"ok"`),
		}}},
	}}
	if err := call.Validate(); err != nil {
		t.Fatalf("the premise must be a valid canonical call whose Chat Completions id has surrounding whitespace: %v", err)
	}
	projected := inputItems(t, decodedBody(t, projectedRequest(t, responses.ProjectInput{Call: call, Model: fixtureModel})))
	if len(projected) != 2 {
		t.Fatalf("%s\n  the input must carry the fallback-id call and its result, got %d items: %v", joinRule, len(projected), projected)
	}
	callID, _ := projected[0]["call_id"].(string)
	resultID, _ := projected[1]["call_id"].(string)
	if callID != resultID {
		t.Fatalf("%s\n  the projected function call carries call_id %q while its function call output carries %q, so the answer no longer joins its model-authored call", joinRule, callID, resultID)
	}
	if callID != joinKey {
		t.Fatalf("%s\n  the projected function call must keep the authored call id %q, got %q", joinRule, joinKey, callID)
	}
}

// TestProjectedFunctionCallCarrierSelection pins which carrier owns the
// function-call identity and which one owns the arguments.
//
// Complete canonical identity on the Part wins before argument data is
// inspected, so an argument member named "type" - or argument data shaped like
// a function-call envelope - is model-authored JSON, not a second identity
// authority. Only a Part with no identity lets the JSON envelope own identity,
// and a Responses item id is never accepted as its call id.
func TestProjectedFunctionCallCarrierSelection(t *testing.T) {
	t.Parallel()
	const carrierRule = "complete canonical tool call identity owns the call id and name, and Part.Content is argument JSON verbatim"

	t.Run("ordinary type parameter is an argument", func(t *testing.T) {
		t.Parallel()
		const arguments = `{"type":"function","description":"function to create"}`
		call := aipapi.Call{Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("create the thing")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
				Kind:       aipapi.PartJSON,
				ToolCallID: "call_1",
				ToolName:   "widget",
				Content:    json.RawMessage(arguments),
			}}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
				Kind:       aipapi.PartToolResult,
				ToolCallID: "call_1",
				Content:    json.RawMessage(`"created"`),
			}}},
		}}
		body := decodedBody(t, projectedRequest(t, responses.ProjectInput{Call: call, Model: fixtureModel}))
		requireSequence(t, carrierRule, inputSequence(t, body),
			[]string{"message:user", "function_call:call_1", "function_call_output:call_1"})
		projected := inputItems(t, body)[1]
		if got := projected["call_id"]; got != "call_1" {
			t.Fatalf("%s\n  projected call id: %v", carrierRule, got)
		}
		if got := projected["name"]; got != "widget" {
			t.Fatalf("%s\n  projected name: %v", carrierRule, got)
		}
		if got := projected["arguments"]; got != arguments {
			t.Fatalf("%s\n  an ordinary argument member named \"type\" must not be mistaken for an envelope\n  projected arguments: %v\n  canonical arguments: %s", carrierRule, got, arguments)
		}
	})

	t.Run("JSON string document is argument text", func(t *testing.T) {
		t.Parallel()
		const arguments = `"hello"`
		call := aipapi.Call{Messages: []aipapi.Message{
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
				Kind:       aipapi.PartJSON,
				ToolCallID: "call_1",
				ToolName:   "widget",
				Content:    json.RawMessage(arguments),
			}}},
		}}
		body := decodedBody(t, projectedRequest(t, responses.ProjectInput{Call: call, Model: fixtureModel}))
		projected := inputItems(t, body)[0]
		if got := projected["arguments"]; got != arguments {
			t.Fatalf("%s\n  a canonical JSON string document is argument text and must not be decoded a second time\n  projected arguments: %v\n  canonical arguments: %s", carrierRule, got, arguments)
		}
	})

	t.Run("envelope-looking argument data is not an identity authority", func(t *testing.T) {
		t.Parallel()
		const arguments = `{"type":"function_call","call_id":"call_1","name":"widget","arguments":{"x":1},"keep":true}`
		call := aipapi.Call{Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("create the thing")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
				Kind:       aipapi.PartJSON,
				ToolCallID: "call_1",
				ToolName:   "widget",
				Content:    json.RawMessage(arguments),
			}}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
				Kind:       aipapi.PartToolResult,
				ToolCallID: "call_1",
				Content:    json.RawMessage(`"created"`),
			}}},
		}}
		body := decodedBody(t, projectedRequest(t, responses.ProjectInput{Call: call, Model: fixtureModel}))
		requireSequence(t, carrierRule+" (argument data must not add, duplicate or replace a call)",
			inputSequence(t, body),
			[]string{"message:user", "function_call:call_1", "function_call_output:call_1"})
		projected := inputItems(t, body)[1]
		if got := projected["arguments"]; got != arguments {
			t.Fatalf("%s\n  envelope-shaped argument data must be preserved whole, not reinterpreted as a duplicate identity or rewritten\n  projected arguments: %v\n  canonical arguments: %s", carrierRule, got, arguments)
		}
	})

	t.Run("a contradictory embedded identity does not replace the canonical one", func(t *testing.T) {
		t.Parallel()
		const arguments = `{"type":"function_call","call_id":"other_call","name":"other_widget","arguments":{"x":1},"keep":true}`
		call := aipapi.Call{Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("create the thing")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
				Kind:       aipapi.PartJSON,
				ToolCallID: "canonical_authority",
				ToolName:   "widget",
				Content:    json.RawMessage(arguments),
			}}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
				Kind:       aipapi.PartToolResult,
				ToolCallID: "canonical_authority",
				Content:    json.RawMessage(`"created"`),
			}}},
		}}
		body := decodedBody(t, projectedRequest(t, responses.ProjectInput{Call: call, Model: fixtureModel}))
		requireSequence(t, carrierRule+" (a contradictory embedded identity must not replace the canonical one)",
			inputSequence(t, body),
			[]string{"message:user", "function_call:canonical_authority", "function_call_output:canonical_authority"})
		projected := inputItems(t, body)[1]
		if got := projected["call_id"]; got != "canonical_authority" {
			t.Fatalf("%s\n  the embedded argument call_id must not replace the canonical call id\n  projected call id: %v", carrierRule, got)
		}
		if got := projected["name"]; got != "widget" {
			t.Fatalf("%s\n  the embedded argument name must not replace the canonical name\n  projected name: %v", carrierRule, got)
		}
		if got := projected["arguments"]; got != arguments {
			t.Fatalf("%s\n  the whole argument object, including its outer members, must survive under the canonical identity\n  projected arguments: %v\n  canonical arguments: %s", carrierRule, got, arguments)
		}
	})

	t.Run("responses item id is not a call id", func(t *testing.T) {
		t.Parallel()
		call := aipapi.Call{Messages: []aipapi.Message{
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
				Kind:    aipapi.PartJSON,
				Content: json.RawMessage(`{"type":"function_call","id":"fc_item","name":"widget","arguments":"{}"}`),
			}}},
		}}
		_, err := responses.ProjectCall(responses.ProjectInput{Call: call, Model: fixtureModel})
		if err == nil {
			t.Fatalf("a Responses function_call item without call_id must be refused: its item id is a different identity and is not the join key")
		}
		if !errors.Is(err, responses.ErrCallNotRepresentable) {
			t.Fatalf("the refusal must classify as ErrCallNotRepresentable, got %v", err)
		}
		var refusal *responses.ProjectionError
		if !errors.As(err, &refusal) {
			t.Fatalf("the refusal must carry a ProjectionError, got %T: %v", err, err)
		}
		if refusal.Reason != responses.ProjectionReasonToolCall || refusal.Field != "Messages[0].Parts[0]" {
			t.Fatalf("the refusal reason must be %q at %q, got %q at %q: %v",
				responses.ProjectionReasonToolCall, "Messages[0].Parts[0]", refusal.Reason, refusal.Field, err)
		}
		if !strings.Contains(refusal.Detail, "call_id") {
			t.Fatalf("the refusal must name the missing call_id so the Responses item id is not mistaken for the join key: %v", err)
		}
	})
}

// TestProjectedRequestCarriesNoCodexIdentityGlue is the absence proof.
//
// The whole projected request is searched - endpoint, raw headers, raw body
// bytes and every decoded body string - for the Codex identity prompt, the
// <user_instructions> wrapper, the Codex CLI originator, the private ChatGPT
// route and the Codex-private header names.
func TestProjectedRequestCarriesNoCodexIdentityGlue(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Instructions: []aipapi.Message{
			{Role: aipapi.RoleDeveloper, Parts: []aipapi.Part{aipapi.TextPart("Be precise.")}},
		},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Say hello.")}},
		},
	}
	req := projectedRequest(t, responses.ProjectInput{Call: call, Model: fixtureModel})
	document := scanStrictWireDocument(req.Body)
	text := strings.ToLower(strictDocumentText(req, document))
	for _, marker := range forbiddenRequestMarkers {
		if strings.Contains(text, marker) {
			t.Fatalf("the projected request must not carry the Codex identity prompt, the <user_instructions> glue, the private route or a Codex-private header, but it carries %q", marker)
		}
	}
	for name := range req.Headers {
		lower := strings.ToLower(name)
		for _, forbidden := range forbiddenHeaderNames {
			if lower == forbidden || strings.HasPrefix(lower, "x-codex") {
				t.Fatalf("the projected request must not carry the Codex-private header %q", name)
			}
		}
	}
}

// TestProjectionRefusesUnrepresentableContent is the refusal proof.
//
// Every arrived item is projected or the input is refused: the projector
// never samples the projectable part and drops the rest. Each case names the
// stable reason and the canonical field it refuses.
func TestProjectionRefusesUnrepresentableContent(t *testing.T) {
	t.Parallel()
	reasoningPart := aipapi.Part{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{
		Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
		Text:    "private thought",
	}}
	cases := []struct {
		name       string
		in         responses.ProjectInput
		wantReason responses.ProjectionReason
		wantField  string
	}{
		{
			name: "model id is required",
			in: responses.ProjectInput{
				Call: aipapi.Call{Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
				}},
			},
			wantReason: responses.ProjectionReasonModelRequired,
			wantField:  "Model",
		},
		{
			name: "typed decision request has no conversation",
			in: responses.ProjectInput{
				Model: fixtureModel,
				Call: aipapi.Call{Decision: &aipapi.DecisionRequest{
					Evidence: json.RawMessage(`"payload"`),
					Questions: []aipapi.DecisionQuestion{{
						ID:           "q1",
						Kind:         aipapi.DecisionKindNoul,
						Instructions: json.RawMessage(`"is it?"`),
					}},
				}},
			},
			wantReason: responses.ProjectionReasonDecision,
			wantField:  "Decision",
		},
		{
			name: "proxy continuation history is not carried by the call",
			in: responses.ProjectInput{
				Model: fixtureModel,
				Call:  aipapi.Call{PreviousResponseID: "resp_1", Items: []aipapi.Item{}},
			},
			wantReason: responses.ProjectionReasonPreviousResponse,
			wantField:  "PreviousResponseID",
		},
		{
			name: "semantic residual has no public carrier",
			in: responses.ProjectInput{
				Model: fixtureModel,
				Call: aipapi.Call{
					Messages: []aipapi.Message{
						{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
					},
					SemanticExtensions: []aipapi.SemanticExtension{{
						Namespace:   "aip",
						Type:        "prompt_cache_key",
						Implementor: "proxy",
						Direction:   aipapi.SemanticExtensionDirectionRequest,
						Presence:    aipapi.SemanticExtensionValue,
						Data:        json.RawMessage(`"cache"`),
					}},
				},
			},
			wantReason: responses.ProjectionReasonSemanticExtension,
			wantField:  "SemanticExtensions",
		},
		{
			name: "allowed-tools subset has no pinned wire shape",
			in: responses.ProjectInput{
				Model: fixtureModel,
				Call: aipapi.Call{
					Tools:      []aipapi.ToolDef{{Name: "lookup"}},
					ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto, AllowedTools: []string{"lookup"}},
					Messages: []aipapi.Message{
						{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
					},
				},
			},
			wantReason: responses.ProjectionReasonToolChoiceSubset,
			wantField:  "ToolChoice.AllowedTools",
		},
		{
			name: "instruction image has no instructions-string form",
			in: responses.ProjectInput{
				Model: fixtureModel,
				Call: aipapi.Call{
					Instructions: []aipapi.Message{
						{Role: aipapi.RoleDeveloper, Parts: []aipapi.Part{
							aipapi.TextPart("look"),
							{Kind: aipapi.PartImageRef, ImageRef: "data:image/png;base64,iVBORw0KGgo="},
						}},
					},
					Messages: []aipapi.Message{
						{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
					},
				},
			},
			wantReason: responses.ProjectionReasonInstructions,
			wantField:  "Instructions[0].Parts[1]",
		},
		{
			name: "system message image cannot be folded",
			in: responses.ProjectInput{
				Model: fixtureModel,
				Call: aipapi.Call{Messages: []aipapi.Message{
					{Role: aipapi.RoleSystem, Parts: []aipapi.Part{{Kind: aipapi.PartImageRef, ImageRef: "data:image/png;base64,iVBORw0KGgo="}}},
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
				}},
			},
			wantReason: responses.ProjectionReasonSystemContent,
			wantField:  "Messages[0].Parts[0]",
		},
		{
			name: "tool-role message may carry only tool results",
			in: responses.ProjectInput{
				Model: fixtureModel,
				Call: aipapi.Call{Messages: []aipapi.Message{
					{Role: aipapi.RoleTool, Parts: []aipapi.Part{aipapi.TextPart("plain text in a tool role")}},
				}},
			},
			wantReason: responses.ProjectionReasonPart,
			wantField:  "Messages[0].Parts[0]",
		},
		{
			name: "user-role function call is not a client turn",
			in: responses.ProjectInput{
				Model: fixtureModel,
				Call: aipapi.Call{Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"type":"function_call","call_id":"call_1","name":"ping","arguments":"{}"}`)}}},
				}},
			},
			wantReason: responses.ProjectionReasonPart,
			wantField:  "Messages[0].Parts[0]",
		},
		{
			name: "function call without a call id has no join key",
			in: responses.ProjectInput{
				Model: fixtureModel,
				Call: aipapi.Call{Messages: []aipapi.Message{
					{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"type":"function_call","name":"ping","arguments":"{}"}`)}}},
				}},
			},
			wantReason: responses.ProjectionReasonToolCall,
			wantField:  "Messages[0].Parts[0]",
		},
		{
			name: "canonical function call identity is incomplete",
			in: responses.ProjectInput{
				Model: fixtureModel,
				Call: aipapi.Call{Messages: []aipapi.Message{
					{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
						Kind:       aipapi.PartJSON,
						ToolCallID: "call_1",
						Content:    json.RawMessage(`{"x":1}`),
					}}},
				}},
			},
			wantReason: responses.ProjectionReasonToolCall,
			wantField:  "Messages[0].Parts[0]",
		},
		{
			name: "literal tool result text and its JSON copy disagree",
			in: responses.ProjectInput{
				Model: fixtureModel,
				Call: aipapi.Call{Messages: []aipapi.Message{
					{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
						Kind:       aipapi.PartToolResult,
						ToolCallID: "call_1",
						Text:       "sunny",
						Content:    json.RawMessage(`"rainy"`),
					}}},
				}},
			},
			wantReason: responses.ProjectionReasonToolResult,
			wantField:  "Messages[0].Parts[0]",
		},
		{
			name: "assistant reasoning part is not replayed",
			in: responses.ProjectInput{
				Model: fixtureModel,
				Call: aipapi.Call{Messages: []aipapi.Message{
					{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{reasoningPart}},
				}},
			},
			wantReason: responses.ProjectionReasonReasoningReplay,
			wantField:  "Messages[0].Parts[0]",
		},
		{
			name: "file reference without inline bytes has no file_data",
			in: responses.ProjectInput{
				Model: fixtureModel,
				Call: aipapi.Call{Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{
						aipapi.FilePart("https://example.test/report.pdf", "application/pdf", "report.pdf"),
					}},
				}},
			},
			wantReason: responses.ProjectionReasonFileReference,
			wantField:  "Messages[0].Parts[0]",
		},
		{
			name: "inline file bytes and their data-URL copy disagree",
			in: responses.ProjectInput{
				Model: fixtureModel,
				Call: aipapi.Call{Items: []aipapi.Item{
					{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{
						Kind:     aipapi.ContentPartFileRef,
						FileRef:  "data:application/pdf;base64,JVBERi0xLjQK",
						FileData: "JVBERi0xLjUK",
						FileName: "report.pdf",
					}}},
				}},
			},
			wantReason: responses.ProjectionReasonFileReference,
			wantField:  "Items[0].Content[0]",
		},
		{
			name: "assistant media has no documented input form",
			in: responses.ProjectInput{
				Model: fixtureModel,
				Call: aipapi.Call{Messages: []aipapi.Message{
					{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartImageRef, ImageRef: "data:image/png;base64,iVBORw0KGgo="}}},
				}},
			},
			wantReason: responses.ProjectionReasonAssistantMedia,
			wantField:  "Messages[0].Parts[0]",
		},
		{
			name: "provider item reference cannot be resolved over HTTP",
			in: responses.ProjectInput{
				Model: fixtureModel,
				Call: aipapi.Call{Items: []aipapi.Item{
					{Kind: aipapi.ItemKindMessage, ID: "msg_1", Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hi"}}},
					{Kind: aipapi.ItemKindItemReference, Reference: &aipapi.ItemReference{ID: "msg_1"}},
				}},
			},
			wantReason: responses.ProjectionReasonItemReference,
			wantField:  "Items[1]",
		},
		{
			name: "reasoning item belongs to optional replay",
			in: responses.ProjectInput{
				Model: fixtureModel,
				Call: aipapi.Call{Items: []aipapi.Item{
					{Kind: aipapi.ItemKindReasoning, Reasoning: &aipapi.ReasoningItem{Reasoning: &aipapi.ReasoningPart{
						Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
						Text:    "private thought",
					}}},
				}},
			},
			wantReason: responses.ProjectionReasonReasoningReplay,
			wantField:  "Items[0]",
		},
		{
			name: "compaction item has no public carrier",
			in: responses.ProjectInput{
				Model: fixtureModel,
				Call: aipapi.Call{Items: []aipapi.Item{
					{Kind: aipapi.ItemKindCompaction, Compaction: &aipapi.CompactionItem{EncapsulatedID: "cmp_1"}},
				}},
			},
			wantReason: responses.ProjectionReasonItemKind,
			wantField:  "Items[0]",
		},
		{
			name: "opaque extension item has no public carrier",
			in: responses.ProjectInput{
				Model: fixtureModel,
				Call: aipapi.Call{Items: []aipapi.Item{
					{Kind: aipapi.ItemKindExtension, Extension: &aipapi.OpaqueExtension{
						Namespace: "acme",
						Type:      "opaque",
						Data:      json.RawMessage(`{"type":"opaque"}`),
					}},
				}},
			},
			wantReason: responses.ProjectionReasonItemKind,
			wantField:  "Items[0]",
		},
		{
			name: "tool-role message item has no call id",
			in: responses.ProjectInput{
				Model: fixtureModel,
				Call: aipapi.Call{Items: []aipapi.Item{
					{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleTool, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartToolResult, Text: "ok"}}},
				}},
			},
			wantReason: responses.ProjectionReasonRole,
			wantField:  "Items[0].Role",
		},
		{
			name: "video input is not supported by this route",
			in: responses.ProjectInput{
				Model: fixtureModel,
				Call: aipapi.Call{Items: []aipapi.Item{
					{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartVideoRef, VideoRef: "data:video/mp4;base64,AAAA"}}},
				}},
			},
			wantReason: responses.ProjectionReasonPart,
			wantField:  "Items[0].Content[0]",
		},
		{
			name: "assistant refusal part has no input form",
			in: responses.ProjectInput{
				Model: fixtureModel,
				Call: aipapi.Call{Items: []aipapi.Item{
					{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleAssistant, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartRefusal, Refusal: "no"}}},
				}},
			},
			wantReason: responses.ProjectionReasonPart,
			wantField:  "Items[0].Content[0]",
		},
		{
			name: "assistant image item has no documented input form",
			in: responses.ProjectInput{
				Model: fixtureModel,
				Call: aipapi.Call{Items: []aipapi.Item{
					{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleAssistant, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartImageRef, ImageRef: "data:image/png;base64,iVBORw0KGgo="}}},
				}},
			},
			wantReason: responses.ProjectionReasonAssistantMedia,
			wantField:  "Items[0].Content[0]",
		},
		{
			name: "structured tool result has no pinned wire shape",
			in: responses.ProjectInput{
				Model: fixtureModel,
				Call: aipapi.Call{Items: []aipapi.Item{
					{Kind: aipapi.ItemKindToolCall, ToolCall: &aipapi.ToolCallItem{CallID: "call_1", Name: "ping"}},
					{Kind: aipapi.ItemKindToolResult, ToolResult: &aipapi.ToolResultItem{
						CallID: "call_1",
						Parts:  []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "ok"}},
					}},
				}},
			},
			wantReason: responses.ProjectionReasonToolResult,
			wantField:  "Items[1].ToolResult",
		},
		{
			name: "assistant phase has no pinned wire shape",
			in: responses.ProjectInput{
				Model: fixtureModel,
				Call: aipapi.Call{Items: []aipapi.Item{
					{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleAssistant, Phase: aipapi.AssistantPhaseCommentary, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "thinking"}}},
				}},
			},
			wantReason: responses.ProjectionReasonAssistantPhase,
			wantField:  "Items[0].Phase",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.in.Call.Validate(); err != nil {
				t.Fatalf("the refusal fixture must be a valid canonical call, otherwise it can fail for the wrong reason: %v", err)
			}
			_, err := responses.ProjectCall(tc.in)
			if err == nil {
				t.Fatalf("the canonical call must be refused with reason %q at %q, but the projection succeeded", tc.wantReason, tc.wantField)
			}
			if !errors.Is(err, responses.ErrCallNotRepresentable) {
				t.Fatalf("the refusal must classify as ErrCallNotRepresentable, got %v", err)
			}
			var refusal *responses.ProjectionError
			if !errors.As(err, &refusal) {
				t.Fatalf("the refusal must carry a ProjectionError, got %T: %v", err, err)
			}
			if refusal.Reason != tc.wantReason || refusal.Field != tc.wantField {
				t.Fatalf("the refusal reason must be %q at %q, got %q at %q: %v", tc.wantReason, tc.wantField, refusal.Reason, refusal.Field, err)
			}
			if strings.Contains(refusal.Detail, "private thought") {
				t.Fatalf("the refusal diagnostic must not carry conversation content: %v", err)
			}
		})
	}
}
