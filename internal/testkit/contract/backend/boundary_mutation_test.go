package backend

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/contract/semantic"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type boundaryHarness struct {
	subject     semantic.SubjectDescriptor
	view        BackendView
	reset       error
	probe       *boundaryProbe
	nilUpstream bool
}

func (h *boundaryHarness) Subject() semantic.SubjectDescriptor { return h.subject }
func (h *boundaryHarness) Backend(context.Context) (BackendView, error) {
	if h.view == nil {
		return nil, nil
	}
	return h.view, nil
}

func (h *boundaryHarness) Upstream() UpstreamProbe {
	if h.nilUpstream {
		return nil
	}
	return h.probe
}
func (h *boundaryHarness) Reset(context.Context) error { return h.reset }

type boundaryProbe struct{ requests int }

type closeErrorStream struct{}

func (closeErrorStream) Recv(context.Context) (aipapi.Event, error) { return aipapi.Event{}, nil }
func (closeErrorStream) Close() error                               { return errors.New("close") }

func (p *boundaryProbe) RequestCount() int          { return p.requests }
func (*boundaryProbe) LastRequest() CapturedRequest { return CapturedRequest{} }
func (*boundaryProbe) Reset()                       {}

type boundaryView struct {
	openCalls        int
	capCalls         int
	openErr          error
	cap              BackendFacts
	probeErr         error
	actualUpstream   int
	actualMismatch   bool
	probe            *boundaryProbe
	stream           aipapi.EventStream
	nilStream        bool
	evidenceExecuted *bool
	evidenceOpened   *bool
	evidenceCaps     *bool
	evidenceBoundary *int
	evidenceUpstream *int
	wrongScenario    bool
}

func (v *boundaryView) Open(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
	v.openCalls++
	if v.openErr != nil {
		return nil, v.openErr
	}
	if v.nilStream {
		return nil, nil
	}
	if v.stream != nil {
		return v.stream, nil
	}
	return aipapi.NewFixedEventStream(nil), nil
}

func (v *boundaryView) EffectiveCapabilities(context.Context, *aipapi.Call) BackendFacts {
	v.capCalls++
	return v.cap
}

func (v *boundaryView) Reject(_ context.Context, _ *aipapi.Call, scenario semantic.ScenarioDescriptor) (semantic.ExecutionEvidence, error) {
	return semantic.ExecutionEvidence{ScenarioID: scenario.ID, Executed: true, Rejected: true, BoundaryCalls: 1}, nil
}

func (v *boundaryView) Probe(_ context.Context, scenario semantic.ScenarioDescriptor, _ UpstreamProbe) (semantic.ExecutionEvidence, error) {
	if v.probeErr != nil {
		return semantic.ExecutionEvidence{}, v.probeErr
	}
	upstreamCalls := 1
	if v.actualUpstream != 0 {
		upstreamCalls = v.actualUpstream
	}
	evidenceUpstreamCalls := upstreamCalls
	if v.actualMismatch {
		evidenceUpstreamCalls = 1
	}
	if upstreamCalls > 0 {
		v.probe.requests += upstreamCalls
	}
	evidence := semantic.ExecutionEvidence{
		ScenarioID: scenario.ID, Executed: true, BoundaryCalls: 1, UpstreamCalls: evidenceUpstreamCalls,
		Opened: true, EffectiveCapabilities: true, StreamValidated: true, LifecycleClosed: true,
	}
	if v.evidenceExecuted != nil || v.evidenceOpened != nil || v.evidenceCaps != nil || v.evidenceBoundary != nil || v.evidenceUpstream != nil || v.wrongScenario {
		if v.evidenceExecuted != nil {
			evidence.Executed = *v.evidenceExecuted
		}
		if v.evidenceOpened != nil {
			evidence.Opened = *v.evidenceOpened
		}
		if v.evidenceCaps != nil {
			evidence.EffectiveCapabilities = *v.evidenceCaps
		}
		if v.evidenceBoundary != nil {
			evidence.BoundaryCalls = *v.evidenceBoundary
		}
		if v.evidenceUpstream != nil {
			evidence.UpstreamCalls = *v.evidenceUpstream
		}
		if v.wrongScenario {
			evidence.ScenarioID = "wrong-scenario"
		}
	}
	return evidence, nil
}

func validBoundaryHarness(view *boundaryView) *boundaryHarness {
	probe := &boundaryProbe{}
	if view == nil {
		return &boundaryHarness{subject: semantic.SubjectDescriptor{ID: "backend", Kind: semantic.KindBackendFamily}, probe: probe}
	}
	view.probe = probe
	return &boundaryHarness{
		subject: semantic.SubjectDescriptor{ID: "backend", Kind: semantic.KindBackendFamily},
		view:    view, probe: probe,
	}
}

func TestCertifyBackendBoundaryMutationMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		makeHarness func() *boundaryHarness
	}{
		{"nil-construction", func() *boundaryHarness { return validBoundaryHarness(nil) }},
		{"nil-upstream", func() *boundaryHarness {
			h := validBoundaryHarness(&boundaryView{})
			h.nilUpstream = true
			return h
		}},
		{"reset-failure", func() *boundaryHarness {
			h := validBoundaryHarness(&boundaryView{})
			h.reset = errors.New("reset")
			return h
		}},
		{"open-failure", func() *boundaryHarness { return validBoundaryHarness(&boundaryView{openErr: errors.New("open")}) }},
		{"nil-stream", func() *boundaryHarness {
			return validBoundaryHarness(&boundaryView{nilStream: true})
		}},
		{"stream-close-failure", func() *boundaryHarness {
			return validBoundaryHarness(&boundaryView{stream: closeErrorStream{}})
		}},
		{"capability-mismatch", func() *boundaryHarness {
			v := &boundaryView{cap: BackendFacts{Capabilities: []aipapi.Capability{aipapi.CapabilityStreaming}}}
			h := validBoundaryHarness(v)
			h.subject.Capabilities = []aipapi.Capability{aipapi.CapabilityTools}
			return h
		}},
		{"probe-error", func() *boundaryHarness { return validBoundaryHarness(&boundaryView{probeErr: errors.New("probe")}) }},
		{"zero-boundary-calls", func() *boundaryHarness {
			zero := 0
			return validBoundaryHarness(&boundaryView{evidenceBoundary: &zero})
		}},
		{"zero-upstream-calls", func() *boundaryHarness {
			zero := 0
			return validBoundaryHarness(&boundaryView{evidenceUpstream: &zero})
		}},
		{"actual-upstream-zero", func() *boundaryHarness {
			return validBoundaryHarness(&boundaryView{actualUpstream: -1})
		}},
		{"actual-upstream-mismatch", func() *boundaryHarness {
			return validBoundaryHarness(&boundaryView{actualUpstream: 2, actualMismatch: true})
		}},
		{"executed-false", func() *boundaryHarness {
			no := false
			return validBoundaryHarness(&boundaryView{evidenceExecuted: &no})
		}},
		{"wrong-scenario-id", func() *boundaryHarness {
			return validBoundaryHarness(&boundaryView{wrongScenario: true})
		}},
		{"evidence-open-not-claimed", func() *boundaryHarness {
			no := false
			return validBoundaryHarness(&boundaryView{evidenceOpened: &no})
		}},
		{"evidence-capabilities-not-claimed", func() *boundaryHarness {
			no := false
			return validBoundaryHarness(&boundaryView{evidenceCaps: &no})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cert, err := CertifyBackend(context.Background(), tc.makeHarness())
			if err == nil || cert.Executed {
				t.Fatalf("mutation %s unexpectedly certified: cert=%+v err=%v", tc.name, cert, err)
			}
		})
	}
}

func TestCertifyBackendInvokesOpenCapabilitiesAndRequiresProbeEvidence(t *testing.T) {
	t.Parallel()
	view := &boundaryView{cap: BackendFacts{}}
	h := validBoundaryHarness(view)
	if _, err := CertifyBackend(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if view.openCalls == 0 || view.capCalls == 0 {
		t.Fatalf("certification did not execute declared backend paths: open=%d capabilities=%d", view.openCalls, view.capCalls)
	}
	if h.probe.requests == 0 {
		t.Fatal("certification did not obtain upstream probe evidence")
	}
}

func TestCertifyBackendRejectsEvidenceLoss(t *testing.T) {
	t.Parallel()
	view := &boundaryView{cap: BackendFacts{}}
	h := validBoundaryHarness(view)
	view.probeErr = errors.New("lost evidence")
	cert, err := CertifyBackend(context.Background(), h)
	if err == nil || cert.Executed {
		t.Fatalf("expected evidence loss to fail: cert=%+v err=%v", cert, err)
	}
}
