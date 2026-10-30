package capabilities_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/capabilities"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestAdmitCandidate_acceptsNativeOrderedCompaction(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Items: []aipapi.Item{
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
	res := capabilities.AdmitCandidate(context.Background(), call, aipapi.Invocation{}, routing.AttemptCandidate{}, capabilities.CandidateFacts{
		Caps: aipapi.NewBackendCaps(
			aipapi.CapabilityStreaming,
			aipapi.CapabilityOrderedItems,
			aipapi.CapabilityCompaction,
		),
		DialectSupport: aipapi.DialectSupport{
			CompactionDialects: []aipapi.DialectRequirement{{
				Kind: "compaction", Dialect: "compact.v1",
			}},
			ItemDialects: []aipapi.DialectRequirement{{
				Kind: "item", Dialect: "compact.v1",
			}},
		},
		ProjectionTarget: aipapi.DefaultLegacyProjectionTarget(
			aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityOrderedItems, aipapi.CapabilityCompaction),
			aipapi.ReasoningReplaySupport{},
		),
	})
	if res.Kind != aipapi.NegotiationLossless {
		t.Fatalf("got kind=%s err=%v", res.Kind, res.Err())
	}
}

func TestAdmitCandidate_acceptsReasoningDowngrade(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
		Options:  aipapi.GenerationOptions{ReasoningEffort: "high"},
	}
	res := capabilities.AdmitCandidate(context.Background(), call, aipapi.Invocation{}, routing.AttemptCandidate{}, capabilities.CandidateFacts{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		ProjectionTarget: aipapi.LegacyProjectionTargetFromCaps(
			aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			aipapi.ReasoningReplaySupport{},
		),
	})
	if res.Kind != aipapi.NegotiationDowngrade {
		t.Fatalf("got kind=%s err=%v", res.Kind, res.Err())
	}
}

func TestFailoverRequirementSet_retainsCompleteRequirements(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
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
		},
	}
	set := capabilities.NewFailoverRequirementSet(call)
	supported := aipapi.ProtocolRequirements{
		Capabilities: []aipapi.Capability{aipapi.CapabilityOrderedItems, aipapi.CapabilityAssistantPhase},
	}
	if !set.CandidateMatchesFailoverRequirements(supported, aipapi.ReasoningReplaySupport{}) {
		t.Fatal("expected matching candidate")
	}
	supported.Capabilities = []aipapi.Capability{aipapi.CapabilityOrderedItems}
	if set.CandidateMatchesFailoverRequirements(supported, aipapi.ReasoningReplaySupport{}) {
		t.Fatal("expected failover mismatch without assistant phase")
	}
}
