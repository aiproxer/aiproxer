package responses

// Input projection for the public Responses request.
//
// # Authority
//
// The canonical call has two authorities, and the projection reads the one the
// call actually carries: Call.Items when the item authority is present, and
// Call.Messages/Instructions otherwise. It never rebuilds one authority from
// the other, so chronology and call ids come from the canonical call and not
// from a reconstructed copy. aipapi.Call.Validate has already refused a call
// that carries both.
//
// # What the public route accepts
//
// The documented preview contract is an `input` array of items: developer,
// user and assistant message items; function_call and function_call_output
// items; and one additional_tools item that carries the function catalogue.
// Explicit system-role items are rejected by the route, so system conversation
// text is folded into the instructions string by projectInstructions instead
// of reaching this file's message path.
//
// # Every arrived item is projected or the input is refused
//
// The projector never samples a projectable part and drops the rest. A form
// with no lawful public representation produces a ProjectionError with the
// canonical field and a stable reason; spec task 4.3 decides which supported
// request the preview policy admits, and spec task 8.2 owns optional reasoning
// replay. Two call-level members stay outside this file: internal
// Call.Extensions are hook bookkeeping with no wire carrier, and
// Call.SemanticExtensions are residual semantics that the envelope check
// refuses rather than drops.
import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// wireAdditionalToolsItem carries the canonical function catalogue.
//
// The documented way to supply function/custom tools on this route is a
// namespace or an additional_tools input item. The item is placed at the head
// of the input because a tool that becomes available in the middle of a
// conversation cannot serve the whole conversation, and because the canonical
// tool catalogue belongs to the request rather than to one turn.
type wireAdditionalToolsItem struct {
	Type  string     `json:"type"`
	Role  string     `json:"role"`
	Tools []wireTool `json:"tools"`
}

// wireTool is one function tool declaration.
type wireTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// wireMessageItem is one message item in the input chronology.
type wireMessageItem struct {
	Type    string      `json:"type"`
	Role    aipapi.Role `json:"role"`
	Content []any       `json:"content"`
}

// wireInputText is one text content part.
type wireInputText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// wireInputImage is one image content part. The canonical reference is the
// image URL the route takes; the canonical media type is redundant with a
// data URL and has no separate public field.
type wireInputImage struct {
	Type     string `json:"type"`
	ImageURL string `json:"image_url"`
}

// wireInputFile is one file content part.
//
// The public input_file shape carries the file name and inline base64 payload
// and has no media-type field. The fixture matrix in payload_fixtures_test.go
// records that this is the documented, not yet live-proven, form.
type wireInputFile struct {
	Type     string `json:"type"`
	Filename string `json:"filename,omitempty"`
	FileData string `json:"file_data"`
}

// wireFunctionCall is one assistant function call.
type wireFunctionCall struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// wireFunctionCallOutput is one tool result. The call id is the join key with
// the function_call item and is carried through unchanged.
type wireFunctionCallOutput struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

// projectInput projects the canonical conversation into the input array.
func projectInput(call aipapi.Call) ([]any, error) {
	input := make([]any, 0, len(call.Messages)+len(call.Items)+1)
	if len(call.Tools) > 0 {
		input = append(input, wireAdditionalToolsItem{
			Type:  "additional_tools",
			Role:  "developer",
			Tools: projectTools(call.Tools),
		})
	}
	if call.HasItemAuthority() {
		for i, item := range call.Items {
			projected, err := projectItem(item, fmt.Sprintf("Items[%d]", i))
			if err != nil {
				return nil, err
			}
			input = append(input, projected...)
		}
		return input, nil
	}
	for i, msg := range call.Messages {
		projected, err := projectMessage(msg, fmt.Sprintf("Messages[%d]", i))
		if err != nil {
			return nil, err
		}
		input = append(input, projected...)
	}
	return input, nil
}

// projectTools preserves the canonical catalogue order.
func projectTools(tools []aipapi.ToolDef) []wireTool {
	out := make([]wireTool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, wireTool{
			Type:        "function",
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  tool.Parameters,
		})
	}
	return out
}

// projectMessage projects one message-authority message.
func projectMessage(msg aipapi.Message, field string) ([]any, error) {
	switch msg.Role {
	case aipapi.RoleSystem:
		// System text lives in the instructions string. Emitting it as an
		// input item would be rejected by the public route.
		return nil, nil
	case aipapi.RoleTool:
		return projectToolResultParts(msg.Parts, field)
	case aipapi.RoleAssistant:
		return projectAssistantParts(msg.Parts, field)
	case aipapi.RoleDeveloper, aipapi.RoleUser:
		content, err := projectMessageParts(msg.Parts, msg.Role, field)
		if err != nil {
			return nil, err
		}
		return []any{wireMessageItem{Type: "message", Role: msg.Role, Content: content}}, nil
	default:
		return nil, projectionError(ProjectionReasonRole, field+".Role",
			fmt.Sprintf("role %q has no legal public input position", msg.Role))
	}
}

// projectMessageParts projects the content parts of one message-authority
// message, preserving their order and count.
func projectMessageParts(parts []aipapi.Part, role aipapi.Role, field string) ([]any, error) {
	content := make([]any, 0, len(parts))
	for i, part := range parts {
		projected, err := projectMessagePart(part, role, fmt.Sprintf("%s.Parts[%d]", field, i))
		if err != nil {
			return nil, err
		}
		content = append(content, projected)
	}
	return content, nil
}

// projectMessagePart projects one message-authority content part.
func projectMessagePart(part aipapi.Part, role aipapi.Role, field string) (any, error) {
	switch part.Kind {
	case aipapi.PartText:
		return wireInputText{Type: "input_text", Text: part.Text}, nil
	case aipapi.PartImageRef:
		if role == aipapi.RoleAssistant {
			return nil, projectionError(ProjectionReasonAssistantMedia, field,
				"an assistant message has no documented image input form on this route")
		}
		return wireInputImage{Type: "input_image", ImageURL: part.ImageRef}, nil
	case aipapi.PartFileRef:
		if role == aipapi.RoleAssistant {
			return nil, projectionError(ProjectionReasonAssistantMedia, field,
				"an assistant message has no documented file input form on this route")
		}
		return projectFile(part.FileRef, "", part.FileName, field)
	default:
		return nil, projectionError(ProjectionReasonPart, field,
			fmt.Sprintf("part kind %q has no lawful public input representation", part.Kind))
	}
}

// projectAssistantParts splits an assistant message at its function calls.
//
// The text before a call is one message item, the call is the next item, and
// the text after it is the item after that. Model-authored content is never
// reordered and no empty message item is invented for an empty segment.
func projectAssistantParts(parts []aipapi.Part, field string) ([]any, error) {
	items := make([]any, 0, len(parts))
	pending := make([]aipapi.Part, 0, len(parts))
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		content, err := projectMessageParts(pending, aipapi.RoleAssistant, field)
		if err != nil {
			return err
		}
		items = append(items, wireMessageItem{Type: "message", Role: aipapi.RoleAssistant, Content: content})
		pending = pending[:0]
		return nil
	}
	for i, part := range parts {
		partField := fmt.Sprintf("%s.Parts[%d]", field, i)
		switch part.Kind {
		case aipapi.PartJSON:
			call, err := projectFunctionCallPart(part, partField)
			if err != nil {
				return nil, err
			}
			if err := flush(); err != nil {
				return nil, err
			}
			items = append(items, call)
		case aipapi.PartReasoning:
			return nil, projectionError(ProjectionReasonReasoningReplay, partField,
				"optional reasoning replay belongs to spec task 8.2")
		default:
			pending = append(pending, part)
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return items, nil
}

// projectToolResultParts projects every tool result part of a tool-role
// message, in canonical order.
func projectToolResultParts(parts []aipapi.Part, field string) ([]any, error) {
	items := make([]any, 0, len(parts))
	for i, part := range parts {
		partField := fmt.Sprintf("%s.Parts[%d]", field, i)
		if part.Kind != aipapi.PartToolResult {
			return nil, projectionError(ProjectionReasonPart, partField,
				"a tool-role message may carry only tool_result parts")
		}
		output, err := toolResultOutput(part)
		if err != nil {
			return nil, projectionError(ProjectionReasonToolResult, partField, err.Error())
		}
		items = append(items, wireFunctionCallOutput{
			Type:   "function_call_output",
			CallID: part.ToolCallID,
			Output: output,
		})
	}
	return items, nil
}

// projectItem projects one item-authority item.
func projectItem(item aipapi.Item, field string) ([]any, error) {
	switch item.Kind {
	case aipapi.ItemKindMessage:
		return projectItemMessage(item, field)
	case aipapi.ItemKindToolCall:
		if item.ToolCall == nil {
			return nil, projectionError(ProjectionReasonItemKind, field, "the tool call payload is missing")
		}
		arguments, err := argumentsText(item.ToolCall.Arguments)
		if err != nil {
			return nil, projectionError(ProjectionReasonToolCall, field+".ToolCall.Arguments", err.Error())
		}
		return []any{wireFunctionCall{
			Type:      "function_call",
			CallID:    item.ToolCall.CallID,
			Name:      item.ToolCall.Name,
			Arguments: arguments,
		}}, nil
	case aipapi.ItemKindToolResult:
		return projectToolResultItem(item, field)
	case aipapi.ItemKindReasoning:
		return nil, projectionError(ProjectionReasonReasoningReplay, field,
			"optional reasoning replay belongs to spec task 8.2")
	case aipapi.ItemKindItemReference:
		return nil, projectionError(ProjectionReasonItemReference, field,
			"an item reference names provider state the public HTTP route cannot resolve")
	case aipapi.ItemKindCompaction:
		return nil, projectionError(ProjectionReasonItemKind, field,
			"a compaction item has no public input carrier")
	case aipapi.ItemKindExtension:
		return nil, projectionError(ProjectionReasonItemKind, field,
			"an opaque extension item has no public input carrier")
	default:
		return nil, projectionError(ProjectionReasonItemKind, field,
			fmt.Sprintf("item kind %q has no public input carrier", item.Kind))
	}
}

// projectItemMessage projects one item-authority message item.
func projectItemMessage(item aipapi.Item, field string) ([]any, error) {
	if item.Phase != "" {
		return nil, projectionError(ProjectionReasonAssistantPhase, field+".Phase",
			"the assistant phase annotation has no pinned wire shape on this route")
	}
	switch item.Role {
	case aipapi.RoleSystem:
		// System text lives in the instructions string.
		return nil, nil
	case aipapi.RoleTool:
		return nil, projectionError(ProjectionReasonRole, field+".Role",
			"a tool-role message item carries no call id; project the result as a tool_result item")
	case aipapi.RoleDeveloper, aipapi.RoleUser, aipapi.RoleAssistant:
		content, err := projectItemContent(item.Content, item.Role, field)
		if err != nil {
			return nil, err
		}
		return []any{wireMessageItem{Type: "message", Role: item.Role, Content: content}}, nil
	default:
		return nil, projectionError(ProjectionReasonRole, field+".Role",
			fmt.Sprintf("role %q has no legal public input position", item.Role))
	}
}

// projectItemContent projects the content parts of one item-authority message
// item, preserving their order and count.
func projectItemContent(parts []aipapi.ContentPart, role aipapi.Role, field string) ([]any, error) {
	content := make([]any, 0, len(parts))
	for i, part := range parts {
		projected, err := projectItemContentPart(part, role, fmt.Sprintf("%s.Content[%d]", field, i))
		if err != nil {
			return nil, err
		}
		content = append(content, projected)
	}
	return content, nil
}

// projectItemContentPart projects one item-authority content part.
func projectItemContentPart(part aipapi.ContentPart, role aipapi.Role, field string) (any, error) {
	switch part.Kind {
	case aipapi.ContentPartText:
		return wireInputText{Type: "input_text", Text: part.Text}, nil
	case aipapi.ContentPartImageRef:
		if role == aipapi.RoleAssistant {
			return nil, projectionError(ProjectionReasonAssistantMedia, field,
				"an assistant message has no documented image input form on this route")
		}
		return wireInputImage{Type: "input_image", ImageURL: part.ImageRef}, nil
	case aipapi.ContentPartFileRef:
		if role == aipapi.RoleAssistant {
			return nil, projectionError(ProjectionReasonAssistantMedia, field,
				"an assistant message has no documented file input form on this route")
		}
		return projectFile(part.FileRef, part.FileData, part.FileName, field)
	case aipapi.ContentPartReasoning:
		return nil, projectionError(ProjectionReasonReasoningReplay, field,
			"optional reasoning replay belongs to spec task 8.2")
	default:
		return nil, projectionError(ProjectionReasonPart, field,
			fmt.Sprintf("content part kind %q has no lawful public input representation", part.Kind))
	}
}

// projectToolResultItem projects one item-authority tool result.
//
// The canonical item carries either the literal output string or structured
// parts. Only the output string has a pinned public wire shape; a structured
// result is refused rather than flattened into a string nobody documented.
func projectToolResultItem(item aipapi.Item, field string) ([]any, error) {
	if item.ToolResult == nil {
		return nil, projectionError(ProjectionReasonToolResult, field, "the tool result payload is missing")
	}
	if len(item.ToolResult.Parts) > 0 {
		return nil, projectionError(ProjectionReasonToolResult, field+".ToolResult",
			"structured tool result parts have no pinned public wire shape")
	}
	return []any{wireFunctionCallOutput{
		Type:   "function_call_output",
		CallID: item.ToolResult.CallID,
		Output: item.ToolResult.Output,
	}}, nil
}

// projectFile projects one canonical file part.
//
// The route's file input carries inline bytes. A data-URL reference supplies
// them; inline FileData from the item authority supplies them directly. A file
// that exists only as an external reference has no bytes to send, so it is
// refused instead of being sent as an unproven file_url.
func projectFile(ref, data, name, field string) (any, error) {
	payload := data
	inline, hasInlineRef := inlineBase64Payload(ref)
	if payload == "" {
		if !hasInlineRef {
			return nil, projectionError(ProjectionReasonFileReference, field,
				"a file reference without an inline base64 data URL has no file_data representation")
		}
		payload = inline
	} else if hasInlineRef && inline != payload {
		return nil, projectionError(ProjectionReasonFileReference, field,
			"the inline file data and the data-URL payload disagree")
	}
	return wireInputFile{Type: "input_file", Filename: name, FileData: payload}, nil
}

// inlineBase64Payload extracts the payload of a base64 data URL.
func inlineBase64Payload(ref string) (string, bool) {
	const prefix = "data:"
	if !strings.HasPrefix(ref, prefix) {
		return "", false
	}
	comma := strings.IndexByte(ref, ',')
	if comma < 0 {
		return "", false
	}
	metadata := ref[len(prefix):comma]
	payload := ref[comma+1:]
	if payload == "" || !hasBase64Flag(metadata) {
		return "", false
	}
	return payload, true
}

// hasBase64Flag reports whether the data-URL metadata carries the base64
// extension as its last parameter. The flag is case-insensitive and must be a
// parameter (separated by ';'), not the media type itself.
func hasBase64Flag(metadata string) bool {
	separator := strings.LastIndexByte(metadata, ';')
	if separator < 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(metadata[separator+1:]), "base64")
}

// projectFunctionCallPart projects one assistant JSON part as a function call.
//
// Two canonical carriers exist and the carrier that owns the identity decides
// the reading. The stream-observer shape puts complete identity on the Part
// (ToolCallID/ToolName) and argument JSON in Content, so Content stays argument
// data verbatim: a member named "type", or even data shaped like a function
// call object, is model-authored JSON and never a second identity authority.
// Only when the Part identity is empty does Content own identity, in the
// Responses function_call or Chat Completions function envelope.
//
// An incomplete Part identity is refused: merging it with the JSON copy would
// invent the identity authority the call did not carry.
//
// Presence and completeness are decided on trimmed copies; the identity bytes
// themselves are never rewritten. Trimming the call id here would rename one
// side of the call/result join and break a valid canonical history.
func projectFunctionCallPart(part aipapi.Part, field string) (wireFunctionCall, error) {
	idPresent := strings.TrimSpace(part.ToolCallID) != ""
	namePresent := strings.TrimSpace(part.ToolName) != ""
	if idPresent || namePresent {
		if !idPresent || !namePresent {
			return wireFunctionCall{}, projectionError(ProjectionReasonToolCall, field,
				"the canonical tool call identity is incomplete")
		}
		arguments, err := canonicalPartArgumentsText(part.Content)
		if err != nil {
			return wireFunctionCall{}, projectionError(ProjectionReasonToolCall, field, err.Error())
		}
		return wireFunctionCall{Type: "function_call", CallID: part.ToolCallID, Name: part.ToolName, Arguments: arguments}, nil
	}
	envelope, hasEnvelope, err := decodeToolCallEnvelope(part.Content)
	if err != nil {
		return wireFunctionCall{}, projectionError(ProjectionReasonToolCall, field, err.Error())
	}
	if !hasEnvelope {
		return wireFunctionCall{}, projectionError(ProjectionReasonToolCall, field,
			"the json part is not a function call with a call id and a name")
	}
	if strings.TrimSpace(envelope.callID) == "" {
		if envelope.chatCompletions {
			return wireFunctionCall{}, projectionError(ProjectionReasonToolCall, field,
				"the Chat Completions function entry carries no id, so the call has no join key")
		}
		return wireFunctionCall{}, projectionError(ProjectionReasonToolCall, field,
			"the Responses function_call item carries no call_id; its item id is a different identity")
	}
	if strings.TrimSpace(envelope.name) == "" {
		return wireFunctionCall{}, projectionError(ProjectionReasonToolCall, field,
			"the function call envelope carries no name")
	}
	arguments, err := argumentsText(envelope.arguments)
	if err != nil {
		return wireFunctionCall{}, projectionError(ProjectionReasonToolCall, field, err.Error())
	}
	return wireFunctionCall{Type: "function_call", CallID: envelope.callID, Name: envelope.name, Arguments: arguments}, nil
}

// toolCallEnvelope is the Responses/Chat-Completions function-call JSON shape.
type toolCallEnvelope struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Function  struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

// decodedToolCall is the identity and arguments of one function-call object.
type decodedToolCall struct {
	callID    string
	name      string
	arguments json.RawMessage
	// chatCompletions records which envelope shape owned the identity. Only the
	// Chat Completions entry uses its id as the call id; a Responses item id is
	// the item's own identity, not the key its output joins.
	chatCompletions bool
}

// decodeToolCallEnvelope reads the function-call JSON shape when the content
// carries one. A content that is not a function-call object is reported as
// absent so the caller can treat it as the stream-observer arguments carrier.
//
// The envelope's identity bytes are the identity a producer put in the JSON, so
// they are returned as authored. Presence and the fallback decision use trimmed
// copies; replacing the bytes would rename the join key one side of the history
// still carries.
func decodeToolCallEnvelope(raw json.RawMessage) (decodedToolCall, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return decodedToolCall{}, false, nil
	}
	if !json.Valid(trimmed) {
		return decodedToolCall{}, false, errors.New("the json part is not valid JSON")
	}
	var envelope toolCallEnvelope
	if err := json.Unmarshal(trimmed, &envelope); err != nil {
		return decodedToolCall{}, false, nil
	}
	chatCompletions := false
	switch envelope.Type {
	case "function_call":
	case "function":
		chatCompletions = true
	default:
		return decodedToolCall{}, false, nil
	}
	call := decodedToolCall{
		callID:          envelope.CallID,
		name:            envelope.Name,
		arguments:       envelope.Arguments,
		chatCompletions: chatCompletions,
	}
	if strings.TrimSpace(call.callID) == "" && chatCompletions {
		call.callID = envelope.ID
	}
	if strings.TrimSpace(call.name) == "" {
		call.name = envelope.Function.Name
	}
	if len(bytes.TrimSpace(call.arguments)) == 0 {
		call.arguments = envelope.Function.Arguments
	}
	return call, true, nil
}

// canonicalPartArgumentsText renders a message-authority PartJSON argument
// document as the wire's JSON-text string. Message authority stores the
// document itself, including a JSON string document that is its own argument
// text, so the bytes are the text and are never decoded a second time.
func canonicalPartArgumentsText(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", nil
	}
	if !json.Valid(trimmed) {
		return "", errors.New("the tool call arguments are not valid JSON")
	}
	return string(raw), nil
}

// argumentsText renders a wire-carrier canonical argument as the wire's
// JSON-text string.
//
// A canonical JSON string carries argument text and is decoded once; any other
// JSON value is its own text. The projector never re-encodes or reorders the
// model-authored argument object.
func argumentsText(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", nil
	}
	if !json.Valid(trimmed) {
		return "", errors.New("the tool call arguments are not valid JSON")
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return "", errors.New("the tool call arguments are not a JSON string")
		}
		return text, nil
	}
	return string(trimmed), nil
}

// toolResultText renders a canonical tool-result content value as the output
// string. A JSON string is decoded once; any other JSON value keeps its text.
func toolResultText(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", nil
	}
	if !json.Valid(trimmed) {
		return "", errors.New("the tool result content is not valid JSON")
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return "", errors.New("the tool result content is not a JSON string")
		}
		return text, nil
	}
	return string(trimmed), nil
}

// toolResultOutput resolves a tool result's output from its populated carrier.
//
// Two canonical forms carry the same answer. The item authority's literal
// output is Part.Text - this is what aipapi.ProjectItemsToLegacyView emits for
// an ordinary ToolResultItem - while the stream-observer form carries it as JSON
// in Part.Content. A single populated carrier decides on its own.
//
// When both carry a value they must agree: an agreeing pair is one answer and
// projects once, while a disagreeing pair is two answers and is refused with
// the tool_result reason instead of one value being picked silently.
func toolResultOutput(part aipapi.Part) (string, error) {
	hasText := part.Text != ""
	hasContent := len(bytes.TrimSpace(part.Content)) > 0
	switch {
	case hasText && hasContent:
		fromContent, err := toolResultText(part.Content)
		if err != nil {
			return "", err
		}
		if fromContent != part.Text {
			return "", errors.New("the literal tool result text and its JSON copy disagree")
		}
		return part.Text, nil
	case hasText:
		return part.Text, nil
	case hasContent:
		return toolResultText(part.Content)
	default:
		return "", nil
	}
}
