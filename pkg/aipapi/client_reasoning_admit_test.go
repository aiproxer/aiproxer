package aipapi_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Claim: attempt policy through admission (req 1.1, 1.2, 1.5) — downgrade
// under log_skip, reject under reject/empty, lossless untouched, other gaps
// hard. Mutant-checked (see report).

// codexStyleCall mirrors the real openairesponses frontend decode output for a
// Codex-style body: legacy messages with one reasoning part between two tool
// calls plus their outputs.
func codexStyleCall() aipapi.Call {
	toolCall := func(id string) aipapi.Part {
		return aipapi.Part{Kind: aipapi.PartJSON, ToolCallID: id, ToolName: "w", Content: []byte(`{"type":"function_call","call_id":"` + id + `"}`)}
	}
	return aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("start")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{toolCall("cA")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
				{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1, Text: "thought", Opaque: json.RawMessage(`{"id":"rs_1"}`)}},
			}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{toolCall("cB")}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "cA", Content: []byte(`"ok-a"`)}}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "cB", Content: []byte(`"ok-b"`)}}},
		},
		Tools: []aipapi.ToolDef{{Name: "w", Parameters: []byte(`{}`)}},
	}
}

// profileCandidateCaps mirrors a catalog profile backend (e.g.
// deepseek-responses): streaming + tools, no reasoning_replay, no declared
// reasoning dialects.
func profileCandidateCaps() aipapi.BackendCaps {
	return aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
}

func admitWithPolicy(call aipapi.Call, caps aipapi.BackendCaps, replay aipapi.ReasoningReplaySupport, dialect aipapi.DialectSupport, policy aipapi.AttemptPolicy) aipapi.CandidateAdmissionResult {
	frozen := aipapi.DeriveProtocolRequirements(call)
	return aipapi.AdmitCandidate(aipapi.CandidateAdmissionInput{
		Call:               call,
		BackendCaps:        caps,
		ReplaySupport:      replay,
		DialectSupport:     dialect,
		ProjectionTarget:   aipapi.LegacyProjectionTargetFromCaps(caps, replay),
		FrozenRequirements: &frozen,
		AttemptPolicy:      policy,
	})
}

func TestAdmitCandidate_AttemptPolicy(t *testing.T) {
	t.Parallel()
	call := codexStyleCall()
	if !slices.Contains(aipapi.RequiredCapabilities(call), aipapi.CapabilityReasoningReplay) {
		t.Fatal("fixture must require reasoning_replay")
	}

	t.Run("log_skip downgrades", func(t *testing.T) {
		t.Parallel()
		got := admitWithPolicy(call, profileCandidateCaps(), aipapi.ReasoningReplaySupport{}, aipapi.DialectSupport{}, aipapi.AttemptLogSkip)
		if got.Kind != aipapi.NegotiationDowngrade {
			t.Fatalf("log_skip must downgrade unrepresentable client reasoning, got kind=%s err=%v", got.Kind, got.Err())
		}
		if !slices.Contains(got.Capability.Downgraded, aipapi.CapabilityReasoningReplay) {
			t.Fatalf("downgrade must name reasoning_replay, got %+v", got.Capability)
		}
		clone := aipapi.CloneCall(call)
		if !aipapi.StripReasoningForAttempt(&clone) {
			t.Fatal("reasoning_replay downgrade must strip reasoning from the attempt clone")
		}
		for _, m := range clone.Messages {
			for _, p := range m.Parts {
				if p.Kind == aipapi.PartReasoning {
					t.Fatalf("downgrade strip must remove reasoning: %+v", m)
				}
			}
		}
		if err := clone.Validate(); err != nil {
			t.Fatalf("stripped clone must stay valid: %v", err)
		}
	})

	for _, policy := range []aipapi.AttemptPolicy{aipapi.AttemptReject, ""} {
		t.Run("reject stays hard", func(t *testing.T) {
			t.Parallel()
			got := admitWithPolicy(call, profileCandidateCaps(), aipapi.ReasoningReplaySupport{}, aipapi.DialectSupport{}, policy)
			if got.Kind != aipapi.NegotiationReject {
				t.Fatalf("policy %q must preserve today's reject, got kind=%s", policy, got.Kind)
			}
		})
	}

	t.Run("lossless candidate unaffected", func(t *testing.T) {
		t.Parallel()
		caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools, aipapi.CapabilityReasoningReplay)
		replay := aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIResponsesItemV1}}
		dialect := aipapi.DialectSupport{
			ReasoningDialects: []aipapi.DialectRequirement{{Kind: "reasoning", Dialect: string(aipapi.ReasoningDialectOpenAIResponsesItemV1)}},
		}
		for _, policy := range []aipapi.AttemptPolicy{aipapi.AttemptLogSkip, aipapi.AttemptReject, ""} {
			got := admitWithPolicy(call, caps, replay, dialect, policy)
			if got.Kind != aipapi.NegotiationLossless {
				t.Fatalf("policy %q must stay lossless for a replay-capable candidate, got kind=%s err=%v", policy, got.Kind, got.Err())
			}
		}
	})

	t.Run("other gaps stay hard", func(t *testing.T) {
		t.Parallel()
		withVision := codexStyleCall()
		withVision.Messages = append(withVision.Messages, aipapi.Message{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartImageRef, ImageRef: "img-1"}},
		})
		got := admitWithPolicy(withVision, profileCandidateCaps(), aipapi.ReasoningReplaySupport{}, aipapi.DialectSupport{}, aipapi.AttemptLogSkip)
		if got.Kind != aipapi.NegotiationReject {
			t.Fatalf("vision gap must stay hard under log_skip, got kind=%s", got.Kind)
		}
	})
}
