package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Claim: the same Codex-style history through two candidates is downgraded
// per attempt: the lossless candidate receives the exact reasoning item with
// no skip recorded, and the non-replaying candidate receives the stripped
// clone (five messages, tool-output order preserved) with exactly one
// backend-labelled skip; under reject the non-replaying candidate still fails
// with the 400-class missing-reasoning_replay reject and opens no further
// attempt.
// Source: .kiro/specs/archive/client-reasoning-unrepresentable-policy/
// requirements.md 1.1, 1.2, 1.5, 2.1, 4.1, 4.2.

func clientReasoningHistoryCall(selector string) *aipapi.Call {
	toolCall := func(id string) aipapi.Part {
		return aipapi.Part{Kind: aipapi.PartJSON, ToolCallID: id, ToolName: "w", Content: []byte(`{"type":"function_call","call_id":"` + id + `"}`)}
	}
	return &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: selector},
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIChatCompletions,
			DeliveryMode: aipapi.DeliveryModeNonStreaming,
		},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("start")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{toolCall("cA")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
				{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1, Text: "client-thought", Opaque: json.RawMessage(`{"id":"rs_1"}`)}},
			}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{toolCall("cB")}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "cA", Content: []byte(`"ok-a"`)}}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "cB", Content: []byte(`"ok-b"`)}}},
		},
		Tools: []aipapi.ToolDef{{Name: "w", Parameters: []byte(`{}`)}},
	}
}

type capturedBackend struct {
	calls atomic.Int64
	got   aipapi.Call
}

func (c *capturedBackend) open(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
	c.calls.Add(1)
	c.got = call
	return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
}

func clientReasoningExecutor(t *testing.T, lossless, profile *capturedBackend) *Executor {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	transport := aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
		Operation: aipapi.OperationOpenAIChatCompletions,
		Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
	})
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"lossless": {
			Caps:          aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools, aipapi.CapabilityReasoningReplay),
			ReplaySupport: aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIResponsesItemV1}},
			DialectSupport: aipapi.DialectSupport{
				ReasoningDialects: []aipapi.DialectRequirement{{Kind: "reasoning", Dialect: string(aipapi.ReasoningDialectOpenAIResponsesItemV1)}},
			},
			TransportCaps: transport,
			Open:          lossless.open,
		},
		"profile": {
			Caps:          aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
			TransportCaps: transport,
			Open:          profile.open,
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	return ex
}

func TestExecute_ClientReasoningStripsOnlyNonReplayingCandidate(t *testing.T) {
	t.Parallel()
	var lossless, profile capturedBackend
	ex := clientReasoningExecutor(t, &lossless, &profile)
	sink := &skipRecordingSink{}
	ex.Metrics = sink

	stream, err := ex.Execute(context.Background(), clientReasoningHistoryCall("lossless:m"))
	if err != nil {
		t.Fatalf("lossless execute: %v", err)
	}
	// Opens (asserted below) are the proof; fixed-stream collection quirks
	// are irrelevant here.
	_, _ = aipapi.Collect(context.Background(), stream)
	if lossless.calls.Load() != 1 {
		t.Fatalf("lossless backend must open once, got %d", lossless.calls.Load())
	}
	foundExact := false
	for _, m := range lossless.got.Messages {
		for _, p := range m.Parts {
			if p.Kind == aipapi.PartReasoning && p.Reasoning != nil &&
				p.Reasoning.Dialect == aipapi.ReasoningDialectOpenAIResponsesItemV1 &&
				string(p.Reasoning.Opaque) == `{"id":"rs_1"}` {
				foundExact = true
			}
		}
	}
	if !foundExact {
		t.Fatalf("lossless candidate must receive the exact reasoning item: %+v", lossless.got.Messages)
	}
	if sink.skips.Load() != 0 {
		t.Fatal("lossless candidate must record no skip")
	}

	stream, err = ex.Execute(context.Background(), clientReasoningHistoryCall("profile:m"))
	if err != nil {
		t.Fatalf("profile execute: %v", err)
	}
	_, _ = aipapi.Collect(context.Background(), stream)
	if profile.calls.Load() != 1 {
		t.Fatalf("profile backend must open once, got %d", profile.calls.Load())
	}
	if len(profile.got.Messages) != 5 {
		t.Fatalf("profile candidate must receive the stripped clone (5 messages), got %d", len(profile.got.Messages))
	}
	var gotCalls []string
	for _, m := range profile.got.Messages {
		for _, p := range m.Parts {
			if p.Kind == aipapi.PartReasoning {
				t.Fatalf("profile candidate must not receive reasoning: %+v", m)
			}
			if p.Kind == aipapi.PartJSON && p.ToolCallID != "" {
				gotCalls = append(gotCalls, p.ToolCallID)
			}
		}
	}
	if len(gotCalls) != 2 || gotCalls[0] != "cA" || gotCalls[1] != "cB" {
		t.Fatalf("stripped order must be preserved, got %v", gotCalls)
	}
	// Exactly one skip per stripped attempt, labelled by backend; this also
	// pins clone-only accounting end to end (see mutant note in report).
	if sink.skips.Load() != 1 {
		t.Fatalf("one stripped attempt must record exactly one skip, got %d", sink.skips.Load())
	}
	if got, _ := sink.backend.Load().(string); got != "profile" {
		t.Fatalf("skip must be labelled by backend, got %q", got)
	}

	// Strict leg (req 1.2): under reject the profile route fails as today.
	ex.AttemptPolicy = aipapi.AttemptReject
	_, rejectErr := ex.Execute(context.Background(), clientReasoningHistoryCall("profile:m"))
	if rejectErr == nil {
		t.Fatal("reject policy must fail the profile route exactly as today")
	}
	if !errors.Is(rejectErr, aipapi.ErrCapabilityReject) {
		t.Fatalf("reject must surface the missing reasoning_replay capability, got %T: %v", rejectErr, rejectErr)
	}
	if !aipapi.IsReject(rejectErr) {
		t.Fatalf("reject must stay a 400-class client reject, got %T: %v", rejectErr, rejectErr)
	}
	if profile.calls.Load() != 1 {
		t.Fatalf("rejected candidate must never open beyond the stripped attempt, got %d", profile.calls.Load())
	}
}
