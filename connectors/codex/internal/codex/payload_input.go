package codex

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/connectors/codex/internal/jsonpresence"
	"github.com/aiproxer/aiproxer/connectors/codex/internal/responseitem"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type inputItem interface {
	inputItem()
}

type textMessageItem struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content string `json:"content"`
	phase   string
}

func (textMessageItem) inputItem() {}

type richMessageItem struct {
	Type    string         `json:"type"`
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
	phase   string
}

func (richMessageItem) inputItem() {}

type functionCallOutputItem struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

func (functionCallOutputItem) inputItem() {}

type functionCallItem struct {
	Type      string `json:"type"`
	ID        string `json:"id,omitempty"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func (functionCallItem) inputItem() {}

type opaqueResponseItem struct {
	raw json.RawMessage
}

func (opaqueResponseItem) inputItem() {}

func (i opaqueResponseItem) MarshalJSON() ([]byte, error) {
	if !json.Valid(i.raw) {
		return nil, fmt.Errorf("%s: invalid opaque response item", ID)
	}
	return append([]byte(nil), i.raw...), nil
}

type contentBlock interface {
	contentBlock()
}

type inputTextPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (inputTextPart) contentBlock() {}

type inputImagePart struct {
	Type     string `json:"type"`
	ImageURL string `json:"image_url"`
}

func (inputImagePart) contentBlock() {}

type inputFilePart struct {
	Type     string `json:"type"`
	FileData string `json:"file_data"`
	Filename string `json:"filename"`
}

func (inputFilePart) contentBlock() {}

// resolveCodexInstructions builds the Codex `instructions` field. The Codex Responses API
// rejects system-role items in `input`, so system messages from the conversation are folded
// into instructions (deduplicated against explicit instructions, e.g. the codex-client-compat
// bridge). Falls back to the default Codex instruction only when no system content is present.
func resolveCodexInstructions(call *aipapi.Call) string {
	instructions := joinInstructionText(call.Instructions)
	for _, sysText := range systemMessageTexts(call.Messages) {
		if sysText == "" || instructionHasBlock(instructions, sysText) {
			continue
		}
		if instructions != "" {
			instructions += "\n\n" + sysText
		} else {
			instructions = sysText
		}
	}
	if strings.TrimSpace(instructions) == "" {
		return defaultCodexInstruction
	}
	return instructions
}

// instructionHasBlock reports whether instructions already contains block as a complete
// \n\n-delimited block. Substring containment is intentionally NOT used: a short system
// message that happens to be a substring of a longer instruction block (e.g. "Be concise."
// within "Be concise and helpful.") must still be merged rather than silently dropped. Only
// an exact full-block match (e.g. the codex-client-compat bridge re-sent verbatim) is a dup.
func instructionHasBlock(instructions, block string) bool {
	if block == "" {
		return false
	}
	for part := range strings.SplitSeq(instructions, "\n\n") {
		if part == block {
			return true
		}
	}
	return false
}

func systemMessageTexts(msgs []aipapi.Message) []string {
	var out []string
	for _, m := range msgs {
		if m.Role != aipapi.RoleSystem {
			continue
		}
		var b strings.Builder
		for _, p := range m.Parts {
			if p.Kind != aipapi.PartText || strings.TrimSpace(p.Text) == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(p.Text)
		}
		if b.Len() > 0 {
			out = append(out, b.String())
		}
	}
	return out
}

func joinInstructionText(insts []aipapi.Message) string {
	var b strings.Builder
	for _, m := range insts {
		for _, p := range m.Parts {
			if p.Kind != aipapi.PartText {
				continue
			}
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(p.Text)
		}
	}
	return strings.TrimSpace(b.String())
}

func normalizeCompactionInputItems(items []inputItem) ([]inputItem, error) {
	out := cloneInputItems(items)
	for i, item := range out {
		opaque, ok := item.(opaqueResponseItem)
		if !ok {
			continue
		}
		var header struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(opaque.raw, &header) != nil {
			return nil, fmt.Errorf("%s: invalid exact replay item", ID)
		}
		var canon json.RawMessage
		var err error
		switch header.Type {
		case "reasoning":
			canon, err = responseitem.CanonizeReasoningItemForInput(opaque.raw)
		case "compaction_summary":
			canon, err = responseitem.CanonizeCompactionSummaryItemOpaque(opaque.raw)
		default:
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%s: invalid exact replay item", ID)
		}
		out[i] = opaqueResponseItem{raw: canon}
	}
	return out, nil
}

func buildInputItems(call *aipapi.Call) ([]inputItem, error) {
	if call.HasItemAuthority() {
		if err := call.Validate(); err != nil {
			return nil, fmt.Errorf("%s: invalid exact input: %w", ID, err)
		}
		return buildItemAuthorityInputItems(call.Items)
	}
	out := make([]inputItem, 0, len(call.Messages))
	hasTools := len(call.Tools) > 0
	for _, m := range call.Messages {
		if m.Role == aipapi.RoleSystem {
			continue
		}
		if m.Role == aipapi.RoleTool {
			for _, p := range m.Parts {
				if p.Kind != aipapi.PartToolResult {
					return nil, fmt.Errorf("%s: unsupported tool part kind %q", ID, p.Kind)
				}
				if !hasTools {
					// No-tools requests are a real client state, not malformed history:
					// OpenCode can resend prior tool transcripts while asking the model
					// to continue without exposing callable tools. Codex must see those
					// records as plain conversation text, because a function_call_output
					// without matching tools can stall the turn or make the model emit raw
					// tool protocol back to the user.
					out = append(out, textMessageItem{
						Type:    "message",
						Role:    "user",
						Content: noToolsToolResultText(p),
					})
					continue
				}
				out = append(out, functionCallOutputItem{
					Type:   "function_call_output",
					CallID: p.ToolCallID,
					Output: toolResultString(p),
				})
			}
			continue
		}
		if m.Role == aipapi.RoleAssistant && len(m.Parts) > 0 {
			items, ok, err := assistantFunctionCallItems(m.Parts, hasTools)
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, items...)
				continue
			}
		}
		item, err := messageToInputItem(m)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

func buildItemAuthorityInputItems(items []aipapi.Item) ([]inputItem, error) {
	out := make([]inputItem, 0, len(items))
	for i, item := range items {
		field := fmt.Sprintf("Items[%d]", i)
		switch item.Kind {
		case aipapi.ItemKindMessage:
			switch item.Role {
			case aipapi.RoleUser, aipapi.RoleDeveloper, aipapi.RoleAssistant:
			default:
				return nil, fmt.Errorf("%s: message role %q is not legal in Codex input items", field, item.Role)
			}
			content, err := canonicalContentBlocks(item.Content)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", field, err)
			}
			if len(content) == 1 {
				if text, ok := content[0].(inputTextPart); ok {
					out = append(out, textMessageItem{Type: "message", Role: roleString(item.Role), Content: text.Text, phase: string(item.Phase)})
					continue
				}
			}
			out = append(out, richMessageItem{Type: "message", Role: roleString(item.Role), Content: content, phase: string(item.Phase)})
		case aipapi.ItemKindReasoning:
			if item.Reasoning == nil || item.Reasoning.Reasoning == nil {
				return nil, fmt.Errorf("%s: reasoning payload is required", field)
			}
			if item.Reasoning.Reasoning.Dialect != aipapi.ReasoningDialectOpenAIResponsesItemV1 {
				return nil, fmt.Errorf("%s: unsupported reasoning input dialect %q", field, item.Reasoning.Reasoning.Dialect)
			}
			canon, err := responseitem.CanonizeReasoningItemOpaque(item.Reasoning.Reasoning.Opaque)
			if err != nil {
				return nil, fmt.Errorf("%s: invalid reasoning input item: %w", field, err)
			}
			out = append(out, opaqueResponseItem{raw: canon})
		case aipapi.ItemKindToolCall:
			if item.ToolCall == nil {
				return nil, fmt.Errorf("%s: tool call payload is required", field)
			}
			out = append(out, functionCallItem{
				Type: "function_call", ID: item.ID, CallID: item.ToolCall.CallID,
				Name: item.ToolCall.Name, Arguments: string(item.ToolCall.Arguments),
			})
		case aipapi.ItemKindToolResult:
			if item.ToolResult == nil {
				return nil, fmt.Errorf("%s: tool result payload is required", field)
			}
			out = append(out, functionCallOutputItem{Type: "function_call_output", CallID: item.ToolResult.CallID, Output: item.ToolResult.Output})
		case aipapi.ItemKindCompaction:
			if item.Compaction == nil {
				return nil, fmt.Errorf("%s: invalid compaction item", field)
			}
			raw := item.Compaction.Opaque
			if len(raw) == 0 {
				if strings.TrimSpace(item.Compaction.EncapsulatedID) == "" && strings.TrimSpace(item.Compaction.EncryptedContent) == "" {
					return nil, fmt.Errorf("%s: invalid compaction item", field)
				}
				raw, _ = json.Marshal(map[string]any{
					"type": "compaction", "id": item.Compaction.EncapsulatedID,
					"encrypted_content": item.Compaction.EncryptedContent,
				})
			}
			var header struct {
				Type string `json:"type"`
			}
			if !json.Valid(raw) || json.Unmarshal(raw, &header) != nil || header.Type != "compaction" {
				return nil, fmt.Errorf("%s: invalid compaction item", field)
			}
			out = append(out, opaqueResponseItem{raw: append(json.RawMessage(nil), raw...)})
		default:
			return nil, fmt.Errorf("%s: unsupported exact input item kind %q", field, item.Kind)
		}
	}
	return out, nil
}

func canonicalContentBlocks(parts []aipapi.ContentPart) ([]contentBlock, error) {
	out := make([]contentBlock, 0, len(parts))
	for _, p := range parts {
		switch p.Kind {
		case aipapi.ContentPartText:
			if p.Text != "" {
				out = append(out, inputTextPart{Type: "input_text", Text: p.Text})
			}
		case aipapi.ContentPartImageRef:
			out = append(out, inputImagePart{Type: "input_image", ImageURL: p.ImageRef})
		case aipapi.ContentPartFileRef:
			out = append(out, inputFilePart{Type: "input_file", FileData: p.FileData, Filename: p.FileName})
		default:
			return nil, fmt.Errorf("unsupported exact message content kind %q", p.Kind)
		}
	}
	return out, nil
}

func assistantFunctionCallItems(parts []aipapi.Part, hasTools bool) ([]inputItem, bool, error) {
	out := make([]inputItem, 0, len(parts))
	contentParts := make([]aipapi.Part, 0, len(parts))
	sawStructuredItem := false
	flushContent := func() error {
		if len(contentParts) == 0 {
			return nil
		}
		item, err := messageToInputItem(aipapi.Message{Role: aipapi.RoleAssistant, Parts: contentParts})
		if err != nil {
			return err
		}
		out = append(out, item)
		contentParts = contentParts[:0]
		return nil
	}
	for _, p := range parts {
		if item, ok, err := partToOpaqueResponseItem(p); err != nil {
			return nil, false, err
		} else if ok {
			if err := flushContent(); err != nil {
				return nil, false, err
			}
			sawStructuredItem = true
			out = append(out, item)
			continue
		}
		item, ok, err := partToFunctionCallItem(p)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			contentParts = append(contentParts, p)
			continue
		}
		if !hasTools {
			sawStructuredItem = true
			// Preserve the fact that a prior assistant action happened, but do not
			// send Codex a structured function_call when this request has no tool
			// schema. The structured form is reserved for tool-enabled turns where
			// the backend can safely continue the protocol.
			contentParts = append(contentParts, aipapi.TextPart(noToolsFunctionCallText(item)))
			continue
		}
		if err := flushContent(); err != nil {
			return nil, false, err
		}
		sawStructuredItem = true
		out = append(out, item)
	}
	if !sawStructuredItem {
		return nil, false, nil
	}
	if err := flushContent(); err != nil {
		return nil, false, err
	}
	return out, true, nil
}

func partToOpaqueResponseItem(p aipapi.Part) (inputItem, bool, error) {
	var raw json.RawMessage
	switch p.Kind {
	case aipapi.PartReasoning:
		if p.Reasoning == nil {
			return nil, false, newNativeHistoryBuildError("malformed_item")
		}
		if p.Reasoning.Dialect != aipapi.ReasoningDialectOpenAIResponsesItemV1 {
			return nil, false, newNativeHistoryBuildError("unsupported_dialect")
		}
		canon, err := responseitem.CanonizeReasoningItemOpaque(p.Reasoning.Opaque)
		if err != nil {
			return nil, false, newNativeHistoryBuildError("invalid_opaque")
		}
		raw = canon
	case aipapi.PartJSON:
		var header struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(p.Content, &header) != nil || header.Type != "compaction" {
			return nil, false, nil
		}
		raw = p.Content
	default:
		return nil, false, nil
	}
	if !json.Valid(raw) {
		return nil, false, newNativeHistoryBuildError("invalid_opaque")
	}
	return opaqueResponseItem{raw: append(json.RawMessage(nil), raw...)}, true, nil
}

func noToolsFunctionCallText(item inputItem) string {
	fc, ok := item.(functionCallItem)
	if !ok {
		return "Prior assistant tool call (tools unavailable in this request)."
	}
	var b strings.Builder
	b.WriteString("Prior assistant tool call (tools unavailable in this request).")
	if strings.TrimSpace(fc.CallID) != "" {
		b.WriteString(" call_id=")
		b.WriteString(strings.TrimSpace(fc.CallID))
		b.WriteByte('.')
	}
	if strings.TrimSpace(fc.Name) != "" {
		b.WriteString(" name=")
		b.WriteString(strings.TrimSpace(fc.Name))
		b.WriteByte('.')
	}
	if strings.TrimSpace(fc.Arguments) != "" {
		b.WriteString(" arguments=")
		b.WriteString(strings.TrimSpace(fc.Arguments))
	}
	return b.String()
}

func noToolsToolResultText(p aipapi.Part) string {
	var b strings.Builder
	b.WriteString("Prior tool output (tools unavailable in this request).")
	if id := strings.TrimSpace(p.ToolCallID); id != "" {
		b.WriteString(" call_id=")
		b.WriteString(id)
		b.WriteByte('.')
	}
	if len(p.Content) > 0 {
		b.WriteByte('\n')
		b.WriteString(toolResultDisplayText(p.Content))
	}
	return b.String()
}

func toolResultDisplayText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	}
	return string(raw)
}

func partToFunctionCallItem(p aipapi.Part) (inputItem, bool, error) {
	if p.Kind != aipapi.PartJSON || len(p.Content) == 0 {
		return nil, false, nil
	}
	var v struct {
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
	if err := json.Unmarshal(p.Content, &v); err != nil {
		return nil, false, nil
	}
	t := strings.TrimSpace(v.Type)
	// Accept Responses-style ("function_call" or empty) and Chat Completions-style
	// ("function") assistant tool calls. Any other concrete type is not a function call.
	if t != "" && t != "function_call" && t != "function" {
		return nil, false, nil
	}
	// Chat Completions carries the call id as "id" and the name/arguments under
	// "function"; Responses carries them at the top level as "call_id"/"name".
	callID := strings.TrimSpace(v.CallID)
	if callID == "" {
		callID = strings.TrimSpace(v.ID)
	}
	name := strings.TrimSpace(v.Name)
	args := v.Arguments
	if name == "" {
		name = strings.TrimSpace(v.Function.Name)
		args = v.Function.Arguments
	}
	if callID == "" || name == "" {
		return nil, false, newNativeHistoryBuildError("missing_call_id")
	}
	argStr := "{}"
	if jsonpresence.IsPresentNonNullJSON(args) {
		switch args[0] {
		case '"':
			var s string
			if err := json.Unmarshal(args, &s); err != nil {
				return nil, false, fmt.Errorf("%s: function_call arguments: %w", ID, err)
			}
			argStr = s
		default:
			argStr = string(args)
		}
	}
	item := functionCallItem{
		Type:      "function_call",
		CallID:    callID,
		Name:      name,
		Arguments: argStr,
	}
	// Preserve the Responses-style item id only when it is distinct from the call_id
	// (i.e. a separate call_id was supplied). For Chat Completions the id IS the call
	// id, so do not duplicate it as the item id.
	if strings.TrimSpace(v.ID) != "" && strings.TrimSpace(v.CallID) != "" {
		item.ID = strings.TrimSpace(v.ID)
	}
	return item, true, nil
}

func toolResultString(p aipapi.Part) string {
	output := ""
	if len(p.Content) > 0 {
		output = string(p.Content)
	}
	outputRaw, err := json.Marshal(output)
	if err != nil {
		return output
	}
	payload := map[string]json.RawMessage{"output": outputRaw}
	var existing map[string]json.RawMessage
	if json.Unmarshal(p.Content, &existing) == nil {
		if raw, ok := existing["exit_code"]; ok {
			payload["exit_code"] = raw
		}
		if raw, ok := existing["workdir"]; ok {
			payload["workdir"] = raw
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return output
	}
	return string(raw)
}

func messageToInputItem(m aipapi.Message) (inputItem, error) {
	role := roleString(m.Role)
	if len(m.Parts) == 1 && m.Parts[0].Kind == aipapi.PartText {
		return textMessageItem{
			Type:    "message",
			Role:    role,
			Content: m.Parts[0].Text,
		}, nil
	}
	content, err := partsToContentList(m.Parts)
	if err != nil {
		return nil, err
	}
	return richMessageItem{
		Type:    "message",
		Role:    role,
		Content: content,
	}, nil
}

func roleString(r aipapi.Role) string {
	switch r {
	case aipapi.RoleUser:
		return "user"
	case aipapi.RoleDeveloper:
		return "developer"
	case aipapi.RoleAssistant:
		return "assistant"
	case aipapi.RoleSystem:
		return "system"
	default:
		return "user"
	}
}

func partsToContentList(parts []aipapi.Part) ([]contentBlock, error) {
	out := make([]contentBlock, 0, len(parts))
	for _, p := range parts {
		switch p.Kind {
		case aipapi.PartText:
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			out = append(out, inputTextPart{Type: "input_text", Text: p.Text})
		case aipapi.PartImageRef:
			out = append(out, inputImagePart{
				Type:     "input_image",
				ImageURL: p.ImageRef,
			})
		case aipapi.PartFileRef:
			b64, fname, err := fileDataFromPart(p)
			if err != nil {
				return nil, err
			}
			out = append(out, inputFilePart{
				Type:     "input_file",
				FileData: b64,
				Filename: fname,
			})
		default:
			return nil, fmt.Errorf("%s: unsupported part kind %q", ID, p.Kind)
		}
	}
	return out, nil
}

func fileDataFromPart(p aipapi.Part) (dataB64, filename string, err error) {
	filename = strings.TrimSpace(p.FileName)
	ref := p.FileRef
	if strings.HasPrefix(ref, "data:") {
		_, b64, ok := stripDataURLBase64(ref)
		if !ok {
			return "", "", fmt.Errorf("%s: invalid data URL in file part", ID)
		}
		return b64, filename, nil
	}
	return "", "", fmt.Errorf("%s: file part requires a data URL", ID)
}

func stripDataURLBase64(dataURL string) (mime, b64 string, ok bool) {
	rest, ok := strings.CutPrefix(dataURL, "data:")
	if !ok {
		return "", "", false
	}
	mime, enc, found := strings.Cut(rest, ";")
	if !found {
		return "", "", false
	}
	const prefix = "base64,"
	encBody, ok := strings.CutPrefix(enc, prefix)
	if !ok {
		return "", "", false
	}
	return mime, encBody, true
}
