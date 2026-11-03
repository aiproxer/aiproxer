package bridge

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// legacyResponsesWrapperPart is the pinned legacy openairesponses canonical
// shape: the frontend keeps the OpenAI Responses function_call envelope in
// PartJSON.Content and leaves the top-level tool identity empty.
func legacyResponsesWrapperPart(callID, name, args string) aipapi.Part {
	return legacyResponsesWrapperPartWithItemID(callID, "", name, args)
}

// legacyResponsesWrapperPartWithItemID carries the Responses item id
// independently so tests can prove the item id is never promoted to the
// opaque tool call id.
func legacyResponsesWrapperPartWithItemID(callID, itemID, name, args string) aipapi.Part {
	wire := map[string]any{
		"type":      "function_call",
		"name":      name,
		"arguments": args,
	}
	if callID != "" {
		wire["call_id"] = callID
	}
	if itemID != "" {
		wire["id"] = itemID
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		panic(err)
	}
	return aipapi.Part{Kind: aipapi.PartJSON, Content: encoded}
}

func legacyResponsesContinuationWithPart(prompt, assistant string, part aipapi.Part, resultCallID, result string) aipapi.Call {
	return aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(prompt)}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart(assistant)}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{part}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
				Kind:       aipapi.PartToolResult,
				ToolCallID: resultCallID,
				Content:    json.RawMessage(`"` + result + `"`),
			}}},
		},
	}
}

func legacyResponsesContinuation(prompt, assistant, callID, result string) aipapi.Call {
	return legacyResponsesContinuationWithPart(
		prompt,
		assistant,
		legacyResponsesWrapperPart(callID, "Read", `{"file_path":"x"}`),
		callID,
		result,
	)
}

func legacyPendingContinuation(t *testing.T, prompt, assistant, callID string) PendingCall {
	t.Helper()
	fresh := aipapi.Call{Messages: []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(prompt)}},
	}}
	before, err := VisiblePrefixDigest(fresh)
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := ExtendPrefixWithCall(before, assistant, callID, "Read", json.RawMessage(`{"file_path":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	separate, err := extendPrefixWithSeparateText(before, assistant, callID, "Read", json.RawMessage(`{"file_path":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	return PendingCall{CallID: callID, Name: "Read", Prefix: prefix, PrefixSeparateText: separate}
}

func TestS3_LegacyResponsesWrapperProvesExactContinuation(t *testing.T) {
	t.Parallel()
	pending := legacyPendingContinuation(t, "legacy responses read", "let me look", "toolu_1")
	continuation := legacyResponsesContinuation("legacy responses read", "let me look", "toolu_1", "contents")
	results, ok := TrailingResults(continuation)
	if !ok || len(results) != 1 || results[0].CallID != "toolu_1" {
		t.Fatalf("trailing results=%+v ok=%v", results, ok)
	}
	if shape := ClassifyContinuation(continuation, "toolu_1"); shape != ContinuationReady {
		t.Fatalf("shape=%v", shape)
	}
	proof, err := MatchPendingContinuation(continuation, &pending, results[0])
	if err != nil {
		t.Fatalf("MatchPendingContinuation: %v", err)
	}
	if !PrefixesEqual(proof.Prefix, pending.Prefix) && !PrefixesEqual(proof.Prefix, pending.PrefixSeparateText) {
		t.Fatal("wrapper continuation proof does not reproduce the parked prefix")
	}
}

// TestS3_LegacyResponsesWrapperItemIDIsNotCallAuthority proves a Responses item
// id is never promoted to the opaque tool call id. A wrapper with the pending
// native id only in "id" cannot establish exact continuation authority.
func TestS3_LegacyResponsesWrapperItemIDIsNotCallAuthority(t *testing.T) {
	t.Parallel()
	pending := legacyPendingContinuation(t, "legacy item id", "let me look", "toolu_1")
	continuation := legacyResponsesContinuationWithPart(
		"legacy item id",
		"let me look",
		legacyResponsesWrapperPartWithItemID("", "toolu_1", "Read", `{"file_path":"x"}`),
		"toolu_1",
		"contents",
	)
	results, ok := TrailingResults(continuation)
	if !ok || len(results) != 1 {
		t.Fatalf("trailing results=%+v ok=%v", results, ok)
	}
	if shape := ClassifyContinuation(continuation, "toolu_1"); shape != ContinuationNoCall {
		t.Fatalf("item id produced continuation shape %v, want no call", shape)
	}
	if _, err := MatchPendingContinuation(continuation, &pending, results[0]); err == nil {
		t.Fatal("item id reproduced exact continuation authority")
	}
}

// legacyResponsesContinuationCallThenText is the order the Codex 0.166 client
// actually emits when a turn carried assistant text and a function_call: the
// function_call item precedes the assistant message item, and the result follows
// both. The connector must keep the parked native turn in its original
// text-then-call order while accepting this exact client serialization.
func legacyResponsesContinuationCallThenText(prompt, assistant string, part aipapi.Part, resultCallID, result string) aipapi.Call {
	return aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(prompt)}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{part}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart(assistant)}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
				Kind:       aipapi.PartToolResult,
				ToolCallID: resultCallID,
				Content:    json.RawMessage(`"` + result + `"`),
			}}},
		},
	}
}

// TestS3_LegacyResponsesWrapperAcceptsCodexCallThenTextOrder proves the
// continuation still releases the parked turn when the client replays the exact
// committed assistant text immediately after the call instead of before it.
// Codex does this for every turn that carried both text and a function_call.
func TestS3_LegacyResponsesWrapperAcceptsCodexCallThenTextOrder(t *testing.T) {
	t.Parallel()
	pending := legacyPendingContinuation(t, "legacy codex order", "let me look", "toolu_1")
	continuation := legacyResponsesContinuationCallThenText(
		"legacy codex order",
		"let me look",
		legacyResponsesWrapperPart("toolu_1", "Read", `{"file_path":"x"}`),
		"toolu_1",
		"contents",
	)
	results, ok := TrailingResults(continuation)
	if !ok || len(results) != 1 || results[0].CallID != "toolu_1" {
		t.Fatalf("trailing results=%+v ok=%v", results, ok)
	}
	if shape := ClassifyContinuation(continuation, "toolu_1"); shape != ContinuationCallThenText {
		t.Fatalf("shape=%v, want call-then-text", shape)
	}
	proof, err := MatchPendingContinuation(continuation, &pending, results[0])
	if err != nil {
		t.Fatalf("MatchPendingContinuation: %v", err)
	}
	if !PrefixesEqual(proof.Prefix, pending.Prefix) {
		t.Fatal("call-then-text continuation proof does not reproduce the parked prefix")
	}
}

// TestS3_LegacyResponsesWrapperCallThenTextStillBindsExactText proves the new
// order is a serialization variant, not a weaker proof: different assistant
// text, or extra content after the call, cannot release the parked turn.
func TestS3_LegacyResponsesWrapperCallThenTextStillBindsExactText(t *testing.T) {
	t.Parallel()
	pending := legacyPendingContinuation(t, "legacy codex tamper", "let me look", "toolu_1")
	part := legacyResponsesWrapperPart("toolu_1", "Read", `{"file_path":"x"}`)
	changed := legacyResponsesContinuationCallThenText("legacy codex tamper", "let me read", part, "toolu_1", "contents")
	results, ok := TrailingResults(changed)
	if !ok || len(results) != 1 {
		t.Fatalf("trailing results=%+v ok=%v", results, ok)
	}
	if _, err := MatchPendingContinuation(changed, &pending, results[0]); err == nil {
		t.Fatal("changed assistant text reproduced the parked prefix")
	}

	padded := legacyResponsesContinuationCallThenText("legacy codex tamper", "let me look", part, "toolu_1", "contents")
	padded.Messages = append(padded.Messages[:3], aipapi.Message{
		Role:  aipapi.RoleAssistant,
		Parts: []aipapi.Part{aipapi.TextPart("extra")},
	}, padded.Messages[3])
	if shape := ClassifyContinuation(padded, "toolu_1"); shape == ContinuationCallThenText {
		t.Fatalf("padded history was classified as an exact call-then-text continuation")
	}
}

// TestS3_LegacyResponsesWrapperCodexOrderTwoSerialLoops proves the client
// serialization composes across a second native tool call: the transcript a
// resumed turn extends is the same client-visible order the next continuation
// replays.
func TestS3_LegacyResponsesWrapperCodexOrderTwoSerialLoops(t *testing.T) {
	t.Parallel()
	args := json.RawMessage(`{"file_path":"x"}`)
	first := legacyResponsesContinuationCallThenText(
		"legacy codex two loops",
		"let me look",
		legacyResponsesWrapperPart("toolu_1", "Read", `{"file_path":"x"}`),
		"toolu_1",
		"contents",
	)
	firstPending := legacyPendingContinuation(t, "legacy codex two loops", "let me look", "toolu_1")
	firstResults, ok := TrailingResults(first)
	if !ok || len(firstResults) != 1 {
		t.Fatalf("first trailing results=%+v ok=%v", firstResults, ok)
	}
	if _, err := MatchPendingContinuation(first, &firstPending, firstResults[0]); err != nil {
		t.Fatalf("first MatchPendingContinuation: %v", err)
	}

	transcript, err := VisiblePrefixDigest(first)
	if err != nil {
		t.Fatal(err)
	}
	secondPrefix, err := ExtendPrefixWithCall(transcript, "second look", "toolu_2", "Read", args)
	if err != nil {
		t.Fatal(err)
	}
	secondPending := PendingCall{CallID: "toolu_2", Name: "Read", Prefix: secondPrefix}
	second := first
	second.Messages = append(append([]aipapi.Message(nil), first.Messages...),
		aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{legacyResponsesWrapperPart("toolu_2", "Read", `{"file_path":"x"}`)}},
		aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("second look")}},
		aipapi.Message{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
			Kind:       aipapi.PartToolResult,
			ToolCallID: "toolu_2",
			Content:    json.RawMessage(`"more"`),
		}}},
	)
	secondResults, ok := TrailingResults(second)
	if !ok || len(secondResults) != 1 || secondResults[0].CallID != "toolu_2" {
		t.Fatalf("second trailing results=%+v ok=%v", secondResults, ok)
	}
	if shape := ClassifyContinuation(second, "toolu_2"); shape != ContinuationCallThenText {
		t.Fatalf("second shape=%v, want call-then-text", shape)
	}
	proof, err := MatchPendingContinuation(second, &secondPending, secondResults[0])
	if err != nil {
		t.Fatalf("second MatchPendingContinuation: %v", err)
	}
	if !PrefixesEqual(proof.Prefix, secondPrefix) {
		t.Fatal("second call-then-text proof does not reproduce the parked prefix")
	}
}

// TestS3_LegacyResponsesWrapperCallThenTextSuffixIsMandatory proves the new
// call-then-text shape is authorized only by the exact text-plus-call digest.
// A matching ordinary or separate-text pre-call prefix must never mask an
// injected assistant suffix, and a rejected suffix leaves the pending call
// available for a correct retry.
func TestS3_LegacyResponsesWrapperCallThenTextSuffixIsMandatory(t *testing.T) {
	t.Parallel()
	args := json.RawMessage(`{"file_path":"x"}`)
	part := legacyResponsesWrapperPart("toolu_1", "Read", `{"file_path":"x"}`)
	toolResult := aipapi.Message{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
		Kind:       aipapi.PartToolResult,
		ToolCallID: "toolu_1",
		Content:    json.RawMessage(`"contents"`),
	}}}

	t.Run("already before call text plus injected suffix", func(t *testing.T) {
		t.Parallel()
		pending := legacyPendingContinuation(t, "suffix ordinary", "let me look", "toolu_1")
		call := aipapi.Call{Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("suffix ordinary")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("let me look")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{part}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("CLIENT_INJECTED_TEXT_NOT_EMITTED_BY_MODEL")}},
			toolResult,
		}}
		results, ok := TrailingResults(call)
		if !ok || len(results) != 1 || results[0].CallID != "toolu_1" {
			t.Fatalf("trailing results=%+v ok=%v", results, ok)
		}
		if shape := ClassifyContinuation(call, "toolu_1"); shape != ContinuationCallThenText {
			t.Fatalf("shape=%v, want call-then-text", shape)
		}
		if _, err := MatchPendingContinuation(call, &pending, results[0]); err == nil {
			t.Fatal("matching pre-call prefix masked the injected assistant suffix")
		}

		valid := legacyResponsesContinuationCallThenText("suffix ordinary", "let me look", part, "toolu_1", "contents")
		validResults, ok := TrailingResults(valid)
		if !ok || len(validResults) != 1 {
			t.Fatalf("valid trailing results=%+v ok=%v", validResults, ok)
		}
		if _, err := MatchPendingContinuation(valid, &pending, validResults[0]); err != nil {
			t.Fatalf("correct retry after suffix mismatch: %v", err)
		}
	})

	t.Run("tool only pending plus injected suffix", func(t *testing.T) {
		t.Parallel()
		before, err := VisiblePrefixDigest(aipapi.Call{Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("tool only")},
		}}})
		if err != nil {
			t.Fatal(err)
		}
		prefix, err := ExtendPrefixWithCall(before, "", "toolu_1", "Read", args)
		if err != nil {
			t.Fatal(err)
		}
		pending := PendingCall{CallID: "toolu_1", Name: "Read", Prefix: prefix}
		call := aipapi.Call{Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("tool only")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{part}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("CLIENT_INJECTED_TEXT_NOT_EMITTED_BY_MODEL")}},
			toolResult,
		}}
		results, ok := TrailingResults(call)
		if !ok || len(results) != 1 {
			t.Fatalf("trailing results=%+v ok=%v", results, ok)
		}
		if shape := ClassifyContinuation(call, "toolu_1"); shape != ContinuationCallThenText {
			t.Fatalf("shape=%v, want call-then-text", shape)
		}
		if _, err := MatchPendingContinuation(call, &pending, results[0]); err == nil {
			t.Fatal("tool-only pending accepted an injected assistant suffix")
		}
	})
}

// TestS3_LegacyResponsesWrapperUsesCallIDDespiteConflictingItemID proves the
// tool call_id remains authoritative when the envelope also carries an
// unrelated Responses item id.
func TestS3_LegacyResponsesWrapperUsesCallIDDespiteConflictingItemID(t *testing.T) {
	t.Parallel()
	pending := legacyPendingContinuation(t, "legacy conflicting item id", "let me look", "toolu_1")
	continuation := legacyResponsesContinuationWithPart(
		"legacy conflicting item id",
		"let me look",
		legacyResponsesWrapperPartWithItemID("toolu_1", "fc_unrelated_item", "Read", `{"file_path":"x"}`),
		"toolu_1",
		"contents",
	)
	results, ok := TrailingResults(continuation)
	if !ok || len(results) != 1 {
		t.Fatalf("trailing results=%+v ok=%v", results, ok)
	}
	if shape := ClassifyContinuation(continuation, "toolu_1"); shape != ContinuationReady {
		t.Fatalf("shape=%v, want ready", shape)
	}
	if _, err := MatchPendingContinuation(continuation, &pending, results[0]); err != nil {
		t.Fatalf("MatchPendingContinuation: %v", err)
	}
}
