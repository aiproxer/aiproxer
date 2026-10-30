package routing_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestCandidateAdmissionCheck_rejectsMissingCapability(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Tools: []aipapi.ToolDef{{Name: "fn"}},
	}
	res := routing.CandidateAdmissionCheck{
		Call:        call,
		BackendCaps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
	}.Evaluate()
	if res.Kind != aipapi.NegotiationReject {
		t.Fatalf("got %s", res.Kind)
	}
}

func TestCandidateAdmissionCheck_acceptsPortableLegacyProjection(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello")},
		}},
	}
	res := routing.CandidateAdmissionCheck{
		Call:              call,
		BackendCaps:       aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		RequireProjection: true,
		ProjectionTarget:  aipapi.DefaultLegacyProjectionTarget(aipapi.NewBackendCaps(aipapi.CapabilityStreaming), aipapi.ReasoningReplaySupport{}),
	}.Evaluate()
	if res.Kind != aipapi.NegotiationLossless {
		t.Fatalf("got %s err=%v", res.Kind, res.Err())
	}
}
