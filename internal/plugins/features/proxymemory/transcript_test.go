package proxymemory

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Fragment fixtures with mixed ASCII case and surrounding whitespace, matching
// the design §5 fragment filter.
const (
	transcriptAgentsFragment = "# AgEnTs.Md InStRuCtIoNs\nRead AGENTS.md before working.\n</InStRuCtIoNs>"
	transcriptSkillFragment  = "  <SKILL>\nbody of a skill\n</SKILL>  "
)

// transcriptJSONPart builds an assistant/JSON tool-call part.
func transcriptJSONPart(callID, name, arguments string) aipapi.Part {
	return aipapi.Part{
		Kind:       aipapi.PartJSON,
		ToolCallID: callID,
		ToolName:   name,
		Content:    json.RawMessage(arguments),
	}
}

// transcriptToolResultPart builds a canonical tool-result part.
func transcriptToolResultPart(callID, name, content string) aipapi.Part {
	return aipapi.Part{
		Kind:       aipapi.PartToolResult,
		ToolCallID: callID,
		ToolName:   name,
		Content:    json.RawMessage(content),
	}
}

// assertEntries compares rendered entries exactly.
func assertEntries(t *testing.T, got, want []Entry) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("entries mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestTranscriptRenderHistoryMessagesAuthority(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		call *aipapi.Call
		want []Entry
	}{
		{
			name: "instructions and system and developer content is absent",
			call: &aipapi.Call{
				Instructions: []aipapi.Message{
					textMessage(aipapi.RoleUser, "instruction text"),
					textMessage(aipapi.RoleSystem, "Working directory: /w/app"),
				},
				Messages: []aipapi.Message{
					textMessage(aipapi.RoleSystem, "system text"),
					textMessage(aipapi.RoleDeveloper, "developer text"),
					textMessage(aipapi.RoleUser, "hello"),
				},
			},
			want: []Entry{{Type: "message", Role: "user", Text: "hello"}},
		},
		{
			name: "text parts are joined with a newline",
			call: &aipapi.Call{Messages: []aipapi.Message{
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{
					{Kind: aipapi.PartText, Text: "first"},
					{Kind: aipapi.PartText, Text: "second"},
				}},
			}},
			want: []Entry{{Type: "message", Role: "user", Text: "first\nsecond"}},
		},
		{
			name: "an assistant text and a JSON tool call become a message then a function_call",
			call: &aipapi.Call{Messages: []aipapi.Message{
				{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
					{Kind: aipapi.PartText, Text: "looking"},
					transcriptJSONPart("call_1", "shell", `{"command":"ls -la"}`),
					{Kind: aipapi.PartText, Text: "now"},
				}},
			}},
			want: []Entry{
				{Type: "message", Role: "assistant", Text: "looking\nnow"},
				{Type: "function_call", Name: "shell", CallID: "call_1", Arguments: `{"command":"ls -la"}`},
			},
		},
		{
			name: "a JSON part without a call id is not rendered",
			call: &aipapi.Call{Messages: []aipapi.Message{
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{
					{Kind: aipapi.PartText, Text: "hello"},
					{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"note":"x"}`)},
				}},
			}},
			want: []Entry{{Type: "message", Role: "user", Text: "hello"}},
		},
		{
			name: "a tool message with JSON string content becomes an unquoted function_call_output",
			call: &aipapi.Call{Messages: []aipapi.Message{
				{Role: aipapi.RoleTool, Parts: []aipapi.Part{
					transcriptToolResultPart("call_2", "shell", `"ok\n"`),
				}},
			}},
			want: []Entry{{Type: "function_call_output", Name: "shell", CallID: "call_2", Output: "ok\n"}},
		},
		{
			name: "a tool message with structured content keeps the raw JSON text",
			call: &aipapi.Call{Messages: []aipapi.Message{
				{Role: aipapi.RoleTool, Parts: []aipapi.Part{
					transcriptToolResultPart("call_3", "shell", `{"stdout":"done"}`),
				}},
			}},
			want: []Entry{{Type: "function_call_output", Name: "shell", CallID: "call_3", Output: `{"stdout":"done"}`}},
		},
		{
			name: "a tool message whose output is carried only in text keeps that text",
			call: &aipapi.Call{Messages: []aipapi.Message{
				{Role: aipapi.RoleTool, Parts: []aipapi.Part{
					{Kind: aipapi.PartToolResult, ToolCallID: "call_10", ToolName: "shell", Text: "plain output"},
				}},
			}},
			want: []Entry{{Type: "function_call_output", Name: "shell", CallID: "call_10", Output: "plain output"}},
		},
		{
			name: "a tool message with both content and text prefers the content",
			call: &aipapi.Call{Messages: []aipapi.Message{
				{Role: aipapi.RoleTool, Parts: []aipapi.Part{
					{Kind: aipapi.PartToolResult, ToolCallID: "call_11", ToolName: "shell", Text: "plain output", Content: json.RawMessage(`"from content"`)},
				}},
			}},
			want: []Entry{{Type: "function_call_output", Name: "shell", CallID: "call_11", Output: "from content"}},
		},
		{
			name: "an explicitly empty JSON string content is respected",
			call: &aipapi.Call{Messages: []aipapi.Message{
				{Role: aipapi.RoleTool, Parts: []aipapi.Part{
					{Kind: aipapi.PartToolResult, ToolCallID: "call_12", ToolName: "shell", Text: "plain output", Content: json.RawMessage(`""`)},
				}},
			}},
			want: []Entry{{Type: "function_call_output", Name: "shell", CallID: "call_12", Output: ""}},
		},
		{
			name: "a whitespace-padded JSON string content is unquoted and unescaped",
			call: &aipapi.Call{Messages: []aipapi.Message{
				{Role: aipapi.RoleTool, Parts: []aipapi.Part{
					transcriptToolResultPart("call_13", "shell", " \n\t\"hello\\n\\t\\\"quoted\\\"\" \n"),
				}},
			}},
			want: []Entry{{Type: "function_call_output", Name: "shell", CallID: "call_13", Output: "hello\n\t\"quoted\""}},
		},
		{
			name: "a whitespace-padded non-string JSON content keeps the raw JSON text",
			call: &aipapi.Call{Messages: []aipapi.Message{
				{Role: aipapi.RoleTool, Parts: []aipapi.Part{
					transcriptToolResultPart("call_14", "shell", "  {\"stdout\":\"done\"}  "),
				}},
			}},
			want: []Entry{{Type: "function_call_output", Name: "shell", CallID: "call_14", Output: "  {\"stdout\":\"done\"}  "}},
		},
		{
			name: "a whitespace-padded empty JSON string stays authoritative over text",
			call: &aipapi.Call{Messages: []aipapi.Message{
				{Role: aipapi.RoleTool, Parts: []aipapi.Part{
					{Kind: aipapi.PartToolResult, ToolCallID: "call_15", ToolName: "shell", Text: "plain output", Content: json.RawMessage("  \"\"  ")},
				}},
			}},
			want: []Entry{{Type: "function_call_output", Name: "shell", CallID: "call_15", Output: ""}},
		},
		{
			name: "reasoning parts are absent and a reasoning-only message is dropped",
			call: &aipapi.Call{Messages: []aipapi.Message{
				{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
					{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIChatTextV1, Text: "hidden"}},
					{Kind: aipapi.PartText, Text: "answer"},
				}},
				{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
					{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIChatTextV1, Text: "only thinking"}},
				}},
			}},
			want: []Entry{{Type: "message", Role: "assistant", Text: "answer"}},
		},
		{
			name: "image and file parts become omission placeholders",
			call: &aipapi.Call{Messages: []aipapi.Message{
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{
					{Kind: aipapi.PartText, Text: "look"},
					{Kind: aipapi.PartImageRef, ImageRef: "img://one"},
					{Kind: aipapi.PartFileRef, FileRef: "file://two"},
					{Kind: aipapi.PartText, Text: "please"},
				}},
			}},
			want: []Entry{{Type: "message", Role: "user", Text: "look\n[image omitted]\n[file omitted]\nplease"}},
		},
		{
			name: "user instruction and skill fragments are dropped",
			call: &aipapi.Call{Messages: []aipapi.Message{
				textMessage(aipapi.RoleUser, transcriptAgentsFragment),
				textMessage(aipapi.RoleUser, transcriptSkillFragment),
				textMessage(aipapi.RoleUser, "keep me"),
			}},
			want: []Entry{{Type: "message", Role: "user", Text: "keep me"}},
		},
		{
			name: "a user message left with only fragments is dropped",
			call: &aipapi.Call{Messages: []aipapi.Message{
				textMessage(aipapi.RoleUser, transcriptAgentsFragment),
				textMessage(aipapi.RoleUser, transcriptSkillFragment),
			}},
			want: nil,
		},
		{
			name: "the fragment filter is user-role only",
			call: &aipapi.Call{Messages: []aipapi.Message{
				textMessage(aipapi.RoleAssistant, transcriptAgentsFragment),
			}},
			want: []Entry{{Type: "message", Role: "assistant", Text: transcriptAgentsFragment}},
		},
		{
			name: "user, assistant, tool and assistant messages keep their order",
			call: &aipapi.Call{Messages: []aipapi.Message{
				textMessage(aipapi.RoleUser, "first question"),
				{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
					{Kind: aipapi.PartText, Text: "first answer"},
					transcriptJSONPart("call_1", "shell", `{"command":"ls"}`),
				}},
				{Role: aipapi.RoleTool, Parts: []aipapi.Part{
					transcriptToolResultPart("call_1", "shell", `"ok"`),
				}},
				textMessage(aipapi.RoleUser, "second question"),
				{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
					{Kind: aipapi.PartText, Text: "second answer"},
					transcriptJSONPart("call_2", "read", `{"path":"a"}`),
				}},
			}},
			want: []Entry{
				{Type: "message", Role: "user", Text: "first question"},
				{Type: "message", Role: "assistant", Text: "first answer"},
				{Type: "function_call", Name: "shell", CallID: "call_1", Arguments: `{"command":"ls"}`},
				{Type: "function_call_output", Name: "shell", CallID: "call_1", Output: "ok"},
				{Type: "message", Role: "user", Text: "second question"},
				{Type: "message", Role: "assistant", Text: "second answer"},
				{Type: "function_call", Name: "read", CallID: "call_2", Arguments: `{"path":"a"}`},
			},
		},
		{
			name: "a nil call renders nothing",
			call: nil,
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertEntries(t, RenderHistory(tc.call), tc.want)
		})
	}
}

// TestTranscriptRenderHistoryResponsesFrontendFunctionCall proves a real
// Responses history decoded by the shipped frontend records the tool call with
// its exact identity and arguments instead of dropping it from the transcript.
func TestTranscriptRenderHistoryResponsesFrontendFunctionCall(t *testing.T) {
	t.Parallel()
	const body = `{
  "model":"gpt-4o-mini",
  "input":[
    {"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
    {"type":"function_call","id":"fc_1","call_id":"call_1","name":"close_agent","arguments":"{\"target\":\"nope\"}"},
    {"type":"function_call_output","call_id":"call_1","output":"err"}
  ]
}`
	d, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{
		RouteSelector: "stub:m",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEntries(t, RenderHistory(d.Call), []Entry{
		{Type: entryTypeMessage, Role: "user", Text: "hi"},
		{Type: entryTypeFunctionCall, Name: "close_agent", CallID: "call_1", Arguments: `{"target":"nope"}`},
		{Type: entryTypeFunctionCallOutput, CallID: "call_1", Output: "err"},
	})
}

// TestTranscriptRenderHistoryResponsesStringDocument proves a JSON string
// argument document is recorded as its own argument text, not as an escaped
// wire carrier.
func TestTranscriptRenderHistoryResponsesStringDocument(t *testing.T) {
	t.Parallel()
	const body = `{"model":"m","input":[{"type":"function_call","call_id":"c1","name":"echo","arguments":"\"hello\""}]}`
	d, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{
		RouteSelector: "stub:m",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEntries(t, RenderHistory(d.Call), []Entry{
		{Type: entryTypeFunctionCall, Name: "echo", CallID: "c1", Arguments: `"hello"`},
	})
}

func TestTranscriptRenderHistoryItemsAuthority(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		call *aipapi.Call
		want []Entry
	}{
		{
			name: "tool_call and tool_result items become function_call and function_call_output",
			call: &aipapi.Call{Items: []aipapi.Item{
				{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartText, Text: "run it"},
				}},
				{Kind: aipapi.ItemKindToolCall, ToolCall: &aipapi.ToolCallItem{
					CallID:    "call_9",
					Name:      "shell",
					Arguments: json.RawMessage(`{"command":"ls"}`),
				}},
				{Kind: aipapi.ItemKindToolResult, ToolResult: &aipapi.ToolResultItem{
					CallID: "call_9",
					Name:   "shell",
					Output: "done",
				}},
				{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleAssistant, Content: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartText, Text: "all good"},
				}},
			}},
			want: []Entry{
				{Type: "message", Role: "user", Text: "run it"},
				{Type: "function_call", Name: "shell", CallID: "call_9", Arguments: `{"command":"ls"}`},
				{Type: "function_call_output", Name: "shell", CallID: "call_9", Output: "done"},
				{Type: "message", Role: "assistant", Text: "all good"},
			},
		},
		{
			name: "tool_result parts provide the output text when Output is empty",
			call: &aipapi.Call{Items: []aipapi.Item{
				{Kind: aipapi.ItemKindToolCall, ToolCall: &aipapi.ToolCallItem{
					CallID:    "call_4",
					Name:      "shell",
					Arguments: json.RawMessage(`{}`),
				}},
				{Kind: aipapi.ItemKindToolResult, ToolResult: &aipapi.ToolResultItem{
					CallID: "call_4",
					Name:   "shell",
					Parts: []aipapi.ContentPart{
						{Kind: aipapi.ContentPartText, Text: "line one"},
						{Kind: aipapi.ContentPartText, Text: "line two"},
					},
				}},
			}},
			want: []Entry{
				{Type: "function_call", Name: "shell", CallID: "call_4", Arguments: `{}`},
				{Type: "function_call_output", Name: "shell", CallID: "call_4", Output: "line one\nline two"},
			},
		},
		{
			name: "reasoning, compaction, extension and item-reference items are absent",
			call: &aipapi.Call{Items: []aipapi.Item{
				{Kind: aipapi.ItemKindReasoning, Reasoning: &aipapi.ReasoningItem{
					Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIChatTextV1, Text: "hidden"},
				}},
				{Kind: aipapi.ItemKindCompaction, Compaction: &aipapi.CompactionItem{EncapsulatedID: "cmp_1"}},
				{Kind: aipapi.ItemKindExtension, Extension: &aipapi.OpaqueExtension{
					Namespace: "acme", Type: "custom", Data: json.RawMessage(`{"x":1}`),
				}},
				{Kind: aipapi.ItemKindItemReference, Reference: &aipapi.ItemReference{ID: "msg-0"}},
				{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartText, Text: "keep"},
				}},
			}},
			want: []Entry{{Type: "message", Role: "user", Text: "keep"}},
		},
		{
			name: "system and developer message items are absent",
			call: &aipapi.Call{Items: []aipapi.Item{
				textItem(aipapi.RoleSystem, "system text"),
				textItem(aipapi.RoleDeveloper, "developer text"),
				textItem(aipapi.RoleUser, "hello"),
			}},
			want: []Entry{{Type: "message", Role: "user", Text: "hello"}},
		},
		{
			name: "image, file and video content becomes placeholders",
			call: &aipapi.Call{Items: []aipapi.Item{
				{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartText, Text: "look"},
					{Kind: aipapi.ContentPartImageRef, ImageRef: "img://one"},
					{Kind: aipapi.ContentPartFileRef, FileRef: "file://two"},
					{Kind: aipapi.ContentPartVideoRef, VideoRef: "video://three"},
					{Kind: aipapi.ContentPartText, Text: "please"},
				}},
			}},
			want: []Entry{{Type: "message", Role: "user", Text: "look\n[image omitted]\n[file omitted]\n[video omitted]\nplease"}},
		},
		{
			name: "user message items drop instruction and skill fragments",
			call: &aipapi.Call{Items: []aipapi.Item{
				textItem(aipapi.RoleUser, transcriptAgentsFragment),
				textItem(aipapi.RoleUser, transcriptSkillFragment),
				textItem(aipapi.RoleUser, "keep me"),
			}},
			want: []Entry{{Type: "message", Role: "user", Text: "keep me"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertEntries(t, RenderHistory(tc.call), tc.want)
		})
	}
}

func TestTranscriptRenderHistoryDoesNotMutateItsInput(t *testing.T) {
	t.Parallel()

	messagesCall := &aipapi.Call{
		Instructions: []aipapi.Message{textMessage(aipapi.RoleSystem, "Working directory: /w/app")},
		Messages: []aipapi.Message{
			textMessage(aipapi.RoleUser, "question"),
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
				{Kind: aipapi.PartText, Text: "answer"},
				transcriptJSONPart("call_1", "shell", `{"command":"ls"}`),
				{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIChatTextV1, Text: "hidden"}},
			}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{
				transcriptToolResultPart("call_1", "shell", `"ok"`),
			}},
		},
	}
	itemsCall := &aipapi.Call{Items: []aipapi.Item{
		{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{
			{Kind: aipapi.ContentPartText, Text: "item text"},
			{Kind: aipapi.ContentPartImageRef, ImageRef: "img://one"},
		}},
		{Kind: aipapi.ItemKindToolCall, ToolCall: &aipapi.ToolCallItem{
			CallID: "call_9", Name: "read", Arguments: json.RawMessage(`{"path":"a"}`),
		}},
		{Kind: aipapi.ItemKindToolResult, ToolResult: &aipapi.ToolResultItem{
			CallID: "call_9", Name: "read", Output: "done",
		}},
	}}

	cases := []struct {
		name string
		call *aipapi.Call
	}{
		{name: "messages authority", call: messagesCall},
		{name: "items authority", call: itemsCall},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			before := aipapi.CloneCall(*tc.call)
			if got := RenderHistory(tc.call); len(got) == 0 {
				t.Fatalf("RenderHistory returned no entries for %s", tc.name)
			}
			if !reflect.DeepEqual(*tc.call, before) {
				t.Errorf("RenderHistory mutated its input\n got: %#v\nwant: %#v", *tc.call, before)
			}
		})
	}
}

func TestTranscriptAssistantAccumulator(t *testing.T) {
	t.Parallel()

	var acc AssistantAccumulator
	acc.Observe(aipapi.Event{Kind: aipapi.EventResponseStarted})
	acc.Observe(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "Hel"})
	acc.Observe(aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "hidden"})
	acc.Observe(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "lo"})
	acc.Observe(aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_7", ToolName: "shell"})
	acc.Observe(aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_7", Delta: `{"command":`})
	acc.Observe(aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_7", Delta: `"ls"}`})
	acc.Observe(aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "still hidden"})
	acc.Observe(aipapi.Event{Kind: aipapi.EventItem, Item: &aipapi.Item{
		Kind:     aipapi.ItemKindToolCall,
		ToolCall: &aipapi.ToolCallItem{CallID: "call_8", Name: "read_file", Arguments: json.RawMessage(`{"path":"a.txt"}`)},
	}})
	acc.Observe(aipapi.Event{Kind: aipapi.EventItem, Item: &aipapi.Item{
		Kind:      aipapi.ItemKindReasoning,
		Reasoning: &aipapi.ReasoningItem{Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIChatTextV1, Text: "hidden"}},
	}})

	want := []Entry{
		{Type: "message", Role: "assistant", Text: "Hello"},
		{Type: "function_call", Name: "shell", CallID: "call_7", Arguments: `{"command":"ls"}`},
		{Type: "function_call", Name: "read_file", CallID: "call_8", Arguments: `{"path":"a.txt"}`},
	}
	assertEntries(t, acc.Entries(), want)
	// Entries is repeatable and does not consume the accumulator.
	assertEntries(t, acc.Entries(), want)

	t.Run("no text means no message entry", func(t *testing.T) {
		t.Parallel()
		var calls AssistantAccumulator
		calls.Observe(aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_6", ToolName: "shell"})
		calls.Observe(aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_6", Delta: `{}`})
		assertEntries(t, calls.Entries(), []Entry{
			{Type: "function_call", Name: "shell", CallID: "call_6", Arguments: `{}`},
		})
	})

	t.Run("an item completes an already-opened call in place", func(t *testing.T) {
		t.Parallel()
		var calls AssistantAccumulator
		calls.Observe(aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_5", ToolName: "shell"})
		calls.Observe(aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_5", Delta: `{"a":`})
		calls.Observe(aipapi.Event{Kind: aipapi.EventItem, Item: &aipapi.Item{
			Kind:     aipapi.ItemKindToolCall,
			ToolCall: &aipapi.ToolCallItem{CallID: "call_5", Name: "shell", Arguments: json.RawMessage(`{"a":1}`)},
		}})
		assertEntries(t, calls.Entries(), []Entry{
			{Type: "function_call", Name: "shell", CallID: "call_5", Arguments: `{"a":1}`},
		})
	})

	t.Run("a late started event names an already-open call", func(t *testing.T) {
		t.Parallel()
		var calls AssistantAccumulator
		calls.Observe(aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_b", Delta: "1"})
		calls.Observe(aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_b", ToolName: "late"})
		calls.Observe(aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_b", Delta: "2"})
		// The canonical collector pins the same expectation for this ordering:
		// pkg/aipapi/collect_tool_test.go TestCollect_argsDeltaBeforeStartedStillOrders.
		assertEntries(t, calls.Entries(), []Entry{
			{Type: "function_call", Name: "late", CallID: "call_b", Arguments: "12"},
		})
	})

	t.Run("a late started event keeps first-seen order and a blank name does not blank the call", func(t *testing.T) {
		t.Parallel()
		var calls AssistantAccumulator
		calls.Observe(aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_a", Delta: "1"})
		calls.Observe(aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_a", ToolName: "alpha"})
		calls.Observe(aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_b", ToolName: "beta"})
		calls.Observe(aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_a", ToolName: "  "})
		calls.Observe(aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_a", Delta: "2"})
		assertEntries(t, calls.Entries(), []Entry{
			{Type: "function_call", Name: "alpha", CallID: "call_a", Arguments: "12"},
			{Type: "function_call", Name: "beta", CallID: "call_b"},
		})
	})
}

func TestTranscriptEncodeTranscript(t *testing.T) {
	t.Parallel()

	t.Run("no entries is an empty array", func(t *testing.T) {
		t.Parallel()
		got, truncated, err := EncodeTranscript(nil, 4096)
		if err != nil {
			t.Fatalf("EncodeTranscript(nil) error = %v", err)
		}
		if got != "[]" || truncated {
			t.Errorf("EncodeTranscript(nil, 4096) = %q, %v; want \"[]\", false", got, truncated)
		}
	})

	t.Run("under the limit is compact json and not truncated", func(t *testing.T) {
		t.Parallel()
		entries := []Entry{
			{Type: "message", Role: "user", Text: "hello"},
			{Type: "message", Role: "assistant", Text: "world"},
		}
		got, truncated, err := EncodeTranscript(entries, 4096)
		if err != nil {
			t.Fatalf("EncodeTranscript error = %v", err)
		}
		if truncated {
			t.Errorf("EncodeTranscript truncated = true; want false")
		}
		want := `[{"type":"message","role":"user","text":"hello"},{"type":"message","role":"assistant","text":"world"}]`
		if got != want {
			t.Errorf("EncodeTranscript = %q; want %q", got, want)
		}
	})

	t.Run("secrets are redacted and NUL bytes are removed", func(t *testing.T) {
		t.Parallel()
		secret := "sk-" + strings.Repeat("a", 24)
		entries := []Entry{{
			Type: "message",
			Role: "user",
			Text: secret + "\x00hunter2 and password: hunter2xyz",
		}}
		got, truncated, err := EncodeTranscript(entries, 4096)
		if err != nil {
			t.Fatalf("EncodeTranscript error = %v", err)
		}
		if truncated {
			t.Errorf("EncodeTranscript truncated = true; want false")
		}
		if !json.Valid([]byte(got)) {
			t.Fatalf("EncodeTranscript output is not valid JSON: %q", got)
		}
		for _, unwanted := range []string{secret, "hunter2xyz", "\x00", `\u0000`} {
			if strings.Contains(got, unwanted) {
				t.Errorf("EncodeTranscript output %q still contains %q", got, unwanted)
			}
		}
		var back []Entry
		if err := json.Unmarshal([]byte(got), &back); err != nil {
			t.Fatalf("unmarshal output: %v", err)
		}
		if len(back) != 1 {
			t.Fatalf("unmarshal output: got %d entries, want 1", len(back))
		}
		// NUL removal now runs before Redact, so the key pattern sees the
		// reassembled run and consumes the directly adjacent "hunter2" as part
		// of the match. The previous expectation preserved it only because the
		// NUL interrupted the alphanumeric run first.
		wantText := "[REDACTED_SECRET] and password: [REDACTED_SECRET]"
		if back[0].Text != wantText {
			t.Errorf("redacted text = %q; want %q", back[0].Text, wantText)
		}
	})

	t.Run("a NUL-split secret cannot be reassembled by NUL removal", func(t *testing.T) {
		t.Parallel()
		// Regression for the judge round-2 probe
		// TestJudgeR2NULRemovalDoesNotExposeSecret: a NUL inside the key keeps
		// both halves below the OpenAI key length, and removing the NUL would
		// splice the stored value back into the secret shape — so NUL removal
		// has to happen before Redact sees the text. The call goes through
		// Validate(), so this is a canonical request, not a hand-built entry.
		secret := "sk-" + strings.Repeat("a", 24)
		split := "sk-" + strings.Repeat("a", 12) + "\x00" + strings.Repeat("a", 12)
		if Redact(split) != split || Redact(secret) == secret {
			t.Fatalf("invalid redaction controls: split = %q, reassembled = %q", Redact(split), Redact(secret))
		}
		call := &aipapi.Call{Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: split}}},
		}}
		if err := call.Validate(); err != nil {
			t.Fatalf("call.Validate() error = %v", err)
		}
		got, truncated, err := EncodeTranscript(RenderHistory(call), 4096)
		if err != nil {
			t.Fatalf("EncodeTranscript error = %v", err)
		}
		if truncated {
			t.Errorf("EncodeTranscript truncated = true; want false")
		}
		if strings.Contains(got, secret) {
			t.Fatalf("NUL removal synthesized an unredacted key-shaped value: %s", got)
		}
		if !strings.Contains(got, redactedSecret) {
			t.Errorf("encoded transcript %q carries no %s marker", got, redactedSecret)
		}
		if !json.Valid([]byte(got)) {
			t.Fatalf("EncodeTranscript output is not valid JSON: %q", got)
		}
	})

	t.Run("the oldest entries are dropped first and the newest are kept", func(t *testing.T) {
		t.Parallel()
		entries := []Entry{
			{Type: "message", Role: "user", Text: strings.Repeat("old ", 64)},
			{Type: "message", Role: "user", Text: "middle"},
			{Type: "message", Role: "assistant", Text: "newest"},
		}
		full, err := json.Marshal(entries)
		if err != nil {
			t.Fatalf("marshal full entries: %v", err)
		}
		wantTail, err := json.Marshal(entries[1:])
		if err != nil {
			t.Fatalf("marshal tail entries: %v", err)
		}

		if got, truncated, err := EncodeTranscript(entries, len(full)); err != nil {
			t.Fatalf("EncodeTranscript at the exact limit error = %v", err)
		} else if truncated || got != string(full) {
			t.Errorf("EncodeTranscript at the exact limit = %q, %v; want all entries untruncated", got, truncated)
		}

		got, truncated, err := EncodeTranscript(entries, len(wantTail))
		if err != nil {
			t.Fatalf("EncodeTranscript error = %v", err)
		}
		if !truncated {
			t.Errorf("EncodeTranscript truncated = false; want true")
		}
		if got != string(wantTail) {
			t.Errorf("EncodeTranscript = %q; want %q (oldest dropped, newest kept)", got, wantTail)
		}
	})

	t.Run("a single oversized entry is truncated and stays valid JSON", func(t *testing.T) {
		t.Parallel()
		entries := []Entry{{Type: "message", Role: "assistant", Text: strings.Repeat("x", 4096)}}
		got, truncated, err := EncodeTranscript(entries, 256)
		if err != nil {
			t.Fatalf("EncodeTranscript error = %v", err)
		}
		if !truncated {
			t.Errorf("EncodeTranscript truncated = false; want true")
		}
		if !json.Valid([]byte(got)) {
			t.Fatalf("EncodeTranscript output is not valid JSON: %q", got)
		}
		var back []Entry
		if err := json.Unmarshal([]byte(got), &back); err != nil {
			t.Fatalf("unmarshal output: %v", err)
		}
		if len(back) != 1 {
			t.Fatalf("unmarshal output: got %d entries, want 1", len(back))
		}
		if back[0].Type != "message" || back[0].Role != "assistant" {
			t.Errorf("truncated entry = %#v; want message/assistant", back[0])
		}
		if len(back[0].Text) >= 4096 {
			t.Errorf("truncated text still has %d bytes; want fewer than 4096", len(back[0].Text))
		}
		if !strings.Contains(back[0].Text, "tokens truncated") {
			t.Errorf("truncated text %q carries no truncation marker", back[0].Text)
		}
	})

	t.Run("a byte limit too small for the array still returns valid JSON", func(t *testing.T) {
		t.Parallel()
		entries := []Entry{{Type: "message", Role: "assistant", Text: strings.Repeat("y", 512)}}
		got, truncated, err := EncodeTranscript(entries, 1)
		if err != nil {
			t.Fatalf("EncodeTranscript error = %v", err)
		}
		if !truncated {
			t.Errorf("EncodeTranscript truncated = false; want true")
		}
		if !json.Valid([]byte(got)) {
			t.Fatalf("EncodeTranscript output is not valid JSON: %q", got)
		}
	})

	t.Run("escaped content never exceeds the configured byte limit", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name     string
			entries  []Entry
			maxBytes int
		}{
			{
				name: "html-escaped characters expand under json encoding",
				entries: []Entry{
					{Type: "message", Role: "user", Text: "superseded"},
					{Type: "message", Role: "assistant", Text: strings.Repeat("<", 8192)},
				},
				maxBytes: 1024,
			},
			{
				name: "control characters expand under json encoding",
				entries: []Entry{
					{Type: "message", Role: "user", Text: "superseded"},
					{Type: "message", Role: "assistant", Text: strings.Repeat("\t", 8192)},
				},
				maxBytes: 1024,
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				got, truncated, err := EncodeTranscript(tc.entries, tc.maxBytes)
				if err != nil {
					t.Fatalf("EncodeTranscript error = %v", err)
				}
				if !truncated {
					t.Errorf("EncodeTranscript truncated = false; want true")
				}
				if len(got) > tc.maxBytes {
					t.Errorf("EncodeTranscript returned %d encoded bytes; want at most %d", len(got), tc.maxBytes)
				}
				if !json.Valid([]byte(got)) {
					t.Fatalf("EncodeTranscript output is not valid JSON: %q", got)
				}
				var back []Entry
				if err := json.Unmarshal([]byte(got), &back); err != nil {
					t.Fatalf("unmarshal output: %v", err)
				}
				if len(back) != 1 || back[0].Type != "message" || back[0].Role != "assistant" {
					t.Errorf("newest entry was not retained: %#v", back)
				}
			})
		}
	})

	t.Run("the newest entry survives verbatim after an escaped older entry", func(t *testing.T) {
		t.Parallel()
		entries := []Entry{
			{Type: "message", Role: "user", Text: strings.Repeat("<", 8192)},
			{Type: "message", Role: "assistant", Text: "newest answer"},
		}
		want, err := json.Marshal(entries[1:])
		if err != nil {
			t.Fatalf("marshal newest entry: %v", err)
		}
		got, truncated, err := EncodeTranscript(entries, 1024)
		if err != nil {
			t.Fatalf("EncodeTranscript error = %v", err)
		}
		if !truncated {
			t.Errorf("EncodeTranscript truncated = false; want true")
		}
		if len(got) > 1024 {
			t.Errorf("EncodeTranscript returned %d encoded bytes; want at most 1024", len(got))
		}
		if got != string(want) {
			t.Errorf("EncodeTranscript = %q; want %q (newest kept verbatim)", got, want)
		}
	})

	t.Run("budgets below the empty array are treated as two bytes", func(t *testing.T) {
		t.Parallel()
		entries := []Entry{{Type: "message", Role: "assistant", Text: "hello"}}
		for _, maxBytes := range []int{0, 1, 8} {
			got, truncated, err := EncodeTranscript(entries, maxBytes)
			if err != nil {
				t.Fatalf("EncodeTranscript(entries, %d) error = %v", maxBytes, err)
			}
			if !truncated {
				t.Errorf("EncodeTranscript(entries, %d) truncated = false; want true", maxBytes)
			}
			// The empty array is the smallest representable transcript.
			limit := max(maxBytes, 2)
			if len(got) > limit {
				t.Errorf("EncodeTranscript(entries, %d) returned %d bytes; want at most %d", maxBytes, len(got), limit)
			}
			if !json.Valid([]byte(got)) {
				t.Fatalf("EncodeTranscript(entries, %d) output is not valid JSON: %q", maxBytes, got)
			}
		}
	})

	t.Run("truncation cannot create a word boundary around a secret-like key", func(t *testing.T) {
		t.Parallel()
		// Regression for the judge round-3 probe
		// TestJudgeR3TruncationBoundaryDoesNotExposeAWSKey: §4 matching is not
		// preserved by byte edits. The §5 step-4 truncation runs after the
		// initial sanitize and keeps a head and a tail around the inserted
		// TruncateTokens marker; here the tail starts exactly at the first byte
		// of a synthetic AWS-shaped key whose only protection was the word
		// character glued to its left. The kept key then starts right after the
		// marker's newline, which supplies the word boundary the pattern needs,
		// so the stored value must be re-sanitized after the truncation and
		// before the marshal that measures and returns it (Requirement 3.3;
		// VISION no-leakage).
		key := "AKIA" + strings.Repeat("A", 16)
		text := strings.Repeat("!", 1000) + "x" + key + " " + strings.Repeat("!", 107)
		if Redact(text) != text || Redact(key) == key {
			t.Fatalf("invalid redaction controls: Redact(text) = %q, Redact(key) = %q", Redact(text), Redact(key))
		}
		// The call goes through Validate(), so this is a canonical request, not
		// a hand-built entry.
		call := &aipapi.Call{Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: text}}},
		}}
		if err := call.Validate(); err != nil {
			t.Fatalf("call.Validate() error = %v", err)
		}
		got, truncated, err := EncodeTranscript(RenderHistory(call), 512)
		if err != nil {
			t.Fatalf("EncodeTranscript error = %v", err)
		}
		if !truncated {
			t.Fatalf("EncodeTranscript truncated = false; want true")
		}
		if len(got) > 512 {
			t.Fatalf("EncodeTranscript returned %d encoded bytes; want at most 512", len(got))
		}
		if !json.Valid([]byte(got)) {
			t.Fatalf("EncodeTranscript output is not valid JSON: %q", got)
		}
		var back []Entry
		if err := json.Unmarshal([]byte(got), &back); err != nil || len(back) != 1 {
			t.Fatalf("decode output: %v (entries %d)", err, len(back))
		}
		if !strings.Contains(back[0].Text, "tokens truncated") {
			t.Fatalf("stored text %q carries no truncation marker", back[0].Text)
		}
		if strings.Contains(got, key) {
			t.Fatalf("truncation kept the synthetic key unredacted: stored text = %q", back[0].Text)
		}
		if awsAccessKeyPattern.MatchString(back[0].Text) {
			t.Fatalf("truncation introduced a §4 word boundary around an unredacted synthetic AWS-shaped key; stored text = %q", back[0].Text)
		}
		if Redact(back[0].Text) != back[0].Text {
			t.Fatalf("stored text is re-redactable: %q -> %q", back[0].Text, Redact(back[0].Text))
		}
		t.Logf("encoded_bytes=%d stored_text=%q", len(got), back[0].Text)
	})

	t.Run("a final-fit reduction cannot create a word boundary around a secret-like key", func(t *testing.T) {
		t.Parallel()
		// The §5 step-4 truncation alone is clean for this shape: the glued word
		// character survives and defeats the AWS word boundary. The escape
		// expansion of the 125 trailing "<" bytes (json.Marshal encodes each as
		// "\u003c") then forces the final fit to keep reducing the single
		// remaining entry, and one of those reductions cuts exactly the glue
		// byte away. Re-sanitizing only once after step 4 would still return the
		// exposed value; the final fit must re-sanitize at the top of every pass
		// before it marshals and measures. The step-4 assertion below pins that
		// this case exercises the reduce path, not the one-field truncation.
		key := "AKIA" + strings.Repeat("A", 16)
		entry := Entry{Type: "message", Role: "user", Text: strings.Repeat("!", 1193) + "x" + key + " " + strings.Repeat("<", 125)}
		if Redact(entry.Text) != entry.Text || Redact(key) == key {
			t.Fatalf("invalid redaction controls: Redact(text) = %q, Redact(key) = %q", Redact(entry.Text), Redact(key))
		}
		step4 := truncateLongestContentField(sanitizeEntry(entry), 1012/8)
		if awsAccessKeyPattern.MatchString(step4.Text) {
			t.Fatalf("step-4 intermediate already exposes the key, the case no longer pins the reduce path: %q", step4.Text)
		}
		got, truncated, err := EncodeTranscript([]Entry{entry}, 1012)
		if err != nil {
			t.Fatalf("EncodeTranscript error = %v", err)
		}
		if !truncated {
			t.Fatalf("EncodeTranscript truncated = false; want true")
		}
		if len(got) > 1012 {
			t.Fatalf("EncodeTranscript returned %d encoded bytes; want at most 1012", len(got))
		}
		if !json.Valid([]byte(got)) {
			t.Fatalf("EncodeTranscript output is not valid JSON: %q", got)
		}
		var back []Entry
		if err := json.Unmarshal([]byte(got), &back); err != nil || len(back) != 1 {
			t.Fatalf("decode output: %v (entries %d)", err, len(back))
		}
		if !strings.Contains(back[0].Text, "tokens truncated") {
			t.Fatalf("stored text %q carries no truncation marker", back[0].Text)
		}
		if strings.Contains(got, key) {
			t.Fatalf("the final-fit reduction kept the synthetic key unredacted: stored text = %q", back[0].Text)
		}
		if awsAccessKeyPattern.MatchString(back[0].Text) {
			t.Fatalf("the final-fit reduction introduced a §4 word boundary around an unredacted synthetic AWS-shaped key; stored text = %q", back[0].Text)
		}
		if Redact(back[0].Text) != back[0].Text {
			t.Fatalf("stored text is re-redactable: %q -> %q", back[0].Text, Redact(back[0].Text))
		}
		t.Logf("encoded_bytes=%d stored_text=%q", len(got), back[0].Text)
	})

	t.Run("every stored field is a Redact fixpoint", func(t *testing.T) {
		t.Parallel()
		// Class pin for Requirement 3.3: the invariant belongs to the FINAL
		// stored values, not to any intermediate. Every later byte-changing
		// transformation — §5 step-4 truncation, the final fit's drops and
		// repeated reductions, NUL stripping and JSON escaping (which expands
		// the "<" padding) — can create a shape no earlier Redact pass saw, so
		// the last byte-changing transformation on every stored value before
		// the marshal that returns it must be a full NUL-strip then Redact.
		// Redact is idempotent-safe on its own output, so that ordering makes
		// every decoded stored field a fixpoint.
		key := "AKIA" + strings.Repeat("A", 16)
		shapes := []struct {
			name string
			text string
		}{
			{"j5-truncation-boundary", strings.Repeat("!", 1000) + "x" + key + " " + strings.Repeat("!", 107)},
			{"reduce-path-boundary", strings.Repeat("!", 1193) + "x" + key + " " + strings.Repeat("<", 125)},
			{"nul-splice", "sk-" + strings.Repeat("a", 12) + "\x00" + strings.Repeat("a", 12)},
			{"bearer", "Bearer " + strings.Repeat("B", 24)},
			{"assignment", "password: hunter22xyz"},
			{"escaped-padding-glued-key", strings.Repeat("<", 300) + "x" + key + " " + strings.Repeat("<", 300)},
		}
		for _, shape := range shapes {
			for _, maxBytes := range []int{2, 64, 128, 256, 512, 1012, 2048} {
				got, _, err := EncodeTranscript([]Entry{{Type: "message", Role: "user", Text: shape.text}}, maxBytes)
				if err != nil {
					t.Fatalf("%s budget %d: %v", shape.name, maxBytes, err)
				}
				if len(got) > maxBytes {
					t.Fatalf("%s budget %d: encoded %d bytes exceed the bound", shape.name, maxBytes, len(got))
				}
				if !json.Valid([]byte(got)) {
					t.Fatalf("%s budget %d: output is not valid JSON: %q", shape.name, maxBytes, got)
				}
				var back []Entry
				if err := json.Unmarshal([]byte(got), &back); err != nil {
					t.Fatalf("%s budget %d: unmarshal output: %v", shape.name, maxBytes, err)
				}
				for i, entry := range back {
					fields := map[string]string{
						"type":      entry.Type,
						"role":      entry.Role,
						"text":      entry.Text,
						"name":      entry.Name,
						"call_id":   entry.CallID,
						"arguments": entry.Arguments,
						"output":    entry.Output,
					}
					for field, value := range fields {
						if value == "" {
							continue
						}
						if r := Redact(value); r != value {
							t.Fatalf("%s budget %d: stored entry[%d].%s = %q is re-redactable to %q", shape.name, maxBytes, i, field, value, r)
						}
					}
				}
			}
		}
	})

	t.Run("the stored-value criterion is the decoded value, not raw escapes", func(t *testing.T) {
		t.Parallel()
		// Documentation case for the R3.3 criterion boundary. The decoded value
		// keeps a tab; JSON marshal turns it into the two-byte escape "\t", and
		// the backslash satisfies the assignment pattern's value class, so the
		// raw JSON can show an assignment match the decoded value does not have.
		// That is not a leak and needs no redact-after-marshal logic: only the
		// decoded stored value must be a Redact fixpoint.
		text := "password: abc\tabcdefgh"
		if Redact(text) != text {
			t.Fatalf("control: decoded value must not match: %q -> %q", text, Redact(text))
		}
		got, _, err := EncodeTranscript([]Entry{{Type: "message", Role: "user", Text: text}}, 4096)
		if err != nil {
			t.Fatalf("EncodeTranscript error = %v", err)
		}
		var back []Entry
		if err := json.Unmarshal([]byte(got), &back); err != nil || len(back) != 1 {
			t.Fatalf("decode output: %v (entries %d)", err, len(back))
		}
		if back[0].Text != text {
			t.Fatalf("decoded stored text = %q; want %q", back[0].Text, text)
		}
		if Redact(back[0].Text) != back[0].Text {
			t.Fatalf("decoded stored text is re-redactable: %q -> %q", back[0].Text, Redact(back[0].Text))
		}
	})
}
