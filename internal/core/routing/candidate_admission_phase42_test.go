package routing_test

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

func TestContinuationCandidatePortableReroutes(t *testing.T) {
	t.Parallel()
	err := routing.CheckContinuationCandidate(routing.ContinuationConstraint{}, routing.AttemptCandidate{Key: "b:m", Primary: routing.Primary{Backend: "b", Model: "m"}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestContinuationCandidatePinsProviderBoundLineage(t *testing.T) {
	t.Parallel()
	err := routing.CheckContinuationCandidate(routing.ContinuationConstraint{Lineage: aipcont.Lineage{ProviderBound: true, ProviderID: "provider-a", Model: "model-a", CandidateKey: "provider-a:model-a"}}, routing.AttemptCandidate{Key: "provider-b:model-a", Primary: routing.Primary{Backend: "provider-b", Model: "model-a"}})
	if !errors.Is(err, routing.ErrContinuationCandidatePinned) {
		t.Fatalf("err=%v", err)
	}
}

func TestContinuationCandidateRejectsNativeMismatch(t *testing.T) {
	t.Parallel()
	err := routing.CheckContinuationCandidate(routing.ContinuationConstraint{NativeRequirements: []aipcont.NativeRequirement{{BackendID: "provider-a", Model: "model-a", Kind: "reasoning"}}}, routing.AttemptCandidate{Primary: routing.Primary{Backend: "provider-a", Model: "model-b"}})
	if !errors.Is(err, routing.ErrContinuationNativeMismatch) {
		t.Fatalf("err=%v", err)
	}
}

func TestContinuationCandidateAdmissionRetainsStoredRequirements(t *testing.T) {
	t.Parallel()
	result := (routing.CandidateAdmissionCheck{
		Call:         aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("new")}}}},
		Candidate:    routing.AttemptCandidate{Key: "provider-a:model-a", Primary: routing.Primary{Backend: "provider-a", Model: "model-a"}},
		BackendCaps:  aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Continuation: &routing.ContinuationConstraint{Requirements: aipapi.ProtocolRequirements{Capabilities: []aipapi.Capability{aipapi.CapabilityCompaction}}},
	}).Evaluate()
	if result.Kind != aipapi.NegotiationReject {
		t.Fatalf("continuation requirements were weakened: %+v", result)
	}
}
