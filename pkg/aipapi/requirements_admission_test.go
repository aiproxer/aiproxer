package aipapi_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestAdmitCandidate_unionsFrozenBaselineWithCurrentCall_NoNetwork(t *testing.T) {
	t.Parallel()

	frozen := aipapi.ProtocolRequirements{
		Capabilities: []aipapi.Capability{aipapi.CapabilityStreaming},
	}
	call := aipapi.Call{
		Tools: []aipapi.ToolDef{{Name: "added-by-hook"}},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIChatCompletions,
			DeliveryMode: aipapi.DeliveryModeNonStreaming,
		},
	}
	res := aipapi.AdmitCandidate(aipapi.CandidateAdmissionInput{
		Call:        call,
		Invocation:  call.Invocation,
		BackendCaps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
			Operation: aipapi.OperationOpenAIChatCompletions,
			Modes:     []aipapi.TransportMode{aipapi.TransportModeNonStreaming},
		}),
		FrozenRequirements: &frozen,
	})
	if res.Kind != aipapi.NegotiationReject {
		t.Fatalf("expected tools reject without backend capability, got %+v", res)
	}
}

func TestAdmitCandidate_acceptsPortableItemAuthorityLegacyProjection(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Items: []aipapi.Item{
			{
				Kind: aipapi.ItemKindMessage, ID: "m1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hello"}},
			},
			{
				Kind: aipapi.ItemKindToolCall, ID: "tc1", Status: aipapi.ItemStatusCompleted,
				ToolCall: &aipapi.ToolCallItem{CallID: "c1", Name: "weather", Arguments: []byte(`{}`)},
			},
		},
	}
	res := aipapi.AdmitCandidate(aipapi.CandidateAdmissionInput{
		Call: call,
		BackendCaps: aipapi.NewBackendCaps(
			aipapi.CapabilityStreaming,
			aipapi.CapabilityTools,
		),
		ProjectionTarget: aipapi.LegacyProjectionTargetFromCaps(
			aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
			aipapi.ReasoningReplaySupport{},
		),
	})
	if res.Kind != aipapi.NegotiationLossless {
		t.Fatalf("got kind=%s err=%v cap=%+v", res.Kind, res.Err(), res.Capability)
	}
}
