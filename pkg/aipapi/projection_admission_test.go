package aipapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/capabilities"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestAdmitCandidate_transportPrecedesCapability(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIChatCompletions,
			DeliveryMode: aipapi.DeliveryModeStreaming,
		},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}
	res := aipapi.AdmitCandidate(aipapi.CandidateAdmissionInput{
		Call:       call,
		Invocation: call.Invocation,
		BackendCaps: aipapi.NewBackendCaps(
			aipapi.CapabilityStreaming,
			aipapi.CapabilityTools,
		),
		TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
			Operation: aipapi.OperationOpenAIChatCompletions,
			Modes:     []aipapi.TransportMode{aipapi.TransportModeNonStreaming},
		}),
		TransportPolicy: aipapi.TransportFallbackExact,
	})
	if res.Kind != aipapi.NegotiationReject || res.Transport.Kind != aipapi.NegotiationReject {
		t.Fatalf("got %+v", res)
	}
}

func TestProjectLegacyToOrderedItems_toolResultValidates(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Messages: []aipapi.Message{
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
	items, _, err := aipapi.ProjectLegacyToOrderedItems(call, aipapi.DefaultOrderedItemProjectionTarget())
	if err != nil {
		t.Fatalf("ProjectLegacyToOrderedItems: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items=%#v", items)
	}
	var toolResult *aipapi.ToolResultItem
	for i := range items {
		if items[i].Kind == aipapi.ItemKindToolResult {
			toolResult = items[i].ToolResult
			break
		}
	}
	if toolResult == nil {
		t.Fatal("missing tool result item")
	}
	if err := (aipapi.Call{Items: items}).Validate(); err != nil {
		t.Fatalf("validate: %v items=%#v", err, items)
	}
	if toolResult.Output != "72F" || len(toolResult.Parts) > 0 {
		t.Fatalf("tool result=%#v", toolResult)
	}
}

func TestProjectItemsToLegacyView_rejectsStructuredToolOutput(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Items: []aipapi.Item{
			{
				Kind:   aipapi.ItemKindToolCall,
				ID:     "tc-1",
				Status: aipapi.ItemStatusCompleted,
				ToolCall: &aipapi.ToolCallItem{
					CallID: "c1", Name: "fn", Arguments: json.RawMessage(`{}`),
				},
			},
			{
				Kind:   aipapi.ItemKindToolResult,
				ID:     "tr-1",
				Status: aipapi.ItemStatusCompleted,
				ToolResult: &aipapi.ToolResultItem{
					CallID: "c1",
					Name:   "fn",
					Parts:  []aipapi.ContentPart{{Kind: aipapi.ContentPartImageRef, ImageRef: "img://x"}},
				},
			},
		},
	}
	target := aipapi.LegacyProjectionTargetFromCaps(
		aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
		aipapi.ReasoningReplaySupport{},
	)
	_, err := aipapi.ProjectItemsToLegacyView(call, target)
	if !aipapi.IsProjectionError(err) {
		t.Fatalf("expected projection error, got %v", err)
	}
}

func TestDeriveProtocolRequirements_preservesCallExtensionKeys(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Extensions: map[string]json.RawMessage{
			"beta":  json.RawMessage(`{"k":1}`),
			"alpha": json.RawMessage(`{"k":2}`),
		},
	}
	req := aipapi.DeriveProtocolRequirements(call)
	if len(req.ExtensionTypes) != 2 {
		t.Fatalf("extensions=%#v", req.ExtensionTypes)
	}
	if req.ExtensionTypes[0].Type != "alpha" || req.ExtensionTypes[1].Type != "beta" {
		t.Fatalf("extension order/types=%#v", req.ExtensionTypes)
	}
}

func TestExecutor_projectionRejectBeforeBackendOpen_NoNetwork(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"legacy": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityOrderedItems),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				atomic.AddInt32(&opens, 1)
				return nil, errors.New("should not open")
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "legacy:g"},
		Items: []aipapi.Item{{
			Kind:   aipapi.ItemKindCompaction,
			ID:     "cmp-1",
			Status: aipapi.ItemStatusCompleted,
			Compaction: &aipapi.CompactionItem{
				Dialect: "compact.v1",
				Opaque:  json.RawMessage(`{"ok":true}`),
			},
		}},
	}
	_, err = ex.Execute(context.Background(), call)
	if err == nil {
		t.Fatal("expected error")
	}
	if atomic.LoadInt32(&opens) != 0 {
		t.Fatalf("backend opened despite projection reject, opens=%d", opens)
	}
}

func TestExecutor_failoverRetainsRequirementSet_NoNetwork(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"partial": {
			Caps: aipapi.NewBackendCaps(
				aipapi.CapabilityStreaming,
				aipapi.CapabilityOrderedItems,
				aipapi.CapabilityAssistantPhase,
			),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				atomic.AddInt32(&opens, 1)
				return nil, errors.New("should not open")
			},
		},
		"full": {
			Caps: aipapi.NewBackendCaps(
				aipapi.CapabilityStreaming,
				aipapi.CapabilityOrderedItems,
				aipapi.CapabilityAssistantPhase,
				aipapi.CapabilityCompaction,
			),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				atomic.AddInt32(&opens, 1)
				return nil, errors.New("should not open")
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "partial:g,full:g"},
		Items: []aipapi.Item{
			{
				Kind:   aipapi.ItemKindMessage,
				ID:     "msg-1",
				Status: aipapi.ItemStatusCompleted,
				Role:   aipapi.RoleAssistant,
				Phase:  aipapi.AssistantPhaseFinalAnswer,
				Content: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartText, Text: "answer"},
				},
			},
			{
				Kind:   aipapi.ItemKindCompaction,
				ID:     "cmp-1",
				Status: aipapi.ItemStatusCompleted,
				Compaction: &aipapi.CompactionItem{
					Dialect: "compact.v1",
					Opaque:  json.RawMessage(`{"ok":true}`),
				},
			},
		},
	}
	_, err = ex.Execute(context.Background(), call)
	if err == nil {
		t.Fatal("expected error")
	}
	if atomic.LoadInt32(&opens) != 0 {
		t.Fatalf("expected zero backend opens during admission/failover, opens=%d", opens)
	}
}

func TestProjectItemsToLegacyView_rejectsRefusalAndSummary(t *testing.T) {
	t.Parallel()

	target := aipapi.LegacyProjectionTargetFromCaps(
		aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityStructuredOutputs),
		aipapi.ReasoningReplaySupport{},
	)
	for name, call := range map[string]aipapi.Call{
		"refusal": {Items: []aipapi.Item{{
			Kind: aipapi.ItemKindMessage, ID: "m1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleAssistant,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartRefusal, Refusal: "no"}},
		}}},
		"summary": {Items: []aipapi.Item{{
			Kind: aipapi.ItemKindMessage, ID: "m1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleAssistant,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartSummary, Summary: "tl;dr"}},
		}}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := aipapi.ProjectItemsToLegacyView(call, target)
			if !aipapi.IsProjectionError(err) {
				t.Fatalf("expected projection error, got %v", err)
			}
		})
	}
}

func TestExecutor_projectionOnlyRejectAfterDialectMatch_NoNetwork(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Items: []aipapi.Item{{
			Kind: aipapi.ItemKindItemReference, ID: "ref-1", Status: aipapi.ItemStatusCompleted,
			Reference: &aipapi.ItemReference{ID: "msg-prev"},
		}},
	}
	res := capabilities.AdmitCandidate(context.Background(), call, aipapi.Invocation{}, routing.AttemptCandidate{}, capabilities.CandidateFacts{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		ProjectionTarget: aipapi.LegacyProjectionTargetFromCaps(
			aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			aipapi.ReasoningReplaySupport{},
		),
	})
	if res.Kind != aipapi.NegotiationReject {
		t.Fatalf("got kind=%s err=%v", res.Kind, res.Err())
	}
}
