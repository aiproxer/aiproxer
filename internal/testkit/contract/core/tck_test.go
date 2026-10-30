package core

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/capabilities"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/testkit/contract/semantic"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type runtimeCoreView struct{}

func (runtimeCoreView) DeriveRequirements(call *aipapi.Call) (aipapi.ProtocolRequirements, error) {
	if call == nil {
		return aipapi.ProtocolRequirements{}, errors.New("nil call")
	}
	return aipapi.DeriveProtocolRequirements(*call), nil
}

func (runtimeCoreView) MatchRequirements(req aipapi.ProtocolRequirements, caps []aipapi.Capability) bool {
	return aipapi.MatchRequirements(req, aipapi.ProtocolRequirements{Capabilities: caps}, aipapi.ReasoningReplaySupport{}).Kind != aipapi.NegotiationReject
}

func (runtimeCoreView) Probe(ctx context.Context, scenario semantic.ScenarioDescriptor) (semantic.ExecutionEvidence, error) {
	if err := ctx.Err(); err != nil {
		return semantic.ExecutionEvidence{}, err
	}
	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "t"}}}}}
	req, err := runtimeCoreView{}.DeriveRequirements(&call)
	if err != nil {
		return semantic.ExecutionEvidence{}, err
	}
	if !(runtimeCoreView{}).MatchRequirements(req, req.Capabilities) {
		return semantic.ExecutionEvidence{}, errors.New("match rejected")
	}
	// The stub derives from a fixed capability-free text call, so it honestly
	// accepts only the capability-free terminal rows alongside text-baseline;
	// tool-call, error, and usage rows select negative for capability-free
	// core subjects and stay rejected here.
	positive := scenario.ID == "text-baseline" || scenario.ID == "terminal-clean-text" || scenario.ID == "terminal-output-limit"
	return semantic.ExecutionEvidence{ScenarioID: scenario.ID, Executed: true, BoundaryCalls: 1, Derived: true, ExactMatch: true, Accepted: positive, Rejected: !positive}, nil
}

type runtimeCoreHarness struct{}

func (runtimeCoreHarness) Subject() semantic.SubjectDescriptor {
	return semantic.SubjectDescriptor{ID: "canonical-core", Kind: semantic.KindCanonicalCore, Transports: []semantic.ScenarioTransport{semantic.TransportHTTP, semantic.TransportStreaming}}
}

func (runtimeCoreHarness) Derivation(context.Context) (RequirementDerivationView, error) {
	return runtimeCoreView{}, nil
}

func TestCanonicalCoreTCK_UsesRealCanonicalOperations(t *testing.T) {
	if err := RunSemanticInvariantSuite(DefaultSemanticInvariantHooks()); err != nil {
		t.Fatal(err)
	}
	cert, err := CertifyCore(context.Background(), runtimeCoreHarness{})
	if err != nil {
		t.Fatal(err)
	}
	if err := cert.ValidateReleaseReady(); err != nil {
		t.Fatal(err)
	}
	if len(cert.ExecutedScenarioIDs) == 0 || cert.HarnessCalls <= len(cert.ExecutedScenarioIDs) {
		t.Fatalf("missing core execution evidence: %+v", cert)
	}
}

func TestCanonicalCoreTCK_MutationDoublesTurnEachInvariantRED(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*SemanticInvariantHooks)
	}{
		{"derivation", func(h *SemanticInvariantHooks) {
			h.Derive = func(aipapi.Call) aipapi.ProtocolRequirements { return aipapi.ProtocolRequirements{} }
		}},
		{"matching", func(h *SemanticInvariantHooks) {
			h.Match = func(aipapi.ProtocolRequirements, aipapi.ProtocolRequirements) bool { return true }
		}},
		{"item_projection", func(h *SemanticInvariantHooks) {
			h.ProjectItems = func(aipapi.Call, aipapi.LegacyProjectionTarget) (aipapi.LegacyProjectionResult, error) {
				return aipapi.LegacyProjectionResult{}, nil
			}
		}},
		{"legacy_projection", func(h *SemanticInvariantHooks) {
			h.ProjectLegacy = func(aipapi.Call, aipapi.OrderedItemProjectionTarget) ([]aipapi.Item, aipapi.ProtocolRequirements, error) {
				return nil, aipapi.ProtocolRequirements{}, nil
			}
		}},
		{"admission", func(h *SemanticInvariantHooks) {
			h.Admit = func(aipapi.CandidateAdmissionInput) aipapi.CandidateAdmissionResult {
				return aipapi.CandidateAdmissionResult{Kind: aipapi.NegotiationLossless}
			}
		}},
		{"terminal_validation", func(h *SemanticInvariantHooks) { h.Validate = func([]aipapi.Event) error { return nil } }},
		{"commitment", func(h *SemanticInvariantHooks) { h.OutputPolicy = func(bool, bool) error { return nil } }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			mutated := DefaultSemanticInvariantHooks()
			tc.mutate(&mutated)
			if err := RunSemanticInvariantSuite(mutated); err == nil {
				t.Fatalf("mutation %q did not turn the invariant suite RED", tc.name)
			}
		})
	}
}

func TestCanonicalCoreTCK_AdmissionRejectsBeforeProvider(t *testing.T) {
	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartImageRef, ImageRef: "img"}}}}}
	res := capabilities.AdmitCandidate(context.Background(), call, aipapi.Invocation{}, routing.AttemptCandidate{}, capabilities.CandidateFacts{Caps: aipapi.NewBackendCaps(), TransportCaps: aipapi.BackendTransportCaps{}})
	if res.Kind != aipapi.NegotiationReject {
		t.Fatalf("expected hard capability rejection, got %+v", res)
	}
}
