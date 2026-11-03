package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func s3ItemCall(items ...aipapi.Item) aipapi.Call {
	return aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "anthropic/sonnet"},
		Items: items,
		Tools: []aipapi.ToolDef{{
			Name:       "Read",
			Parameters: json.RawMessage(`{"type":"object"}`),
		}},
	}
}

func s3UserItem(text string) aipapi.Item {
	return aipapi.Item{
		Kind:    aipapi.ItemKindMessage,
		Status:  aipapi.ItemStatusCompleted,
		Role:    aipapi.RoleUser,
		Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: text}},
	}
}

func s3AssistantItem(text string) aipapi.Item {
	return aipapi.Item{
		Kind:    aipapi.ItemKindMessage,
		Status:  aipapi.ItemStatusCompleted,
		Role:    aipapi.RoleAssistant,
		Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: text}},
	}
}

func s3ToolCallItem(callID, name, args string) aipapi.Item {
	return aipapi.Item{
		Kind:   aipapi.ItemKindToolCall,
		Status: aipapi.ItemStatusCompleted,
		ToolCall: &aipapi.ToolCallItem{
			CallID:    callID,
			Name:      name,
			Arguments: json.RawMessage(args),
		},
	}
}

func s3ToolResultItem(callID, name, output string) aipapi.Item {
	return aipapi.Item{
		Kind:   aipapi.ItemKindToolResult,
		Status: aipapi.ItemStatusCompleted,
		ToolResult: &aipapi.ToolResultItem{
			CallID: callID,
			Name:   name,
			Output: output,
		},
	}
}

func TestS3_ItemVisiblePrefixMatchesAndRefusesAmbiguity(t *testing.T) {
	t.Parallel()
	fresh := s3ItemCall(s3UserItem("read the file"))
	before, err := VisiblePrefixDigest(fresh)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := extendItemPrefixWithCall(before, "let me look", "call_1", "Read", json.RawMessage(`{"file_path":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	full := s3ItemCall(
		s3UserItem("read the file"),
		s3AssistantItem("let me look"),
		s3ToolCallItem("call_1", "Read", `{"file_path":"x"}`),
		s3ToolResultItem("call_1", "Read", "contents"),
	)
	got, found, err := VisiblePrefixThroughCall(full, "call_1")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if !PrefixesEqual(got, committed) {
		t.Fatal("the item history must reproduce the committed item prefix")
	}

	// Reordered history still carries the call, but the ordered digest differs.
	reordered := s3ItemCall(
		s3UserItem("read the file"),
		s3ToolCallItem("call_1", "Read", `{"file_path":"x"}`),
		s3AssistantItem("let me look"),
		s3ToolResultItem("call_1", "Read", "contents"),
	)
	got, found, err = VisiblePrefixThroughCall(reordered, "call_1")
	if err != nil || !found {
		t.Fatalf("reordered found=%v err=%v", found, err)
	}
	if PrefixesEqual(got, committed) {
		t.Fatal("a reordered item history must not reproduce the committed prefix")
	}

	// Omitting the assistant call removes the correlation anchor entirely.
	omitted := s3ItemCall(
		s3UserItem("read the file"),
		s3ToolResultItem("call_1", "Read", "contents"),
	)
	if _, found, err := VisiblePrefixThroughCall(omitted, "call_1"); err != nil || found {
		t.Fatalf("omitted found=%v err=%v", found, err)
	}

	// Duplicating the opaque call id cannot establish one authority.
	duplicate := s3ItemCall(
		s3UserItem("read the file"),
		s3ToolCallItem("call_1", "Read", `{"file_path":"x"}`),
		s3ToolCallItem("call_1", "Read", `{"file_path":"x"}`),
		s3ToolResultItem("call_1", "Read", "contents"),
	)
	if _, found, err := VisiblePrefixThroughCall(duplicate, "call_1"); err != nil || found {
		t.Fatalf("duplicate found=%v err=%v", found, err)
	}

	// An item reference has no resolved content, so the connector cannot prove
	// the history it stands for.
	withReference := s3ItemCall(
		aipapi.Item{Kind: aipapi.ItemKindItemReference, ID: "item_1", Reference: &aipapi.ItemReference{ID: "prior_1"}},
		s3UserItem("read the file"),
		s3AssistantItem("let me look"),
		s3ToolCallItem("call_1", "Read", `{"file_path":"x"}`),
		s3ToolResultItem("call_1", "Read", "contents"),
	)
	_, _, err = VisiblePrefixThroughCall(withReference, "call_1")
	var continuationErr *ContinuationError
	if !errors.As(err, &continuationErr) || continuationErr.Code != CodeHistoryUnavailable {
		t.Fatalf("item reference err=%v", err)
	}
}

func TestS3_TrailingResultsItemAuthority(t *testing.T) {
	t.Parallel()
	one := s3ItemCall(
		s3UserItem("read the file"),
		s3ToolCallItem("call_1", "Read", `{}`),
		s3ToolResultItem("call_1", "Read", "done"),
	)
	results, ok := TrailingResults(one)
	if !ok || len(results) != 1 || results[0].CallID != "call_1" || results[0].Text != "done" {
		t.Fatalf("results=%+v ok=%v", results, ok)
	}

	two := s3ItemCall(
		s3UserItem("read the file"),
		s3ToolResultItem("call_1", "Read", "a"),
		s3ToolResultItem("call_2", "Bash", "b"),
	)
	results, ok = TrailingResults(two)
	if !ok || len(results) != 2 {
		t.Fatalf("parallel results=%+v ok=%v", results, ok)
	}

	after := s3ItemCall(
		s3UserItem("read the file"),
		s3ToolResultItem("call_1", "Read", "a"),
		s3AssistantItem("next"),
	)
	if _, ok := TrailingResults(after); ok {
		t.Fatal("content after the result must not be a trailing result group")
	}

	// An earlier completed result is history; only the final item is the new
	// continuation candidate.
	historical := s3ItemCall(
		s3UserItem("read the file"),
		s3ToolCallItem("call_1", "Read", `{}`),
		s3ToolResultItem("call_1", "Read", "old"),
		s3AssistantItem("got:old"),
		s3ToolCallItem("call_2", "Read", `{}`),
		s3ToolResultItem("call_2", "Read", "new"),
	)
	results, ok = TrailingResults(historical)
	if !ok || len(results) != 1 || results[0].CallID != "call_2" {
		t.Fatalf("historical results=%+v ok=%v", results, ok)
	}
}

func TestS3_ClassifyItemContinuation(t *testing.T) {
	t.Parallel()
	ready := s3ItemCall(
		s3UserItem("read the file"),
		s3AssistantItem("let me look"),
		s3ToolCallItem("call_1", "Read", `{}`),
		s3ToolResultItem("call_1", "Read", "done"),
	)
	if shape := ClassifyContinuation(ready, "call_1"); shape != ContinuationReady {
		t.Fatalf("ready shape=%v", shape)
	}

	mixed := s3ItemCall(
		s3UserItem("read the file"),
		s3ToolCallItem("call_1", "Read", `{}`),
		s3AssistantItem("also check this"),
		s3ToolResultItem("call_1", "Read", "done"),
	)
	if shape := ClassifyContinuation(mixed, "call_1"); shape != ContinuationMixed {
		t.Fatalf("mixed shape=%v", shape)
	}

	duplicate := s3ItemCall(
		s3UserItem("read the file"),
		s3ToolCallItem("call_1", "Read", `{}`),
		s3ToolCallItem("call_1", "Read", `{}`),
		s3ToolResultItem("call_1", "Read", "done"),
	)
	if shape := ClassifyContinuation(duplicate, "call_1"); shape != ContinuationDuplicate {
		t.Fatalf("duplicate shape=%v", shape)
	}

	missing := s3ItemCall(
		s3UserItem("read the file"),
		s3ToolResultItem("call_1", "Read", "done"),
	)
	if shape := ClassifyContinuation(missing, "call_1"); shape != ContinuationNoCall {
		t.Fatalf("missing shape=%v", shape)
	}

	referenced := s3ItemCall(
		aipapi.Item{Kind: aipapi.ItemKindItemReference, ID: "item_1", Reference: &aipapi.ItemReference{ID: "prior_1"}},
		s3UserItem("read the file"),
		s3ToolCallItem("call_1", "Read", `{}`),
		s3ToolResultItem("call_1", "Read", "done"),
	)
	if shape := ClassifyContinuation(referenced, "call_1"); shape != ContinuationHistoryUnavailable {
		t.Fatalf("reference shape=%v", shape)
	}

	twoResults := s3ItemCall(
		s3UserItem("read the file"),
		s3ToolCallItem("call_1", "Read", `{}`),
		s3ToolResultItem("call_1", "Read", "a"),
		s3ToolResultItem("call_2", "Read", "b"),
	)
	if shape := ClassifyContinuation(twoResults, "call_1"); shape != ContinuationMixed {
		t.Fatalf("two-result shape=%v", shape)
	}
}

func s3NameOnlyPending(t *testing.T, args json.RawMessage) *PendingCall {
	t.Helper()
	base, err := VisiblePrefixDigest(freshCall("read the file"))
	if err != nil {
		t.Fatal(err)
	}
	prefixByName, err := ExtendPrefixWithCall(base, "let me look", "Read", "Read", args)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := argsDigest(args)
	if err != nil {
		t.Fatal(err)
	}
	return &PendingCall{
		CallID:       "toolu_01ABC",
		Name:         "Read",
		Args:         append(json.RawMessage(nil), args...),
		PrefixByName: prefixByName,
		ArgsDigest:   digest,
		Model:        "sonnet",
		ToolCount:    1,
	}
}

func s3NameOnlyContinuation(name, args, result string) aipapi.Call {
	return s3NameOnlyContinuationFor("read the file", "let me look", name, args, result)
}

func s3NameOnlyContinuationFor(prompt, assistantText, name, args, result string) aipapi.Call {
	return aipapi.Call{Messages: []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(prompt)}},
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
			aipapi.TextPart(assistantText),
			{Kind: aipapi.PartJSON, ToolName: name, Content: json.RawMessage(args)},
		}},
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{{
			Kind:       aipapi.PartToolResult,
			ToolCallID: name,
			ToolName:   name,
			Text:       result,
		}}},
	}}
}

func TestS3_MatchPendingNameOnly(t *testing.T) {
	t.Parallel()
	args := json.RawMessage(`{"file_path":"x"}`)
	pending := s3NameOnlyPending(t, args)
	call := s3NameOnlyContinuation("Read", string(args), `{"contents":"x"}`)
	proof, err := MatchPendingContinuation(call, pending, ToolResult{
		CallID: "Read",
		Name:   "Read",
		Text:   `{"contents":"x"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !PrefixesEqual(proof.Prefix, pending.PrefixByName) {
		t.Fatalf("proof=%+v", proof)
	}

	changedArgs := s3NameOnlyContinuation("Read", `{"file_path":"/etc/passwd"}`, "contents")
	if _, err := MatchPendingContinuation(changedArgs, pending, ToolResult{CallID: "Read", Name: "Read", Text: "contents"}); err == nil {
		t.Fatal("changed arguments must not match the parked name-only call")
	} else {
		var continuationErr *ContinuationError
		if !errors.As(err, &continuationErr) || continuationErr.Code != CodeContinuationMismatch {
			t.Fatalf("changed-args err=%v", err)
		}
	}

	omitted := aipapi.Call{Messages: []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("read the file")}},
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{{
			Kind:       aipapi.PartToolResult,
			ToolCallID: "Read",
			ToolName:   "Read",
			Text:       "contents",
		}}},
	}}
	if _, err := MatchPendingContinuation(omitted, pending, ToolResult{CallID: "Read", Name: "Read", Text: "contents"}); err == nil {
		t.Fatal("an omitted assistant call must not match")
	}

	if _, err := MatchPendingContinuation(call, pending, ToolResult{CallID: "Bash", Name: "Bash", Text: "contents"}); err == nil {
		t.Fatal("a different tool name must not match the parked call")
	}
}

func TestS3_MatchPendingItemPreviousResponseUnavailable(t *testing.T) {
	t.Parallel()
	fresh := s3ItemCall(s3UserItem("read the file"))
	base, err := VisiblePrefixDigest(fresh)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := extendItemPrefixWithCall(base, "let me look", "call_1", "Read", json.RawMessage(`{"file_path":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := argsDigest(json.RawMessage(`{"file_path":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	pending := &PendingCall{
		CallID:        "call_1",
		Name:          "Read",
		Args:          json.RawMessage(`{"file_path":"x"}`),
		Prefix:        committed,
		ArgsDigest:    digest,
		Model:         "sonnet",
		ToolCount:     1,
		ItemAuthority: true,
	}

	truncated := s3ItemCall(
		s3UserItem("read the file"),
		s3AssistantItem("let me look"),
		s3ToolResultItem("call_1", "Read", "contents"),
	)
	truncated.PreviousResponseID = "resp_parent"
	truncated.Extensions = map[string]json.RawMessage{"aip.continuation.materialized.v1": json.RawMessage(`true`)}
	_, err = MatchPendingContinuation(truncated, pending, ToolResult{CallID: "call_1", Name: "Read", Text: "contents"})
	var continuationErr *ContinuationError
	if !errors.As(err, &continuationErr) || continuationErr.Code != CodeHistoryUnavailable {
		t.Fatalf("truncated previous response err=%v", err)
	}

	referenced := s3ItemCall(
		aipapi.Item{Kind: aipapi.ItemKindItemReference, ID: "item_1", Reference: &aipapi.ItemReference{ID: "prior_1"}},
		s3UserItem("read the file"),
		s3AssistantItem("let me look"),
		s3ToolCallItem("call_1", "Read", `{"file_path":"x"}`),
		s3ToolResultItem("call_1", "Read", "contents"),
	)
	_, err = MatchPendingContinuation(referenced, pending, ToolResult{CallID: "call_1", Name: "Read", Text: "contents"})
	if !errors.As(err, &continuationErr) || continuationErr.Code != CodeHistoryUnavailable {
		t.Fatalf("item reference err=%v", err)
	}
}

func s3NameShape(t *testing.T, call aipapi.Call, name string) ContinuationShape {
	t.Helper()
	_, shape, err := NameContinuation(call, name)
	if err != nil {
		return ContinuationMixed
	}
	return shape
}

func TestS3_ClassifyNameContinuation(t *testing.T) {
	t.Parallel()
	ready := s3NameOnlyContinuation("Read", `{"file_path":"x"}`, "contents")
	if shape := s3NameShape(t, ready, "Read"); shape != ContinuationReady {
		t.Fatalf("ready shape=%v", shape)
	}

	mixed := aipapi.Call{Messages: []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("read the file")}},
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
			{Kind: aipapi.PartJSON, ToolName: "Read", Content: json.RawMessage(`{"file_path":"x"}`)},
			aipapi.TextPart("after the call"),
		}},
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{{
			Kind:       aipapi.PartToolResult,
			ToolCallID: "Read",
			ToolName:   "Read",
			Text:       "contents",
		}}},
	}}
	if shape := s3NameShape(t, mixed, "Read"); shape != ContinuationMixed {
		t.Fatalf("mixed shape=%v", shape)
	}

	unknown := aipapi.Call{Messages: []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("read the file")}},
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{{
			Kind:       aipapi.PartToolResult,
			ToolCallID: "Read",
			ToolName:   "Read",
			Text:       "contents",
		}}},
	}}
	if shape := s3NameShape(t, unknown, "Read"); shape != ContinuationNoCall {
		t.Fatalf("unknown shape=%v", shape)
	}

	wrongRole := aipapi.Call{Messages: []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("read the file")}},
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{{
			Kind:     aipapi.PartJSON,
			ToolName: "Read",
			Content:  json.RawMessage(`{"file_path":"x"}`),
		}}},
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{{
			Kind:       aipapi.PartToolResult,
			ToolCallID: "Read",
			ToolName:   "Read",
			Text:       "contents",
		}}},
	}}
	if shape := s3NameShape(t, wrongRole, "Read"); shape != ContinuationRoleMismatch {
		t.Fatalf("wrong-role shape=%v", shape)
	}
}

func s3StartNameSession(t *testing.T, m *Manager, key, prompt, mode string, ttl time.Duration, catalog *Catalog) {
	t.Helper()
	turn, err := m.Start(context.Background(), toolTestLocation(t), toolTestRuntime(t, mode), TurnRequest{
		RequestID:  "req-" + key,
		Model:      "sonnet",
		Prompt:     prompt,
		Catalog:    catalog,
		SessionKey: key,
		Prefix:     firstTextPrefix(t, freshCall(prompt)),
		PendingTTL: ttl,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drainTurn(t, turn); err != nil {
		t.Fatal(err)
	}
	_ = turn.Close()
}

func s3NameContinuationProof(t *testing.T, prompt, assistantText, name, args, result string) (aipapi.Call, NameProof) {
	t.Helper()
	call := s3NameOnlyContinuationFor(prompt, assistantText, name, args, result)
	proof, shape, err := NameContinuation(call, name)
	if err != nil {
		t.Fatal(err)
	}
	if shape != ContinuationReady {
		t.Fatalf("name continuation shape=%v", shape)
	}
	return call, proof
}

func s3ResumeName(t *testing.T, session *Session, proof NameProof, call aipapi.Call, result string, catalog *Catalog, requestID string) []aipapi.Event {
	t.Helper()
	pending, ok := session.PendingSnapshot()
	if !ok {
		t.Fatal("expected one pending name-only call")
	}
	transcript, err := VisiblePrefixDigest(call)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := session.Resume(context.Background(), ResumeSpec{
		CallID:           pending.CallID,
		NameOnly:         true,
		Model:            "sonnet",
		CatalogDigest:    catalog.Digest(),
		Prefix:           proof.Prefix,
		ArgsDigest:       proof.ArgsDigest,
		Result:           ToolResult{CallID: proof.Name, Name: proof.Name, Text: result},
		TranscriptPrefix: transcript,
	}, requestID)
	if err != nil {
		t.Fatal(err)
	}
	events, err := drainTurn(t, resumed)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func s3EventText(events []aipapi.Event) string {
	var text strings.Builder
	for _, event := range events {
		if event.Kind == aipapi.EventTextDelta {
			text.WriteString(event.Delta)
		}
	}
	return text.String()
}

func TestS3_NameOnlySessionParksThenReleasesExactlyOnce(t *testing.T) {
	t.Parallel()
	m := NewManager(nil, 5*time.Second, time.Minute, 5*time.Second, time.Now)
	catalog := toolTestCatalog(t, "Read")
	s3StartNameSession(t, m, "session-a", "read the file", "", 5*time.Second, catalog)

	call, proof := s3NameContinuationProof(t, "read the file", "let me look", "Read", `{"file_path":"x"}`, "contents")
	session, err := m.LookupName(proof, "session-a")
	if err != nil {
		t.Fatal(err)
	}
	events := s3ResumeName(t, session, proof, call, "contents", catalog, "req-2")
	if text := s3EventText(events); text != "got:contents" {
		t.Fatalf("text=%q", text)
	}
	if _, err := m.LookupName(proof, "session-a"); err == nil {
		t.Fatal("the released name-only call must not be releasable again")
	} else {
		var continuationErr *ContinuationError
		if !errors.As(err, &continuationErr) || continuationErr.Code != CodeContinuationConsumed {
			t.Fatalf("err=%v", err)
		}
	}
	_ = m.Close()
}

func TestS3_NameOnlyDuplicatePendingAcrossSessionsIsAmbiguous(t *testing.T) {
	t.Parallel()
	m := NewManager(nil, 5*time.Second, time.Minute, 5*time.Second, time.Now)
	catalog := toolTestCatalog(t, "Read")
	s3StartNameSession(t, m, "session-a", "read the file", "", 5*time.Second, catalog)
	s3StartNameSession(t, m, "session-b", "read the file", "", 5*time.Second, catalog)

	call, proof := s3NameContinuationProof(t, "read the file", "let me look", "Read", `{"file_path":"x"}`, "contents")
	if _, err := m.LookupName(proof, ""); err == nil {
		t.Fatal("two identical pending names without a trusted session must be ambiguous")
	} else {
		var continuationErr *ContinuationError
		if !errors.As(err, &continuationErr) || continuationErr.Code != CodeAmbiguousContinuation {
			t.Fatalf("err=%v", err)
		}
	}
	sessionA, err := m.LookupName(proof, "session-a")
	if err != nil {
		t.Fatal(err)
	}
	s3ResumeName(t, sessionA, proof, call, "contents", catalog, "req-a")

	// Session A's release must not hide session B: an authoritative session id
	// still resolves the surviving parked turn.
	if _, err := m.LookupName(proof, "session-b"); err != nil {
		t.Fatalf("session B was lost after session A released: %v", err)
	}
	// A stateless duplicate matches both the live B and the consumed A prefix;
	// without trusted binding it is ambiguous and must not release B.
	if _, err := m.LookupName(proof, ""); err == nil {
		t.Fatal("a stateless duplicate after a same-name release must be refused")
	} else {
		var continuationErr *ContinuationError
		if !errors.As(err, &continuationErr) || continuationErr.Code != CodeAmbiguousContinuation {
			t.Fatalf("stateless duplicate err=%v", err)
		}
	}
	_ = m.Close()
}

func TestS3_NameTombstoneDoesNotShadowDifferentPrefixSurvivor(t *testing.T) {
	t.Parallel()
	m := NewManager(nil, 5*time.Second, time.Minute, 5*time.Second, time.Now)
	catalog := toolTestCatalog(t, "Read")
	s3StartNameSession(t, m, "session-a", "read a", "unique_ids", 5*time.Second, catalog)
	s3StartNameSession(t, m, "session-b", "read b", "unique_ids", 5*time.Second, catalog)

	callA, proofA := s3NameContinuationProof(t, "read a", "let me look", "Read", `{"file_path":"x"}`, "contents-a")
	callB, proofB := s3NameContinuationProof(t, "read b", "let me look", "Read", `{"file_path":"x"}`, "contents-b")
	if PrefixesEqual(proofA.Prefix, proofB.Prefix) {
		t.Fatal("the fixture prefixes must differ")
	}
	sessionA, err := m.LookupName(proofA, "session-a")
	if err != nil {
		t.Fatal(err)
	}
	if text := s3EventText(s3ResumeName(t, sessionA, proofA, callA, "contents-a", catalog, "req-a")); text != "got:contents-a" {
		t.Fatalf("A text=%q", text)
	}

	// A genuine replay is still typed as consumed, even though B is live.
	if _, err := m.LookupName(proofA, ""); err == nil {
		t.Fatal("A replay must be refused")
	} else {
		var continuationErr *ContinuationError
		if !errors.As(err, &continuationErr) || continuationErr.Code != CodeContinuationConsumed {
			t.Fatalf("A replay err=%v", err)
		}
	}
	// B's distinct prefix is not shadowed by A's consumed marker.
	sessionB, err := m.LookupName(proofB, "")
	if err != nil {
		t.Fatalf("B was shadowed by A's tombstone: %v", err)
	}
	if text := s3EventText(s3ResumeName(t, sessionB, proofB, callB, "contents-b", catalog, "req-b")); text != "got:contents-b" {
		t.Fatalf("B text=%q", text)
	}
	_ = m.Close()
}

func TestS3_ExpiredNameTombstoneDoesNotShadowDifferentPrefixSurvivor(t *testing.T) {
	t.Parallel()
	now := time.Now()
	m := NewManager(nil, time.Minute, time.Minute, 5*time.Second, func() time.Time { return now })
	catalog := toolTestCatalog(t, "Read")
	// A expires on the fake clock; its ten-second real timer cannot fire during
	// the test, so the explicit Resume below is the deterministic expiry trigger.
	s3StartNameSession(t, m, "session-a", "read a", "unique_ids", 10*time.Second, catalog)
	s3StartNameSession(t, m, "session-b", "read b", "unique_ids", 30*time.Second, catalog)

	callA, proofA := s3NameContinuationProof(t, "read a", "let me look", "Read", `{"file_path":"x"}`, "contents-a")
	callB, proofB := s3NameContinuationProof(t, "read b", "let me look", "Read", `{"file_path":"x"}`, "contents-b")
	now = now.Add(11 * time.Second)

	sessionA, err := m.LookupName(proofA, "session-a")
	if err != nil {
		t.Fatal(err)
	}
	pendingA, ok := sessionA.PendingSnapshot()
	if !ok {
		t.Fatal("expected A pending")
	}
	transcriptA, err := VisiblePrefixDigest(callA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessionA.Resume(context.Background(), ResumeSpec{
		CallID:           pendingA.CallID,
		NameOnly:         true,
		Model:            "sonnet",
		CatalogDigest:    catalog.Digest(),
		Prefix:           proofA.Prefix,
		ArgsDigest:       proofA.ArgsDigest,
		Result:           ToolResult{CallID: "Read", Name: "Read", Text: "contents-a"},
		TranscriptPrefix: transcriptA,
	}, "req-a"); err == nil {
		t.Fatal("expected A to expire")
	} else {
		var continuationErr *ContinuationError
		if !errors.As(err, &continuationErr) || continuationErr.Code != CodeContinuationExpired {
			t.Fatalf("A expiry err=%v", err)
		}
	}

	if _, err := m.LookupName(proofA, ""); err == nil {
		t.Fatal("A replay must be refused as expired")
	} else {
		var continuationErr *ContinuationError
		if !errors.As(err, &continuationErr) || continuationErr.Code != CodeContinuationExpired {
			t.Fatalf("A replay err=%v", err)
		}
	}
	sessionB, err := m.LookupName(proofB, "")
	if err != nil {
		t.Fatalf("B was shadowed by A's expired tombstone: %v", err)
	}
	if text := s3EventText(s3ResumeName(t, sessionB, proofB, callB, "contents-b", catalog, "req-b")); text != "got:contents-b" {
		t.Fatalf("B text=%q", text)
	}
	_ = m.Close()
}

func TestS3_NewSameNamePendingDoesNotEraseReplayProtection(t *testing.T) {
	t.Parallel()
	m := NewManager(nil, 5*time.Second, time.Minute, 5*time.Second, time.Now)
	catalog := toolTestCatalog(t, "Read")
	s3StartNameSession(t, m, "session-a", "same prompt", "unique_ids", 5*time.Second, catalog)
	call, proof := s3NameContinuationProof(t, "same prompt", "let me look", "Read", `{"file_path":"x"}`, "old-result-a")
	sessionA, err := m.LookupName(proof, "session-a")
	if err != nil {
		t.Fatal(err)
	}
	s3ResumeName(t, sessionA, proof, call, "old-result-a", catalog, "req-a")

	// A new conversation with the same visible prefix but a different native id
	// must not erase A's replay evidence.
	s3StartNameSession(t, m, "session-b", "same prompt", "unique_ids", 5*time.Second, catalog)
	newCall, newProof := s3NameContinuationProof(t, "same prompt", "let me look", "Read", `{"file_path":"x"}`, "new-result-b")
	if !PrefixesEqual(proof.Prefix, newProof.Prefix) {
		t.Fatal("the fixture prefixes must be identical")
	}
	sessionB, err := m.LookupName(newProof, "session-b")
	if err != nil {
		t.Fatalf("trusted B lookup failed: %v", err)
	}
	if _, err := m.LookupName(proof, ""); err == nil {
		t.Fatal("A's old stateless result must not select B")
	} else {
		var continuationErr *ContinuationError
		if !errors.As(err, &continuationErr) || continuationErr.Code != CodeAmbiguousContinuation {
			t.Fatalf("A retry err=%v", err)
		}
	}
	// B remains parked and can be released only with its trusted session.
	if _, err := m.LookupName(newProof, "session-b"); err != nil {
		t.Fatalf("B was released or lost by A's retry: %v", err)
	}
	if text := s3EventText(s3ResumeName(t, sessionB, newProof, newCall, "new-result-b", catalog, "req-b")); text != "got:new-result-b" {
		t.Fatalf("B text=%q", text)
	}
	_ = m.Close()
}

func TestS3_NameContinuationSelectsOneOccurrence(t *testing.T) {
	t.Parallel()
	// The original occurrence is followed by added user content and a second
	// same-name call. Its suffix is dirty; the last occurrence is clean but its
	// prefix/arguments differ from the parked original.
	attack := aipapi.Call{Messages: []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("read the file")}},
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
			aipapi.TextPart("let me look"),
			{Kind: aipapi.PartJSON, ToolName: "Read", Content: json.RawMessage(`{"file_path":"x"}`)},
		}},
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("added user content")}},
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
			aipapi.TextPart("second"),
			{Kind: aipapi.PartJSON, ToolName: "Read", Content: json.RawMessage(`{"file_path":"y"}`)},
		}},
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{{
			Kind:       aipapi.PartToolResult,
			ToolCallID: "Read",
			ToolName:   "Read",
			Text:       "result-for-other-call",
		}}},
	}}
	proof, shape, err := NameContinuation(attack, "Read")
	if err != nil {
		t.Fatal(err)
	}
	if shape != ContinuationReady {
		t.Fatalf("attack shape=%v", shape)
	}
	original, originalProof := s3NameContinuationProof(t, "read the file", "let me look", "Read", `{"file_path":"x"}`, "contents")
	if PrefixesEqual(proof.Prefix, originalProof.Prefix) {
		t.Fatal("selection used the original occurrence with a conflicting suffix")
	}
	pending := s3NameOnlyPending(t, json.RawMessage(`{"file_path":"x"}`))
	if err := MatchNamePending(pending, proof); err == nil {
		t.Fatal("the selected later occurrence must not match the original parked call")
	}
	if _, _, err := NameContinuation(original, "Read"); err != nil {
		t.Fatal(err)
	}
}

func TestS3_ToolArgumentStringAndObjectFormsMatch(t *testing.T) {
	t.Parallel()
	object := json.RawMessage(`{"file_path":"x"}`)
	wireString := json.RawMessage(`"{\"file_path\":\"x\"}"`)
	objectDigest, err := argsDigest(object)
	if err != nil {
		t.Fatal(err)
	}
	stringDigest, err := argsDigest(wireString)
	if err != nil {
		t.Fatal(err)
	}
	if objectDigest != stringDigest {
		t.Fatal("the Responses wire string form must digest as the same argument document")
	}
	emptyString, err := argsDigest(json.RawMessage(`""`))
	if err != nil {
		t.Fatal(err)
	}
	emptyObject, err := argsDigest(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if emptyString != emptyObject {
		t.Fatal(`an empty wire argument string must digest as "{}"`)
	}

	fresh := s3ItemCall(s3UserItem("read the file"))
	base, err := VisiblePrefixDigest(fresh)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := extendItemPrefixWithCall(base, "let me look", "call_1", "Read", object)
	if err != nil {
		t.Fatal(err)
	}
	continuation := s3ItemCall(
		s3UserItem("read the file"),
		s3AssistantItem("let me look"),
		s3ToolCallItem("call_1", "Read", string(wireString)),
		s3ToolResultItem("call_1", "Read", "contents"),
	)
	got, found, err := VisiblePrefixThroughCall(continuation, "call_1")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if !PrefixesEqual(got, committed) {
		t.Fatal("the wire string argument form must reproduce the committed object prefix")
	}
}

func TestS3_AmbiguousCallMarkerIsRemovedWhenOwnersLeave(t *testing.T) {
	t.Parallel()
	now := time.Now()
	m := NewManager(nil, 5*time.Second, time.Minute, 5*time.Second, func() time.Time { return now })
	catalog := toolTestCatalog(t, "Read")
	start := func(key string) {
		turn, err := m.Start(context.Background(), toolTestLocation(t), toolTestRuntime(t, ""), TurnRequest{
			RequestID: "req-" + key, Model: "sonnet", Prompt: "read the file", Catalog: catalog,
			SessionKey: key, Prefix: firstTextPrefix(t, freshCall("read the file")), PendingTTL: 5 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := drainTurn(t, turn); err != nil {
			t.Fatal(err)
		}
		_ = turn.Close()
	}
	// The fixture uses one opaque id for both conversations, creating the nil
	// collision marker.
	start("session-a")
	start("session-b")
	if _, err := m.Lookup("toolu_01ABC", ""); err == nil {
		t.Fatal("the shared opaque id must be ambiguous")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	now = now.Add(11 * time.Second)
	if _, err := m.Lookup("toolu_01ABC", ""); !errors.Is(err, ErrNoPending) {
		t.Fatalf("stale ambiguity marker survived owner removal: %v", err)
	}
}
