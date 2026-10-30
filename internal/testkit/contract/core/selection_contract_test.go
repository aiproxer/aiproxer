package core

import (
	"context"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/contract/semantic"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type selectionCoreHarness struct{ subject semantic.SubjectDescriptor }

func (h selectionCoreHarness) Subject() semantic.SubjectDescriptor { return h.subject }
func (selectionCoreHarness) Derivation(context.Context) (RequirementDerivationView, error) {
	return selectionDerivation{}, nil
}

type selectionDerivation struct{}

func (selectionDerivation) DeriveRequirements(*aipapi.Call) (aipapi.ProtocolRequirements, error) {
	return aipapi.ProtocolRequirements{}, nil
}

func (selectionDerivation) MatchRequirements(aipapi.ProtocolRequirements, []aipapi.Capability) bool {
	return true
}

func (selectionDerivation) Probe(_ context.Context, scenario semantic.ScenarioDescriptor) (semantic.ExecutionEvidence, error) {
	// Same fixed-call limitation as the runtime stub: only capability-free
	// rows are accepted; the rest select negative for capability-free subjects.
	positive := scenario.ID == "text-baseline" || scenario.ID == "terminal-clean-text" || scenario.ID == "terminal-output-limit"
	return semantic.ExecutionEvidence{ScenarioID: scenario.ID, Executed: true, BoundaryCalls: 1, Derived: true, ExactMatch: true, Accepted: positive, Rejected: !positive}, nil
}

func TestCertifyCoreRejectsMissingProofEvidence(t *testing.T) {
	cert, err := CertifyCore(context.Background(), coreNoEvidenceHarness{selectionCoreHarness{subject: semantic.SubjectDescriptor{ID: "core", Kind: semantic.KindCanonicalCore}}})
	if err == nil || cert.Executed {
		t.Fatalf("expected missing core evidence to fail: cert=%+v err=%v", cert, err)
	}
}

type coreNoEvidenceHarness struct{ selectionCoreHarness }

func (coreNoEvidenceHarness) Derivation(context.Context) (RequirementDerivationView, error) {
	return noEvidenceCoreView{}, nil
}

type noEvidenceCoreView struct{}

func (noEvidenceCoreView) DeriveRequirements(*aipapi.Call) (aipapi.ProtocolRequirements, error) {
	return aipapi.ProtocolRequirements{}, nil
}

func (noEvidenceCoreView) MatchRequirements(aipapi.ProtocolRequirements, []aipapi.Capability) bool {
	return true
}

func (noEvidenceCoreView) Probe(context.Context, semantic.ScenarioDescriptor) (semantic.ExecutionEvidence, error) {
	return semantic.ExecutionEvidence{}, nil
}

func TestCertifyCoreSelectionIncludesKindTransportAndEvidence(t *testing.T) {
	subject := semantic.SubjectDescriptor{ID: "core", Kind: semantic.KindCanonicalCore, Capabilities: nil, Transports: []semantic.ScenarioTransport{semantic.TransportHTTP}}
	cert, err := CertifyCore(context.Background(), selectionCoreHarness{subject: subject})
	if err != nil {
		t.Fatal(err)
	}
	if cert.SubjectKind != semantic.KindCanonicalCore || len(cert.Passed) != 3 || cert.Passed[0] != "text-baseline" {
		t.Fatalf("unexpected positive evidence: %+v", cert)
	}
	// Terminal-semantics promotion: only the capability-free terminal rows
	// join text-baseline as positive for this capability-free HTTP subject.
	for _, id := range []semantic.ScenarioID{"terminal-clean-text", "terminal-output-limit"} {
		if !slices.Contains(cert.Passed, id) {
			t.Fatalf("expected %q in positive core evidence: %+v", id, cert)
		}
	}
	if err := cert.ValidateReleaseReady(); err != nil {
		t.Fatalf("incomplete core evidence: %v", err)
	}
}
