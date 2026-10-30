package aipapi_test

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestProjectItemsToLegacyView_portableMessageTrajectory(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Items: []aipapi.Item{
			{
				Kind:   aipapi.ItemKindMessage,
				ID:     "inst-0",
				Status: aipapi.ItemStatusCompleted,
				Role:   aipapi.RoleSystem,
				Content: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartText, Text: "You are helpful."},
				},
			},
			{
				Kind:   aipapi.ItemKindMessage,
				ID:     "msg-1",
				Status: aipapi.ItemStatusCompleted,
				Role:   aipapi.RoleUser,
				Content: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartText, Text: "Hello"},
				},
			},
			{
				Kind:   aipapi.ItemKindToolCall,
				ID:     "call-1",
				Status: aipapi.ItemStatusCompleted,
				ToolCall: &aipapi.ToolCallItem{
					CallID:    "call_abc",
					Name:      "weather",
					Arguments: json.RawMessage(`{"city":"SF"}`),
				},
			},
			{
				Kind:   aipapi.ItemKindToolResult,
				ID:     "result-1",
				Status: aipapi.ItemStatusCompleted,
				ToolResult: &aipapi.ToolResultItem{
					CallID: "call_abc",
					Name:   "weather",
					Output: "72F",
				},
			},
		},
	}
	target := aipapi.DefaultLegacyProjectionTarget(
		aipapi.NewBackendCaps(aipapi.CapabilityTools, aipapi.CapabilityStreaming),
		aipapi.ReasoningReplaySupport{},
	)
	got, err := aipapi.ProjectItemsToLegacyView(call, target)
	if err != nil {
		t.Fatalf("ProjectItemsToLegacyView: %v", err)
	}
	if len(got.Instructions) != 1 || got.Instructions[0].Role != aipapi.RoleSystem {
		t.Fatalf("instructions=%#v", got.Instructions)
	}
	if len(got.Messages) != 3 {
		t.Fatalf("messages len=%d", len(got.Messages))
	}
	if got.Messages[1].Role != aipapi.RoleAssistant || got.Messages[1].Parts[0].ToolCallID != "call_abc" {
		t.Fatalf("tool call projection=%#v", got.Messages[1])
	}
	if got.Messages[2].Role != aipapi.RoleTool {
		t.Fatalf("tool result projection=%#v", got.Messages[2])
	}
}

func TestProjectItemsToLegacyView_rejectsPhaseBeforeBackend(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Items: []aipapi.Item{
			{
				Kind:   aipapi.ItemKindMessage,
				ID:     "msg-1",
				Status: aipapi.ItemStatusCompleted,
				Role:   aipapi.RoleAssistant,
				Phase:  aipapi.AssistantPhaseCommentary,
				Content: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartText, Text: "thinking"},
				},
			},
		},
	}
	target := aipapi.DefaultLegacyProjectionTarget(aipapi.NewBackendCaps(aipapi.CapabilityStreaming), aipapi.ReasoningReplaySupport{})
	_, err := aipapi.ProjectItemsToLegacyView(call, target)
	if !aipapi.IsProjectionError(err) {
		t.Fatalf("expected projection error, got %v", err)
	}
	var pe *aipapi.ProjectionError
	if !errors.As(err, &pe) || pe.Reason != aipapi.ProjectionReasonAssistantPhase {
		t.Fatalf("got %#v", err)
	}
}

func TestProjectItemsToLegacyView_rejectsPhaseDespiteCapabilityFlag(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Items: []aipapi.Item{{
			Kind:   aipapi.ItemKindMessage,
			ID:     "msg-1",
			Status: aipapi.ItemStatusCompleted,
			Role:   aipapi.RoleAssistant,
			Phase:  aipapi.AssistantPhaseFinalAnswer,
			Content: []aipapi.ContentPart{
				{Kind: aipapi.ContentPartText, Text: "answer"},
			},
		}},
	}
	target := aipapi.LegacyProjectionTargetFromCaps(
		aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityAssistantPhase),
		aipapi.ReasoningReplaySupport{},
	)
	if target.SupportsPhase {
		t.Fatal("legacy projection must not advertise phase support")
	}
	_, err := aipapi.ProjectItemsToLegacyView(call, target)
	if !aipapi.IsProjectionError(err) {
		t.Fatalf("expected projection error, got %v", err)
	}
}

func TestProjectItemsToLegacyView_rejectsItemReferenceNoNetwork(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Items: []aipapi.Item{
			{
				Kind:      aipapi.ItemKindItemReference,
				ID:        "ref-1",
				Status:    aipapi.ItemStatusCompleted,
				Reference: &aipapi.ItemReference{ID: "msg-prev"},
			},
		},
	}
	target := aipapi.DefaultLegacyProjectionTarget(aipapi.NewBackendCaps(aipapi.CapabilityStreaming), aipapi.ReasoningReplaySupport{})
	_, err := aipapi.ProjectItemsToLegacyView(call, target)
	if !aipapi.IsProjectionError(err) {
		t.Fatalf("expected projection error, got %v", err)
	}
}

func TestProjectLegacyToOrderedItems_preservesToolFlow(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
			{
				Role: aipapi.RoleAssistant,
				Parts: []aipapi.Part{{
					Kind:       aipapi.PartJSON,
					ToolCallID: "call_1",
					ToolName:   "weather",
					Content:    json.RawMessage(`{"city":"SF"}`),
				}},
			},
			{
				Role: aipapi.RoleTool,
				Parts: []aipapi.Part{{
					Kind:       aipapi.PartToolResult,
					ToolCallID: "call_1",
					ToolName:   "weather",
					Text:       "72F",
				}},
			},
		},
	}
	items, req, err := aipapi.ProjectLegacyToOrderedItems(call, aipapi.DefaultOrderedItemProjectionTarget())
	if err != nil {
		t.Fatalf("ProjectLegacyToOrderedItems: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items=%#v", items)
	}
	if items[1].Kind != aipapi.ItemKindToolCall || items[1].ToolCall.CallID != "call_1" {
		t.Fatalf("tool call item=%#v", items[1])
	}
	if items[2].Kind != aipapi.ItemKindToolResult {
		t.Fatalf("tool result item=%#v", items[2])
	}
	if !containsCapability(req.Capabilities, aipapi.CapabilityTools) {
		t.Fatalf("requirements=%#v", req)
	}
}

func TestProjectLegacyToOrderedItems_rejectsConflictingAuthority(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Items:    []aipapi.Item{{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "x"}}}},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("y")}}},
	}
	_, _, err := aipapi.ProjectLegacyToOrderedItems(call, aipapi.DefaultOrderedItemProjectionTarget())
	if err == nil {
		t.Fatal("expected error for conflicting authority")
	}
}

func TestDeriveProtocolRequirements_reasoningDialectAuthority(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleAssistant,
			Parts: []aipapi.Part{{
				Kind: aipapi.PartReasoning,
				Reasoning: &aipapi.ReasoningPart{
					Dialect: aipapi.ReasoningDialectOpenAIChatTextV1,
					Text:    "thought",
				},
			}},
		}},
	}
	req := aipapi.DeriveProtocolRequirements(call)
	if len(req.ReasoningDialects) != 1 || req.ReasoningDialects[0].Dialect != string(aipapi.ReasoningDialectOpenAIChatTextV1) {
		t.Fatalf("reasoning dialects=%#v", req.ReasoningDialects)
	}
	if !containsCapability(req.Capabilities, aipapi.CapabilityReasoningReplay) {
		t.Fatalf("caps=%#v", req.Capabilities)
	}
}

func TestMatchRequirements_rejectsMissingDialect(t *testing.T) {
	t.Parallel()

	required := aipapi.ProtocolRequirements{
		ReasoningDialects: []aipapi.DialectRequirement{{
			Kind:    "reasoning",
			Dialect: string(aipapi.ReasoningDialectOpenAIChatTextV1),
		}},
	}
	res := aipapi.MatchRequirements(required, aipapi.ProtocolRequirements{}, aipapi.ReasoningReplaySupport{})
	if res.Kind != aipapi.NegotiationReject {
		t.Fatalf("got %s", res.Kind)
	}
}

func TestAdmitCandidate_acceptsNativeItemReferenceBeforeNetwork(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Items: []aipapi.Item{
			{
				Kind:      aipapi.ItemKindItemReference,
				ID:        "ref-1",
				Status:    aipapi.ItemStatusCompleted,
				Reference: &aipapi.ItemReference{ID: "msg-prev"},
			},
		},
	}
	res := aipapi.AdmitCandidate(aipapi.CandidateAdmissionInput{
		Call: call,
		BackendCaps: aipapi.NewBackendCaps(
			aipapi.CapabilityStreaming,
			aipapi.CapabilityOrderedItems,
			aipapi.CapabilityItemReferences,
		),
		DialectSupport: aipapi.DialectSupport{
			ItemDialects: []aipapi.DialectRequirement{{Kind: "item", Dialect: "item_reference"}},
		},
		ProjectionTarget: aipapi.DefaultLegacyProjectionTarget(
			aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityOrderedItems),
			aipapi.ReasoningReplaySupport{},
		),
	})
	if res.Kind != aipapi.NegotiationLossless {
		t.Fatalf("got %s err=%v", res.Kind, res.Err())
	}
}

func TestRequiresProjectionAdaptation_authorityAware(t *testing.T) {
	t.Parallel()

	itemCall := aipapi.Call{Items: []aipapi.Item{{Kind: aipapi.ItemKindMessage, ID: "m1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hi"}}}}}
	legacyCall := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}}}
	ordered := aipapi.NewBackendCaps(aipapi.CapabilityOrderedItems)
	legacy := aipapi.NewBackendCaps(aipapi.CapabilityStreaming)

	if aipapi.RequiresProjectionAdaptation(itemCall, ordered) {
		t.Fatal("native ordered backend should not require projection for item authority")
	}
	if !aipapi.RequiresProjectionAdaptation(itemCall, legacy) {
		t.Fatal("legacy backend should require projection for item authority")
	}
	if !aipapi.RequiresProjectionAdaptation(legacyCall, ordered) {
		t.Fatal("ordered backend should require projection for legacy authority")
	}
	if aipapi.RequiresProjectionAdaptation(legacyCall, legacy) {
		t.Fatal("legacy backend should not require projection for legacy authority")
	}
}

func containsCapability(caps []aipapi.Capability, want aipapi.Capability) bool {
	return slices.Contains(caps, want)
}

func TestProjectItemsToLegacyView_sourceNonMutation(t *testing.T) {
	t.Parallel()

	origItem := aipapi.Item{
		Kind:   aipapi.ItemKindMessage,
		ID:     "msg-1",
		Status: aipapi.ItemStatusCompleted,
		Role:   aipapi.RoleUser,
		Content: []aipapi.ContentPart{
			{Kind: aipapi.ContentPartText, Text: "hello"},
		},
	}
	call := aipapi.Call{
		Items: []aipapi.Item{origItem},
	}
	target := aipapi.DefaultLegacyProjectionTarget(
		aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		aipapi.ReasoningReplaySupport{},
	)
	res, err := aipapi.ProjectItemsToLegacyView(call, target)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("expected 1 projected message, got %d", len(res.Messages))
	}
	// Verify source call was not mutated
	if len(call.Items) != 1 || call.Items[0].ID != "msg-1" || call.Items[0].Content[0].Text != "hello" {
		t.Fatalf("source Call was mutated: %#v", call)
	}
}

func TestProjectLegacyToOrderedItems_sourceNonMutation(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Instructions: []aipapi.Message{
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("system prompt")}},
		},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("user prompt")}},
		},
	}
	items, _, err := aipapi.ProjectLegacyToOrderedItems(call, aipapi.DefaultOrderedItemProjectionTarget())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 projected items, got %d", len(items))
	}
	// Verify source call was not mutated
	if len(call.Instructions) != 1 || call.Instructions[0].Parts[0].Text != "system prompt" {
		t.Fatalf("source Instructions were mutated: %#v", call.Instructions)
	}
	if len(call.Messages) != 1 || call.Messages[0].Parts[0].Text != "user prompt" {
		t.Fatalf("source Messages were mutated: %#v", call.Messages)
	}
}

func TestProjectItemsToLegacyView_deterministicRejectionReasonsTable(t *testing.T) {
	t.Parallel()

	defaultCaps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
	noReplay := aipapi.ReasoningReplaySupport{}

	tests := []struct {
		name       string
		call       aipapi.Call
		target     aipapi.LegacyProjectionTarget
		wantReason aipapi.ProjectionReason
	}{
		{
			name: "ConflictingAuthority_LegacyOnlyCall",
			call: aipapi.Call{
				Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
			},
			target:     aipapi.DefaultLegacyProjectionTarget(defaultCaps, noReplay),
			wantReason: aipapi.ProjectionReasonConflictingAuthority,
		},
		{
			name: "AssistantPhase",
			call: aipapi.Call{
				Items: []aipapi.Item{{
					Kind: aipapi.ItemKindMessage, ID: "m1", Status: aipapi.ItemStatusCompleted,
					Role: aipapi.RoleAssistant, Phase: aipapi.AssistantPhaseCommentary,
					Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "phase text"}},
				}},
			},
			target:     aipapi.DefaultLegacyProjectionTarget(defaultCaps, noReplay),
			wantReason: aipapi.ProjectionReasonAssistantPhase,
		},
		{
			name: "ItemReference",
			call: aipapi.Call{
				Items: []aipapi.Item{{
					Kind: aipapi.ItemKindItemReference, ID: "r1", Status: aipapi.ItemStatusCompleted,
					Reference: &aipapi.ItemReference{ID: "m0"},
				}},
			},
			target:     aipapi.DefaultLegacyProjectionTarget(defaultCaps, noReplay),
			wantReason: aipapi.ProjectionReasonItemReference,
		},
		{
			name: "Compaction",
			call: aipapi.Call{
				Items: []aipapi.Item{{
					Kind: aipapi.ItemKindCompaction, ID: "c1", Status: aipapi.ItemStatusCompleted,
					Compaction: &aipapi.CompactionItem{Dialect: "test"},
				}},
			},
			target:     aipapi.DefaultLegacyProjectionTarget(defaultCaps, noReplay),
			wantReason: aipapi.ProjectionReasonCompaction,
		},
		{
			name: "OpaqueExtension",
			call: aipapi.Call{
				Items: []aipapi.Item{{
					Kind: aipapi.ItemKindExtension, ID: "e1", Status: aipapi.ItemStatusCompleted,
					Extension: &aipapi.OpaqueExtension{Namespace: "custom", Type: "foo"},
				}},
			},
			target:     aipapi.DefaultLegacyProjectionTarget(defaultCaps, noReplay),
			wantReason: aipapi.ProjectionReasonOpaqueExtension,
		},
		{
			name: "VideoInput",
			call: aipapi.Call{
				Items: []aipapi.Item{{
					Kind: aipapi.ItemKindMessage, ID: "m1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser,
					Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartVideoRef, VideoRef: "vid://1"}},
				}},
			},
			target:     aipapi.DefaultLegacyProjectionTarget(defaultCaps, noReplay),
			wantReason: aipapi.ProjectionReasonVideoInput,
		},
		{
			name: "Annotation",
			call: aipapi.Call{
				Items: []aipapi.Item{{
					Kind: aipapi.ItemKindMessage, ID: "m1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser,
					Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartAnnotation, Annotation: &aipapi.AnnotationPart{Type: "note"}}},
				}},
			},
			target:     aipapi.DefaultLegacyProjectionTarget(defaultCaps, noReplay),
			wantReason: aipapi.ProjectionReasonAnnotation,
		},
		{
			name: "AssistantMediaRef",
			call: aipapi.Call{
				Items: []aipapi.Item{{
					Kind: aipapi.ItemKindMessage, ID: "m1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleAssistant,
					Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartAssistantRef, AssistantRef: "ref://1"}},
				}},
			},
			target:     aipapi.DefaultLegacyProjectionTarget(defaultCaps, noReplay),
			wantReason: aipapi.ProjectionReasonAssistantMediaRef,
		},
		{
			name: "Refusal",
			call: aipapi.Call{
				Items: []aipapi.Item{{
					Kind: aipapi.ItemKindMessage, ID: "m1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleAssistant,
					Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartRefusal, Refusal: "I cannot do that"}},
				}},
			},
			target:     aipapi.DefaultLegacyProjectionTarget(defaultCaps, noReplay),
			wantReason: aipapi.ProjectionReasonRefusal,
		},
		{
			name: "Summary",
			call: aipapi.Call{
				Items: []aipapi.Item{{
					Kind: aipapi.ItemKindMessage, ID: "m1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleAssistant,
					Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartSummary, Summary: "summary text"}},
				}},
			},
			target:     aipapi.DefaultLegacyProjectionTarget(defaultCaps, noReplay),
			wantReason: aipapi.ProjectionReasonSummary,
		},
		{
			name: "ReasoningReplay_UnsupportedDialect",
			call: aipapi.Call{
				Items: []aipapi.Item{{
					Kind: aipapi.ItemKindMessage, ID: "m1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleAssistant,
					Content: []aipapi.ContentPart{{
						Kind:      aipapi.ContentPartReasoning,
						Reasoning: &aipapi.ReasoningPart{Dialect: "unknown_dialect", Text: "thinking"},
					}},
				}},
			},
			target:     aipapi.DefaultLegacyProjectionTarget(defaultCaps, noReplay),
			wantReason: aipapi.ProjectionReasonReasoningReplay,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := aipapi.ProjectItemsToLegacyView(tt.call, tt.target)
			if err == nil {
				t.Fatalf("expected projection error for %s", tt.name)
			}
			if !aipapi.IsProjectionError(err) {
				t.Fatalf("expected ProjectionError, got %T: %v", err, err)
			}
			var pe *aipapi.ProjectionError
			if !errors.As(err, &pe) {
				t.Fatalf("errors.As failed for %T", err)
			}
			if pe.Reason != tt.wantReason {
				t.Fatalf("reason mismatch: got %q, want %q (err string: %s)", pe.Reason, tt.wantReason, pe.Error())
			}
		})
	}
}

func TestProjectItemsToLegacyView_developerPlacementAndOrderPreservation(t *testing.T) {
	t.Parallel()

	target := aipapi.DefaultLegacyProjectionTarget(
		aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		aipapi.ReasoningReplaySupport{},
	)

	t.Run("leading_developer_projects_to_instructions", func(t *testing.T) {
		t.Parallel()
		call := aipapi.Call{
			Items: []aipapi.Item{
				{
					Kind:    aipapi.ItemKindMessage,
					ID:      "dev-leading",
					Status:  aipapi.ItemStatusCompleted,
					Role:    aipapi.RoleDeveloper,
					Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "system prompt"}},
				},
				{
					Kind:    aipapi.ItemKindMessage,
					ID:      "user-1",
					Status:  aipapi.ItemStatusCompleted,
					Role:    aipapi.RoleUser,
					Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "user question"}},
				},
			},
		}

		proj, err := aipapi.ProjectItemsToLegacyView(call, target)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(proj.Instructions) != 1 {
			t.Fatalf("expected 1 instruction, got %d", len(proj.Instructions))
		}
		if proj.Instructions[0].Role != aipapi.RoleDeveloper || proj.Instructions[0].Parts[0].Text != "system prompt" {
			t.Fatalf("unexpected instruction: %#v", proj.Instructions[0])
		}
		if len(proj.Messages) != 1 {
			t.Fatalf("expected 1 message, got %d", len(proj.Messages))
		}
		if proj.Messages[0].Role != aipapi.RoleUser || proj.Messages[0].Parts[0].Text != "user question" {
			t.Fatalf("unexpected message: %#v", proj.Messages[0])
		}
	})

	t.Run("mid_trajectory_developer_preserves_position_in_messages", func(t *testing.T) {
		t.Parallel()
		call := aipapi.Call{
			Items: []aipapi.Item{
				{
					Kind:    aipapi.ItemKindMessage,
					ID:      "user-1",
					Status:  aipapi.ItemStatusCompleted,
					Role:    aipapi.RoleUser,
					Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "start turn"}},
				},
				{
					Kind:    aipapi.ItemKindMessage,
					ID:      "dev-recovery",
					Status:  aipapi.ItemStatusCompleted,
					Role:    aipapi.RoleDeveloper,
					Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "<automated-recovery>continue</automated-recovery>"}},
				},
				{
					Kind:    aipapi.ItemKindMessage,
					ID:      "asst-1",
					Status:  aipapi.ItemStatusCompleted,
					Role:    aipapi.RoleAssistant,
					Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "recovered answer"}},
				},
			},
		}

		proj, err := aipapi.ProjectItemsToLegacyView(call, target)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(proj.Instructions) != 0 {
			t.Fatalf("expected 0 instructions, got %d", len(proj.Instructions))
		}
		if len(proj.Messages) != 3 {
			t.Fatalf("expected 3 messages, got %d", len(proj.Messages))
		}
		if proj.Messages[0].Role != aipapi.RoleUser || proj.Messages[0].Parts[0].Text != "start turn" {
			t.Errorf("messages[0] mismatch: %#v", proj.Messages[0])
		}
		if proj.Messages[1].Role != aipapi.RoleDeveloper || proj.Messages[1].Parts[0].Text != "<automated-recovery>continue</automated-recovery>" {
			t.Errorf("messages[1] mid-trajectory developer mismatch: %#v", proj.Messages[1])
		}
		if proj.Messages[2].Role != aipapi.RoleAssistant || proj.Messages[2].Parts[0].Text != "recovered answer" {
			t.Errorf("messages[2] mismatch: %#v", proj.Messages[2])
		}
	})

	t.Run("leading_and_mid_trajectory_mixed_order_preservation", func(t *testing.T) {
		t.Parallel()
		call := aipapi.Call{
			Items: []aipapi.Item{
				{
					Kind:    aipapi.ItemKindMessage,
					ID:      "sys-leading",
					Status:  aipapi.ItemStatusCompleted,
					Role:    aipapi.RoleSystem,
					Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "base system"}},
				},
				{
					Kind:    aipapi.ItemKindMessage,
					ID:      "user-1",
					Status:  aipapi.ItemStatusCompleted,
					Role:    aipapi.RoleUser,
					Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "user query"}},
				},
				{
					Kind:    aipapi.ItemKindMessage,
					ID:      "dev-steering",
					Status:  aipapi.ItemStatusCompleted,
					Role:    aipapi.RoleDeveloper,
					Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "steering injection"}},
				},
				{
					Kind:    aipapi.ItemKindMessage,
					ID:      "user-2",
					Status:  aipapi.ItemStatusCompleted,
					Role:    aipapi.RoleUser,
					Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "user follow-up"}},
				},
			},
		}

		proj, err := aipapi.ProjectItemsToLegacyView(call, target)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(proj.Instructions) != 1 || proj.Instructions[0].Role != aipapi.RoleSystem {
			t.Fatalf("expected 1 leading system instruction, got %#v", proj.Instructions)
		}
		if len(proj.Messages) != 3 {
			t.Fatalf("expected 3 messages, got %d", len(proj.Messages))
		}
		if proj.Messages[0].Role != aipapi.RoleUser || proj.Messages[0].Parts[0].Text != "user query" {
			t.Errorf("messages[0] mismatch: %#v", proj.Messages[0])
		}
		if proj.Messages[1].Role != aipapi.RoleDeveloper || proj.Messages[1].Parts[0].Text != "steering injection" {
			t.Errorf("messages[1] mid-trajectory developer mismatch: %#v", proj.Messages[1])
		}
		if proj.Messages[2].Role != aipapi.RoleUser || proj.Messages[2].Parts[0].Text != "user follow-up" {
			t.Errorf("messages[2] mismatch: %#v", proj.Messages[2])
		}
	})
}
