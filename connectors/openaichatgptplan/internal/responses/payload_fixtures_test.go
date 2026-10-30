package responses_test

// The canonical-to-wire fixture matrix.
//
// These fixtures ARE the wire contract of the new connector. They are written
// before the projection exists so that the target request document is a
// reviewable artifact rather than an emergent property of an implementation.
//
// # Authority
//
// The public OpenAI documentation is the authority for every rule below:
//
//   - https://developers.openai.com/siwc/token-sharing-open-source/models-and-inference
//   - https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations
//
// The two pages that matter say, in the preview limitations page:
//
//   - "Set `store: false` and `stream: true`. Send `input` as an array
//     containing the context needed for each request. Use `instructions` or
//     developer messages; explicit `{type: "message", role: "system"}` items
//     are rejected."
//   - "Omit `background`, `conversation`, `max_output_tokens`,
//     `max_tool_calls`, `metadata`, `moderation`, `multi_agent`, `prompt`,
//     `prompt_cache_retention`, `safety_identifier`, `temperature`,
//     `top_logprobs`, `top_p`, `truncation`, and `user`."
//   - "Omit `previous_response_id` over HTTP and send the required history in
//     `input`."
//   - "Group function/custom tools in namespaces or supply them through
//     `additional_tools` input items."
//
// # The wire contract these fixtures pin
//
//  1. Endpoint. The request is `POST https://api.openai.com/v1/responses`.
//     The origin is a constant of the package, so no configuration can point
//     the connector at a private ChatGPT route.
//
//  2. Transport flags. Every request carries `"store": false` and
//     `"stream": true`, independently of the downstream delivery mode. The
//     downstream mode is a client-side collection concern and never changes
//     the upstream request.
//
//  3. Instructions. The canonical `Call.Instructions` text is projected into
//     the top-level `instructions` string. Explicit system-role conversation
//     text is merged into the same string, after the instruction blocks, in
//     conversation order. The system text is extracted from wherever it sits
//     in the conversation, not only from its head. No system-role item is
//     ever emitted as an `input` message item: the public route rejects it.
//
//  4. Instruction merge. A text part contributes its text verbatim. A
//     message's block is its text parts joined with "\n"; a block whose
//     trimmed text is empty contributes nothing. The blocks are joined with
//     "\n\n": the instruction blocks first, in `Call.Instructions` order, and
//     then the system blocks, in conversation order. When no block survives,
//     the `instructions` key is absent from the request - not empty, not
//     null.
//
//  5. Chronology. A developer-, user- or assistant-role message becomes one
//     `{"type":"message","role":...}` item with the message's content parts
//     in order. The order of the canonical messages is the order of the
//     `input` items. A message that also carries a function call is split at
//     that part: the text before it is one message item, the call is the
//     next item, and the text after it is the item after that.
//
//  6. Content parts. Text becomes `input_text`, an image reference becomes
//     `input_image` with the reference as `image_url`, and a file reference
//     becomes `input_file` with the file name and the base64 payload of a
//     data-URL reference. The public `input_file` shape has no media-type
//     field, so the canonical media type has no representation and is not
//     carried.
//
//     The exact file form is a documented, not a proven, choice. The public
//     file-input guide's SDK examples send a data URL while its curl example
//     describes bare base64, and the preview page does not settle it. The
//     live route has to confirm the form this matrix pins; if it does not,
//     this fixture and the projection change together.
//
//  7. Tools. The canonical function tools are supplied through one
//     `{"type":"additional_tools","role":"developer","tools":[...]}` input
//     item placed at the head of `input`, because the documentation names
//     namespaces and `additional_tools` items as the supported ways to supply
//     function/custom tools on this route, and because a tool that becomes
//     available at a point in the input cannot serve the whole conversation
//     from the middle of it. A tool is `{"type":"function","name":...}` plus
//     the canonical description and parameter schema when it declares them.
//     The canonical contract has no strict-schema control, so no `strict`
//     key is sent.
//
//     This is a documented choice, not a proven one: the same page also
//     says that one tool type is not accepted in the top-level `tools` array
//     on this route, which implies the array exists here for other tools. The
//     human acceptance gate has to confirm that the route accepts the
//     `additional_tools` item; if it does not, this fixture and the
//     projection change together.
//
//  8. Tool choice. The canonical tool choice is the top-level `tool_choice`,
//     and every canonical mode keeps its own meaning:
//
//     - an unset mode sends no `tool_choice` key at all;
//     - the explicit automatic mode sends "auto";
//     - the explicit no-tool mode sends "none" (the canonical contract
//       allows it only without a declared tool catalogue);
//     - the "any" mode sends "required";
//     - the required mode with a name sends {"type":"function","name":...}.
//
//     The modes are not interchangeable: "auto" and "required" decide
//     whether the model must call a tool, so each one is pinned on its own.
//
//  9. Tool calls and results. An assistant function call becomes a
//     `function_call` item with the canonical call id, name and argument
//     text; a tool result becomes a `function_call_output` item with the same
//     call id. The call id is the join key between them, so it is carried
//     through unchanged.
//
// 10. Reasoning. The canonical reasoning effort becomes
//     `{"reasoning":{"effort":...}}`.
//
// # What every fixture also asserts
//
// The expected document is compared as a whole, so a projection that adds a
// key the contract does not have fails the same way a projection that drops
// one does. On top of that, every projected request is checked against the
// markers that must not appear anywhere in it: the Codex identity
// instruction, the `<user_instructions>` glue, the Codex CLI originator, the
// private ChatGPT route, and the Codex-private headers.
//
// The request document is read WITHOUT loss before those checks run. A JSON
// object that repeats a member can hide a value behind the repetition, and a
// reader that keeps the last value of a repeated member would drop exactly
// the value the repetition hides. The strict reader is in
// jsonstrict_test.go: it collects every decoded string the document carries
// and reports the first member the document repeats, at every nesting level.
// A document that repeats a member is refused for the shape comparison,
// because a last-wins decoder would read a document the projection did not
// produce.
//
// # Known gaps, deliberately not pinned here
//
//   - The canonical `ToolChoice.AllowedTools` subset has no pinned wire
//     shape. The public route's `tool_choice` is a string or a single
//     function reference, so the subset decision belongs to the policy task.
//
//   - The canonical `Options.Temperature`, `Options.TopP`,
//     `Options.MaxOutputTokens`, `Options.Verbosity` and
//     `Options.ParallelToolCalls` have no pinned wire shape. The
//     documentation lists the first three as unsupported fields on this
//     route, and what to do with a request that carries them is the policy
//     task's decision, not the projection's.
//
//   - The canonical item-authority form (`Call.Items`) is not pinned. The
//     matrix is message-authority based, and that is a LIMIT OF THIS
//     EVIDENCE, not a statement that the item form is somebody else's
//     problem: task 4.2 still owes the lawful ordinary item-authority
//     message and tool projection, task 8.1 owes its integration consumers,
//     and task 8.2 owns the OPTIONAL reasoning replay specifically - the
//     item form's reasoning items and item references are a different
//     question from the optional replay. Nothing in this matrix certifies
//     any canonical form beyond the message-authority corpus it contains.
//
//   - A system-role conversation item that carries content which cannot be
//     expressed in the `instructions` string has no pinned behaviour. The
//     public route has no legal system-role item, and whether such an item
//     becomes a developer-role item or an explicit refusal is a policy
//     decision.
//
//   - Whether the route asks for `reasoning.encrypted_content` is not
//     pinned. That is the exact reasoning replay question, which is
//     revalidated separately.
//
//   - No rejection case is pinned. Every requirement this task owns is a
//     "must be representable" requirement, and the requests the preview
//     policy has to refuse - unsupported fields, unsupported hosted tools, a
//     previous_response_id - belong to the policy task. The seam already
//     reports a failure, so the refusal vocabulary has a home when that task
//     fills it.
import (
	"encoding/json"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// fixtureModel is the model slug used by the matrix. It is a plain provider
// slug: the matrix is about the request document, not about model selection.
const fixtureModel = "gpt-6.1-sol"

// wireContractCase is one golden fixture pair: the canonical input and the
// wire request it must produce.
//
// The endpoint and the header set are identical for every case, so they are
// asserted once by the contract test. The request body is the part that
// differs per case, and it is the part the matrix exists for.
type wireContractCase struct {
	// name identifies the case and becomes the subtest name.
	name string
	// call is the canonical request.
	call aipapi.Call
	// model is the provider model id for the attempt.
	model string
	// wantBody is the expected JSON request document.
	wantBody string
}

// wireContractCases is the fixture matrix.
//
// Every case is a canonical call that the public route can represent, and
// every case is projected in both downstream delivery modes.
func wireContractCases() []wireContractCase {
	return []wireContractCase{
		{
			// Requirement 5.2: the canonical instructions map to the
			// Responses `instructions` field.
			name:  "instructions_map_to_the_instructions_field",
			model: fixtureModel,
			call: aipapi.Call{
				ID: "call-instructions",
				Instructions: []aipapi.Message{
					{Role: aipapi.RoleDeveloper, Parts: []aipapi.Part{aipapi.TextPart("You are a terse assistant.")}},
				},
				Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Say exactly: Hello, world!")}},
				},
			},
			wantBody: `{
  "model": "gpt-6.1-sol",
  "instructions": "You are a terse assistant.",
  "input": [
    {
      "type": "message",
      "role": "user",
      "content": [{"type": "input_text", "text": "Say exactly: Hello, world!"}]
    }
  ],
  "store": false,
  "stream": true
}`,
		},
		{
			// Requirements 5.3 and 5.4: a system-role conversation item is
			// not emitted as an input message item; its text is merged into
			// the instructions deterministically, after the instruction
			// blocks, without touching the user chronology.
			name:  "system_history_is_merged_into_the_instructions",
			model: fixtureModel,
			call: aipapi.Call{
				ID: "call-system-history",
				Instructions: []aipapi.Message{
					{Role: aipapi.RoleDeveloper, Parts: []aipapi.Part{aipapi.TextPart("You are a terse assistant.")}},
				},
				Messages: []aipapi.Message{
					{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("Never invent citations.")}},
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("What is the capital of France?")}},
				},
			},
			wantBody: `{
  "model": "gpt-6.1-sol",
  "instructions": "You are a terse assistant.\n\nNever invent citations.",
  "input": [
    {
      "type": "message",
      "role": "user",
      "content": [{"type": "input_text", "text": "What is the capital of France?"}]
    }
  ],
  "store": false,
  "stream": true
}`,
		},
		{
			// Requirements 5.3 and 5.4 under a conflict-seeded history: the
			// system text is extracted from wherever it sits, not only from
			// the head of the conversation, and every block keeps its
			// position in the merged instructions. The instruction side
			// contributes two blocks, the first of them with two text parts,
			// and one system block carries whitespace only, so the
			// empty-block rule is exercised too.
			//
			// The non-system chronology is untouched: the user, assistant and
			// tool items keep their order, and no system item appears in the
			// input.
			name:  "system_blocks_interleaved_with_history_are_merged_in_order",
			model: fixtureModel,
			call: aipapi.Call{
				ID: "call-interleaved-system",
				Instructions: []aipapi.Message{
					{Role: aipapi.RoleDeveloper, Parts: []aipapi.Part{
						aipapi.TextPart("Answer in one sentence."),
						aipapi.TextPart("Prefer small diffs."),
					}},
					{Role: aipapi.RoleDeveloper, Parts: []aipapi.Part{aipapi.TextPart("Use the repository's own tests.")}},
				},
				Tools: []aipapi.ToolDef{
					{
						Name:        "lookup_order",
						Description: "Look up an order.",
						Parameters:  json.RawMessage(`{"type":"object","properties":{"order_id":{"type":"string"}},"required":["order_id"]}`),
					},
				},
				Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Where is order 42?")}},
					{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("Never invent citations.")}},
					{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("Let me look that up.")}},
					{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("   ")}},
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("And order 43?")}},
					{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("Always name the shipping file.")}},
					{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
						{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"type":"function_call","call_id":"call_order_1","name":"lookup_order","arguments":"{\"order_id\":\"42\"}"}`)},
					}},
					{Role: aipapi.RoleTool, Parts: []aipapi.Part{
						{Kind: aipapi.PartToolResult, ToolCallID: "call_order_1", Content: json.RawMessage(`"shipped"`)},
					}},
				},
			},
			wantBody: `{
  "model": "gpt-6.1-sol",
  "instructions": "Answer in one sentence.\nPrefer small diffs.\n\nUse the repository's own tests.\n\nNever invent citations.\n\nAlways name the shipping file.",
  "input": [
    {
      "type": "additional_tools",
      "role": "developer",
      "tools": [
        {
          "type": "function",
          "name": "lookup_order",
          "description": "Look up an order.",
          "parameters": {
            "type": "object",
            "properties": {"order_id": {"type": "string"}},
            "required": ["order_id"]
          }
        }
      ]
    },
    {
      "type": "message",
      "role": "user",
      "content": [{"type": "input_text", "text": "Where is order 42?"}]
    },
    {
      "type": "message",
      "role": "assistant",
      "content": [{"type": "input_text", "text": "Let me look that up."}]
    },
    {
      "type": "message",
      "role": "user",
      "content": [{"type": "input_text", "text": "And order 43?"}]
    },
    {
      "type": "function_call",
      "call_id": "call_order_1",
      "name": "lookup_order",
      "arguments": "{\"order_id\":\"42\"}"
    },
    {
      "type": "function_call_output",
      "call_id": "call_order_1",
      "output": "shipped"
    }
  ],
  "store": false,
  "stream": true
}`,
		},
		{
			// Requirements 5.3, 5.4 and 5.5: the legal roles keep their
			// identity and their order, and a call without instruction text
			// sends no `instructions` key at all.
			name:  "developer_user_and_assistant_history_keeps_its_order",
			model: fixtureModel,
			call: aipapi.Call{
				ID: "call-history",
				Messages: []aipapi.Message{
					{Role: aipapi.RoleDeveloper, Parts: []aipapi.Part{aipapi.TextPart("Answer in one sentence.")}},
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("What is 2+2?")}},
					{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("4")}},
				},
			},
			wantBody: `{
  "model": "gpt-6.1-sol",
  "input": [
    {
      "type": "message",
      "role": "developer",
      "content": [{"type": "input_text", "text": "Answer in one sentence."}]
    },
    {
      "type": "message",
      "role": "user",
      "content": [{"type": "input_text", "text": "What is 2+2?"}]
    },
    {
      "type": "message",
      "role": "assistant",
      "content": [{"type": "input_text", "text": "4"}]
    }
  ],
  "store": false,
  "stream": true
}`,
		},
		{
			// Requirement 6.5: the function tools are supplied as an
			// `additional_tools` input item, and the call and its result keep
			// the same call id.
			name:  "function_call_and_result_keep_the_call_id",
			model: fixtureModel,
			call: aipapi.Call{
				ID: "call-tools",
				Tools: []aipapi.ToolDef{
					{
						Name:        "get_weather",
						Description: "Look up the current weather.",
						Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`),
					},
					{Name: "ping"},
				},
				Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("What is the weather in Paris?")}},
					{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
						{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"type":"function_call","call_id":"call_abc","name":"get_weather","arguments":"{\"city\":\"Paris\"}"}`)},
					}},
					{Role: aipapi.RoleTool, Parts: []aipapi.Part{
						{Kind: aipapi.PartToolResult, ToolCallID: "call_abc", Content: json.RawMessage(`"sunny"`)},
					}},
				},
			},
			wantBody: `{
  "model": "gpt-6.1-sol",
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
      "role": "user",
      "content": [{"type": "input_text", "text": "What is the weather in Paris?"}]
    },
    {
      "type": "function_call",
      "call_id": "call_abc",
      "name": "get_weather",
      "arguments": "{\"city\":\"Paris\"}"
    },
    {
      "type": "function_call_output",
      "call_id": "call_abc",
      "output": "sunny"
    }
  ],
  "store": false,
  "stream": true
}`,
		},
		{
			// Requirement 6.5 and the tool choice part of the contract: a
			// forced function is a top-level tool choice.
			name:  "tool_choice_forces_the_named_function",
			model: fixtureModel,
			call: aipapi.Call{
				ID: "call-tool-choice",
				Tools: []aipapi.ToolDef{
					{
						Name:        "get_weather",
						Description: "Look up the current weather.",
						Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`),
					},
				},
				ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired, Name: "get_weather"},
				Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("What is the weather in Paris?")}},
				},
			},
			wantBody: `{
  "model": "gpt-6.1-sol",
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
        }
      ]
    },
    {
      "type": "message",
      "role": "user",
      "content": [{"type": "input_text", "text": "What is the weather in Paris?"}]
    }
  ],
  "tool_choice": {"type": "function", "name": "get_weather"},
  "store": false,
  "stream": true
}`,
		},
		{
			// The explicit automatic mode is not the required mode: a
			// projection that turns it into "required" changes whether the
			// model must call a tool. The canonical mode is unset in the
			// other fixtures, and an unset mode sends no tool choice at all,
			// so the explicit mode has to be pinned on its own.
			name:  "tool_choice_auto_is_forwarded_as_auto",
			model: fixtureModel,
			call: aipapi.Call{
				ID: "call-tool-choice-auto",
				Tools: []aipapi.ToolDef{
					{
						Name:        "get_weather",
						Description: "Look up the current weather.",
						Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`),
					},
				},
				ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
				Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("What is the weather in Paris?")}},
				},
			},
			wantBody: `{
  "model": "gpt-6.1-sol",
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
        }
      ]
    },
    {
      "type": "message",
      "role": "user",
      "content": [{"type": "input_text", "text": "What is the weather in Paris?"}]
    }
  ],
  "tool_choice": "auto",
  "store": false,
  "stream": true
}`,
		},
		{
			// The explicit no-tool mode is legal only without declared tools:
			// the canonical contract refuses "none" next to a tool
			// catalogue. The wire keeps the explicit refusal instead of
			// dropping the client's constraint.
			name:  "tool_choice_none_without_tools_is_forwarded_as_none",
			model: fixtureModel,
			call: aipapi.Call{
				ID:         "call-tool-choice-none",
				ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceNone},
				Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Say exactly: Hello, world!")}},
				},
			},
			wantBody: `{
  "model": "gpt-6.1-sol",
  "input": [
    {
      "type": "message",
      "role": "user",
      "content": [{"type": "input_text", "text": "Say exactly: Hello, world!"}]
    }
  ],
  "tool_choice": "none",
  "store": false,
  "stream": true
}`,
		},
		{
			// The "any" mode means a tool call is required without naming
			// one, so it is the wire string "required" and not a forced
			// function reference.
			name:  "tool_choice_any_requires_a_tool_call",
			model: fixtureModel,
			call: aipapi.Call{
				ID: "call-tool-choice-any",
				Tools: []aipapi.ToolDef{
					{
						Name:        "get_weather",
						Description: "Look up the current weather.",
						Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`),
					},
				},
				ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAny},
				Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("What is the weather in Paris?")}},
				},
			},
			wantBody: `{
  "model": "gpt-6.1-sol",
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
        }
      ]
    },
    {
      "type": "message",
      "role": "user",
      "content": [{"type": "input_text", "text": "What is the weather in Paris?"}]
    }
  ],
  "tool_choice": "required",
  "store": false,
  "stream": true
}`,
		},
		{
			// Requirement 6.6: a supported image stays available.
			name:  "image_input_stays_available",
			model: fixtureModel,
			call: aipapi.Call{
				ID: "call-image",
				Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{
						aipapi.TextPart("What is in this picture?"),
						{Kind: aipapi.PartImageRef, ImageRef: "data:image/png;base64,iVBORw0KGgo=", ImageMIME: "image/png"},
					}},
				},
			},
			wantBody: `{
  "model": "gpt-6.1-sol",
  "input": [
    {
      "type": "message",
      "role": "user",
      "content": [
        {"type": "input_text", "text": "What is in this picture?"},
        {"type": "input_image", "image_url": "data:image/png;base64,iVBORw0KGgo="}
      ]
    }
  ],
  "store": false,
  "stream": true
}`,
		},
		{
			// Requirement 6.6: a supported file stays available. The public
			// input_file shape carries the file name and the payload, and has
			// no media-type field.
			name:  "file_input_stays_available",
			model: fixtureModel,
			call: aipapi.Call{
				ID: "call-file",
				Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{
						aipapi.TextPart("Summarize the attached report."),
						aipapi.FilePart("data:application/pdf;base64,JVBERi0xLjQK", "application/pdf", "report.pdf"),
					}},
				},
			},
			wantBody: `{
  "model": "gpt-6.1-sol",
  "input": [
    {
      "type": "message",
      "role": "user",
      "content": [
        {"type": "input_text", "text": "Summarize the attached report."},
        {"type": "input_file", "filename": "report.pdf", "file_data": "JVBERi0xLjQK"}
      ]
    }
  ],
  "store": false,
  "stream": true
}`,
		},
		{
			// The reasoning option maps to the request-side reasoning control.
			name:  "reasoning_effort_maps_to_the_reasoning_control",
			model: fixtureModel,
			call: aipapi.Call{
				ID: "call-reasoning",
				Options: aipapi.GenerationOptions{
					ReasoningEffort: "high",
				},
				Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Solve 17*23 step by step.")}},
				},
			},
			wantBody: `{
  "model": "gpt-6.1-sol",
  "input": [
    {
      "type": "message",
      "role": "user",
      "content": [{"type": "input_text", "text": "Solve 17*23 step by step."}]
    }
  ],
  "reasoning": {"effort": "high"},
  "store": false,
  "stream": true
}`,
		},
		{
			// The assistant text around the assistant function call keeps its
			// relative order: the text before the call is one message item,
			// the call is the next item, and the text after the call is the
			// item after that, before the tool result. A projection that
			// drops the text behind the call would still look right without
			// it, so the suffix is pinned here. A tool result that is not a
			// JSON string is forwarded as its JSON text.
			name:  "assistant_text_around_a_function_call_keeps_its_order",
			model: fixtureModel,
			call: aipapi.Call{
				ID: "call-mixed",
				Tools: []aipapi.ToolDef{
					{
						Name:        "get_weather",
						Description: "Look up the current weather.",
						Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`),
					},
				},
				Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("What is the weather in Paris?")}},
					{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
						aipapi.TextPart("Let me look that up."),
						{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"type":"function_call","call_id":"call_xyz","name":"get_weather","arguments":"{\"city\":\"Paris\"}"}`)},
						aipapi.TextPart("It is 21 degrees there."),
					}},
					{Role: aipapi.RoleTool, Parts: []aipapi.Part{
						{Kind: aipapi.PartToolResult, ToolCallID: "call_xyz", Content: json.RawMessage(`{"temperature_c": 21}`)},
					}},
				},
			},
			wantBody: `{
  "model": "gpt-6.1-sol",
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
        }
      ]
    },
    {
      "type": "message",
      "role": "user",
      "content": [{"type": "input_text", "text": "What is the weather in Paris?"}]
    },
    {
      "type": "message",
      "role": "assistant",
      "content": [{"type": "input_text", "text": "Let me look that up."}]
    },
    {
      "type": "function_call",
      "call_id": "call_xyz",
      "name": "get_weather",
      "arguments": "{\"city\":\"Paris\"}"
    },
    {
      "type": "message",
      "role": "assistant",
      "content": [{"type": "input_text", "text": "It is 21 degrees there."}]
    },
    {
      "type": "function_call_output",
      "call_id": "call_xyz",
      "output": "{\"temperature_c\": 21}"
    }
  ],
  "store": false,
  "stream": true
}`,
		},
	}
}
