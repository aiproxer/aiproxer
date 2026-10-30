package backend

import (
	"context"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"

	"github.com/aiproxer/aiproxer/internal/testkit/contract/semantic"
)

type selectionBackendHarness struct{ subject semantic.SubjectDescriptor }

func (h selectionBackendHarness) Subject() semantic.SubjectDescriptor { return h.subject }
func (selectionBackendHarness) Backend(context.Context) (BackendView, error) {
	return selectionBackendView{}, nil
}
func (selectionBackendHarness) Upstream() UpstreamProbe     { return &selectionProbe{} }
func (selectionBackendHarness) Reset(context.Context) error { return nil }

type selectionBackendView struct{}

func (selectionBackendView) Open(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
	return aipapi.NewFixedEventStream(nil), nil
}

func (selectionBackendView) EffectiveCapabilities(context.Context, *aipapi.Call) BackendFacts {
	return BackendFacts{}
}

func (selectionBackendView) Reject(context.Context, *aipapi.Call, semantic.ScenarioDescriptor) (semantic.ExecutionEvidence, error) {
	return semantic.ExecutionEvidence{Executed: true, Rejected: true, BoundaryCalls: 1}, nil
}

func (selectionBackendView) Probe(_ context.Context, scenario semantic.ScenarioDescriptor, probe UpstreamProbe) (semantic.ExecutionEvidence, error) {
	p, ok := probe.(*selectionProbe)
	if !ok {
		return semantic.ExecutionEvidence{}, context.Canceled
	}
	p.count++
	return semantic.ExecutionEvidence{ScenarioID: scenario.ID, Executed: true, BoundaryCalls: 1, UpstreamCalls: 1, Opened: true, EffectiveCapabilities: true, StreamValidated: true, UsagePresent: true, LifecycleClosed: true}, nil
}

type selectionProbe struct{ count int }

func (p *selectionProbe) RequestCount() int          { return p.count }
func (*selectionProbe) LastRequest() CapturedRequest { return CapturedRequest{} }
func (*selectionProbe) Reset()                       {}

func TestCertifyBackendRejectsMissingUpstreamEvidence(t *testing.T) {
	cert, err := CertifyBackend(context.Background(), backendNoEvidenceHarness{selectionBackendHarness{subject: semantic.SubjectDescriptor{ID: "be", Kind: semantic.KindBackendFamily}}})
	if err == nil || cert.Executed {
		t.Fatalf("expected missing backend evidence to fail: cert=%+v err=%v", cert, err)
	}
}

type backendNoEvidenceHarness struct{ selectionBackendHarness }

func (backendNoEvidenceHarness) Backend(context.Context) (BackendView, error) {
	return noEvidenceBackendView{}, nil
}

type noEvidenceBackendView struct{}

func (noEvidenceBackendView) Open(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
	return aipapi.NewFixedEventStream(nil), nil
}

func (noEvidenceBackendView) EffectiveCapabilities(context.Context, *aipapi.Call) BackendFacts {
	return BackendFacts{}
}

func (noEvidenceBackendView) Reject(context.Context, *aipapi.Call, semantic.ScenarioDescriptor) (semantic.ExecutionEvidence, error) {
	return semantic.ExecutionEvidence{}, nil
}

func (noEvidenceBackendView) Probe(context.Context, semantic.ScenarioDescriptor, UpstreamProbe) (semantic.ExecutionEvidence, error) {
	return semantic.ExecutionEvidence{}, nil
}

func TestCertifyBackendSelectionIncludesKindTransportAndEvidence(t *testing.T) {
	subject := semantic.SubjectDescriptor{ID: "be", Kind: semantic.KindBackendFamily, Capabilities: nil, Transports: []semantic.ScenarioTransport{semantic.TransportHTTP, semantic.TransportStreaming}}
	cert, err := CertifyBackend(context.Background(), selectionBackendHarness{subject: subject})
	if err != nil {
		t.Fatal(err)
	}
	if cert.SubjectKind != semantic.KindBackendFamily || len(cert.Passed) != 3 || cert.Passed[0] != "text-baseline" {
		t.Fatalf("unexpected positive evidence: %+v", cert)
	}
	// Terminal-semantics promotion: only the capability-free terminal rows
	// join text-baseline as positive for this capability-free subject.
	for _, id := range []semantic.ScenarioID{"terminal-clean-text", "terminal-output-limit"} {
		found := slices.Contains(cert.Passed, id)
		if !found {
			t.Fatalf("expected %q in positive backend evidence: %+v", id, cert)
		}
	}
	if err := cert.ValidateReleaseReady(); err != nil {
		t.Fatalf("incomplete backend evidence: %v", err)
	}
}
