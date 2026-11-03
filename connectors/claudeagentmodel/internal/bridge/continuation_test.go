package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestToolResults_LegacyChatShape(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{Messages: []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("read the file")}},
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
			Kind:       aipapi.PartJSON,
			ToolCallID: "toolu_01",
			ToolName:   "Read",
			Content:    json.RawMessage(`{"file_path":"x"}`),
		}}},
		{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
			Kind:       aipapi.PartToolResult,
			ToolCallID: "toolu_01",
			Content:    json.RawMessage(`"contents"`),
		}}},
	}}
	results := ToolResults(call)
	if len(results) != 1 || results[0].CallID != "toolu_01" {
		t.Fatalf("results=%+v", results)
	}
	text, err := results[0].Content()
	if err != nil || text != "contents" {
		t.Fatalf("text=%q err=%v", text, err)
	}
}

func TestToolResults_ItemAuthority(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{Items: []aipapi.Item{
		{Kind: aipapi.ItemKindToolCall, ToolCall: &aipapi.ToolCallItem{CallID: "call_1", Name: "Read", Arguments: json.RawMessage(`{"file_path":"x"}`)}},
		{Kind: aipapi.ItemKindToolResult, ToolResult: &aipapi.ToolResultItem{CallID: "call_1", Name: "Read", Output: "done"}},
	}}
	results := ToolResults(call)
	if len(results) != 1 || results[0].CallID != "call_1" || results[0].Text != "done" {
		t.Fatalf("results=%+v", results)
	}
	if text, err := results[0].Content(); err != nil || text != "done" {
		t.Fatalf("text=%q err=%v", text, err)
	}
}

func TestToolResultContent_PreservesExactJSONAndRefusesMultimodal(t *testing.T) {
	t.Parallel()
	structured := ToolResult{CallID: "id", JSON: json.RawMessage(`{"b":1,"a": 2}`)}
	text, err := structured.Content()
	if err != nil || text != `{"a":2,"b":1}` {
		t.Fatalf("text=%q err=%v", text, err)
	}
	if _, err := (ToolResult{CallID: "id", Parts: []aipapi.ContentPart{{Kind: aipapi.ContentPartImageRef}}}).Content(); !errors.Is(err, aipapi.ErrCapabilityReject) {
		t.Fatalf("multimodal err=%v", err)
	}
	if text, err := (ToolResult{CallID: "id"}).Content(); err != nil || text != "" {
		t.Fatalf("empty text=%q err=%v", text, err)
	}
}

func TestVisiblePrefix_MatchesTheEchoedToolCall(t *testing.T) {
	t.Parallel()
	fresh := aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("read the file")}}},
	}
	before, err := VisiblePrefixDigest(fresh)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := ExtendPrefixWithCall(before, "", "toolu_01", "Read", json.RawMessage(`{"file_path":"x"}`))
	if err != nil {
		t.Fatal(err)
	}

	continuation := aipapi.Call{Messages: []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("read the file")}},
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
			Kind:       aipapi.PartJSON,
			ToolCallID: "toolu_01",
			ToolName:   "Read",
			// Wire whitespace and key order may differ; the canonical content is
			// the same call.
			Content: json.RawMessage(`{ "file_path" : "x" }`),
		}}},
		{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "toolu_01", Text: "contents"}}},
	}}
	got, found, err := VisiblePrefixThroughCall(continuation, "toolu_01")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if !PrefixesEqual(got, committed) {
		t.Fatal("the echoed tool call must reproduce the committed prefix")
	}

	reordered := continuation
	reordered.Messages = []aipapi.Message{continuation.Messages[1], continuation.Messages[0], continuation.Messages[2]}
	if _, found, _ := VisiblePrefixThroughCall(reordered, "toolu_01"); !found {
		// The call is still present but the ordered digest differs.
		t.Fatal("expected the call to be found in the reordered transcript")
	} else if prefix, _, _ := VisiblePrefixThroughCall(reordered, "toolu_01"); PrefixesEqual(prefix, committed) {
		t.Fatal("a reordered transcript must not reproduce the committed prefix")
	}

	omitted := aipapi.Call{Messages: []aipapi.Message{
		continuation.Messages[0],
		continuation.Messages[2],
	}}
	if _, found, _ := VisiblePrefixThroughCall(omitted, "toolu_01"); found {
		t.Fatal("an omitted assistant tool call must not match")
	}
}

func TestVisiblePrefix_ChangedArgumentsDoNotMatch(t *testing.T) {
	t.Parallel()
	fresh := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("read the file")}}}}
	before, err := VisiblePrefixDigest(fresh)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := ExtendPrefixWithCall(before, "", "toolu_01", "Read", json.RawMessage(`{"file_path":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	tampered := aipapi.Call{Messages: []aipapi.Message{
		fresh.Messages[0],
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
			Kind:       aipapi.PartJSON,
			ToolCallID: "toolu_01",
			ToolName:   "Read",
			Content:    json.RawMessage(`{"file_path":"/etc/passwd"}`),
		}}},
	}}
	got, found, err := VisiblePrefixThroughCall(tampered, "toolu_01")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if PrefixesEqual(got, committed) {
		t.Fatal("changed arguments must not reproduce the committed prefix")
	}
}

func TestCanonicalJSON_IsOrderInsensitive(t *testing.T) {
	t.Parallel()
	first, err := canonicalJSON(json.RawMessage(`{"b":1,"a":[true,null]}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := canonicalJSON(json.RawMessage(`{ "a": [true, null], "b": 1 }`))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("first=%s second=%s", first, second)
	}
	if _, err := canonicalJSON(json.RawMessage(`{`)); err == nil {
		t.Fatal("expected invalid JSON rejection")
	}
	if _, err := canonicalJSON(nil); err != nil {
		t.Fatal(err)
	}
}

func TestClassifyContinuation(t *testing.T) {
	t.Parallel()
	assistant := aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
		Kind: aipapi.PartJSON, ToolCallID: "toolu_01", ToolName: "Read", Content: json.RawMessage(`{}`),
	}}}
	result := aipapi.Message{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
		Kind: aipapi.PartToolResult, ToolCallID: "toolu_01", Text: "done",
	}}}
	user := aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("go")}}
	call := aipapi.Call{Messages: []aipapi.Message{user, assistant, result}}

	trailing, ok := TrailingResults(call)
	if !ok || len(trailing) != 1 || trailing[0].CallID != "toolu_01" {
		t.Fatalf("trailing=%+v ok=%v", trailing, ok)
	}
	if shape := ClassifyContinuation(call, "toolu_01"); shape != ContinuationReady {
		t.Fatalf("shape=%v", shape)
	}
	// A mismatched id is not a continuation for that id, but the trailing group is
	// still a single result the caller must classify.
	if _, ok := TrailingResults(call); !ok {
		t.Fatal("the trailing result group must be detected")
	}
	if shape := ClassifyContinuation(call, "toolu_other"); shape != ContinuationNoCall {
		t.Fatalf("foreign shape=%v", shape)
	}

	// A separate user message between the committed call and its result is not a
	// clean suffix; the engine must replay instead of dropping it.
	intervening := aipapi.Call{Messages: []aipapi.Message{
		user,
		assistant,
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("also check this")}},
		result,
	}}
	if trailing, ok := TrailingResults(intervening); !ok || len(trailing) != 1 {
		t.Fatalf("intervening trailing=%+v ok=%v", trailing, ok)
	}
	if shape := ClassifyContinuation(intervening, "toolu_01"); shape != ContinuationMixed {
		t.Fatalf("intervening shape=%v", shape)
	}

	// Text in the same result message is equally not a clean suffix.
	sameMessage := aipapi.Call{Messages: []aipapi.Message{
		user,
		assistant,
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{
			aipapi.TextPart("also check this"),
			{Kind: aipapi.PartToolResult, ToolCallID: "toolu_01", Text: "done"},
		}},
	}}
	if shape := ClassifyContinuation(sameMessage, "toolu_01"); shape != ContinuationMixed {
		t.Fatalf("same-message shape=%v", shape)
	}

	// Content after the result is a new turn, not a continuation.
	after := aipapi.Call{Messages: append(append([]aipapi.Message(nil), call.Messages...),
		aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("next")}})}
	if _, ok := TrailingResults(after); ok {
		t.Fatal("content after the result must not be a trailing result group")
	}

	// Two occurrences of the same opaque call id cannot establish one authority.
	duplicate := aipapi.Call{Messages: []aipapi.Message{
		user,
		assistant,
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
			aipapi.TextPart("INJECTED-VISIBLE-TEXT"),
			{Kind: aipapi.PartJSON, ToolCallID: "toolu_01", ToolName: "Read", Content: json.RawMessage(`{}`)},
		}},
		result,
	}}
	if trailing, ok := TrailingResults(duplicate); !ok || len(trailing) != 1 {
		t.Fatalf("duplicate trailing=%+v ok=%v", trailing, ok)
	}
	if shape := ClassifyContinuation(duplicate, "toolu_01"); shape != ContinuationDuplicate {
		t.Fatalf("duplicate shape=%v", shape)
	}
	if _, found, err := VisiblePrefixThroughCall(duplicate, "toolu_01"); err != nil || found {
		t.Fatalf("duplicate prefix found=%v err=%v", found, err)
	}

	// The committed call must be an assistant call.
	wrongRole := aipapi.Call{Messages: []aipapi.Message{
		user,
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{{
			Kind: aipapi.PartJSON, ToolCallID: "toolu_01", ToolName: "Read", Content: json.RawMessage(`{}`),
		}}},
		result,
	}}
	if shape := ClassifyContinuation(wrongRole, "toolu_01"); shape != ContinuationRoleMismatch {
		t.Fatalf("role shape=%v", shape)
	}

	// Multiple text parts for the same committed call are one result group (an
	// item-authority tool result projected to legacy messages), not a parallel
	// submission.
	multiPart := aipapi.Call{Messages: []aipapi.Message{
		user,
		assistant,
		{Role: aipapi.RoleTool, Parts: []aipapi.Part{
			{Kind: aipapi.PartToolResult, ToolCallID: "toolu_01", Text: "a"},
			{Kind: aipapi.PartToolResult, ToolCallID: "toolu_01", Text: "b"},
		}},
	}}
	multiTrailing, ok := TrailingResults(multiPart)
	if !ok || len(multiTrailing) != 1 || multiTrailing[0].Text != "ab" {
		t.Fatalf("multi-part trailing=%+v ok=%v", multiTrailing, ok)
	}
	if shape := ClassifyContinuation(multiPart, "toolu_01"); shape != ContinuationReady {
		t.Fatalf("multi-part shape=%v", shape)
	}

	// Two results in the trailing group are a parallel submission.
	two := aipapi.Call{Messages: []aipapi.Message{
		user,
		assistant,
		{Role: aipapi.RoleTool, Parts: []aipapi.Part{
			{Kind: aipapi.PartToolResult, ToolCallID: "toolu_01", Text: "a"},
			{Kind: aipapi.PartToolResult, ToolCallID: "toolu_02", Text: "b"},
		}},
	}}
	if trailing, ok := TrailingResults(two); !ok || len(trailing) != 2 {
		t.Fatalf("two trailing=%+v ok=%v", trailing, ok)
	}

	items := aipapi.Call{Items: []aipapi.Item{
		{Kind: aipapi.ItemKindToolResult, ToolResult: &aipapi.ToolResultItem{CallID: "call_1", Name: "Read", Output: "done"}},
	}}
	if trailing, ok := TrailingResults(items); !ok || len(trailing) != 1 || trailing[0].CallID != "call_1" {
		t.Fatalf("item-authoritative trailing=%+v ok=%v", trailing, ok)
	}
}

func TestLookup_FreshHostSessionFallsBackToTheExactCallID(t *testing.T) {
	t.Parallel()
	admission := &countingAdmission{limit: 4}
	m := NewManager(admission, 5*time.Second, time.Minute, 5*time.Second, time.Now)
	turn, err := m.Start(context.Background(), toolTestLocation(t), toolTestRuntime(t, ""), TurnRequest{
		RequestID: "req-1", Prompt: "read the file", Catalog: toolTestCatalog(t, "Read"),
		SessionKey: "host-session-1", Prefix: firstTextPrefix(t, freshCall("read the file")), PendingTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drainTurn(t, turn); err != nil {
		t.Fatal(err)
	}
	_ = turn.Close()
	// A different host-issued id must not hide the only matching pending call; the
	// exact-ID and prefix proof still guard the release.
	if _, err := m.Lookup("toolu_01ABC", "host-session-2"); err != nil {
		t.Fatalf("fresh host session blocked the stateless proof: %v", err)
	}
	if _, err := m.Lookup("toolu_OTHER", "host-session-3"); err == nil {
		t.Fatal("a foreign call id must not resolve")
	}
	_ = m.Close()
}
