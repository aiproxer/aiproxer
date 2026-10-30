package proxymemory

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// minTranscriptBytes is the encoded length of "[]", the smallest representable
// transcript. A budget below it is treated as this value (Requirement 3.6).
const minTranscriptBytes = 2

// truncationMarkerAllowance bounds the byte length of the TruncateTokens marker
// for any field below the 64 MiB transcript cap: 27 bytes plus at most eight
// decimal digits, with headroom.
const truncationMarkerAllowance = 48

// Transcript entry type discriminators (design §5).
const (
	entryTypeMessage            = "message"
	entryTypeFunctionCall       = "function_call"
	entryTypeFunctionCallOutput = "function_call_output"
)

// Omission placeholders replacing non-text content in transcripts (design §5).
const (
	imageOmitted = "[image omitted]"
	fileOmitted  = "[file omitted]"
	videoOmitted = "[video omitted]"
)

// entryTextSeparator joins the rendered pieces of one message.
const entryTextSeparator = "\n"

// Entry is one rendered transcript line (design §5): a user or assistant
// message, a tool call, or a tool output.
type Entry struct {
	Type      string `json:"type"`                // "message" | "function_call" | "function_call_output"
	Role      string `json:"role,omitempty"`      // "user" | "assistant" (message only)
	Text      string `json:"text,omitempty"`      // message text; parts joined with "\n"
	Name      string `json:"name,omitempty"`      // tool name (call/output)
	CallID    string `json:"call_id,omitempty"`   // tool call identifier (call/output)
	Arguments string `json:"arguments,omitempty"` // tool call arguments (raw JSON text)
	Output    string `json:"output,omitempty"`    // tool output text
}

// RenderHistory renders the transcript entries of one request history
// (design §5, Requirements 3.1 and 3.2). It never mutates call.
//
// Message authority renders call.Messages in order; item authority renders
// call.Items. Instructions, system and developer content, reasoning, and
// compaction, extension and item-reference items are skipped entirely. User
// AGENTS.md instruction and skill fragments are dropped, and a message left
// with no text and no tool entries is not emitted.
func RenderHistory(call *aipapi.Call) []Entry {
	if call == nil {
		return nil
	}
	var entries []Entry
	for _, msg := range call.Messages {
		entries = append(entries, renderMessage(msg.Role, msg.Parts)...)
	}
	if call.HasItemAuthority() {
		for _, item := range call.Items {
			entries = append(entries, renderItem(item)...)
		}
	}
	return entries
}

// renderMessage renders one legacy message. Only user, assistant and tool
// messages contribute entries.
func renderMessage(role aipapi.Role, parts []aipapi.Part) []Entry {
	if role == aipapi.RoleTool {
		var outputs []Entry
		for _, part := range parts {
			if part.Kind == aipapi.PartToolResult {
				outputs = append(outputs, toolResultEntry(part.ToolCallID, part.ToolName, part.Content, part.Text))
			}
		}
		return outputs
	}
	if role != aipapi.RoleUser && role != aipapi.RoleAssistant {
		return nil
	}
	userRole := role == aipapi.RoleUser
	var pieces []string
	var toolEntries []Entry
	for _, part := range parts {
		switch part.Kind {
		case aipapi.PartText:
			if part.Text == "" || (userRole && isInstructionFragment(part.Text)) {
				continue
			}
			pieces = append(pieces, part.Text)
		case aipapi.PartImageRef:
			pieces = append(pieces, imageOmitted)
		case aipapi.PartFileRef:
			pieces = append(pieces, fileOmitted)
		case aipapi.PartJSON:
			if part.ToolCallID != "" {
				toolEntries = append(toolEntries, Entry{
					Type:      entryTypeFunctionCall,
					Name:      part.ToolName,
					CallID:    part.ToolCallID,
					Arguments: string(part.Content),
				})
			}
		case aipapi.PartToolResult:
			toolEntries = append(toolEntries, toolResultEntry(part.ToolCallID, part.ToolName, part.Content, part.Text))
		}
	}
	var entries []Entry
	if len(pieces) > 0 {
		entries = append(entries, Entry{
			Type: entryTypeMessage,
			Role: string(role),
			Text: strings.Join(pieces, entryTextSeparator),
		})
	}
	return append(entries, toolEntries...)
}

// renderItem renders one canonical item. Only message items with a user or
// assistant role, tool_call items and tool_result items contribute entries.
func renderItem(item aipapi.Item) []Entry {
	switch item.Kind {
	case aipapi.ItemKindMessage:
		return renderItemMessage(item)
	case aipapi.ItemKindToolCall:
		if item.ToolCall == nil {
			return nil
		}
		return []Entry{{
			Type:      entryTypeFunctionCall,
			Name:      item.ToolCall.Name,
			CallID:    item.ToolCall.CallID,
			Arguments: string(item.ToolCall.Arguments),
		}}
	case aipapi.ItemKindToolResult:
		if item.ToolResult == nil {
			return nil
		}
		output := item.ToolResult.Output
		if output == "" {
			output = toolResultPartsText(item.ToolResult.Parts)
		}
		return []Entry{{
			Type:   entryTypeFunctionCallOutput,
			Name:   item.ToolResult.Name,
			CallID: item.ToolResult.CallID,
			Output: output,
		}}
	default:
		// Reasoning, compaction, extension and item-reference items are
		// skipped entirely (design §5).
		return nil
	}
}

func renderItemMessage(item aipapi.Item) []Entry {
	if item.Role != aipapi.RoleUser && item.Role != aipapi.RoleAssistant {
		return nil
	}
	userRole := item.Role == aipapi.RoleUser
	var pieces []string
	for _, part := range item.Content {
		switch part.Kind {
		case aipapi.ContentPartText:
			if part.Text == "" || (userRole && isInstructionFragment(part.Text)) {
				continue
			}
			pieces = append(pieces, part.Text)
		case aipapi.ContentPartImageRef:
			pieces = append(pieces, imageOmitted)
		case aipapi.ContentPartFileRef:
			pieces = append(pieces, fileOmitted)
		case aipapi.ContentPartVideoRef:
			pieces = append(pieces, videoOmitted)
		}
	}
	if len(pieces) == 0 {
		return nil
	}
	return []Entry{{
		Type: entryTypeMessage,
		Role: string(item.Role),
		Text: strings.Join(pieces, entryTextSeparator),
	}}
}

// toolResultEntry renders a legacy tool result part. Output is the JSON string
// value when Content is a JSON string, else the raw content text (design §5).
// A part that carries its payload only in Text (the plain-text canonical
// carrier used by Anthropic-shaped tool results) falls back to that text, so a
// captured tool output is never dropped solely because of its carrier.
func toolResultEntry(callID, name string, content json.RawMessage, text string) Entry {
	return Entry{
		Type:   entryTypeFunctionCallOutput,
		Name:   name,
		CallID: callID,
		Output: toolResultOutput(content, text),
	}
}

// toolResultOutput renders tool result content as the output text. When the
// content decodes to a JSON string — after any amount of legal JSON whitespace
// — the string value is rendered without its quotes or escapes; every other
// value keeps the raw JSON text (design §5). A part that carries its payload
// only in Text (the plain-text canonical carrier used by Anthropic-shaped tool
// results) falls back to that text, so a captured tool output is never dropped
// solely because of its carrier. An explicitly empty JSON string stays
// authoritative over Text.
func toolResultOutput(content json.RawMessage, text string) string {
	if len(content) > 0 {
		trimmed := bytes.TrimLeft(content, " \t\r\n")
		if len(trimmed) > 0 {
			// Decoding into any distinguishes a JSON string from null and the
			// other value kinds: unmarshalling null into a string succeeds
			// without touching it and would silently fabricate an empty output.
			var decoded any
			if err := json.Unmarshal(trimmed, &decoded); err == nil {
				if value, ok := decoded.(string); ok {
					return value
				}
			}
		}
		return string(content)
	}
	return text
}

// toolResultPartsText renders the text of tool result content parts, joined
// with a newline (design §5).
func toolResultPartsText(parts []aipapi.ContentPart) string {
	var pieces []string
	for _, part := range parts {
		if part.Kind == aipapi.ContentPartText && part.Text != "" {
			pieces = append(pieces, part.Text)
		}
	}
	return strings.Join(pieces, entryTextSeparator)
}

// isInstructionFragment reports whether text is a user instruction fragment
// (an AGENTS.md instructions block or a skill block) that is dropped from
// transcripts (design §5, Requirement 3.2). The trimmed text must start with
// the opening marker and end with the closing marker, compared ASCII
// case-insensitively.
func isInstructionFragment(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	lowered := asciiLower(trimmed)
	if strings.HasPrefix(lowered, "# agents.md instructions") && strings.HasSuffix(lowered, "</instructions>") {
		return true
	}
	return strings.HasPrefix(lowered, "<skill>") && strings.HasSuffix(lowered, "</skill>")
}

// asciiLower lowercases only ASCII letters, leaving non-ASCII bytes untouched.
func asciiLower(s string) string {
	var out []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' {
			if out == nil {
				out = []byte(s)
			}
			out[i] = c + ('a' - 'A')
		}
	}
	if out == nil {
		return s
	}
	return string(out)
}

// AssistantAccumulator collects the released assistant output of one attempt
// stream (design §5): streamed text deltas become one assistant message, and
// tool calls become function_call entries in first-seen order.
type AssistantAccumulator struct {
	text  strings.Builder
	calls []*accumulatedCall
	index map[string]*accumulatedCall
}

// accumulatedCall is one tool call being accumulated.
type accumulatedCall struct {
	callID    string
	name      string
	arguments strings.Builder
}

// Observe consumes one canonical stream event. Text deltas append to the
// assistant text, tool call events accumulate one call, and an EventItem
// carrying a complete tool_call completes the call it names. Every other kind,
// including reasoning, is ignored.
func (a *AssistantAccumulator) Observe(ev aipapi.Event) {
	switch ev.Kind {
	case aipapi.EventTextDelta:
		a.text.WriteString(ev.Delta)
	case aipapi.EventToolCallStarted:
		if strings.TrimSpace(ev.ToolCallID) == "" {
			return
		}
		// A started event can arrive after argument deltas already opened the
		// call (the canonical collector accepts that ordering, and calls the
		// name in this window the late name). Apply the name without resetting
		// the accumulated arguments or the first-seen position; a blank name
		// never becomes or blanks the call's name, matching the collector.
		call := a.openCall(ev.ToolCallID, "")
		if strings.TrimSpace(ev.ToolName) != "" {
			call.name = ev.ToolName
		}
	case aipapi.EventToolCallArgsDelta:
		if strings.TrimSpace(ev.ToolCallID) == "" {
			return
		}
		a.openCall(ev.ToolCallID, "").arguments.WriteString(ev.Delta)
	case aipapi.EventItem:
		if ev.Item == nil || ev.Item.Kind != aipapi.ItemKindToolCall || ev.Item.ToolCall == nil {
			return
		}
		if strings.TrimSpace(ev.Item.ToolCall.CallID) == "" {
			return
		}
		call := a.openCall(ev.Item.ToolCall.CallID, ev.Item.ToolCall.Name)
		call.name = ev.Item.ToolCall.Name
		call.arguments.Reset()
		call.arguments.Write(ev.Item.ToolCall.Arguments)
	}
}

// openCall returns the accumulated call for callID, opening one in first-seen
// order when it is new. An argument delta for an unknown call opens it, the
// same way the canonical event collector orders unseen tool call ids.
func (a *AssistantAccumulator) openCall(callID, name string) *accumulatedCall {
	if call, ok := a.index[callID]; ok {
		return call
	}
	if a.index == nil {
		a.index = make(map[string]*accumulatedCall)
	}
	call := &accumulatedCall{callID: callID, name: name}
	a.index[callID] = call
	a.calls = append(a.calls, call)
	return call
}

// Entries returns one assistant message entry (when the streamed text is
// non-empty) followed by the function_call entries in first-seen order. It
// does not consume the accumulator.
func (a *AssistantAccumulator) Entries() []Entry {
	var entries []Entry
	if a.text.Len() > 0 {
		entries = append(entries, Entry{
			Type: entryTypeMessage,
			Role: string(aipapi.RoleAssistant),
			Text: a.text.String(),
		})
	}
	for _, call := range a.calls {
		entries = append(entries, Entry{
			Type:      entryTypeFunctionCall,
			Name:      call.name,
			CallID:    call.callID,
			Arguments: call.arguments.String(),
		})
	}
	return entries
}

// EncodeTranscript renders entries as the compact JSON array stored with a
// captured session (design §5, Requirements 3.3 and 3.6): every string field
// is redacted and stripped of NUL bytes, and when the array exceeds maxBytes
// the oldest entries are dropped first, then a single remaining oversized
// entry has its longest content field truncated with TruncateTokens. A final
// encoded-byte fit (see fitEncodedTranscript) then enforces the Requirement
// 3.6 hard bound even when JSON escaping expands the surviving raw strings.
// That fit re-sanitizes every pass and measures the bound after the
// redaction, because its truncation cuts and marker insertions are themselves
// byte-changing transformations and can create new secret-like shapes from
// values the initial pass saw as clean. The result is always a valid JSON
// array; a budget below minTranscriptBytes (the empty array) is treated as
// that value.
func EncodeTranscript(entries []Entry, maxBytes int) (string, bool, error) {
	if maxBytes < minTranscriptBytes {
		maxBytes = minTranscriptBytes
	}
	cleaned := make([]Entry, len(entries))
	for i, entry := range entries {
		cleaned[i] = sanitizeEntry(entry)
	}
	data, err := json.Marshal(cleaned)
	if err != nil {
		return "[]", false, err
	}
	if len(data) <= maxBytes {
		return string(data), false, nil
	}
	transcript, err := fitTranscript(cleaned, maxBytes)
	if err != nil {
		return "[]", true, err
	}
	return transcript, true, nil
}

// sanitizeEntry applies the §5 encoding step 1 to every string field.
func sanitizeEntry(entry Entry) Entry {
	entry.Type = cleanTranscriptString(entry.Type)
	entry.Role = cleanTranscriptString(entry.Role)
	entry.Text = cleanTranscriptString(entry.Text)
	entry.Name = cleanTranscriptString(entry.Name)
	entry.CallID = cleanTranscriptString(entry.CallID)
	entry.Arguments = cleanTranscriptString(entry.Arguments)
	entry.Output = cleanTranscriptString(entry.Output)
	return entry
}

// sanitizeEntries applies sanitizeEntry to every entry. fitEncodedTranscript
// calls it at the top of every pass so the marshal it evaluates, and every
// value it can return, encodes post-Redact bytes: §4 matching is not preserved
// by byte edits, and the fit's own truncation cuts and marker insertions can
// create a shape no earlier redaction pass saw (Requirement 3.3).
func sanitizeEntries(entries []Entry) []Entry {
	for i := range entries {
		entries[i] = sanitizeEntry(entries[i])
	}
	return entries
}

// cleanTranscriptString applies the §5 encoding step 1 in the safe order: NUL
// bytes are removed first, then secret-like values are redacted. Redact must
// be the last byte-changing transformation on every value before the marshal
// that returns it, and the encoded bound must be measured after it: removing
// NULs after redaction would be an ordering bypass (a secret split by a NUL
// escapes the patterns as two short halves, and the strip would splice the
// stored transcript back into the exact secret shape Redact recognizes), and
// the later truncation steps cut bytes and insert the truncation marker, so
// the final fit re-sanitizes each candidate before it is measured
// (Requirement 3.3; VISION no-leakage).
func cleanTranscriptString(s string) string {
	if s == "" {
		return s
	}
	s = strings.ReplaceAll(s, "\x00", "")
	return Redact(s)
}

// fitTranscript applies the design §5 steps 3 and 4 — drop the oldest entries
// until the array fits, then truncate the longest content field of a single
// oversized remaining entry — and hands the result to the final encoded-byte
// fit.
func fitTranscript(entries []Entry, maxBytes int) (string, error) {
	sizes := make([]int, len(entries))
	total := 2 // "[" and "]"
	if len(entries) > 0 {
		total += len(entries) - 1 // separating commas
	}
	for i, entry := range entries {
		data, err := json.Marshal(entry)
		if err != nil {
			return "", err
		}
		sizes[i] = len(data)
		total += sizes[i]
	}
	start := 0
	for start < len(entries)-1 && total > maxBytes {
		total -= sizes[start] + 1
		start++
	}
	if total > maxBytes {
		// The drop loop keeps at least one entry, so this is the single
		// remaining oversized entry of design §5 step 4.
		entries = []Entry{truncateLongestContentField(entries[start], maxBytes/8)}
	} else {
		entries = entries[start:]
	}
	return fitEncodedTranscript(entries, maxBytes)
}

// fitEncodedTranscript enforces the Requirement 3.6 encoded-byte bound on the
// candidate transcript produced by the first pass, design §5 steps 1-4
// (EncodeTranscript's redaction, NUL removal and initial marshal, then
// fitTranscript's oldest-first dropping and one-field truncation). The first
// pass alone cannot guarantee the bound: json.Marshal escapes control and HTML
// characters ("\u003c", "\t"), so a surviving raw string can encode to more
// than maxBytes even after the one-field token truncation. This final fit is
// the controller-recorded design §5 clarification for that gap.
//
// Every pass starts by re-sanitizing the candidate entries, because this loop
// is itself a byte-changing transformation: dropping preserves the remaining
// bytes, but the one-field truncation and every reduction cut bytes and insert
// the marker, and a cut can remove the glue byte or supply the word boundary
// that makes a previously unmatchable secret-like run match §4. The bound is
// then measured on the post-Redact encoding, so an assignment redaction counts
// only after its replacement bytes are in.
//
// While the encoded form exceeds maxBytes it drops the oldest entry (while
// more than one remains), then repeatedly shrinks the single remaining entry's
// longest content field, re-measuring after every reduction. Each reduction
// strictly shrinks the selected field's raw bytes (a drop sheds an entry) and
// a re-sanitization only rewrites fields into their bounded, idempotent Redact
// fixpoints, so the measured repeat converges rather than oscillating; a
// single reduction need not shed the whole excess (the selected field can be
// shorter than it) and maxPasses remains a hard guard far above the pass count
// any input needs. An exhausted guard still returns the empty array, the
// smallest valid transcript. When even the minimal single entry cannot fit,
// the empty array is returned: the Requirement 3.6 bound wins over retention.
// The result is always valid JSON, and every stored value was sanitized in the
// pass that marshaled and returned it. EncodeTranscript floors maxBytes at
// minTranscriptBytes before calling this.
func fitEncodedTranscript(entries []Entry, maxBytes int) (string, error) {
	maxPasses := len(entries) + 64
	for pass := 0; ; pass++ {
		entries = sanitizeEntries(entries)
		data, err := json.Marshal(entries)
		if err != nil {
			return "", err
		}
		if len(data) <= maxBytes {
			return string(data), nil
		}
		if pass >= maxPasses {
			return "[]", nil
		}
		if len(entries) == 0 {
			return "[]", nil
		}
		if len(entries) > 1 {
			entries = entries[1:]
			continue
		}
		reduced, ok := reduceEntryContent(entries[0], len(data)-maxBytes)
		if !ok {
			return "[]", nil
		}
		entries[0] = reduced
	}
}

// reduceEntryContent shrinks the longest non-empty content field of entry by
// roughly excess bytes when the field is long enough to absorb the
// TruncateTokens marker — the replacement keeps a head and a tail around that
// marker — and empties a field too short for the marker instead. A single
// reduction need not shed the whole excess: the selected field can be shorter
// than it. Correctness therefore rests on the caller re-measuring and
// repeating until the encoded form fits (fitEncodedTranscript), not on any
// single step. Type and Role stay intact: they are fixed discriminators and
// truncating them would falsify the entry's shape. It reports false when no
// content field is left to shrink, which means only the empty array can
// satisfy the budget.
func reduceEntryContent(entry Entry, excess int) (Entry, bool) {
	fields := []*string{&entry.Text, &entry.Name, &entry.CallID, &entry.Arguments, &entry.Output}
	var longest *string
	for _, field := range fields {
		if *field == "" {
			continue
		}
		if longest == nil || len(*field) > len(*longest) {
			longest = field
		}
	}
	if longest == nil {
		return entry, false
	}
	target := max(len(*longest)-excess, 0)
	limit := max((target-truncationMarkerAllowance)/4, 0)
	reduced := TruncateTokens(*longest, limit)
	if len(reduced) >= len(*longest) {
		// The marker alone does not fit inside a short field; empty the field
		// so every pass strictly shrinks the entry.
		reduced = ""
	}
	*longest = reduced
	return entry, true
}

// truncateLongestContentField shortens the longest of entry's content string
// fields with TruncateTokens. Type and Role are fixed discriminators and stay
// intact; truncating them would falsify the entry's shape.
func truncateLongestContentField(entry Entry, limit int) Entry {
	fields := []*string{&entry.Text, &entry.Name, &entry.CallID, &entry.Arguments, &entry.Output}
	longest := fields[0]
	for _, field := range fields[1:] {
		if len(*field) > len(*longest) {
			longest = field
		}
	}
	if *longest != "" {
		*longest = TruncateTokens(*longest, limit)
	}
	return entry
}
