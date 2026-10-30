package frontend

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/contract/semantic"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type selectionFrontendHarness struct{ subject semantic.SubjectDescriptor }

func (h selectionFrontendHarness) Subject() semantic.SubjectDescriptor { return h.subject }
func (selectionFrontendHarness) Frontend(context.Context) (FrontendView, error) {
	return selectionFrontendView{}, nil
}

func (selectionFrontendHarness) Executor() *CapturingExecutor {
	return &CapturingExecutor{Script: EventScript{Events: []aipapi.Event{{}}}}
}
func (selectionFrontendHarness) Reset() error { return nil }

type selectionFrontendView struct{}

func (selectionFrontendView) ServeHTTP(http.ResponseWriter, *http.Request) {}
func (selectionFrontendView) Subject() semantic.SubjectDescriptor {
	return semantic.SubjectDescriptor{ID: "fe", Kind: semantic.KindFrontend}
}

func (selectionFrontendView) Probe(_ context.Context, scenario semantic.ScenarioDescriptor, executor *CapturingExecutor) (semantic.ExecutionEvidence, error) {
	if _, err := executor.Execute(context.Background(), &aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser}}}); err != nil {
		return semantic.ExecutionEvidence{}, err
	}
	// Same fixed-call limitation as the core stubs: only capability-free
	// rows are accepted; the rest select negative for capability-free subjects.
	positive := scenario.ID == "text-baseline" || scenario.ID == "terminal-clean-text" || scenario.ID == "terminal-output-limit"
	return semantic.ExecutionEvidence{ScenarioID: scenario.ID, Executed: true, BoundaryCalls: 1, Accepted: positive, Rejected: !positive, CanonicalCall: positive, WireResponse: positive, ErrorMapped: true}, nil
}

func TestCertifyFrontendRejectsMissingBoundaryEvidence(t *testing.T) {
	h := selectionFrontendHarness{subject: semantic.SubjectDescriptor{ID: "fe", Kind: semantic.KindFrontend}}
	cert, err := CertifyFrontend(context.Background(), frontendNoEvidenceHarness{selectionFrontendHarness: h})
	if err == nil || cert.Executed {
		t.Fatalf("expected missing frontend boundary evidence to fail: cert=%+v err=%v", cert, err)
	}
}

type frontendNoEvidenceHarness struct{ selectionFrontendHarness }

func (h frontendNoEvidenceHarness) Frontend(context.Context) (FrontendView, error) {
	return noEvidenceFrontendView{}, nil
}

type noEvidenceFrontendView struct{}

func (noEvidenceFrontendView) ServeHTTP(http.ResponseWriter, *http.Request) {}
func (noEvidenceFrontendView) Subject() semantic.SubjectDescriptor {
	return semantic.SubjectDescriptor{ID: "fe", Kind: semantic.KindFrontend}
}

func (noEvidenceFrontendView) Probe(context.Context, semantic.ScenarioDescriptor, *CapturingExecutor) (semantic.ExecutionEvidence, error) {
	return semantic.ExecutionEvidence{}, nil
}

func TestCertifyFrontendSelectionIncludesKindTransportAndEvidence(t *testing.T) {
	subject := semantic.SubjectDescriptor{ID: "fe", Kind: semantic.KindFrontend, Capabilities: nil, Transports: []semantic.ScenarioTransport{semantic.TransportHTTP}}
	cert, err := CertifyFrontend(context.Background(), selectionFrontendHarness{subject: subject})
	if err != nil {
		t.Fatal(err)
	}
	if cert.SubjectKind != semantic.KindFrontend || len(cert.Passed) != 3 || cert.Passed[0] != "text-baseline" {
		t.Fatalf("unexpected positive evidence: %+v", cert)
	}
	// Terminal-semantics promotion: only the capability-free terminal rows
	// join text-baseline as positive for this capability-free HTTP subject.
	for _, id := range []semantic.ScenarioID{"terminal-clean-text", "terminal-output-limit"} {
		found := slices.Contains(cert.Passed, id)
		if !found {
			t.Fatalf("expected %q in positive frontend evidence: %+v", id, cert)
		}
	}
	if err := cert.ValidateReleaseReady(); err != nil {
		t.Fatalf("incomplete frontend evidence: %v", err)
	}
}
