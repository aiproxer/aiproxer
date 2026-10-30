package acp

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestSerializeTranscript_SingleUser(t *testing.T) {
	t.Parallel()
	msgs := []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Hello")}},
	}
	got := serializeTranscript(msgs)
	if !strings.Contains(got, "Hello") {
		t.Fatalf("expected transcript to contain 'Hello', got: %s", got)
	}
	if strings.Contains(got, "Previous Context") {
		t.Fatal("single message should not have Previous Context")
	}
}

func TestSerializeTranscript_MultiTurn(t *testing.T) {
	t.Parallel()
	msgs := []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("First question")}},
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("First answer")}},
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Second question")}},
	}
	got := serializeTranscript(msgs)
	if !strings.Contains(got, "Previous Context") {
		t.Fatal("multi-turn should have Previous Context")
	}
	if !strings.Contains(got, "First question") {
		t.Fatal("missing first question")
	}
	if !strings.Contains(got, "First answer") {
		t.Fatal("missing first answer")
	}
	if !strings.Contains(got, "Second question") {
		t.Fatal("missing second question")
	}
}

func TestSerializeTranscript_NoUserMessage(t *testing.T) {
	t.Parallel()
	msgs := []aipapi.Message{
		{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("System prompt")}},
	}
	got := serializeTranscript(msgs)
	if !strings.Contains(got, "System prompt") {
		t.Fatalf("expected transcript to contain system prompt, got: %s", got)
	}
}

func TestSerializeTranscript_Empty(t *testing.T) {
	t.Parallel()
	got := serializeTranscript(nil)
	if got != "" {
		t.Fatalf("expected empty transcript, got: %s", got)
	}
}

func TestSerializeTranscriptTail(t *testing.T) {
	t.Parallel()
	msgs := []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("msg1")}},
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("msg2")}},
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("msg3")}},
	}
	got := serializeTranscriptTail(msgs, 2)
	if !strings.Contains(got, "msg3") {
		t.Fatalf("expected tail to contain msg3, got: %s", got)
	}
	if strings.Contains(got, "msg1") {
		t.Fatal("tail should not contain msg1")
	}
}

func TestSerializeTranscriptTail_BeyondEnd(t *testing.T) {
	t.Parallel()
	msgs := []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("msg1")}},
	}
	got := serializeTranscriptTail(msgs, 5)
	if got != "" {
		t.Fatalf("expected empty tail, got: %s", got)
	}
}

func TestHashMessagesPrefix_Stable(t *testing.T) {
	t.Parallel()
	msgs := []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}},
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("world")}},
	}
	h1 := hashMessagesPrefix(msgs, 2)
	h2 := hashMessagesPrefix(msgs, 2)
	if h1 != h2 {
		t.Fatal("same messages should produce same hash")
	}
}

func TestHashMessagesPrefix_DifferentMessages(t *testing.T) {
	t.Parallel()
	msgs1 := []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}},
	}
	msgs2 := []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("goodbye")}},
	}
	h1 := hashMessagesPrefix(msgs1, 1)
	h2 := hashMessagesPrefix(msgs2, 1)
	if h1 == h2 {
		t.Fatal("different messages should produce different hashes")
	}
}

func TestHashMessagesPrefix_PartialHash(t *testing.T) {
	t.Parallel()
	msgs := []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("msg1")}},
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("msg2")}},
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("msg3")}},
	}
	h1 := hashMessagesPrefix(msgs, 2)
	h2 := hashMessagesPrefix(msgs, 3)
	if h1 == h2 {
		t.Fatal("different prefix lengths should produce different hashes")
	}
}

func TestExtractLastUserMessage(t *testing.T) {
	t.Parallel()
	msgs := []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("first")}},
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("reply")}},
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("second")}},
	}
	got := extractLastUserMessage(msgs)
	if got != "second" {
		t.Fatalf("got %q, want 'second'", got)
	}
}

func TestExtractLastUserMessage_NoUser(t *testing.T) {
	t.Parallel()
	msgs := []aipapi.Message{
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("reply")}},
	}
	got := extractLastUserMessage(msgs)
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestComputeHistory_FreshProcess(t *testing.T) {
	t.Parallel()
	hc := &TranscriptHistoryCoordinator{}
	msgs := []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}},
	}
	result := hc.ComputeHistoryAndUserMessage(msgs, historyState{})
	if result.UserMessage == "" {
		t.Fatal("expected non-empty user message for fresh process")
	}
	if !strings.Contains(result.UserMessage, "hello") {
		t.Fatalf("expected message to contain 'hello', got: %s", result.UserMessage)
	}
	if result.HistoryState.messageCount != 1 {
		t.Fatalf("expected messageCount=1, got %d", result.HistoryState.messageCount)
	}
	if result.ResetNeeded {
		t.Fatal("fresh process must not request reset")
	}
}

func TestComputeHistory_AppendOnly(t *testing.T) {
	t.Parallel()
	hc := &TranscriptHistoryCoordinator{}
	msgs := []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("first")}},
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("reply")}},
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("second")}},
	}
	priorState := historyState{
		messageCount: 2,
		prefixHash:   hashMessagesPrefix(msgs, 2),
	}
	result := hc.ComputeHistoryAndUserMessage(msgs, priorState)
	if !strings.Contains(result.UserMessage, "second") {
		t.Fatalf("expected tail to contain 'second', got: %s", result.UserMessage)
	}
	if strings.Contains(result.UserMessage, "first") {
		t.Fatal("append-only tail should not contain 'first'")
	}
	if result.HistoryState.messageCount != 3 {
		t.Fatalf("expected messageCount=3, got %d", result.HistoryState.messageCount)
	}
	if result.ResetNeeded {
		t.Fatal("append-only must not request reset")
	}
}

func TestComputeHistory_SameMessages(t *testing.T) {
	t.Parallel()
	hc := &TranscriptHistoryCoordinator{}
	msgs := []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("same question")}},
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("same answer")}},
	}
	priorState := historyState{
		messageCount: 2,
		prefixHash:   hashMessagesPrefix(msgs, 2),
	}
	result := hc.ComputeHistoryAndUserMessage(msgs, priorState)
	// Same message count — should extract just the last user message.
	if result.UserMessage != "same question" {
		t.Fatalf("got %q, want 'same question'", result.UserMessage)
	}
	if result.HistoryState != priorState {
		t.Fatal("state should be unchanged for same messages")
	}
	if result.ResetNeeded {
		t.Fatal("same-messages retry must not request reset")
	}
}

func TestComputeHistory_Diverged(t *testing.T) {
	t.Parallel()
	hc := &TranscriptHistoryCoordinator{}
	msgs := []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("edited question")}},
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("answer")}},
	}
	// Prior state has a different prefix hash → divergence.
	priorState := historyState{
		messageCount: 2,
		prefixHash:   "different_hash_value_here",
	}
	result := hc.ComputeHistoryAndUserMessage(msgs, priorState)
	if !strings.Contains(result.UserMessage, "edited question") {
		t.Fatalf("expected full transcript on divergence, got: %s", result.UserMessage)
	}
	if result.HistoryState.messageCount != 2 {
		t.Fatalf("expected messageCount=2, got %d", result.HistoryState.messageCount)
	}
	if !result.ResetNeeded {
		t.Fatal("divergence must request reset")
	}
}

func TestComputeHistory_Shrunk(t *testing.T) {
	t.Parallel()
	hc := &TranscriptHistoryCoordinator{}
	msgs := []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("only one now")}},
	}
	// Prior state thought there were 3 messages — history shrank.
	priorState := historyState{
		messageCount: 3,
		prefixHash:   hashMessagesPrefix(msgs, 1), // correct hash but wrong count
	}
	result := hc.ComputeHistoryAndUserMessage(msgs, priorState)
	// len(messages) < n → should serialize full transcript and request reset.
	if !strings.Contains(result.UserMessage, "only one now") {
		t.Fatalf("expected full transcript on shrink, got: %s", result.UserMessage)
	}
	if !result.ResetNeeded {
		t.Fatal("shrink must request reset")
	}
}

func TestRoleLabel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		role aipapi.Role
		want string
	}{
		{aipapi.RoleUser, "User"},
		{aipapi.RoleAssistant, "Assistant"},
		{aipapi.RoleSystem, "System"},
		{aipapi.RoleTool, "Tool"},
	}
	for _, c := range cases {
		if got := roleLabel(c.role); got != c.want {
			t.Errorf("roleLabel(%q) = %q, want %q", c.role, got, c.want)
		}
	}
}

func TestNewHistoryState(t *testing.T) {
	t.Parallel()
	msgs := []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("test")}},
	}
	state := newHistoryState(msgs)
	if state.messageCount != 1 {
		t.Fatalf("messageCount = %d, want 1", state.messageCount)
	}
	if state.prefixHash == "" {
		t.Fatal("expected non-empty prefixHash")
	}
}

func TestFreshHistoryState_IsZero(t *testing.T) {
	t.Parallel()
	s := FreshHistoryState()
	if s.messageCount != 0 || s.prefixHash != "" {
		t.Fatalf("FreshHistoryState must be zero, got %+v", s)
	}
}

func TestPrepareTranscriptCall_NilReturnsNil(t *testing.T) {
	t.Parallel()
	if got := prepareTranscriptCall(nil, "anything"); got != nil {
		t.Fatalf("expected nil for nil orig, got %+v", got)
	}
}

func TestPrepareTranscriptCall_EmptyUserMessagePreservesMessages(t *testing.T) {
	t.Parallel()
	orig := &aipapi.Call{
		ID: "c1",
		Messages: []aipapi.Message{
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("no user here")}},
		},
		Instructions: []aipapi.Message{{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("be brief")}}},
	}
	got := prepareTranscriptCall(orig, "")
	if len(got.Messages) != len(orig.Messages) {
		t.Fatalf("empty userMessage must preserve messages: got %d, want %d", len(got.Messages), len(orig.Messages))
	}
	if len(got.Instructions) != len(orig.Instructions) {
		t.Fatal("Instructions must be preserved")
	}
	if got.ID != orig.ID {
		t.Fatal("ID must be preserved")
	}
}

func TestPrepareTranscriptCall_NonEmptyReplacesWithSingleUserMessage(t *testing.T) {
	t.Parallel()
	orig := &aipapi.Call{
		ID: "c2",
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("first")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("reply")}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("second")}},
		},
	}
	got := prepareTranscriptCall(orig, "Previous Context:\n\n**User:** first\n\n---\n\n**User:** second")
	if len(got.Messages) != 1 {
		t.Fatalf("expected single replacement message, got %d", len(got.Messages))
	}
	if got.Messages[0].Role != aipapi.RoleUser {
		t.Fatalf("expected user role, got %q", got.Messages[0].Role)
	}
	if len(got.Messages[0].Parts) != 1 || got.Messages[0].Parts[0].Kind != aipapi.PartText {
		t.Fatalf("expected single text part, got %+v", got.Messages[0].Parts)
	}
	if !strings.Contains(got.Messages[0].Parts[0].Text, "second") {
		t.Fatalf("expected transcript text in part, got %q", got.Messages[0].Parts[0].Text)
	}
	if got.ID != orig.ID {
		t.Fatal("ID must be preserved on transcript replacement")
	}
}
