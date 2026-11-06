package bridge

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

// Continuation error codes. They are stable, bounded identifiers surfaced to the
// caller as a typed terminal; a later request can distinguish an expired or
// already consumed pending call from a tampered one.
const (
	CodeContinuationMismatch         = "continuation_mismatch"
	CodeAmbiguousContinuation        = "ambiguous_continuation"
	CodeContinuationExpired          = "continuation_expired"
	CodeContinuationConsumed         = "continuation_consumed"
	CodeContinuationFailed           = "continuation_failed"
	CodeContinuationPending          = "continuation_pending"
	CodeUnsupportedParallelToolCalls = "unsupported_parallel_tool_calls"
	CodeConversationBusy             = "conversation_busy"
	CodeRuntimeLimit                 = "runtime_limit"
	// CodeHistoryUnavailable reports that the request cannot prove the base
	// prefix: it carries an unresolved item reference, or a previous-response
	// marker without the materialized items that describe the parent trajectory.
	CodeHistoryUnavailable = "history_unavailable"
)

// ErrNoPending reports that no live pending tool call matches a request, so the
// request is a fresh conversation turn rather than a continuation.
var ErrNoPending = errors.New("claudeagentmodel: no pending tool result")

// ContinuationError is a typed refusal to release a parked native invocation.
type ContinuationError struct {
	Code   string
	Detail string
}

func (e *ContinuationError) Error() string {
	if e == nil {
		return "claudeagentmodel: continuation refused"
	}
	detail := boundedText(e.Detail)
	if detail == "" {
		return "claudeagentmodel: " + e.Code
	}
	return "claudeagentmodel: " + e.Code + ": " + detail
}

func continuationError(code, format string, args ...any) *ContinuationError {
	return &ContinuationError{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// ToolResult is one caller-supplied tool result from the next HTTP request. The
// connector never synthesizes one: Text and JSON are the caller's exact bytes.
type ToolResult struct {
	CallID string
	Name   string
	Text   string
	JSON   []byte
	Parts  []aipapi.ContentPart
}

// Content renders the caller's exact result for the deferred MCP handler. Text
// and structured JSON keep their original bytes; multimodal results require an
// explicit capability decision and are refused here instead of flattened.
func (r ToolResult) Content() (string, error) {
	if len(r.JSON) > 0 {
		trimmed := bytes.TrimSpace(r.JSON)
		var text string
		if err := json.Unmarshal(trimmed, &text); err == nil {
			return text, nil
		}
		canonical, err := canonicalJSON(trimmed)
		if err != nil {
			return "", continuationError(CodeContinuationMismatch, "the tool result is not valid JSON")
		}
		return string(canonical), nil
	}
	if len(r.Parts) > 0 {
		var b strings.Builder
		for _, part := range r.Parts {
			if part.Kind != aipapi.ContentPartText {
				return "", fmt.Errorf("%w: multimodal tool results are not supported by this connector slice", aipapi.ErrCapabilityReject)
			}
			b.WriteString(part.Text)
		}
		return b.String(), nil
	}
	return r.Text, nil
}

// ToolResults extracts the caller tool results in source order from either the
// legacy message channel or an item-authoritative trajectory.
func ToolResults(call aipapi.Call) []ToolResult {
	var out []ToolResult
	if call.HasItemAuthority() {
		for _, item := range call.Items {
			if item.Kind != aipapi.ItemKindToolResult || item.ToolResult == nil {
				continue
			}
			out = append(out, ToolResult{
				CallID: strings.TrimSpace(item.ToolResult.CallID),
				Name:   item.ToolResult.Name,
				Text:   item.ToolResult.Output,
				Parts:  append([]aipapi.ContentPart(nil), item.ToolResult.Parts...),
			})
		}
		return out
	}
	for _, message := range call.Messages {
		for _, part := range message.Parts {
			if part.Kind != aipapi.PartToolResult {
				continue
			}
			out = append(out, ToolResult{
				CallID: strings.TrimSpace(part.ToolCallID),
				Name:   part.ToolName,
				Text:   part.Text,
				JSON:   append([]byte(nil), part.Content...),
			})
		}
	}
	return out
}

// VisiblePrefixDigest digests the ordered client-visible transcript of one call.
// It is the "before" state for a turn that may emit a tool call.
func VisiblePrefixDigest(call aipapi.Call) ([32]byte, error) {
	if call.HasItemAuthority() {
		var out [32]byte
		for _, item := range call.Items {
			next, err := chainItem(out, item)
			if err != nil {
				return [32]byte{}, err
			}
			out = next
		}
		return out, nil
	}
	var out [32]byte
	for _, message := range call.Instructions {
		next, err := chainInstruction(out, message)
		if err != nil {
			return [32]byte{}, err
		}
		out = next
	}
	for _, message := range call.Messages {
		next, err := chainMessage(out, message)
		if err != nil {
			return [32]byte{}, err
		}
		out = next
	}
	return out, nil
}

// VisiblePrefixThroughCall digests the ordered client-visible transcript up to
// and including the assistant tool call with callID. It reports false when the
// transcript does not carry that call in a shape this connector can correlate,
// which is a reordered, omitted or unrepresentable history.
func VisiblePrefixThroughCall(call aipapi.Call, callID string) ([32]byte, bool, error) {
	if call.HasItemAuthority() {
		return visibleItemPrefixThroughCall(call, callID)
	}
	messageIndex, partIndex, unique := singleCallOccurrence(call, callID)
	if !unique {
		// Zero or duplicate occurrences never establish a release authority: a
		// transcript that carries the same opaque call twice can move visible
		// content between the occurrences.
		return [32]byte{}, false, nil
	}
	if call.Messages[messageIndex].Role != aipapi.RoleAssistant {
		return [32]byte{}, false, nil
	}
	var out [32]byte
	for _, message := range call.Instructions {
		next, err := chainInstruction(out, message)
		if err != nil {
			return [32]byte{}, false, err
		}
		out = next
	}
	for _, message := range call.Messages[:messageIndex] {
		next, err := chainMessage(out, message)
		if err != nil {
			return [32]byte{}, false, err
		}
		out = next
	}
	var text strings.Builder
	for _, part := range call.Messages[messageIndex].Parts[:partIndex] {
		if part.Kind != aipapi.PartText {
			return [32]byte{}, false, nil
		}
		text.WriteString(part.Text)
	}
	if text.Len() > 0 {
		next, err := chainText(out, text.String())
		if err != nil {
			return [32]byte{}, false, err
		}
		out = next
	}
	callPart := call.Messages[messageIndex].Parts[partIndex]
	callID, name, args := ToolCallFields(callPart)
	out, err := chainToolCall(out, callID, name, args)
	if err != nil {
		return [32]byte{}, false, err
	}
	return out, true, nil
}

// HasItemReferences reports whether an item-authoritative trajectory carries an
// unresolved item reference. A reference stands for content this connector
// cannot materialize, so it can never prove a client-visible prefix.
func HasItemReferences(call aipapi.Call) bool {
	if !call.HasItemAuthority() {
		return false
	}
	for _, item := range call.Items {
		if item.Kind == aipapi.ItemKindItemReference {
			return true
		}
	}
	return false
}

// visibleItemPrefixThroughCall digests an item trajectory up to and including
// the one tool-call item that carries callID. Item references are refused
// because their target content is not available to the connector.
func visibleItemPrefixThroughCall(call aipapi.Call, callID string) ([32]byte, bool, error) {
	if HasItemReferences(call) {
		return [32]byte{}, false, continuationError(CodeHistoryUnavailable, "the request contains an unresolved item reference")
	}
	if strings.TrimSpace(callID) == "" {
		return [32]byte{}, false, nil
	}
	callIndex := -1
	for i, item := range call.Items {
		if item.Kind != aipapi.ItemKindToolCall || item.ToolCall == nil || item.ToolCall.CallID != callID {
			continue
		}
		if callIndex >= 0 {
			// Two occurrences of the same opaque call id cannot establish one
			// authority; visible content could move between them.
			return [32]byte{}, false, nil
		}
		callIndex = i
	}
	if callIndex < 0 {
		return [32]byte{}, false, nil
	}
	var prefix [32]byte
	for _, item := range call.Items[:callIndex] {
		next, err := chainItem(prefix, item)
		if err != nil {
			return [32]byte{}, false, err
		}
		prefix = next
	}
	item := call.Items[callIndex]
	out, err := chainToolCall(prefix, item.ToolCall.CallID, item.ToolCall.Name, item.ToolCall.Arguments)
	if err != nil {
		return [32]byte{}, false, err
	}
	return out, true, nil
}

// extendItemPrefixWithCall appends the client-visible item representation of an
// emitted assistant turn: an assistant message item carrying any text the client
// saw, followed by the tool-call item. Legacy ExtendPrefixWithCall hashes the
// text inline because Chat and Anthropic keep it in the same assistant message;
// Responses item authority carries it as a separate message item.
func extendItemPrefixWithCall(prefix [32]byte, text, callID, name string, args []byte) ([32]byte, error) {
	out := prefix
	if text != "" {
		var err error
		out, err = chainItem(out, aipapi.Item{
			Kind:    aipapi.ItemKindMessage,
			Role:    aipapi.RoleAssistant,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: text}},
		})
		if err != nil {
			return [32]byte{}, err
		}
	}
	return chainToolCall(out, callID, name, args)
}

// argsDigest binds one exact canonical argument document to a pending call. It
// is a second, independent check for the name-only form, where the client wire
// has no opaque call id.
func argsDigest(args []byte) ([32]byte, error) {
	canonical, err := canonicalToolArguments(args)
	if err != nil {
		return [32]byte{}, continuationError(CodeContinuationMismatch, "the tool arguments are not valid JSON")
	}
	return sha256.Sum256(canonical), nil
}

// canonicalToolArguments normalizes the canonical tool-call argument value. Some
// frontends preserve the wire's JSON-string encoding of an argument document
// (OpenAI Responses function_call.arguments); others decode it to the object.
// Both describe the same exact call, so the correlation fingerprint uses the
// inner document. A bare non-document string keeps its original value.
func canonicalToolArguments(raw []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return []byte("{}"), nil
	}
	if trimmed[0] == '"' {
		var encoded string
		if err := json.Unmarshal(trimmed, &encoded); err == nil {
			inner := bytes.TrimSpace([]byte(encoded))
			if len(inner) == 0 {
				return canonicalJSON([]byte("{}"))
			}
			if json.Valid(inner) {
				return canonicalJSON(inner)
			}
		}
	}
	return canonicalJSON(trimmed)
}

// ContinuationProof is the validated exact-ID/user-item identity of one
// continuation request. It is computed from the caller's visible history before
// any release.
type ContinuationProof struct {
	Prefix     [32]byte
	ArgsDigest [32]byte
}

// NameProof is the selected name-only call occurrence plus the exact ordered
// prefix and argument digest that prove it. Name is never an opaque native id.
type NameProof struct {
	Name       string
	Prefix     [32]byte
	ArgsDigest [32]byte
}

// MatchPendingContinuation proves that call's visible history reproduces the
// parked pending call and carries the matching result. It never mutates the
// pending entry or the session state.
func MatchPendingContinuation(call aipapi.Call, pending *PendingCall, result ToolResult) (ContinuationProof, error) {
	if pending == nil {
		return ContinuationProof{}, continuationError(CodeContinuationMismatch, "there is no pending native call")
	}
	if pending.ItemAuthority {
		return matchItemPending(call, pending, result)
	}
	if IsNameOnlyResult(result) {
		name := strings.TrimSpace(result.Name)
		if name == "" {
			name = strings.TrimSpace(result.CallID)
		}
		if name != pending.Name {
			return ContinuationProof{}, continuationError(CodeContinuationMismatch, "the tool result name does not match the pending native call")
		}
		proof, shape, err := NameContinuation(call, pending.Name)
		if err != nil {
			return ContinuationProof{}, err
		}
		switch shape {
		case ContinuationReady:
		case ContinuationAmbiguous:
			return ContinuationProof{}, continuationError(CodeAmbiguousContinuation, "more than one visible name-only tool call could be the committed one")
		default:
			return ContinuationProof{}, continuationError(CodeContinuationMismatch, "the request history does not select one committed name-only tool call")
		}
		if err := MatchNamePending(pending, proof); err != nil {
			return ContinuationProof{}, err
		}
		return ContinuationProof{Prefix: proof.Prefix, ArgsDigest: proof.ArgsDigest}, nil
	}
	return matchExactPending(call, pending, result)
}

// IsNameOnlyResult reports the Gemini wire form: the frontend has no distinct
// call id, so it sets both the result id and name to the tool name. A missing id
// with a name is the same form. The connector never treats that name as an
// opaque native id.
func IsNameOnlyResult(result ToolResult) bool {
	name := strings.TrimSpace(result.Name)
	if name == "" {
		return false
	}
	callID := strings.TrimSpace(result.CallID)
	return callID == "" || callID == name
}

// previousResponseEvidence reports that the host resolved a previous_response_id
// into this call. The canonical field is not carried across the backend-plugin
// ABI, but the materializer provenance extension is.
func previousResponseEvidence(call aipapi.Call) bool {
	return strings.TrimSpace(call.PreviousResponseID) != "" || aipcont.Materialized(call)
}

func matchItemPending(call aipapi.Call, pending *PendingCall, result ToolResult) (ContinuationProof, error) {
	callID := strings.TrimSpace(result.CallID)
	if callID != pending.CallID {
		return ContinuationProof{}, continuationError(CodeContinuationMismatch, "the tool result does not match the pending native call")
	}
	prefix, found, err := VisiblePrefixThroughCall(call, pending.CallID)
	if err != nil {
		return ContinuationProof{}, err
	}
	if !found {
		if previousResponseEvidence(call) {
			return ContinuationProof{}, continuationError(CodeHistoryUnavailable, "previous_response_id did not materialize the committed assistant tool call")
		}
		return ContinuationProof{}, continuationError(CodeContinuationMismatch, "the request history does not carry the committed assistant tool call")
	}
	if !PrefixesEqual(prefix, pending.Prefix) {
		return ContinuationProof{}, continuationError(CodeContinuationMismatch, "the request history does not match the committed tool-call prefix")
	}
	// Prefix equality already binds the exact arguments from the history.
	return ContinuationProof{Prefix: prefix, ArgsDigest: pending.ArgsDigest}, nil
}

func matchExactPending(call aipapi.Call, pending *PendingCall, result ToolResult) (ContinuationProof, error) {
	callID := strings.TrimSpace(result.CallID)
	if callID != pending.CallID {
		return ContinuationProof{}, continuationError(CodeContinuationMismatch, "the tool result does not match the pending native call")
	}
	prefix, found, err := VisiblePrefixThroughCall(call, pending.CallID)
	if err != nil {
		return ContinuationProof{}, err
	}
	if !found {
		if previousResponseEvidence(call) {
			return ContinuationProof{}, continuationError(CodeHistoryUnavailable, "previous_response_id did not materialize the committed assistant tool call")
		}
		return ContinuationProof{}, continuationError(CodeContinuationMismatch, "the request history does not carry the committed assistant tool call")
	}
	// A call-then-text request carries an assistant suffix after the call. That
	// suffix is only safe when the exact alternate text-plus-call digest
	// reproduces the parked prefix. Require it before the ordinary/separate-text
	// early returns so an unproven suffix can never be masked by a matching
	// pre-call prefix.
	alternate, callThenText, alternateErr := VisiblePrefixCallThenText(call, pending.CallID)
	if alternateErr != nil {
		return ContinuationProof{}, alternateErr
	}
	if callThenText {
		if !PrefixesEqual(alternate, pending.Prefix) {
			return ContinuationProof{}, continuationError(CodeContinuationMismatch, "the assistant text after the committed tool call does not match the parked turn")
		}
		return ContinuationProof{Prefix: alternate, ArgsDigest: pending.ArgsDigest}, nil
	}
	if PrefixesEqual(prefix, pending.Prefix) ||
		(pending.PrefixSeparateText != ([32]byte{}) && PrefixesEqual(prefix, pending.PrefixSeparateText)) {
		return ContinuationProof{Prefix: prefix, ArgsDigest: pending.ArgsDigest}, nil
	}
	return ContinuationProof{}, continuationError(CodeContinuationMismatch, "the request history does not match the committed tool-call prefix")
}

// MatchNamePending verifies that one selected name-only occurrence is exactly
// the parked call. The caller must pass the proof of the same occurrence whose
// trailing suffix was classified.
func MatchNamePending(pending *PendingCall, proof NameProof) error {
	if pending == nil {
		return continuationError(CodeContinuationMismatch, "there is no pending native call")
	}
	if strings.TrimSpace(proof.Name) != pending.Name {
		return continuationError(CodeContinuationMismatch, "the tool result name does not match the pending native call")
	}
	if proof.Prefix == ([32]byte{}) || pending.PrefixByName == ([32]byte{}) {
		return continuationError(CodeContinuationMismatch, "the name-only continuation proof is unavailable")
	}
	if !PrefixesEqual(pending.PrefixByName, proof.Prefix) {
		return continuationError(CodeContinuationMismatch, "the request history does not match the committed name-only tool-call prefix")
	}
	if pending.ArgsDigest != proof.ArgsDigest {
		return continuationError(CodeContinuationMismatch, "the committed tool arguments do not match the parked turn")
	}
	return nil
}

// NameContinuation selects exactly one name-only call occurrence whose ordered
// suffix reaches the trailing result without dropping content, and returns that
// occurrence's prefix and argument proof. Classification and prefix validation
// therefore describe the same position.
func NameContinuation(call aipapi.Call, name string) (NameProof, ContinuationShape, error) {
	name = strings.TrimSpace(name)
	proof := NameProof{Name: name}
	if call.HasItemAuthority() || len(call.Messages) == 0 || name == "" {
		return proof, ContinuationNoCall, nil
	}
	last := len(call.Messages) - 1
	hasResult := false
	for _, part := range call.Messages[last].Parts {
		if part.Kind != aipapi.PartToolResult {
			continue
		}
		if !nameResultMatches(part, name) {
			return proof, ContinuationMixed, nil
		}
		hasResult = true
	}
	if !hasResult {
		return proof, ContinuationNoCall, nil
	}

	var clean []NameProof
	sawAssistant, sawWrongRole := false, false
	for mi, message := range call.Messages {
		for pi, part := range message.Parts {
			if part.Kind != aipapi.PartJSON || part.ToolCallID != "" || part.ToolName != name {
				continue
			}
			if message.Role != aipapi.RoleAssistant {
				sawWrongRole = true
				continue
			}
			sawAssistant = true
			if !cleanNameSuffix(call, mi, pi, name) {
				continue
			}
			candidate, err := nameCallCandidateAt(call, mi, pi, name)
			if err != nil {
				return proof, ContinuationMixed, err
			}
			clean = append(clean, candidate)
		}
	}
	switch len(clean) {
	case 1:
		return clean[0], ContinuationReady, nil
	case 0:
		if !sawAssistant && sawWrongRole {
			return proof, ContinuationRoleMismatch, nil
		}
		if sawAssistant || sawWrongRole {
			return proof, ContinuationMixed, nil
		}
		return proof, ContinuationNoCall, nil
	default:
		return proof, ContinuationAmbiguous, nil
	}
}

// cleanNameSuffix reports whether the only non-empty content after the selected
// occurrence is the trailing name result group in the final message. A second
// call, added user text or any other part makes the occurrence ineligible.
func cleanNameSuffix(call aipapi.Call, messageIndex, partIndex int, name string) bool {
	last := len(call.Messages) - 1
	found := false
	for i := messageIndex; i < len(call.Messages); i++ {
		parts := call.Messages[i].Parts
		if i == messageIndex {
			parts = parts[partIndex+1:]
		}
		for _, part := range parts {
			switch part.Kind {
			case aipapi.PartText:
				if part.Text != "" {
					return false
				}
			case aipapi.PartToolResult:
				if i != last || !nameResultMatches(part, name) {
					return false
				}
				found = true
			default:
				return false
			}
		}
	}
	return found
}

func nameCallCandidateAt(call aipapi.Call, messageIndex, partIndex int, name string) (NameProof, error) {
	message := call.Messages[messageIndex]
	var text strings.Builder
	for _, before := range message.Parts[:partIndex] {
		if before.Kind != aipapi.PartText {
			return NameProof{}, continuationError(CodeContinuationMismatch, "the name-only call shares its message with other content")
		}
		text.WriteString(before.Text)
	}
	var base [32]byte
	for _, instruction := range call.Instructions {
		next, err := chainInstruction(base, instruction)
		if err != nil {
			return NameProof{}, err
		}
		base = next
	}
	for _, previous := range call.Messages[:messageIndex] {
		next, err := chainMessage(base, previous)
		if err != nil {
			return NameProof{}, err
		}
		base = next
	}
	part := message.Parts[partIndex]
	prefix, err := ExtendPrefixWithCall(base, text.String(), name, name, part.Content)
	if err != nil {
		return NameProof{}, err
	}
	digest, err := argsDigest(part.Content)
	if err != nil {
		return NameProof{}, err
	}
	return NameProof{Name: name, Prefix: prefix, ArgsDigest: digest}, nil
}

// ExtendPrefixWithCall appends the assistant message that carries one emitted
// tool call to a prior visible prefix. text is the assistant text the client saw
// before the call in the same message.
func ExtendPrefixWithCall(prefix [32]byte, text, callID, name string, args []byte) ([32]byte, error) {
	out := prefix
	if text != "" {
		var err error
		out, err = chainText(out, text)
		if err != nil {
			return [32]byte{}, err
		}
	}
	return chainToolCall(out, callID, name, args)
}

// extendPrefixWithSeparateText is the second accepted legacy prefix form: an
// item-authority request projected to messages by the host carries the assistant
// text as its own message item, so the continuation hashes it as a message
// rather than inline Chat/Anthropic text. Both forms describe the same exact
// visible content; accepting either never weakens the ordered proof.
func extendPrefixWithSeparateText(prefix [32]byte, text, callID, name string, args []byte) ([32]byte, error) {
	out := prefix
	if text != "" {
		var err error
		out, err = chainMessage(out, aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart(text)}})
		if err != nil {
			return [32]byte{}, err
		}
	}
	return chainToolCall(out, callID, name, args)
}

// PrefixesEqual compares two visible-prefix digests without an early exit.
func PrefixesEqual(a, b [32]byte) bool {
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

func writeWholeMessage(h hash.Hash, message aipapi.Message) error {
	writeDigestField(h, "message")
	writeDigestField(h, string(message.Role))
	return writeParts(h, message.Parts)
}

// writeItemFields writes one item's canonical fields. It is shared by the flat
// item digest and the chained item digest so both spell every item identically.
func writeItemFields(h hash.Hash, item aipapi.Item) error {
	switch item.Kind {
	case aipapi.ItemKindMessage:
		writeDigestField(h, "message")
		writeDigestField(h, string(item.Role))
		var text strings.Builder
		flush := func() {
			if text.Len() == 0 {
				return
			}
			writeDigestField(h, "text")
			writeDigestField(h, text.String())
			text.Reset()
		}
		for _, part := range item.Content {
			if part.Kind == aipapi.ContentPartText {
				text.WriteString(part.Text)
				continue
			}
			flush()
			writeDigestField(h, string(part.Kind))
			writeDigestField(h, part.Text)
		}
		flush()
	case aipapi.ItemKindToolCall:
		if item.ToolCall != nil {
			return writeToolCall(h, item.ToolCall.CallID, item.ToolCall.Name, item.ToolCall.Arguments)
		}
	case aipapi.ItemKindToolResult:
		if item.ToolResult != nil {
			return writeItemResult(h, item.ToolResult)
		}
	default:
		writeDigestField(h, "item")
		writeDigestField(h, string(item.Kind))
	}
	return nil
}

// chainItem extends one ordered-item digest with a single item. Item authority
// uses a chained digest so a parked prefix can be extended with the assistant
// message and tool call the client will resend, without retaining the original
// request bytes.
func chainInstruction(prefix [32]byte, message aipapi.Message) ([32]byte, error) {
	h := sha256.New()
	h.Write(prefix[:])
	writeDigestField(h, "instruction")
	writeDigestField(h, string(message.Role))
	if err := writeParts(h, message.Parts); err != nil {
		return [32]byte{}, err
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}

func chainMessage(prefix [32]byte, message aipapi.Message) ([32]byte, error) {
	h := sha256.New()
	h.Write(prefix[:])
	if err := writeWholeMessage(h, message); err != nil {
		return [32]byte{}, err
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}

func chainText(prefix [32]byte, text string) ([32]byte, error) {
	h := sha256.New()
	h.Write(prefix[:])
	writeDigestField(h, "text")
	writeDigestField(h, text)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}

func chainItem(prefix [32]byte, item aipapi.Item) ([32]byte, error) {
	h := sha256.New()
	h.Write(prefix[:])
	if err := writeItemFields(h, item); err != nil {
		return [32]byte{}, err
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}

func chainToolCall(prefix [32]byte, callID, name string, args []byte) ([32]byte, error) {
	h := sha256.New()
	h.Write(prefix[:])
	if err := writeToolCall(h, callID, name, args); err != nil {
		return [32]byte{}, err
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}

// writeParts canonicalizes one message's content: consecutive text parts are one
// visible text run (so Chat's single string and Anthropic's text blocks hash
// equally), and every tool call or result keeps its exact ordered position.
func writeParts(h hash.Hash, parts []aipapi.Part) error {
	var text strings.Builder
	flush := func() {
		if text.Len() == 0 {
			return
		}
		writeDigestField(h, "text")
		writeDigestField(h, text.String())
		text.Reset()
	}
	for _, part := range parts {
		switch part.Kind {
		case aipapi.PartText:
			text.WriteString(part.Text)
		case aipapi.PartJSON:
			flush()
			callID, name, args := ToolCallFields(part)
			if err := writeToolCall(h, callID, name, args); err != nil {
				return err
			}
		case aipapi.PartToolResult:
			flush()
			if err := writeResult(h, part.ToolCallID, part.Text, part.Content); err != nil {
				return err
			}
		default:
			flush()
			writeDigestField(h, string(part.Kind))
			writeDigestField(h, part.Text)
		}
	}
	flush()
	return nil
}

// writeResult hashes one tool result's identity and exact content. A Chat JSON
// string result and the same text in an Anthropic text block normalize to the
// same bytes; structured JSON keeps its canonical content. Content is never
// discarded from the fingerprint.
func writeResult(h hash.Hash, callID, text string, content []byte) error {
	writeDigestField(h, "result")
	writeDigestField(h, callID)
	body, err := resultDigestText(text, content)
	if err != nil {
		return continuationError(CodeContinuationMismatch, "the transcript contains invalid tool result content")
	}
	writeDigestField(h, body)
	return nil
}

// writeItemResult hashes one item-authoritative tool result. The result's exact
// text (or concatenated text parts) participates in the fingerprint so a changed
// historical result cannot reproduce the committed prefix. Non-text parts are
// recorded by kind and text; the continuation release itself refuses them when
// the canonical carrier cannot represent them.
func writeItemResult(h hash.Hash, result *aipapi.ToolResultItem) error {
	writeDigestField(h, "result")
	writeDigestField(h, result.CallID)
	if len(result.Parts) > 0 {
		var text strings.Builder
		for _, part := range result.Parts {
			if part.Kind == aipapi.ContentPartText {
				text.WriteString(part.Text)
				continue
			}
			writeDigestField(h, "part")
			writeDigestField(h, string(part.Kind))
			writeDigestField(h, part.Text)
		}
		writeDigestField(h, "text")
		writeDigestField(h, text.String())
		return nil
	}
	writeDigestField(h, "text")
	writeDigestField(h, result.Output)
	return nil
}

func resultDigestText(text string, content []byte) (string, error) {
	if text != "" {
		return text, nil
	}
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) == 0 {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(trimmed, &value); err == nil {
		return value, nil
	}
	canonical, err := canonicalJSON(trimmed)
	if err != nil {
		return "", err
	}
	return string(canonical), nil
}

func writeToolCall(h hash.Hash, callID, name string, args []byte) error {
	canonical, err := canonicalToolArguments(args)
	if err != nil {
		return continuationError(CodeContinuationMismatch, "the transcript contains invalid tool arguments")
	}
	writeDigestField(h, "call")
	writeDigestField(h, callID)
	writeDigestField(h, name)
	writeDigestField(h, string(canonical))
	return nil
}

func writeDigestField(h hash.Hash, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = h.Write(size[:])
	_, _ = h.Write([]byte(value))
}

// canonicalJSON re-encodes one JSON document deterministically so wire
// whitespace and key order never change correlation identity while the content
// stays exact.
func canonicalJSON(raw []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return []byte("{}"), nil
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing JSON content")
	}
	return json.Marshal(value)
}

// ContinuationShape classifies the request's trailing result group against the
// committed assistant tool call it names.
type ContinuationShape int

const (
	// ContinuationReady is a clean continuation: the result follows the committed
	// assistant call with no intervening content.
	ContinuationReady ContinuationShape = iota
	// ContinuationMixed has the result but also additional content in the suffix.
	ContinuationMixed
	// ContinuationRoleMismatch echoes the call under a non-assistant role.
	ContinuationRoleMismatch
	// ContinuationNoCall does not carry the assistant call for the result id. A
	// live pending still makes this a typed continuation refusal.
	ContinuationNoCall
	// ContinuationDuplicate carries the same opaque call id more than once. The
	// transcript cannot establish one authority, so the request is refused.
	ContinuationDuplicate
	// ContinuationHistoryUnavailable carries an unresolved item reference or a
	// previous-response marker whose parent items cannot prove the base prefix.
	ContinuationHistoryUnavailable
	// ContinuationAmbiguous has more than one visible call that could be the
	// committed one (for example two identical name-only occurrences).
	ContinuationAmbiguous
	// ContinuationCallThenText has the committed assistant call followed by the
	// exact assistant text as its own message, then the one result. Codex
	// serializes a text-plus-call turn in this order even though the model
	// emitted the text first; the exact text is still bound by the prefix proof.
	ContinuationCallThenText
)

// callThenTextSuffix recognizes the client serialization where a turn's
// assistant text is replayed as its own assistant message immediately after the
// committed tool call. The call must be the only part of its message and the
// final message must carry exactly that call's one result. The returned text is
// the exact client bytes the parked prefix must reproduce. Any other content,
// extra message, or extra result is not this shape.
func callThenTextSuffix(call aipapi.Call, messageIndex, partIndex int, callID string) (string, bool) {
	if call.HasItemAuthority() || messageIndex < 0 || partIndex != 0 {
		return "", false
	}
	if messageIndex+2 != len(call.Messages)-1 {
		return "", false
	}
	callMessage := call.Messages[messageIndex]
	if callMessage.Role != aipapi.RoleAssistant || len(callMessage.Parts) != 1 {
		return "", false
	}
	if id, _, _ := ToolCallFields(callMessage.Parts[0]); id != callID {
		return "", false
	}
	textMessage := call.Messages[messageIndex+1]
	if textMessage.Role != aipapi.RoleAssistant {
		return "", false
	}
	var text strings.Builder
	for _, part := range textMessage.Parts {
		if part.Kind != aipapi.PartText {
			return "", false
		}
		text.WriteString(part.Text)
	}
	if text.Len() == 0 {
		return "", false
	}
	resultMessage := call.Messages[len(call.Messages)-1]
	if resultMessage.Role != aipapi.RoleTool || len(resultMessage.Parts) != 1 {
		return "", false
	}
	result := resultMessage.Parts[0]
	if result.Kind != aipapi.PartToolResult || strings.TrimSpace(result.ToolCallID) != callID {
		return "", false
	}
	return text.String(), true
}

// VisiblePrefixCallThenText computes the parked-prefix digest for the client
// serialization Codex uses when a turn carried assistant text and a tool call:
// the call comes first and the exact text follows as its own assistant message.
// The digest is the same canonical text-then-call digest the connector parked,
// built by re-inserting that exact text before the call. It reports false for
// every other order or any extra content, so it is a proof variant, not a
// weaker match.
func VisiblePrefixCallThenText(call aipapi.Call, callID string) ([32]byte, bool, error) {
	if call.HasItemAuthority() {
		return [32]byte{}, false, nil
	}
	messageIndex, partIndex, unique := singleCallOccurrence(call, callID)
	if !unique {
		return [32]byte{}, false, nil
	}
	if call.Messages[messageIndex].Role != aipapi.RoleAssistant {
		return [32]byte{}, false, nil
	}
	text, ok := callThenTextSuffix(call, messageIndex, partIndex, callID)
	if !ok {
		return [32]byte{}, false, nil
	}
	var out [32]byte
	for _, message := range call.Instructions {
		next, err := chainInstruction(out, message)
		if err != nil {
			return [32]byte{}, false, err
		}
		out = next
	}
	for _, message := range call.Messages[:messageIndex] {
		next, err := chainMessage(out, message)
		if err != nil {
			return [32]byte{}, false, err
		}
		out = next
	}
	next, err := chainText(out, text)
	if err != nil {
		return [32]byte{}, false, err
	}
	callPart := call.Messages[messageIndex].Parts[partIndex]
	callID, name, args := ToolCallFields(callPart)
	out, err = chainToolCall(next, callID, name, args)
	if err != nil {
		return [32]byte{}, false, err
	}
	return out, true, nil
}

// TrailingResults returns the tool results in the request's final message. It
// ignores empty text parts and reports false when the final message carries no
// tool result. Historical results in earlier messages are never continuation
// candidates.
func TrailingResults(call aipapi.Call) ([]ToolResult, bool) {
	if call.HasItemAuthority() {
		items := call.Items
		if len(items) == 0 || items[len(items)-1].Kind != aipapi.ItemKindToolResult {
			return nil, false
		}
		var results []ToolResult
		for i := len(items) - 1; i >= 0 && items[i].Kind == aipapi.ItemKindToolResult; i-- {
			item := items[i]
			if item.ToolResult == nil {
				continue
			}
			results = append(results, ToolResult{
				CallID: strings.TrimSpace(item.ToolResult.CallID),
				Name:   item.ToolResult.Name,
				Text:   item.ToolResult.Output,
				Parts:  append([]aipapi.ContentPart(nil), item.ToolResult.Parts...),
			})
		}
		// The backward walk collected the trailing run in reverse source order.
		for i, j := 0, len(results)-1; i < j; i, j = i+1, j-1 {
			results[i], results[j] = results[j], results[i]
		}
		return results, len(results) > 0
	}
	if len(call.Messages) == 0 {
		return nil, false
	}
	last := call.Messages[len(call.Messages)-1]
	var results []ToolResult
	byCallID := make(map[string]int)
	for _, part := range last.Parts {
		switch part.Kind {
		case aipapi.PartText:
			if part.Text != "" {
				continue
			}
		case aipapi.PartToolResult:
			callID := strings.TrimSpace(part.ToolCallID)
			if index, seen := byCallID[callID]; seen && callID != "" &&
				len(results[index].JSON) == 0 && len(part.Content) == 0 {
				// A single canonical tool result may arrive as multiple text
				// content parts (for example an item trajectory projected to
				// legacy messages). They are one result, not a parallel group.
				results[index].Text += part.Text
				continue
			}
			if _, seen := byCallID[callID]; !seen {
				byCallID[callID] = len(results)
			}
			results = append(results, ToolResult{
				CallID: callID,
				Name:   part.ToolName,
				Text:   part.Text,
				JSON:   append([]byte(nil), part.Content...),
			})
		default:
			continue
		}
	}
	return results, len(results) > 0
}

// singleCallOccurrence returns the only message/part that carries callID. A
// transcript with two occurrences of the same opaque call id is ambiguous and
// never establishes a release authority.
func singleCallOccurrence(call aipapi.Call, callID string) (int, int, bool) {
	if call.HasItemAuthority() {
		return -1, -1, false
	}
	messageIndex, partIndex := -1, -1
	for i, message := range call.Messages {
		for j, part := range message.Parts {
			if part.Kind != aipapi.PartJSON {
				continue
			}
			if id, _, _ := ToolCallFields(part); id != callID {
				continue
			}
			if messageIndex >= 0 {
				return -1, -1, false
			}
			messageIndex, partIndex = i, j
		}
	}
	if messageIndex < 0 {
		return -1, -1, false
	}
	return messageIndex, partIndex, true
}

// ClassifyContinuation validates the whole visible suffix from the committed
// assistant tool call to the submitted result. Anything between them (user text,
// a second call, a different result, or a call under another role) is reported
// so the caller can replay history instead of releasing while dropping content.
// A duplicate occurrence of the call id is refused rather than resolved.
func ClassifyContinuation(call aipapi.Call, callID string) ContinuationShape {
	if call.HasItemAuthority() {
		return classifyItemContinuation(call, callID)
	}
	if len(call.Messages) == 0 {
		return ContinuationNoCall
	}
	messageIndex, partIndex, unique := singleCallOccurrence(call, callID)
	if !unique {
		// Distinguish "no such call" from "more than one": a duplicate is a
		// typed refusal even though the opaque id is present.
		occurrences := 0
		for _, message := range call.Messages {
			for _, part := range message.Parts {
				if part.Kind != aipapi.PartJSON {
					continue
				}
				if id, _, _ := ToolCallFields(part); id == callID {
					occurrences++
				}
			}
		}
		if occurrences > 1 {
			return ContinuationDuplicate
		}
		return ContinuationNoCall
	}
	if call.Messages[messageIndex].Role != aipapi.RoleAssistant {
		return ContinuationRoleMismatch
	}
	if _, ok := callThenTextSuffix(call, messageIndex, partIndex, callID); ok {
		return ContinuationCallThenText
	}
	foundAt := -1
	for i := messageIndex; i < len(call.Messages); i++ {
		parts := call.Messages[i].Parts
		if i == messageIndex {
			parts = parts[partIndex+1:]
		}
		for _, part := range parts {
			switch part.Kind {
			case aipapi.PartText:
				if part.Text == "" {
					continue
				}
				return ContinuationMixed
			case aipapi.PartToolResult:
				if part.ToolCallID != callID {
					return ContinuationMixed
				}
				// Multiple content parts for the one committed call form a single
				// result group. The same opaque call id in a second result message
				// is conflicting evidence: releasing the final group would drop the
				// earlier result, so the transcript cannot establish one authority.
				if foundAt >= 0 && foundAt != i {
					return ContinuationDuplicate
				}
				foundAt = i
			default:
				return ContinuationMixed
			}
		}
	}
	if foundAt < 0 {
		return ContinuationMixed
	}
	return ContinuationReady
}

// classifyItemContinuation classifies the item-authority suffix. The committed
// call must be the single item immediately before exactly one trailing result;
// any other item between them is content the connector cannot drop.
func classifyItemContinuation(call aipapi.Call, callID string) ContinuationShape {
	if HasItemReferences(call) {
		return ContinuationHistoryUnavailable
	}
	if strings.TrimSpace(callID) == "" {
		return ContinuationNoCall
	}
	callIndex := -1
	for i, item := range call.Items {
		if item.Kind != aipapi.ItemKindToolCall || item.ToolCall == nil || item.ToolCall.CallID != callID {
			continue
		}
		if callIndex >= 0 {
			return ContinuationDuplicate
		}
		callIndex = i
	}
	if callIndex < 0 {
		return ContinuationNoCall
	}
	last := call.Items[len(call.Items)-1]
	if last.Kind != aipapi.ItemKindToolResult || last.ToolResult == nil || last.ToolResult.CallID != callID {
		return ContinuationMixed
	}
	if callIndex != len(call.Items)-2 {
		return ContinuationMixed
	}
	return ContinuationReady
}

// ToolCallFields returns the canonical tool-call identity and arguments of one
// legacy message part. Canonical parts carry the identity at the top level and
// the argument document in Content. An older openairesponses frontend instead
// kept the OpenAI Responses function_call envelope in PartJSON.Content and left
// the top-level fields empty; recovering those exact fields keeps continuations
// captured from older builds faithful without changing the public canonical
// contract.
func ToolCallFields(part aipapi.Part) (callID, name string, args json.RawMessage) {
	if part.Kind != aipapi.PartJSON {
		return "", "", nil
	}
	if part.ToolCallID != "" || part.ToolName != "" {
		return part.ToolCallID, part.ToolName, part.Content
	}
	if len(part.Content) == 0 {
		return "", "", nil
	}
	var wire struct {
		Type      string          `json:"type"`
		CallID    string          `json:"call_id"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(part.Content, &wire); err != nil || wire.Type != "function_call" || wire.Name == "" {
		return "", "", part.Content
	}
	// The Responses item id is a different identifier. Only the opaque tool
	// call_id can prove continuation authority; a missing call_id stays
	// unreleasable instead of borrowing the item id as a surrogate.
	callID = strings.TrimSpace(wire.CallID)
	if callID == "" {
		return "", "", nil
	}
	if len(wire.Arguments) == 0 {
		wire.Arguments = json.RawMessage(`{}`)
	}
	return callID, wire.Name, wire.Arguments
}

func nameResultMatches(part aipapi.Part, name string) bool {
	if part.ToolCallID == name {
		return part.ToolName == "" || part.ToolName == name
	}
	return part.ToolCallID == "" && part.ToolName == name
}

// PendingCall is one committed native tool call whose result has not been
// released yet. A pending call is created from PreToolUse evidence and is
// released exactly once.
type PendingCall struct {
	CallID     string
	Name       string
	NativeName string
	Args       json.RawMessage
	Prefix     [32]byte
	// PrefixSeparateText is the item-projected message form of the same emitted
	// assistant text. A host that projects an item continuation to legacy
	// messages places the text in its own message, which hashes differently.
	PrefixSeparateText [32]byte
	PrefixByName       [32]byte
	ArgsDigest         [32]byte
	Model              string
	Effort             string
	CatalogDigest      [32]byte
	ToolCount          int
	// ItemAuthority records whether the parked request used ordered items. The
	// extension of the visible prefix for a subsequent call differs from the
	// legacy inline-text form.
	ItemAuthority bool
	CreatedAt     time.Time
	ExpiresAt     time.Time

	released bool
}
